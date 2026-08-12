package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type TaskImportStore interface {
	BeginImport(context.Context, uuid.UUID, uuid.UUID) (domain.ImportCommand, error)
	CompleteImport(context.Context, domain.ImportCompletion) error
}

type EmbyImportHandler struct {
	configuration MediaConfiguration
	store         TaskImportStore
	libraryAccess ImportedLibraryAccess
}

type embyImportPayload struct {
	ImportID uuid.UUID `json:"importId"`
}

func NewEmbyImportHandler(
	configuration MediaConfiguration,
	store TaskImportStore,
	libraryAccess ImportedLibraryAccess,
) *EmbyImportHandler {
	return &EmbyImportHandler{configuration: configuration, store: store, libraryAccess: libraryAccess}
}

func (handler *EmbyImportHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "episode_task" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_import_operation", "emby.import requires an episode task resource", nil)
	}
	if handler.configuration == nil || handler.store == nil || handler.libraryAccess == nil {
		return permanentFailure("import_handler_not_configured", "Emby import handler dependencies are unavailable", nil)
	}
	var payload embyImportPayload
	if json.Unmarshal(operation.Payload, &payload) != nil || payload.ImportID == uuid.Nil {
		return permanentFailure("invalid_import_operation", "emby.import payload requires an import ID", nil)
	}
	command, err := handler.store.BeginImport(ctx, operation.ResourceID, payload.ImportID)
	if err != nil {
		return mediaStoreFailure("import", err)
	}
	if command.TaskID != operation.ResourceID || command.ImportID != payload.ImportID {
		return permanentFailure("import_resource_mismatch", "the operation does not match its task import", nil)
	}
	if command.TaskState == domain.TaskImported && command.ImportState == "succeeded" {
		return nil
	}
	configuration, err := handler.configuration.Load(ctx)
	if err != nil {
		return retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	videoName := filepath.Base(command.Video.FilePath)
	subtitleName := filepath.Base(command.Subtitle.FilePath)
	var libraryRoot string
	var relative domain.LibraryRelativePaths
	switch command.MediaType {
	case domain.TaskMediaMovie:
		libraryRoot = strings.TrimSpace(configuration.Settings.Paths.MovieLibraryRoot)
		if libraryRoot == "" {
			return permanentFailure("movie_library_root_not_configured", "the movie library root is not configured", nil)
		}
		relative, err = domain.BuildMovieLibraryRelativePaths(command.MovieTitle, command.ReleaseYear, videoName, subtitleName)
	case domain.TaskMediaEpisode, "":
		libraryRoot = strings.TrimSpace(configuration.Settings.Paths.EffectiveAnimeLibraryRoot())
		if libraryRoot == "" {
			return permanentFailure("anime_library_root_not_configured", "the anime library root is not configured", nil)
		}
		relative, err = domain.BuildLibraryRelativePaths(command.SeriesTitle, command.Season, videoName, subtitleName)
	default:
		return permanentFailure("media_type_invalid", "the import task has an unsupported media type", nil)
	}
	if err != nil {
		return permanentFailure("library_path_invalid", "the Emby library destination is invalid", err)
	}
	destinationVideo, err := secureJoin(libraryRoot, relative.Video)
	if err != nil {
		return permanentFailure("library_path_invalid", "the video library destination is unsafe", err)
	}
	destinationSubtitle, err := secureJoin(libraryRoot, relative.Subtitle)
	if err != nil {
		return permanentFailure("library_path_invalid", "the subtitle library destination is unsafe", err)
	}
	if err := importArtifactPair(
		ctx,
		command.Video,
		command.Subtitle,
		libraryRoot,
		destinationVideo,
		destinationSubtitle,
		operation.ID,
		handler.libraryAccess,
	); err != nil {
		var conflict *destinationConflictError
		if errors.As(err, &conflict) {
			return permanentFailure("library_destination_conflict", "an Emby library destination contains different content", err)
		}
		return retryableFailure("library_import_failed", "the paired media files could not be imported", err)
	}
	if err := handler.store.CompleteImport(ctx, domain.ImportCompletion{
		TaskID:                  command.TaskID,
		ImportID:                command.ImportID,
		OperationID:             operation.ID,
		DestinationVideoPath:    destinationVideo,
		DestinationSubtitlePath: destinationSubtitle,
	}); err != nil {
		return mediaStoreFailure("import", err)
	}
	return nil
}

type destinationConflictError struct {
	path string
}

func (err *destinationConflictError) Error() string {
	return fmt.Sprintf("destination %q does not match the artifact checksum", err.path)
}

type importDestination struct {
	artifact  domain.MediaArtifact
	final     string
	temporary string
	prepared  bool
}

