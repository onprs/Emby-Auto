package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// DownloadCommandWorkflow executes explicit download recovery commands.
type DownloadCommandWorkflow struct {
	transactor *database.Transactor
	operations *OperationScheduler
}

func NewDownloadCommandWorkflow(transactor *database.Transactor, operations *OperationScheduler) *DownloadCommandWorkflow {
	return &DownloadCommandWorkflow{transactor: transactor, operations: operations}
}

func (workflow *DownloadCommandWorkflow) Retry(ctx context.Context, downloadID uuid.UUID, expectedVersion int32, idempotencyKey string, actorUserID uuid.UUID) (domain.DownloadView, domain.Operation, error) {
	var view domain.DownloadView
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, replayed, err := findIdempotentResourceCommand(
			ctx, scope, idempotencyKey, "download", downloadID, "retry",
			appqueue.KindDownloadEnqueue, appqueue.KindDownloadSelectionApply, appqueue.KindDownloadSync, appqueue.KindDownloadMaterialize,
		)
		if err != nil {
			return err
		}
		if replayed {
			operation = existing
			var loadErr error
			view, loadErr = loadDownloadCommandView(ctx, scope, downloadID)
			return loadErr
		}
		current, err := scope.Queries.LockDownloadByID(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock download: %w", err)
		}
		if current.Version != expectedVersion {
			return NewError("state_conflict", "the download was modified by another request", ErrStateConflict, map[string]any{"expectedVersion": expectedVersion})
		}
		if current.Status != "failed" {
			return NewError("invalid_state", "only a failed download can be retried", ErrStateConflict, map[string]any{"status": current.Status})
		}
		failureStage := valueOrEmpty(current.FailureStage)
		schedule := ScheduleOperationRequest{
			ResourceType:   "download",
			ResourceID:     downloadID,
			IdempotencyKey: idempotencyKey,
			ActorUserID:    actorUserID,
			Payload:        map[string]any{"command": "retry"},
		}
		switch failureStage {
		case "enqueue":
			if _, err := scope.Queries.RequeueDownloadEnqueueStage(ctx, db.RequeueDownloadEnqueueStageParams{
				ID: repository.UUIDToPG(downloadID), ExpectedVersion: expectedVersion,
			}); err != nil {
				return fmt.Errorf("requeue download enqueue: %w", err)
			}
			var payload struct {
				SourceSeason  int  `json:"sourceSeason"`
				SourceEpisode int  `json:"sourceEpisode"`
				SingleEpisode bool `json:"singleEpisode"`
			}
			acquisition, err := scope.Queries.GetAcquisitionByID(ctx, current.AcquisitionID)
			if err != nil {
				return fmt.Errorf("load retry acquisition: %w", err)
			}
			if err := json.Unmarshal(acquisition.SourcePayload, &payload); err != nil {
				return fmt.Errorf("decode retry acquisition: %w", err)
			}
			schedule.Kind = appqueue.KindDownloadEnqueue
			schedule.MaxAttempts = 5
			schedule.Timeout = DownloadEnqueueTimeout
			schedule.Payload = map[string]any{
				"command": "retry", "defaultSeason": payload.SourceSeason, "defaultEpisode": payload.SourceEpisode, "singleEpisode": payload.SingleEpisode,
			}
		case "file_resolution":
			errorCode := valueOrEmpty(current.ErrorCode)
			files, listErr := scope.Queries.ListDownloadFiles(ctx, current.ID)
			if listErr != nil {
				return fmt.Errorf("list download files for retry: %w", listErr)
			}
			if shouldRetryViaEnqueueForNoMainVideo(errorCode, current.TorrentHash, len(files)) {
				var payload struct {
					SourceSeason  int  `json:"sourceSeason"`
					SourceEpisode int  `json:"sourceEpisode"`
					SingleEpisode bool `json:"singleEpisode"`
				}
				acquisition, err := scope.Queries.GetAcquisitionByID(ctx, current.AcquisitionID)
				if err != nil {
					return fmt.Errorf("load retry acquisition: %w", err)
				}
				if err := json.Unmarshal(acquisition.SourcePayload, &payload); err != nil {
					return fmt.Errorf("decode retry acquisition: %w", err)
				}
				options := domain.FileSelectionOptions{
					DefaultSeason:  payload.SourceSeason,
					DefaultEpisode: payload.SourceEpisode,
					SingleEpisode:  payload.SingleEpisode,
				}
				hasVideo, classifyErr := hasReclassifiableVideo(files, options)
				if classifyErr != nil {
					return NewError("download_file_manifest_invalid", "the download file manifest cannot be reclassified safely", ErrInvalidInput, map[string]any{"reason": classifyErr.Error()})
				}
				if hasVideo {
					if err := scope.Queries.DeleteDownloadFilesByDownloadID(ctx, current.ID); err != nil {
						return fmt.Errorf("delete stale download files: %w", err)
					}
					if _, err := scope.Queries.RequeueDownloadNoMainVideoFromFileResolution(ctx, db.RequeueDownloadNoMainVideoFromFileResolutionParams{
						ID: repository.UUIDToPG(downloadID), ExpectedVersion: expectedVersion,
					}); err != nil {
						if errors.Is(err, pgx.ErrNoRows) {
							return NewError("state_conflict", "the download was modified by another request", ErrStateConflict, map[string]any{"expectedVersion": expectedVersion})
						}
						return fmt.Errorf("requeue download for no main video: %w", err)
					}
					schedule.Kind = appqueue.KindDownloadEnqueue
					schedule.MaxAttempts = 5
					schedule.Timeout = DownloadEnqueueTimeout
					schedule.Payload = map[string]any{
						"command": "retry", "defaultSeason": payload.SourceSeason, "defaultEpisode": payload.SourceEpisode, "singleEpisode": payload.SingleEpisode,
					}
				} else {
					if _, err := scope.Queries.RequeueDownloadFileResolutionStage(ctx, db.RequeueDownloadFileResolutionStageParams{
						ID: repository.UUIDToPG(downloadID), ExpectedVersion: expectedVersion,
					}); err != nil {
						return fmt.Errorf("requeue download file resolution: %w", err)
					}
					schedule.Kind = appqueue.KindDownloadSelectionApply
					schedule.MaxAttempts = 5
					schedule.Timeout = time.Minute
				}
			} else {
				if _, err := scope.Queries.RequeueDownloadFileResolutionStage(ctx, db.RequeueDownloadFileResolutionStageParams{
					ID: repository.UUIDToPG(downloadID), ExpectedVersion: expectedVersion,
				}); err != nil {
					return fmt.Errorf("requeue download file resolution: %w", err)
				}
				schedule.Kind = appqueue.KindDownloadSelectionApply
				schedule.MaxAttempts = 5
				schedule.Timeout = time.Minute
			}
		case "sync":
			if _, err := scope.Queries.RequeueDownloadSyncStage(ctx, db.RequeueDownloadSyncStageParams{
				ID: repository.UUIDToPG(downloadID), ExpectedVersion: expectedVersion,
			}); err != nil {
				return fmt.Errorf("requeue download sync: %w", err)
			}
			schedule.Kind = appqueue.KindDownloadSync
			schedule.MaxAttempts = 5
			schedule.Timeout = 30 * time.Second
		case "materialize":
			if _, err := scope.Queries.RequeueDownloadMaterializeStage(ctx, db.RequeueDownloadMaterializeStageParams{
				ID: repository.UUIDToPG(downloadID), ExpectedVersion: expectedVersion,
			}); err != nil {
				return fmt.Errorf("requeue download materialization: %w", err)
			}
			schedule.Kind = appqueue.KindDownloadMaterialize
			schedule.MaxAttempts = 3
			schedule.Timeout = 10 * time.Minute
		default:
			return NewError("invalid_state", "the failed download has no retryable stage", ErrStateConflict, map[string]any{"failureStage": failureStage})
		}
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, schedule)
		if err != nil {
			return fmt.Errorf("schedule download retry: %w", err)
		}
		operation = scheduled.Operation
		if err := appendResourceEvent(ctx, scope.Queries, "download", downloadID, scheduled.Operation.ID, actorUserID, "download.retry_requested", map[string]any{"failureStage": failureStage}); err != nil {
			return err
		}
		updated, err := scope.Queries.GetDownloadByID(ctx, repository.UUIDToPG(downloadID))
		if err != nil {
			return fmt.Errorf("reload download: %w", err)
		}
		files, err := scope.Queries.ListDownloadFiles(ctx, updated.ID)
		if err != nil {
			return fmt.Errorf("list download files: %w", err)
		}
		view = downloadViewFromDB(updated, files)
		return nil
	})
	if err != nil {
		return domain.DownloadView{}, domain.Operation{}, err
	}
	return view, operation, nil
}

