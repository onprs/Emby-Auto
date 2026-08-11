package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/riverqueue/river"
)

type DownloadSyncStore interface {
	LoadSyncCommand(context.Context, uuid.UUID) (domain.DownloadSyncCommand, error)
	RecordProgress(context.Context, uuid.UUID, uuid.UUID, float64, string) error
	CompleteDownload(context.Context, uuid.UUID, uuid.UUID, string) error
}

type DownloadSyncHandler struct {
	configuration DownloadConfiguration
	store         DownloadSyncStore
	newClient     TorrentClientFactory
	pollInterval  time.Duration
}

func NewDownloadSyncHandler(
	configuration DownloadConfiguration,
	store DownloadSyncStore,
	newClient TorrentClientFactory,
	pollInterval time.Duration,
) *DownloadSyncHandler {
	return &DownloadSyncHandler{
		configuration: configuration,
		store:         store,
		newClient:     newClient,
		pollInterval:  pollInterval,
	}
}

func (handler *DownloadSyncHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "download" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_download_operation", "download.sync requires a download resource", nil)
	}
	if handler.configuration == nil || handler.store == nil || handler.newClient == nil || handler.pollInterval <= 0 {
		return permanentFailure("download_handler_not_configured", "download sync handler dependencies are unavailable", nil)
	}
	command, err := handler.store.LoadSyncCommand(ctx, operation.ResourceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("download_not_found", "the download no longer exists", err)
		}
		return retryableFailure("download_storage_unavailable", "download storage is unavailable", err)
	}
	if command.DownloadID != operation.ResourceID {
		return permanentFailure("download_resource_mismatch", "the operation does not match its download", nil)
	}
	switch command.Status {
	case domain.DownloadCompleted, domain.DownloadSelectingFiles, domain.DownloadMaterialized:
		return nil
	case domain.DownloadDownloading:
	default:
		return permanentFailure("download_state_conflict", fmt.Sprintf("download cannot be synchronized from state %q", command.Status), nil)
	}
	if command.TorrentHash == "" {
		return permanentFailure("download_hash_missing", "the downloading torrent has no confirmed hash", nil)
	}

	settings, client, err := loadConfiguredTorrentClient(ctx, handler.configuration, handler.newClient)
	if err != nil {
		return err
	}
	downloadRateLimit, err := rateLimitBytesPerSecond(settings.QBittorrent.DownloadRateLimitKibPerSecond)
	if err != nil {
		return permanentFailure("qbittorrent_configuration_invalid", "the qBittorrent download rate limit is invalid", err)
	}
	uploadRateLimit, err := rateLimitBytesPerSecond(settings.QBittorrent.UploadRateLimitKibPerSecond)
	if err != nil {
		return permanentFailure("qbittorrent_configuration_invalid", "the qBittorrent upload rate limit is invalid", err)
	}
	torrents, err := client.ListTorrents(ctx, "")
	if err != nil {
		return retryableFailure("qbittorrent_unavailable", "qBittorrent torrent state is unavailable", err)
	}
	var matched *qbittorrent.Torrent
	for index := range torrents {
		if strings.EqualFold(strings.TrimSpace(torrents[index].Hash), command.TorrentHash) {
			matched = &torrents[index]
			break
		}
	}
	if matched == nil {
		return retryableFailure("qbittorrent_torrent_not_found", "the confirmed torrent is not visible in qBittorrent", nil)
	}
	if max(int64(0), matched.DownloadLimit) != downloadRateLimit || max(int64(0), matched.UploadLimit) != uploadRateLimit {
		if err := client.SetTorrentRateLimits(ctx, matched.Hash, downloadRateLimit, uploadRateLimit); err != nil {
			return retryableFailure("qbittorrent_rate_limit_failed", "qBittorrent torrent rate limits could not be applied", err)
		}
	}
	progress := max(0, min(1, matched.Progress))
	if err := handler.store.RecordProgress(ctx, command.DownloadID, operation.ID, progress, matched.State); err != nil {
		return retryableFailure("download_storage_unavailable", "download progress could not be persisted", err)
	}
	if !qbittorrent.IsTorrentComplete(*matched) {
		return river.JobSnooze(handler.pollInterval)
	}
	if err := handler.store.CompleteDownload(ctx, command.DownloadID, operation.ID, matched.State); err != nil {
		return retryableFailure("download_storage_unavailable", "download completion could not be persisted", err)
	}
	return nil
}
