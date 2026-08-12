package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/riverqueue/river"
)

const cleanupGuardInterval = time.Minute

type CleanupStore interface {
	BeginCleanup(context.Context, uuid.UUID, uuid.UUID) (domain.CleanupCommand, error)
	CompleteCleanup(context.Context, domain.CleanupCompletion) error
}

type CleanupTorrentClient interface {
	Login(context.Context) error
	DeleteTorrent(context.Context, string, bool) error
}

type CleanupTorrentClientFactory func(qbittorrent.ClientOptions) (CleanupTorrentClient, error)

type CleanupRunHandler struct {
	configuration DownloadConfiguration
	store         CleanupStore
	newClient     CleanupTorrentClientFactory
}

type cleanupRunPayload struct {
	CleanupID uuid.UUID `json:"cleanupId"`
}

func NewCleanupRunHandler(
	configuration DownloadConfiguration,
	store CleanupStore,
	newClient CleanupTorrentClientFactory,
) *CleanupRunHandler {
	return &CleanupRunHandler{configuration: configuration, store: store, newClient: newClient}
}

func (handler *CleanupRunHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "episode_task" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_cleanup_operation", "cleanup.run requires an episode task resource", nil)
	}
	if handler.configuration == nil || handler.store == nil || handler.newClient == nil {
		return permanentFailure("cleanup_handler_not_configured", "cleanup handler dependencies are unavailable", nil)
	}
	var payload cleanupRunPayload
	if json.Unmarshal(operation.Payload, &payload) != nil || payload.CleanupID == uuid.Nil {
		return permanentFailure("invalid_cleanup_operation", "cleanup.run payload requires a cleanup ID", nil)
	}
	command, err := handler.store.BeginCleanup(ctx, operation.ResourceID, payload.CleanupID)
	if err != nil {
		return mediaStoreFailure("cleanup", err)
	}
	if command.TaskID != operation.ResourceID || command.CleanupID != payload.CleanupID {
		return permanentFailure("cleanup_resource_mismatch", "the operation does not match its task cleanup", nil)
	}
	if command.CleanupState == domain.CleanupCompleted {
		return nil
	}
	configuration, err := handler.configuration.Load(ctx)
	if err != nil {
		return retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	settings := configuration.Settings
	if strings.TrimSpace(settings.Paths.StagingRoot) == "" {
		return permanentFailure("cleanup_roots_not_configured", "the staging cleanup root is not configured", nil)
	}

	torrentRemoved := false
	if command.DownloadRemovable {
		if strings.TrimSpace(command.TorrentHash) == "" || strings.TrimSpace(command.DownloadPath) == "" || strings.TrimSpace(settings.Paths.DownloadRoot) == "" {
			return permanentFailure("cleanup_download_identity_missing", "guarded download cleanup requires a torrent hash and configured download path", nil)
		}
		password, err := handler.configuration.ResolveSecret(ctx, domain.SecretQBittorrentPassword)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return permanentFailure("qbittorrent_not_configured", "qBittorrent credentials are not configured", err)
			}
			return retryableFailure("configuration_unavailable", "qBittorrent credentials are unavailable", err)
		}
		client, err := handler.newClient(qbittorrent.ClientOptions{
			BaseURL:        settings.QBittorrent.URL,
			Username:       settings.QBittorrent.Username,
			Password:       password,
			RequestTimeout: qBittorrentRequestTimeout,
			PollInterval:   qBittorrentPollInterval,
			ConfirmTimeout: qBittorrentConfirmTimeout,
		})
		if err != nil {
			return permanentFailure("qbittorrent_configuration_invalid", "qBittorrent configuration is invalid", err)
		}
		if err := client.Login(ctx); err != nil {
			return retryableFailure("qbittorrent_unavailable", "qBittorrent login failed during cleanup", err)
		}
		if err := client.DeleteTorrent(ctx, command.TorrentHash, false); err != nil {
			return retryableFailure("qbittorrent_cleanup_failed", "qBittorrent could not remove the torrent without data deletion", err)
		}
		torrentRemoved = true
		if err := removeAllowedPath(command.DownloadPath, []string{settings.Paths.DownloadRoot}, true); err != nil {
			return cleanupPathFailure("download cache", err)
		}
	}

	stagingRoots := []string{settings.Paths.StagingRoot, settings.Paths.WorkRoot}
	for _, path := range []string{command.StagedVideoPath, command.StagedSubtitlePath} {
		if err := removeAllowedPath(path, stagingRoots, false); err != nil {
			return cleanupPathFailure("staged artifact", err)
		}
	}
	for _, path := range []string{command.StagedVideoPath, command.StagedSubtitlePath} {
		pruneEmptyParents(filepath.Dir(path), stagingRoots)
	}
	stagedFilesRemoved := pathsAbsent(command.StagedVideoPath, command.StagedSubtitlePath)
	if !stagedFilesRemoved {
		return retryableFailure("cleanup_staged_files_remain", "staged media files remain after cleanup", nil)
	}
	if !command.DownloadRemovable {
		return river.JobSnooze(cleanupGuardInterval)
	}
	if err := handler.store.CompleteCleanup(ctx, domain.CleanupCompletion{
		TaskID:             command.TaskID,
		CleanupID:          command.CleanupID,
		OperationID:        operation.ID,
		TorrentRemoved:     torrentRemoved,
		StagedFilesRemoved: stagedFilesRemoved,
	}); err != nil {
		return mediaStoreFailure("cleanup", err)
	}
	return nil
}

type cleanupPathSafetyError struct {
	cause error
}

func (err *cleanupPathSafetyError) Error() string { return err.cause.Error() }
func (err *cleanupPathSafetyError) Unwrap() error { return err.cause }

func cleanupPathFailure(kind string, err error) error {
	var safetyError *cleanupPathSafetyError
	if errors.As(err, &safetyError) {
		return permanentFailure("cleanup_path_unsafe", "the "+kind+" path is outside configured cleanup roots", err)
	}
	return retryableFailure("cleanup_delete_failed", "the "+kind+" path could not be removed", err)
}

func removeAllowedPath(path string, roots []string, recursive bool) error {
	target, root, err := resolveAllowedPath(path, roots)
	if err != nil {
		return err
	}
	if target == root {
		return &cleanupPathSafetyError{cause: fmt.Errorf("cleanup target must not be a configured root")}
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && !recursive {
		return &cleanupPathSafetyError{cause: fmt.Errorf("staged artifact cleanup target must be a file")}
	}
	if recursive {
		return os.RemoveAll(target)
	}
	return os.Remove(target)
}

func resolveAllowedPath(path string, roots []string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", fmt.Errorf("cleanup path must not be blank")
	}
	target, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", err
	}
	for _, rawRoot := range roots {
		if strings.TrimSpace(rawRoot) == "" {
			continue
		}
		root, err := filepath.Abs(filepath.Clean(rawRoot))
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if err := ensureExistingPathWithinRoot(target, root); err != nil {
			return "", "", &cleanupPathSafetyError{cause: err}
		}
		return target, root, nil
	}
	return "", "", &cleanupPathSafetyError{cause: fmt.Errorf("cleanup path is outside configured roots")}
}

func ensureExistingPathWithinRoot(target, root string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("cleanup path resolves outside configured root")
	}
	return nil
}

func pruneEmptyParents(directory string, roots []string) {
	current, root, err := resolveAllowedPath(directory, roots)
	if err != nil {
		return
	}
	for current != root {
		if err := os.Remove(current); err != nil {
			return
		}
		current = filepath.Dir(current)
	}
}

func pathsAbsent(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			return false
		}
	}
	return true
}