func (workflow *DownloadCommandWorkflow) Cancel(ctx context.Context, downloadID uuid.UUID, expectedVersion int32, idempotencyKey string, actorUserID uuid.UUID) (domain.DownloadView, domain.Operation, error) {
	var view domain.DownloadView
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, replayed, err := findIdempotentResourceCommand(
			ctx, scope, idempotencyKey, "download", downloadID, "cancel", appqueue.KindDownloadCancel,
		)
		if err != nil {
			return err
		}
		if replayed {
			operation = existing
			var loadErr error
			view, loadErr = loadDownloadCommandView(ctx, scope, downloadID)
			return loadErr
		}
		if err := requestResourceOperationCancellations(ctx, scope, "download", downloadID, actorUserID); err != nil {
			return err
		}
		current, err := scope.Queries.LockDownloadByID(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock download: %w", err)
		}
		if current.Version != expectedVersion {
			return NewError("state_conflict", "the download was modified by another request", ErrStateConflict, map[string]any{"expectedVersion": expectedVersion})
		}
		cancelled, err := scope.Queries.CancelDownloadIfActive(ctx, db.CancelDownloadIfActiveParams{
			ID:              repository.UUIDToPG(downloadID),
			ExpectedVersion: expectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return NewError("invalid_state", "only a non-terminal download can be cancelled", ErrStateConflict, map[string]any{"status": current.Status})
		}
		if err != nil {
			return fmt.Errorf("cancel download: %w", err)
		}
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindDownloadCancel,
			ResourceType:   "download",
			ResourceID:     downloadID,
			IdempotencyKey: idempotencyKey,
			MaxAttempts:    1,
			Timeout:        time.Minute,
			Payload:        map[string]any{"command": "cancel"},
			ActorUserID:    actorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule download cancel sync: %w", err)
		}
		operation = scheduled.Operation
		if err := appendResourceEvent(ctx, scope.Queries, "download", downloadID, scheduled.Operation.ID, actorUserID, "download.cancel_requested", map[string]any{}); err != nil {
			return err
		}
		files, err := scope.Queries.ListDownloadFiles(ctx, cancelled.ID)
		if err != nil {
			return fmt.Errorf("list download files: %w", err)
		}
		view = downloadViewFromDB(cancelled, files)
		return nil
	})
	if err != nil {
		return domain.DownloadView{}, domain.Operation{}, err
	}
	return view, operation, nil
}

