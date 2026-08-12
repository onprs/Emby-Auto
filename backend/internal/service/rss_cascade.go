package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// RSSCascadeDownloadCommand removes one download.
type RSSCascadeDownloadCommand interface {
	Remove(ctx context.Context, downloadID uuid.UUID, expectedVersion int32, idempotencyKey string, actorUserID uuid.UUID) (domain.DownloadView, domain.Operation, error)
}

// RSSCascadeTaskCommand cancels one task.
type RSSCascadeTaskCommand interface {
	Cancel(ctx context.Context, taskID uuid.UUID, expectedVersion int32, idempotencyKey string, actorUserID uuid.UUID) (domain.EpisodeTask, domain.Operation, error)
}

// RSSCascadeRunner executes the cascading cleanup for a deleted subscription.
// It reuses the existing download Remove and task Cancel commands so torrent,
// file, and reference safety rules stay in one place.
type RSSCascadeRunner struct {
	queries          *db.Queries
	transactor       *database.Transactor
	downloadCommands RSSCascadeDownloadCommand
	taskCommands     RSSCascadeTaskCommand
}

func NewRSSCascadeRunner(
	queries *db.Queries,
	transactor *database.Transactor,
	downloadCommands RSSCascadeDownloadCommand,
	taskCommands RSSCascadeTaskCommand,
) *RSSCascadeRunner {
	return &RSSCascadeRunner{queries: queries, transactor: transactor, downloadCommands: downloadCommands, taskCommands: taskCommands}
}

// subscriptionCascadeItems loads every acquisition for a subscription with
// enough detail to decide what must be stopped, removed, or preserved.
func (runner *RSSCascadeRunner) subscriptionCascadeItems(ctx context.Context, subscriptionID uuid.UUID) ([]domain.RSSCascadeItem, error) {
	rows, err := runner.queries.ListSubscriptionCascadeAcquisitions(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return nil, fmt.Errorf("list subscription cascade acquisitions: %w", err)
	}
	items := make([]domain.RSSCascadeItem, 0, len(rows))
	for _, row := range rows {
		item := domain.RSSCascadeItem{
			AcquisitionID:   repository.UUIDFromPG(row.AcquisitionID),
			DownloadStatus:  row.DownloadStatus,
			DownloadVersion: row.DownloadVersion,
			ActiveTasks:     int(row.ActiveTasks),
			ImportedTasks:   int(row.ImportedTasks),
		}
		if row.DownloadID.Valid {
			item.DownloadID = repository.UUIDFromPG(row.DownloadID)
		}
		if row.TorrentHash != nil {
			item.TorrentHash = *row.TorrentHash
		}
		if row.SavePath != nil {
			item.SavePath = *row.SavePath
		}
		items = append(items, item)
	}
	return items, nil
}

// Run cascades the deletion. Each item is processed independently so a single
// failure does not roll back the rest; failures are collected for reporting.
func (runner *RSSCascadeRunner) Run(ctx context.Context, operationID, subscriptionID, actorUserID uuid.UUID) (domain.RSSCascadeResult, error) {
	if runner.queries == nil || runner.transactor == nil || runner.downloadCommands == nil || runner.taskCommands == nil {
		return domain.RSSCascadeResult{}, fmt.Errorf("RSS cascade runner dependencies are unavailable")
	}
	items, err := runner.subscriptionCascadeItems(ctx, subscriptionID)
	if err != nil {
		return domain.RSSCascadeResult{}, err
	}
	result := domain.RSSCascadeResult{SubscriptionID: subscriptionID, Acquisitions: len(items)}
	for _, item := range items {
		runner.cascadeItem(ctx, item, actorUserID, &result)
	}
	if err := runner.recordResult(ctx, operationID, subscriptionID, result); err != nil {
		return domain.RSSCascadeResult{}, fmt.Errorf("record RSS cascade result: %w", err)
	}
	return result, nil
}

func (runner *RSSCascadeRunner) recordResult(ctx context.Context, operationID, subscriptionID uuid.UUID, result domain.RSSCascadeResult) error {
	return runner.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		failures := make([]map[string]any, 0, len(result.FailedItems))
		for _, item := range result.FailedItems {
			failures = append(failures, map[string]any{
				"acquisitionId": item.AcquisitionID,
				"stage":         item.Stage,
				"reason":        truncate(item.Reason, 2000),
			})
		}
		data, err := json.Marshal(map[string]any{
			"acquisitions":     result.Acquisitions,
			"tasksCancelled":   result.TasksCancelled,
			"downloadsRemoved": result.DownloadsRemoved,
			"importedKept":     result.ImportedKept,
			"failedItems":      failures,
			"summary":          FormatCascadeSummary(result),
		})
		if err != nil {
			return fmt.Errorf("encode RSS cascade event: %w", err)
		}
		topic := "rss.subscription.delete_completed"
		if len(result.FailedItems) > 0 {
			topic = "rss.subscription.delete_partial"
		}
		return appendRSSEvent(ctx, scope.Queries, subscriptionID, operationID, topic, data)
	})
}

