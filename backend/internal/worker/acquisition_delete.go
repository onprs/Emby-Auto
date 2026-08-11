package worker

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/riverqueue/river"
)

const acquisitionDeletionGuardInterval = cancellationReconcileInterval

type AcquisitionDeleteStore interface {
	DeletionReady(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	LoadDeletionCommand(context.Context, uuid.UUID) (domain.AcquisitionDeletionCommand, error)
	CompleteDeletion(context.Context, uuid.UUID, uuid.UUID) (domain.AcquisitionDeletionResult, error)
}

type AcquisitionDeleteTorrentClient interface {
	Login(context.Context) error
	DeleteTorrent(context.Context, string, bool) error
}

type AcquisitionDeleteClientFactory func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error)

// AcquisitionDeleteHandler removes source and temporary resources. Imported
// destinations are removed only for an explicit delete-imported command and
// remain constrained to configured media library roots.
type AcquisitionDeleteHandler struct {
	configuration DownloadConfiguration
	store         AcquisitionDeleteStore
	newClient     AcquisitionDeleteClientFactory
}

func NewAcquisitionDeleteHandler(configuration DownloadConfiguration, store AcquisitionDeleteStore, newClient AcquisitionDeleteClientFactory) *AcquisitionDeleteHandler {
	return &AcquisitionDeleteHandler{configuration: configuration, store: store, newClient: newClient}
}

func (handler *AcquisitionDeleteHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "acquisition" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_acquisition_delete_operation", "acquisition.delete requires an acquisition resource", nil)
	}
	var payload struct {
		DeleteImported bool `json:"deleteImported"`
	}
	if len(operation.Payload) > 0 && json.Unmarshal(operation.Payload, &payload) != nil {
		return permanentFailure("invalid_acquisition_delete_operation", "acquisition.delete payload is invalid", nil)
	}
	return handler.deleteAcquisition(ctx, operation.ResourceID, operation.ID, payload.DeleteImported)
}

func (handler *AcquisitionDeleteHandler) deleteAcquisition(ctx context.Context, acquisitionID, operationID uuid.UUID, deleteImported bool) error {
	if handler.configuration == nil || handler.store == nil || handler.newClient == nil {
		return permanentFailure("acquisition_delete_not_configured", "acquisition deletion dependencies are unavailable", nil)
	}
	ready, err := handler.store.DeletionReady(ctx, acquisitionID, operationID)
	if err != nil {
		return retryableFailure("acquisition_delete_storage_unavailable", "task deletion readiness could not be checked", err)
	}
	if !ready {
		return river.JobSnooze(acquisitionDeletionGuardInterval)
	}
	command, err := handler.store.LoadDeletionCommand(ctx, acquisitionID)
	if err != nil {
		return retryableFailure("acquisition_delete_storage_unavailable", "task deletion resources could not be loaded", err)
	}
	if len(command.TaskIDs) == 0 && len(command.ArtifactPaths) == 0 && len(command.LibraryFiles) == 0 && len(command.Downloads) == 0 {
		_, err := handler.store.CompleteDeletion(ctx, acquisitionID, operationID)
		if err != nil {
			return retryableFailure("acquisition_delete_storage_unavailable", "task workflow records could not be deleted", err)
		}
		return nil
	}
	configuration, err := handler.configuration.Load(ctx)
	if err != nil {
		return retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	settings := configuration.Settings

	if err := handler.removeTorrents(ctx, settings, command.Downloads); err != nil {
		return err
	}
	if err := removeDeletionFiles(settings.Paths, command, deleteImported); err != nil {
		return err
	}
	if _, err := handler.store.CompleteDeletion(ctx, acquisitionID, operationID); err != nil {
		return retryableFailure("acquisition_delete_storage_unavailable", "task workflow records could not be deleted", err)
	}
	return nil
}

func (handler *AcquisitionDeleteHandler) removeTorrents(ctx context.Context, settings domain.RuntimeSettings, downloads []domain.AcquisitionDeletionDownload) error {
	hashes := make([]string, 0, len(downloads))
	seen := make(map[string]struct{}, len(downloads))
	for _, download := range downloads {
		hash := strings.ToLower(strings.TrimSpace(download.TorrentHash))
		if hash == "" || download.PreserveTorrent {
			continue
		}
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		hashes = append(hashes, hash)
	}
	if len(hashes) == 0 {
		return nil
	}
	password, err := handler.configuration.ResolveSecret(ctx, domain.SecretQBittorrentPassword)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("qbittorrent_not_configured", "qBittorrent credentials are not configured", err)
		}
		return retryableFailure("configuration_unavailable", "qBittorrent credentials are unavailable", err)
	}
	client, err := handler.newClient(qbittorrent.ClientOptions{
		BaseURL: settings.QBittorrent.URL, Username: settings.QBittorrent.Username, Password: password,
		RequestTimeout: qBittorrentRequestTimeout,
	})
	if err != nil {
		return permanentFailure("qbittorrent_configuration_invalid", "qBittorrent configuration is invalid", err)
	}
	if err := client.Login(ctx); err != nil {
		return retryableFailure("qbittorrent_unavailable", "qBittorrent login failed during task deletion", err)
	}
	for _, hash := range hashes {
		if err := client.DeleteTorrent(ctx, hash, false); err != nil {
			return retryableFailure("qbittorrent_delete_failed", "qBittorrent could not remove a task torrent", err)
		}
	}
	return nil
}

