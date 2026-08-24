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
	// 选中文件为空时退化为既有聚合逻辑，保持历史单测与无 manifest 场景的兼容性
	if len(command.SelectedFiles) == 0 {
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
// 每个预期文件需在 qB 列表中按 file index 唯一匹配、priority 非零且 file progress 明确完成才视为完成。
// 进度优先按 manifest size 加权，零总大小时按数量平均；结果 0..1，需由调用方再次 clamp。
func evaluateSelectedFilesCompletion(selected []domain.DownloadSyncFile, qbFiles []qbittorrent.TorrentFile) (float64, bool) {
	if len(selected) == 0 {
		return 0, false
	}
	// 统计 qB 侧 index 出现次数与首条记录，用于检测重复/缺失
	countByIndex := make(map[int]int, len(qbFiles))
	firstByIndex := make(map[int]qbittorrent.TorrentFile, len(qbFiles))
	for _, file := range qbFiles {
		countByIndex[file.Index]++
		if countByIndex[file.Index] == 1 {
			firstByIndex[file.Index] = file
		}
	}
	var total int64
	for _, item := range selected {
		if item.SizeBytes > 0 {
			total += item.SizeBytes
		}
	}
	allComplete := true
	if total > 0 {
		// 存在零大小选中文件时退化为数量平均，避免零权重导致进度提前为 1
		hasZero := false
		for _, item := range selected {
			if item.SizeBytes == 0 {
				hasZero = true
				break
			}
		}
		if hasZero {
			var sum float64
			for _, item := range selected {
				count := countByIndex[item.FileIndex]
				qb, ok := firstByIndex[item.FileIndex]
				if !ok || count != 1 {
					allComplete = false
					continue
				}
				progress := qb.Progress
				if qb.IsSeed {
					progress = 1
				}
				if progress < 0 {
					progress = 0
				}
				if progress > 1 {
					progress = 1
				}
				sum += progress
				if qb.Priority == 0 || !(qb.Progress >= 1 || qb.IsSeed) {
					allComplete = false
				}
			}
			avg := 0.0
			if len(selected) > 0 {
				avg = sum / float64(len(selected))
			}
			if avg < 0 {
				avg = 0
			}
			if avg > 1 {
				avg = 1
			}
			return avg, allComplete
		}
		var weighted float64
		for _, item := range selected {
			count := countByIndex[item.FileIndex]
			qb, ok := firstByIndex[item.FileIndex]
			// 缺失或重复 qB index 视为未完成且不贡献进度
			if !ok || count != 1 {
				allComplete = false
				continue
			}
			progress := qb.Progress
			if qb.IsSeed {
				progress = 1
			}
			if progress < 0 {
				progress = 0
			}
			if progress > 1 {
				progress = 1
			}
			weighted += float64(item.SizeBytes) * progress
			if qb.Priority == 0 || !(qb.Progress >= 1 || qb.IsSeed) {
				allComplete = false
			}
		}
		progress := weighted / float64(total)
		if progress < 0 {
			progress = 0
		}
		if progress > 1 {
			progress = 1
		}
		// 若存在选中缺失/重复，即使加权因零大小等因素为 1，也不应视为完成
		return progress, allComplete
	}
	// 零总大小：按数量平均进度，避免除零；仍需所有文件满足完成条件
	var sum float64
	for _, item := range selected {
		count := countByIndex[item.FileIndex]
		qb, ok := firstByIndex[item.FileIndex]
		if !ok || count != 1 {
			allComplete = false
			continue
		}
		progress := qb.Progress
		if qb.IsSeed {
			progress = 1
		}
		if progress < 0 {
			progress = 0
		}
		if progress > 1 {
			progress = 1
		}
		sum += progress
		if qb.Priority == 0 || !(qb.Progress >= 1 || qb.IsSeed) {
			allComplete = false
		}
	}
	avg := 0.0
	if len(selected) > 0 {
		avg = sum / float64(len(selected))
	}
	if avg < 0 {
		avg = 0
	}
	if avg > 1 {
		avg = 1
	}
	return avg, allComplete
}