func (workflow *DownloadCommandWorkflow) Remove(ctx context.Context, downloadID uuid.UUID, expectedVersion int32, idempotencyKey string, actorUserID uuid.UUID) (domain.DownloadView, domain.Operation, error) {
	var view domain.DownloadView
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, replayed, err := findIdempotentResourceCommand(
			ctx, scope, idempotencyKey, "download", downloadID, "remove", appqueue.KindDownloadCancel,
		)
		if err != nil {
			return err
		}
		if replayed {
			operation = existing
			row, loadErr := scope.Queries.GetDownloadByIDIncludingDeleted(ctx, repository.UUIDToPG(downloadID))
			if loadErr != nil {
				return loadErr
			}
			files, loadErr := scope.Queries.ListDownloadFiles(ctx, row.ID)
			if loadErr != nil {
				return loadErr
			}
			view = downloadViewFromDB(row, files)
			return nil
		}
		current, err := scope.Queries.LockDownloadByID(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) || current.DeletedAt.Valid {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock download: %w", err)
		}
		if current.Version != expectedVersion {
			return NewError("state_conflict", "the download was modified by another request", ErrStateConflict, map[string]any{"expectedVersion": expectedVersion})
		}
		blockingTasks, err := scope.Queries.CountBlockingTasksForDownload(ctx, repository.UUIDToPG(downloadID))
		if err != nil {
			return fmt.Errorf("count active media tasks: %w", err)
		}
		if blockingTasks > 0 {
			return NewError("download_in_use", "the download is still being used by media processing", ErrStateConflict, map[string]any{"activeTaskCount": blockingTasks})
		}
		preserveTorrent, err := scope.Queries.TorrentUsedByOtherDownload(ctx, repository.UUIDToPG(downloadID))
		if err != nil {
			return fmt.Errorf("check torrent ownership: %w", err)
		}
		if err := requestResourceOperationCancellations(ctx, scope, "download", downloadID, actorUserID); err != nil {
			return err
		}
		removed, err := scope.Queries.MarkDownloadRemovalRequested(ctx, db.MarkDownloadRemovalRequestedParams{
			ID: repository.UUIDToPG(downloadID), ExpectedVersion: expectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return NewError("invalid_state", "this download cannot be deleted yet", ErrStateConflict, map[string]any{"status": current.Status})
		}
		if err != nil {
			return fmt.Errorf("request download removal: %w", err)
		}
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindDownloadCancel,
			ResourceType:   "download",
			ResourceID:     downloadID,
			IdempotencyKey: idempotencyKey,
			MaxAttempts:    3,
			Timeout:        2 * time.Minute,
			Payload:        map[string]any{"command": "remove", "deleteFiles": !preserveTorrent, "preserveTorrent": preserveTorrent},
			ActorUserID:    actorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule download removal: %w", err)
		}
		operation = scheduled.Operation
		if err := appendResourceEvent(ctx, scope.Queries, "download", downloadID, scheduled.Operation.ID, actorUserID, "download.removal_requested", map[string]any{"deleteFiles": !preserveTorrent, "preserveTorrent": preserveTorrent}); err != nil {
			return err
		}
		files, err := scope.Queries.ListDownloadFiles(ctx, removed.ID)
		if err != nil {
			return fmt.Errorf("list download files: %w", err)
		}
		view = downloadViewFromDB(removed, files)
		return nil
	})
	if err != nil {
		return domain.DownloadView{}, domain.Operation{}, err
	}
	return view, operation, nil
}

