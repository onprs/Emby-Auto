package worker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type MediaConfiguration interface {
	Load(context.Context) (domain.Configuration, error)
}

type MediaTools interface {
	Probe(context.Context, string, string) (domain.MediaProbe, error)
	RunFFmpeg(context.Context, string, []string) error
}

type mediaOutputPaths struct {
	Directory string
	Final     string
	Temporary string
}

func loadMediaSettings(ctx context.Context, configuration MediaConfiguration) (domain.RuntimeSettings, error) {
	if configuration == nil {
		return domain.RuntimeSettings{}, permanentFailure("media_configuration_unavailable", "media runtime configuration is unavailable", nil)
	}
	loaded, err := configuration.Load(ctx)
	if err != nil {
		return domain.RuntimeSettings{}, retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	settings := loaded.Settings
	for field, value := range map[string]string{
		"staging root": settings.Paths.StagingRoot,
		"FFmpeg":       settings.Paths.FFmpegPath,
		"FFprobe":      settings.Paths.FFprobePath,
	} {
		if strings.TrimSpace(value) == "" {
			return domain.RuntimeSettings{}, permanentFailure("media_configuration_invalid", field+" is not configured", nil)
		}
	}
	return settings, nil
}

func secureJoin(basePath, relativePath string) (string, error) {
	if strings.TrimSpace(basePath) == "" || strings.TrimSpace(relativePath) == "" || filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("base path and safe relative path are required")
	}
	base, err := filepath.Abs(filepath.Clean(basePath))
	if err != nil {
		return "", err
	}
	candidate, err := filepath.Abs(filepath.Join(base, filepath.FromSlash(strings.ReplaceAll(relativePath, `\`, "/"))))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes configured base")
	}
	return candidate, nil
}

func buildMediaOutputPaths(stagingRoot string, taskID, operationID uuid.UUID, relativePath string) (mediaOutputPaths, error) {
	if taskID == uuid.Nil || operationID == uuid.Nil || strings.TrimSpace(relativePath) == "" {
		return mediaOutputPaths{}, fmt.Errorf("task, operation, and relative output path are required")
	}
	normalized := filepath.FromSlash(strings.ReplaceAll(relativePath, `\`, "/"))
	if filepath.IsAbs(normalized) || filepath.VolumeName(normalized) != "" {
		return mediaOutputPaths{}, fmt.Errorf("media output path must be relative")
	}
	normalized = filepath.Clean(normalized)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, ".."+string(filepath.Separator)) {
		return mediaOutputPaths{}, fmt.Errorf("media output path escapes the task directory")
	}
	fileName := filepath.Base(normalized)
	if strings.TrimSpace(fileName) == "" || fileName == "." {
		return mediaOutputPaths{}, fmt.Errorf("media output filename is required")
	}
	root, err := filepath.Abs(filepath.Clean(stagingRoot))
	if err != nil {
		return mediaOutputPaths{}, err
	}
	taskRoot := filepath.Join(root, taskID.String())
	final := filepath.Join(taskRoot, normalized)
	if relative, err := filepath.Rel(taskRoot, final); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return mediaOutputPaths{}, fmt.Errorf("media output path escapes the task directory")
	}
	directory := filepath.Dir(final)
	extension := filepath.Ext(fileName)
	stem := strings.TrimSuffix(fileName, extension)
	return mediaOutputPaths{
		Directory: directory,
		Final:     final,
		Temporary: filepath.Join(directory, "."+stem+"."+operationID.String()+".part"+extension),
	}, nil
}

func taskMediaOutputPath(command domain.TaskMediaCommand, fileName string) string {
	if command.OutputRelativeDirectory == "" {
		return fileName
	}
	return filepath.Join(command.OutputRelativeDirectory, fileName)
}

func prepareOutputDirectory(paths mediaOutputPaths) error {
	if err := os.MkdirAll(paths.Directory, 0o750); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	if err := os.Remove(paths.Temporary); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale temporary output: %w", err)
	}
	return nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	removeOutput := true
	defer func() {
		_ = output.Close()
		if removeOutput {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	removeOutput = false
	return nil
}

func commitTemporaryFile(temporary, final string) error {
	if _, err := os.Stat(final); err == nil {
		return fmt.Errorf("final output already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporary, final); err != nil {
		return fmt.Errorf("atomically rename output: %w", err)
	}
	return nil
}

func fileIdentity(filePath string) (int64, []byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return 0, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return 0, nil, fmt.Errorf("media output must be a non-empty regular file")
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return 0, nil, err
	}
	return info.Size(), hasher.Sum(nil), nil
}

func verifyArtifactFile(artifact domain.MediaArtifact) error {
	size, checksum, err := fileIdentity(artifact.FilePath)
	if err != nil {
		return err
	}
	if size != artifact.SizeBytes || string(checksum) != string(artifact.ChecksumSHA256) {
		return fmt.Errorf("artifact file identity does not match database record")
	}
	return nil
}