func removeDeletionFiles(paths domain.PathSettings, command domain.AcquisitionDeletionCommand, deleteImported bool) error {
	stagingRoots := []string{paths.StagingRoot, paths.WorkRoot}
	libraryRoots := []string{paths.EffectiveAnimeLibraryRoot(), paths.MovieLibraryRoot}
	seenFiles := make(map[string]struct{}, len(command.ArtifactPaths))
	for _, filePath := range command.ArtifactPaths {
		cleaned := filepath.Clean(filePath)
		if _, exists := seenFiles[cleaned]; exists {
			continue
		}
		seenFiles[cleaned] = struct{}{}
		if err := ensureDeletionPathOutsideLibraries(filePath, libraryRoots); err != nil {
			return acquisitionDeletePathFailure("temporary artifact", err)
		}
		if err := removeAllowedPath(filePath, stagingRoots, false); err != nil {
			return acquisitionDeletePathFailure("temporary artifact", err)
		}
	}

	if deleteImported {
		seenLibraryFiles := make(map[string]struct{}, len(command.LibraryFiles))
		for _, libraryFile := range command.LibraryFiles {
			if libraryFile.Preserve {
				continue
			}
			cleaned := filepath.Clean(libraryFile.FilePath)
			if _, exists := seenLibraryFiles[cleaned]; exists {
				continue
			}
			seenLibraryFiles[cleaned] = struct{}{}
			if err := removeAllowedPath(libraryFile.FilePath, libraryRoots, false); err != nil {
				return acquisitionDeleteLibraryPathFailure(err)
			}
			pruneEmptyParents(filepath.Dir(libraryFile.FilePath), libraryRoots)
		}
	}

	seenDownloads := make(map[string]struct{}, len(command.Downloads))
	for _, download := range command.Downloads {
		if strings.TrimSpace(download.SavePath) == "" || download.PreservePath || download.PreserveTorrent {
			continue
		}
		cleaned := filepath.Clean(download.SavePath)
		if _, exists := seenDownloads[cleaned]; exists {
			continue
		}
		seenDownloads[cleaned] = struct{}{}
		if strings.TrimSpace(paths.DownloadRoot) == "" {
			return permanentFailure("acquisition_delete_roots_not_configured", "the download cleanup root is not configured", nil)
		}
		if err := ensureDeletionPathOutsideLibraries(download.SavePath, libraryRoots); err != nil {
			return acquisitionDeletePathFailure("download directory", err)
		}
		if err := removeAllowedPath(download.SavePath, []string{paths.DownloadRoot}, true); err != nil {
			return acquisitionDeletePathFailure("download directory", err)
		}
	}

	if len(command.TaskIDs) > 0 && strings.TrimSpace(paths.StagingRoot) == "" {
		return permanentFailure("acquisition_delete_roots_not_configured", "the staging cleanup root is not configured", nil)
	}
	for _, taskID := range command.TaskIDs {
		for _, root := range []string{paths.StagingRoot, paths.WorkRoot} {
			if strings.TrimSpace(root) == "" {
				continue
			}
			taskRoot := filepath.Join(root, taskID.String())
			if err := ensureDeletionPathOutsideLibraries(taskRoot, libraryRoots); err != nil {
				return acquisitionDeletePathFailure("task temporary directory", err)
			}
			if err := removeAllowedPath(taskRoot, []string{root}, true); err != nil {
				return acquisitionDeletePathFailure("task temporary directory", err)
			}
		}
	}
	return nil
}

func ensureDeletionPathOutsideLibraries(path string, libraryRoots []string) error {
	targets := comparableDeletionPaths(path)
	for _, rawRoot := range libraryRoots {
		if strings.TrimSpace(rawRoot) == "" {
			continue
		}
		roots := comparableDeletionPaths(rawRoot)
		for _, target := range targets {
			for _, root := range roots {
				if deletionPathsOverlap(target, root) {
					return &cleanupPathSafetyError{cause: errors.New("cleanup target overlaps a media library root")}
				}
			}
		}
	}
	return nil
}

func comparableDeletionPaths(path string) []string {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return []string{filepath.Clean(path)}
	}
	paths := []string{absolute}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil && !strings.EqualFold(resolved, absolute) {
		paths = append(paths, resolved)
	}
	return paths
}

func deletionPathsOverlap(left, right string) bool {
	return deletionPathContains(left, right) || deletionPathContains(right, left)
}

func deletionPathContains(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func acquisitionDeleteLibraryPathFailure(err error) error {
	var safetyError *cleanupPathSafetyError
	if errors.As(err, &safetyError) {
		return permanentFailure("acquisition_delete_library_path_unsafe", "an imported media path is outside configured library roots", err)
	}
	return retryableFailure("acquisition_delete_library_file_failed", "an imported media file could not be removed", err)
}

func acquisitionDeletePathFailure(kind string, err error) error {
	var safetyError *cleanupPathSafetyError
	if errors.As(err, &safetyError) {
		return permanentFailure("acquisition_delete_path_unsafe", "the "+kind+" is outside configured cleanup roots", err)
	}
	return retryableFailure("acquisition_delete_file_failed", "the "+kind+" could not be removed", err)
}