func loadDownloadCommandView(ctx context.Context, scope database.TxScope, downloadID uuid.UUID) (domain.DownloadView, error) {
	download, err := scope.Queries.GetDownloadByID(ctx, repository.UUIDToPG(downloadID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DownloadView{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.DownloadView{}, fmt.Errorf("load download command replay: %w", err)
	}
	files, err := scope.Queries.ListDownloadFiles(ctx, download.ID)
	if err != nil {
		return domain.DownloadView{}, fmt.Errorf("list download command replay files: %w", err)
	}
	return downloadViewFromDB(download, files), nil
}

func (workflow *DownloadCommandWorkflow) SaveFileResolution(
	ctx context.Context,
	downloadID uuid.UUID,
	expectedVersion int32,
	items []domain.DownloadFileResolutionItem,
	idempotencyKey string,
	actorUserID uuid.UUID,
) (domain.DownloadView, domain.Operation, error) {
	var view domain.DownloadView
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, replayed, err := findIdempotentResourceCommand(
			ctx, scope, idempotencyKey, "download", downloadID, "file_resolution", appqueue.KindDownloadSelectionApply,
		)
		if err != nil {
			return err
		}
		if replayed {
			operation = existing
			view, err = loadDownloadCommandView(ctx, scope, downloadID)
			return err
		}
		current, err := scope.Queries.LockDownloadByID(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock download: %w", err)
		}
		if current.Version != expectedVersion {
			return NewError("state_conflict", "the download was modified by another request", ErrStateConflict, map[string]any{"expectedVersion": expectedVersion})
		}
		if current.Status != string(domain.DownloadFileResolutionPending) && (current.Status != string(domain.DownloadFailed) || valueOrEmpty(current.FailureStage) != "file_resolution") {
			return NewError("invalid_state", "the download is not waiting for file resolution", ErrStateConflict, map[string]any{"status": current.Status})
		}
		files, err := scope.Queries.ListDownloadFiles(ctx, current.ID)
		if err != nil {
			return fmt.Errorf("list download files: %w", err)
		}
		contextRow, err := scope.Queries.GetAgentDownloadContext(ctx, current.ID)
		if err != nil {
			return fmt.Errorf("load download coordinate constraints: %w", err)
		}
		normalized, err := validateDownloadFileResolution(files, items)
		if err != nil {
			return err
		}
		if err := validateSingleEpisodeFileResolution(files, normalized, int(contextRow.DefaultSourceSeason), int32PointerToInt(contextRow.DefaultSourceEpisode)); err != nil {
			return err
		}
		for _, item := range normalized {
			if _, err := scope.Queries.SetDownloadFileResolution(ctx, db.SetDownloadFileResolutionParams{
				Selected: item.Selected, SourceSeason: optionalResolutionInt32(item.SourceSeason),
				SourceEpisode:                   optionalResolutionInt32(item.SourceEpisode),
				SourceEpisodeFractionHundredths: int32(item.SourceEpisodeFractionHundredths),
				ID:                              repository.UUIDToPG(item.FileID), DownloadID: repository.UUIDToPG(downloadID),
			}); err != nil {
				return fmt.Errorf("save download file resolution: %w", err)
			}
		}
		source := string(domain.DecisionSourceUser)
		updated, err := scope.Queries.SetDownloadResolutionSource(ctx, db.SetDownloadResolutionSourceParams{
			FileResolutionSource: &source, ID: repository.UUIDToPG(downloadID),
			ExpectedVersion: expectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return NewError("state_conflict", "the download changed before file resolution was saved", ErrStateConflict, nil)
		}
		if err != nil {
			return fmt.Errorf("save download resolution source: %w", err)
		}
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind: appqueue.KindDownloadSelectionApply, ResourceType: "download", ResourceID: downloadID,
			IdempotencyKey: idempotencyKey, MaxAttempts: 5, Timeout: time.Minute,
			Payload: map[string]any{"command": "file_resolution"}, ActorUserID: actorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule download selection apply: %w", err)
		}
		operation = scheduled.Operation
		if err := appendResourceEvent(ctx, scope.Queries, "download", downloadID, scheduled.Operation.ID, actorUserID, "download.file_resolution_saved", map[string]any{
			"source": domain.DecisionSourceUser,
		}); err != nil {
			return err
		}
		updatedFiles, err := scope.Queries.ListDownloadFiles(ctx, updated.ID)
		if err != nil {
			return fmt.Errorf("reload download files: %w", err)
		}
		view = downloadViewFromDB(updated, updatedFiles)
		return nil
	})
	if err != nil {
		return domain.DownloadView{}, domain.Operation{}, err
	}
	return view, operation, nil
}

