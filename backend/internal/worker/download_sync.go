package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	if len(command.SelectedFiles) == 0 {
		return permanentFailure("download_state_conflict", "download has no selected files while synchronizing", nil)
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
	qbFiles, err := client.TorrentFiles(ctx, command.TorrentHash)
	if err != nil {
		return retryableFailure("qbittorrent_unavailable", "qBittorrent torrent files are unavailable", err)
	}
	progress, allComplete := evaluateSelectedFilesCompletion(command.SelectedFiles, qbFiles)
	progress = max(0, min(1, progress))
	if err := handler.store.RecordProgress(ctx, command.DownloadID, operation.ID, progress, matched.State); err != nil {
		return retryableFailure("download_storage_unavailable", "download progress could not be persisted", err)
	}
	if !allComplete {
		return river.JobSnooze(handler.pollInterval)
	}
	if err := handler.store.CompleteDownload(ctx, command.DownloadID, operation.ID, matched.State); err != nil {
		return retryableFailure("download_storage_unavailable", "download completion could not be persisted", err)
	}
	return nil
}

// evaluateSelectedFilesCompletion 以 DB 选中文件为预期，基于 qB file 列表计算加权进度与是否全部完成。
// 每个预期文件按稳定 file index 唯一匹配，缺失/重复视为未完成且不贡献进度；priority 为 0 时按 0 进度处理以避免瞬态聚合污染。
func evaluateSelectedFilesCompletion(selected []domain.DownloadSyncFile, qbFiles []qbittorrent.TorrentFile) (float64, bool) {
	if len(selected) == 0 {
		return 0, false
	}
	countByIndex := make(map[int]int, len(qbFiles))
	firstByIndex := make(map[int]qbittorrent.TorrentFile, len(qbFiles))
	for _, file := range qbFiles {
		countByIndex[file.Index]++
		if countByIndex[file.Index] == 1 {
			firstByIndex[file.Index] = file
		}
	}
	var totalSize float64
	hasZeroOrNegative := false
	for _, item := range selected {
		if item.SizeBytes <= 0 {
			hasZeroOrNegative = true
			continue
		}
		totalSize += float64(item.SizeBytes)
		if math.IsInf(totalSize, 0) || math.IsNaN(totalSize) {
			hasZeroOrNegative = true
			totalSize = 0
			break
		}
	}
	useWeighted := totalSize > 0 && !hasZeroOrNegative && !math.IsInf(totalSize, 0) && !math.IsNaN(totalSize)
	var sum float64
	var weighted float64
	allComplete := true
	for _, item := range selected {
		qb, ok := firstByIndex[item.FileIndex]
		count := countByIndex[item.FileIndex]
		if !ok || count != 1 {
			allComplete = false
			continue
		}
		if qb.Priority == 0 {
			allComplete = false
			continue
		}
		eff := qb.Progress
		if math.IsNaN(eff) {
			eff = 0
		}
		if qb.IsSeed {
			eff = 1
		}
		if eff < 0 {
			eff = 0
		} else if eff > 1 {
			eff = 1
		}
		if math.IsNaN(qb.Progress) || !(qb.Progress >= 1 || qb.IsSeed) {
			allComplete = false
		}
		if useWeighted {
			if item.SizeBytes > 0 {
				weighted += float64(item.SizeBytes) * eff
				if math.IsInf(weighted, 0) || math.IsNaN(weighted) {
					weighted = 0
					hasZeroOrNegative = true
				}
			}
		} else {
			sum += eff
			if math.IsInf(sum, 0) || math.IsNaN(sum) {
				sum = 0
			}
		}
	}
	if hasZeroOrNegative && useWeighted {
		useWeighted = false
		sum = 0
		for _, item := range selected {
			qb, ok := firstByIndex[item.FileIndex]
			count := countByIndex[item.FileIndex]
			if !ok || count != 1 || qb.Priority == 0 {
				continue
			}
			eff := qb.Progress
			if math.IsNaN(eff) {
				eff = 0
			}
			if qb.IsSeed {
				eff = 1
			}
			if eff < 0 {
				eff = 0
			} else if eff > 1 {
				eff = 1
			}
			sum += eff
		}
	}
	var progress float64
	if useWeighted {
		progress = weighted / totalSize
	} else if len(selected) > 0 {
		progress = sum / float64(len(selected))
	}
	if math.IsNaN(progress) || math.IsInf(progress, 0) {
		progress = 0
	}
	if progress < 0 {
		progress = 0
	} else if progress > 1 {
		progress = 1
	}
	return progress, allComplete
}