func (runner *RSSCascadeRunner) cascadeItem(ctx context.Context, item domain.RSSCascadeItem, actorUserID uuid.UUID, result *domain.RSSCascadeResult) {
	// Cancel any task that is still active so processing stops safely.
	if item.ActiveTasks > 0 {
		taskIDs, err := runner.activeTaskIDs(ctx, item.AcquisitionID)
		if err != nil {
			result.FailedItems = append(result.FailedItems, domain.RSSCascadeFailure{AcquisitionID: item.AcquisitionID, Stage: "load_tasks", Reason: err.Error()})
		} else {
			for _, taskID := range taskIDs {
				version, err := runner.taskVersion(ctx, taskID)
				if err != nil {
					result.FailedItems = append(result.FailedItems, domain.RSSCascadeFailure{AcquisitionID: item.AcquisitionID, Stage: "load_task", Reason: err.Error()})
					continue
				}
				if _, _, err := runner.taskCommands.Cancel(ctx, taskID, version, cascadeIdempotency("task-cancel", taskID), actorUserID); err != nil && !errors.Is(err, ErrStateConflict) {
					result.FailedItems = append(result.FailedItems, domain.RSSCascadeFailure{AcquisitionID: item.AcquisitionID, Stage: "cancel_task", Reason: err.Error()})
					continue
				}
				result.TasksCancelled++
			}
		}
	}
	// Preserve content already imported into the Emby library.
	if item.ImportedTasks > 0 {
		result.ImportedKept += item.ImportedTasks
	}
	// Remove the download only when nothing is still importing from it.
	if item.DownloadID == uuid.Nil {
		return
	}
	if item.ImportedTasks > 0 {
		// Download already fed an imported resource; keep the record but do
		// not touch files that Emby now relies on.
		return
	}
	if _, _, err := runner.downloadCommands.Remove(ctx, item.DownloadID, item.DownloadVersion, cascadeIdempotency("download-remove", item.DownloadID), actorUserID); err != nil {
		var serviceErr *Error
		switch {
		case errors.As(err, &serviceErr) && errors.Is(err, ErrStateConflict) && serviceErr.Code == "download_in_use":
			// A task is still processing this download; cancel already ran, so
			// leave the download for the cleanup guard rather than failing.
			return
		case errors.Is(err, domain.ErrNotFound):
			// Already removed by an earlier run.
			return
		default:
			result.FailedItems = append(result.FailedItems, domain.RSSCascadeFailure{AcquisitionID: item.AcquisitionID, Stage: "remove_download", Reason: err.Error()})
		}
		return
	}
	result.DownloadsRemoved++
}

func cascadeIdempotency(action string, id uuid.UUID) string {
	return "rss-cascade:" + action + ":" + id.String()
}

func (runner *RSSCascadeRunner) activeTaskIDs(ctx context.Context, acquisitionID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := runner.queries.ListActiveTaskIDsForAcquisition(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return nil, fmt.Errorf("list active tasks for acquisition: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, repository.UUIDFromPG(row))
	}
	return ids, nil
}

func (runner *RSSCascadeRunner) taskVersion(ctx context.Context, taskID uuid.UUID) (int32, error) {
	row, err := runner.queries.GetEpisodeTaskVersion(ctx, repository.UUIDToPG(taskID))
	if err != nil {
		return 0, fmt.Errorf("load task version: %w", err)
	}
	return row, nil
}

// FormatCascadeSummary renders a one-line Chinese summary of the cascade for
// the operation result message.
func FormatCascadeSummary(result domain.RSSCascadeResult) string {
	parts := []string{
		fmt.Sprintf("共 %d 项内容", result.Acquisitions),
		fmt.Sprintf("停止任务 %d 个", result.TasksCancelled),
		fmt.Sprintf("删除下载 %d 个", result.DownloadsRemoved),
	}
	if result.ImportedKept > 0 {
		parts = append(parts, fmt.Sprintf("保留已入库 %d 个", result.ImportedKept))
	}
	if len(result.FailedItems) > 0 {
		parts = append(parts, fmt.Sprintf("失败 %d 项", len(result.FailedItems)))
	}
	return strings.Join(parts, "，")
}