func validateDownloadFileResolution(files []db.DownloadFile, items []domain.DownloadFileResolutionItem) ([]domain.DownloadFileResolutionItem, error) {
	if len(items) != len(files) || len(items) == 0 {
		return nil, NewError("download_file_resolution_invalid", "the complete download file manifest is required", ErrInvalidInput, map[string]any{"expectedFileCount": len(files)})
	}
	known := make(map[uuid.UUID]db.DownloadFile, len(files))
	for _, file := range files {
		known[repository.UUIDFromPG(file.ID)] = file
	}
	seen := make(map[uuid.UUID]struct{}, len(items))
	videoCoordinates := make(map[[3]int]uuid.UUID)
	subtitleCoordinates := make([][3]int, 0)
	selectedVideos := 0
	for _, item := range items {
		file, ok := known[item.FileID]
		if !ok {
			return nil, NewError("download_file_scope_violation", "a file does not belong to the download", ErrInvalidInput, map[string]any{"fileId": item.FileID})
		}
		if _, duplicate := seen[item.FileID]; duplicate {
			return nil, NewError("download_file_duplicate", "a file appears more than once", ErrInvalidInput, map[string]any{"fileId": item.FileID})
		}
		seen[item.FileID] = struct{}{}
		if (item.SourceSeason == nil) != (item.SourceEpisode == nil) ||
			item.SourceEpisodeFractionHundredths < 0 || item.SourceEpisodeFractionHundredths > 99 ||
			(item.SourceEpisode == nil && item.SourceEpisodeFractionHundredths != 0) ||
			(item.SourceSeason != nil && (*item.SourceSeason <= 0 || *item.SourceEpisode <= 0 || *item.SourceSeason > math.MaxInt32 || *item.SourceEpisode > math.MaxInt32)) {
			return nil, NewError("download_coordinate_invalid", "source season, episode, and fraction must form a valid coordinate", ErrInvalidInput, map[string]any{"fileId": item.FileID})
		}
		if !item.Selected {
			continue
		}
		if item.SourceSeason == nil {
			return nil, NewError("download_coordinate_invalid", "selected media files require source coordinates", ErrInvalidInput, map[string]any{"fileId": item.FileID})
		}
		coordinate := [3]int{*item.SourceSeason, *item.SourceEpisode, item.SourceEpisodeFractionHundredths}
		switch file.MediaKind {
		case string(domain.MediaVideo):
			if _, duplicate := videoCoordinates[coordinate]; duplicate {
				return nil, NewError("download_coordinate_duplicate", "only one video can be selected for each source coordinate", ErrInvalidInput, map[string]any{
					"sourceSeason": coordinate[0], "sourceEpisode": coordinate[1],
					"sourceEpisodeFractionHundredths": coordinate[2],
				})
			}
			videoCoordinates[coordinate] = item.FileID
			selectedVideos++
		case string(domain.MediaSubtitle):
			subtitleCoordinates = append(subtitleCoordinates, coordinate)
		default:
			return nil, NewError("download_media_kind_invalid", "only supported video and text subtitle files can be selected", ErrInvalidInput, map[string]any{"fileId": item.FileID})
		}
	}
	if selectedVideos == 0 {
		return nil, NewError("download_no_main_video", "at least one video file must be selected", ErrInvalidInput, nil)
	}
	subtitleCount := make(map[[3]int]int)
	for _, coordinate := range subtitleCoordinates {
		if _, ok := videoCoordinates[coordinate]; !ok {
			return nil, NewError("download_subtitle_video_invalid", "each selected subtitle must match a selected video coordinate", ErrInvalidInput, map[string]any{"sourceSeason": coordinate[0], "sourceEpisode": coordinate[1]})
		}
		subtitleCount[coordinate]++
		if subtitleCount[coordinate] > 8 {
			return nil, NewError("download_subtitle_limit_exceeded", "at most eight subtitles can be selected for one episode", ErrInvalidInput, map[string]any{"sourceSeason": coordinate[0], "sourceEpisode": coordinate[1]})
		}
	}
	return items, nil
}