func importArtifactPair(
	ctx context.Context,
	video, subtitle domain.MediaArtifact,
	libraryRoot, destinationVideo, destinationSubtitle string,
	operationID uuid.UUID,
	libraryAccess ImportedLibraryAccess,
) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if operationID == uuid.Nil {
		return fmt.Errorf("import operation ID is required")
	}
	if libraryAccess == nil {
		return fmt.Errorf("imported library access is required")
	}
	if video.BaseName == "" || video.BaseName != subtitle.BaseName {
		return fmt.Errorf("paired import artifacts must share a basename")
	}
	if filepath.Dir(destinationVideo) != filepath.Dir(destinationSubtitle) {
		return fmt.Errorf("paired import destinations must share a directory")
	}
	libraryTreeRoot, err := prepareImportedLibraryDirectories(
		libraryRoot,
		filepath.Dir(destinationVideo),
		libraryAccess,
	)
	if err != nil {
		return err
	}
	destinations := []*importDestination{
		{artifact: video, final: destinationVideo, temporary: importTemporaryPath(destinationVideo, operationID)},
		{artifact: subtitle, final: destinationSubtitle, temporary: importTemporaryPath(destinationSubtitle, operationID)},
	}
	defer func() {
		for _, destination := range destinations {
			_ = os.Remove(destination.temporary)
		}
	}()
	for _, destination := range destinations {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		prepared, err := prepareImportDestination(*destination, libraryAccess)
		if err != nil {
			return err
		}
		destination.prepared = prepared
	}
	for _, destination := range destinations {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if !destination.prepared {
			continue
		}
		if err := os.Rename(destination.temporary, destination.final); err != nil {
			if existingErr := verifyFileAgainstArtifact(destination.final, destination.artifact); existingErr == nil {
				if accessErr := libraryAccess.Apply(destination.final); accessErr != nil {
					return accessErr
				}
				continue
			}
			return fmt.Errorf("atomically commit library file: %w", err)
		}
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	for _, destination := range destinations {
		if err := verifyFileAgainstArtifact(destination.final, destination.artifact); err != nil {
			return &destinationConflictError{path: destination.final}
		}
		if err := libraryAccess.Apply(destination.final); err != nil {
			return err
		}
	}
	if err := libraryAccess.ApplyTree(ctx, libraryTreeRoot); err != nil {
		return err
	}
	return nil
}

func prepareImportDestination(destination importDestination, libraryAccess ImportedLibraryAccess) (bool, error) {
	if _, err := os.Stat(destination.final); err == nil {
		if err := verifyFileAgainstArtifact(destination.final, destination.artifact); err != nil {
			return false, &destinationConflictError{path: destination.final}
		}
		if err := libraryAccess.Apply(destination.final); err != nil {
			return false, err
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := verifyArtifactFile(destination.artifact); err != nil {
		return false, fmt.Errorf("verify staged artifact: %w", err)
	}
	if err := os.Remove(destination.temporary); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := copyFile(destination.artifact.FilePath, destination.temporary); err != nil {
		return false, fmt.Errorf("copy artifact to library temporary file: %w", err)
	}
	if err := verifyFileAgainstArtifact(destination.temporary, destination.artifact); err != nil {
		return false, fmt.Errorf("verify library temporary file: %w", err)
	}
	if err := libraryAccess.Apply(destination.temporary); err != nil {
		return false, err
	}
	return true, nil
}

func prepareImportedLibraryDirectories(
	libraryRoot, destinationDirectory string,
	libraryAccess ImportedLibraryAccess,
) (string, error) {
	root, err := filepath.Abs(filepath.Clean(libraryRoot))
	if err != nil {
		return "", fmt.Errorf("resolve library root: %w", err)
	}
	destination, err := filepath.Abs(filepath.Clean(destinationDirectory))
	if err != nil {
		return "", fmt.Errorf("resolve library directory: %w", err)
	}
	relative, err := filepath.Rel(root, destination)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("library directory must be a descendant of the configured root")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect library root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", fmt.Errorf("configured library root must be a directory, not a symlink")
	}

	current := root
	libraryTreeRoot := ""
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if libraryTreeRoot == "" {
			libraryTreeRoot = current
		}
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			if err := os.Mkdir(current, importedLibraryPathMode); err != nil {
				return "", fmt.Errorf("create imported library directory: %w", err)
			}
		} else if statErr != nil {
			return "", fmt.Errorf("inspect imported library directory: %w", statErr)
		} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("imported library path contains a symlink or non-directory")
		}
		if err := libraryAccess.Apply(current); err != nil {
			return "", err
		}
	}
	return libraryTreeRoot, nil
}

func importTemporaryPath(final string, operationID uuid.UUID) string {
	extension := filepath.Ext(final)
	stem := strings.TrimSuffix(filepath.Base(final), extension)
	return filepath.Join(filepath.Dir(final), "."+stem+"."+operationID.String()+".import.part"+extension)
}

func verifyFileAgainstArtifact(path string, artifact domain.MediaArtifact) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("library artifact must be a regular file, not a symlink")
	}
	size, checksum, err := fileIdentity(path)
	if err != nil {
		return err
	}
	if size != artifact.SizeBytes || string(checksum) != string(artifact.ChecksumSHA256) {
		return fmt.Errorf("file identity does not match artifact")
	}
	return nil
}
