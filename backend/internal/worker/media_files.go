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

func copyFile(ctx context.Context, source, destination string) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
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
	if _, err := copyMediaContent(ctx, output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	removeOutput = false
	return nil
}

// copyMediaContent 分块检查取消，同时保留普通文件之间的内核复制能力。
func copyMediaContent(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	var total int64
	for {
		if err := context.Cause(ctx); err != nil {
			return total, err
		}
		copied, err := io.CopyN(destination, source, 4<<20)
		total += copied
		if err == io.EOF {
			return total, context.Cause(ctx)
		}
		if err != nil {
			return total, err
		}
	}
}

func commitTemporaryFile(temporary, final string) error {
	if err := renameMediaFile(temporary, final); err != nil {
		return fmt.Errorf("commit media output without replacing an existing file: %w", err)
	}
	return nil
}

func linkMediaFile(temporary, final string) error {
	if err := os.Link(temporary, final); err != nil {
		return err
	}
	return os.Remove(temporary)
}

type contextMediaReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextMediaReader) Read(buffer []byte) (int, error) {
	if err := context.Cause(reader.ctx); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func fileIdentity(ctx context.Context, filePath string) (int64, []byte, error) {
	if err := context.Cause(ctx); err != nil {
		return 0, nil, err
	}
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
	copied, err := io.Copy(hasher, contextMediaReader{ctx: ctx, reader: file})
	if err != nil {
		return 0, nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return 0, nil, err
	}
	if copied != info.Size() {
		return 0, nil, fmt.Errorf("media file size changed while calculating its identity")
	}
	return copied, hasher.Sum(nil), nil
}

func verifyArtifactFile(ctx context.Context, artifact domain.MediaArtifact) error {
	size, checksum, err := fileIdentity(ctx, artifact.FilePath)
	if err != nil {
		return err
	}
	if size != artifact.SizeBytes || string(checksum) != string(artifact.ChecksumSHA256) {
		return fmt.Errorf("artifact file identity does not match database record")
	}
	return nil
}