func validateSingleEpisodeFileResolution(files []db.DownloadFile, items []domain.DownloadFileResolutionItem, season int, episode *int) error {
	if episode == nil {
		return nil
	}
	kinds := make(map[uuid.UUID]string, len(files))
	for _, file := range files {
		kinds[repository.UUIDFromPG(file.ID)] = file.MediaKind
	}
	for _, item := range items {
		if !item.Selected || kinds[item.FileID] != string(domain.MediaVideo) {
			continue
		}
		if item.SourceSeason == nil || item.SourceEpisode == nil || *item.SourceSeason != season || *item.SourceEpisode != *episode || item.SourceEpisodeFractionHundredths != 0 {
			return NewError("download_single_episode_coordinate_mismatch", "a single-episode acquisition must keep its requested source coordinate", ErrInvalidInput, map[string]any{
				"sourceSeason": season, "sourceEpisode": *episode,
			})
		}
	}
	return nil
}

// shouldRetryViaEnqueueForNoMainVideo 判断 file_resolution 下 download_no_main_video 是否应走重新 enqueue。
// 仅当存在 torrent hash 且已有 manifest 时才重新分类，避免对无 manifest 或其它 file_resolution 失败误走 enqueue。
func shouldRetryViaEnqueueForNoMainVideo(errorCode string, torrentHash *string, fileCount int) bool {
	if errorCode != "download_no_main_video" {
		return false
	}
	if torrentHash == nil || strings.TrimSpace(*torrentHash) == "" {
		return false
	}
	return fileCount > 0
}

// hasReclassifiableVideo 使用当前 domain.ClassifyDownloadFiles 对持久化文件的 index/path/size 做确定性重分类，
// 使用 acquisition payload 的 DefaultSeason/DefaultEpisode/SingleEpisode 约束。
// 仅当分类结果含至少一个 MediaVideo 时才允许重新 enqueue， genuine extra-only/other-only 不应重新 fetch。
func hasReclassifiableVideo(files []db.DownloadFile, options domain.FileSelectionOptions) (bool, error) {
	downloadFiles := make([]domain.DownloadFile, 0, len(files))
	for _, file := range files {
		downloadFiles = append(downloadFiles, domain.DownloadFile{
			Index:        int(file.FileIndex),
			RelativePath: file.RelativePath,
			SizeBytes:    file.SizeBytes,
		})
	}
	classified, err := domain.ClassifyDownloadFiles(downloadFiles, options)
	if err != nil {
		return false, err
	}
	for _, file := range classified {
		if file.Kind == domain.MediaVideo {
			return true, nil
		}
	}
	return false, nil
}

func optionalResolutionInt32(value *int) *int32 {
	if value == nil {
		return nil
	}
	converted := int32(*value)
	return &converted
}

func (workflow *DownloadCommandWorkflow) SaveFileSelection(ctx context.Context, downloadID uuid.UUID, expectedVersion int32, selections map[uuid.UUID]bool, idempotencyKey string, actorUserID uuid.UUID) (domain.DownloadView, domain.Operation, error) {
	var view domain.DownloadView
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		current, err := scope.Queries.LockDownloadByID(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock download: %w", err)
		}
		if current.Version != expectedVersion {
			return NewError("state_conflict", "the download was modified by another request", ErrStateConflict, map[string]any{"expectedVersion": expectedVersion})
		}
		if current.Status != "completed" && current.Status != "selecting_files" {
			return NewError("invalid_state", "file selection can only change before materialization", ErrStateConflict, map[string]any{"status": current.Status})
		}
		files, err := scope.Queries.ListDownloadFiles(ctx, current.ID)
		if err != nil {
			return fmt.Errorf("list download files: %w", err)
		}
		known := make(map[uuid.UUID]db.DownloadFile, len(files))
		for _, file := range files {
			known[repository.UUIDFromPG(file.ID)] = file
		}
		for fileID := range selections {
			if _, ok := known[fileID]; !ok {
				return NewError("invalid_file", "a selected file does not belong to the download", ErrInvalidInput, map[string]any{"fileId": fileID.String()})
			}
		}
		selectedVideos := 0
		for fileID, selected := range selections {
			file := known[fileID]
			if selected && file.MediaKind == "video" {
				selectedVideos++
			}
			if _, err := scope.Queries.SetDownloadFileSelection(ctx, db.SetDownloadFileSelectionParams{
				ID:       repository.UUIDToPG(fileID),
				Selected: selected,
			}); err != nil {
				return fmt.Errorf("update file selection: %w", err)
			}
		}
		if selectedVideos == 0 {
			return NewError("no_main_video", "at least one video file must be selected", ErrInvalidInput, map[string]any{})
		}
		if current.Status == "completed" {
			if _, err := scope.Queries.MarkDownloadSelectingFiles(ctx, repository.UUIDToPG(downloadID)); err != nil {
				return fmt.Errorf("mark download selecting files: %w", err)
			}
		}
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindDownloadMaterialize,
			ResourceType:   "download",
			ResourceID:     downloadID,
			IdempotencyKey: idempotencyKey,
			MaxAttempts:    3,
			Timeout:        10 * time.Minute,
			Payload:        map[string]any{},
			ActorUserID:    actorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule download materialize: %w", err)
		}
		operation = scheduled.Operation
		if err := appendResourceEvent(ctx, scope.Queries, "download", downloadID, scheduled.Operation.ID, actorUserID, "download.file_selection_saved", map[string]any{}); err != nil {
			return err
		}
		updated, err := scope.Queries.GetDownloadByID(ctx, repository.UUIDToPG(downloadID))
		if err != nil {
			return fmt.Errorf("reload download: %w", err)
		}
		updatedFiles, err := scope.Queries.ListDownloadFiles(ctx, updated.ID)
		if err != nil {
			return fmt.Errorf("list download files: %w", err)
		}
		view = downloadViewFromDB(updated, updatedFiles)
		return nil
	})
	if err != nil {
		return domain.DownloadView{}, domain.Operation{}, err
	}
	return view, operation, nil
}
