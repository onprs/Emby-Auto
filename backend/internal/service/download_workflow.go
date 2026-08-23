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
	"github.com/jackc/pgx/v5/pgconn"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type DownloadWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
	operations *OperationScheduler
}

func NewDownloadWorkflow(
	queries *db.Queries,
	transactor *database.Transactor,
	operations *OperationScheduler,
) *DownloadWorkflow {
	return &DownloadWorkflow{queries: queries, transactor: transactor, operations: operations}
}

func (workflow *DownloadWorkflow) LoadEnqueueCommand(
	ctx context.Context,
	downloadID uuid.UUID,
) (domain.DownloadEnqueueCommand, error) {
	row, err := workflow.queries.GetDownloadEnqueueCommand(ctx, repository.UUIDToPG(downloadID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DownloadEnqueueCommand{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.DownloadEnqueueCommand{}, fmt.Errorf("load download enqueue command: %w", err)
	}
	return domain.DownloadEnqueueCommand{
		DownloadID:           repository.UUIDFromPG(row.ID),
		AcquisitionID:        repository.UUIDFromPG(row.AcquisitionID),
		Status:               domain.DownloadState(row.Status),
		SourceURI:            row.SourceUri,
		TorrentHash:          stringValue(row.TorrentHash),
		FileResolutionSource: stringValue(row.FileResolutionSource),
	}, nil
}

func (workflow *DownloadWorkflow) CompleteEnqueue(
	ctx context.Context,
	completion domain.DownloadEnqueueCompletion,
) error {
	if completion.OperationID == uuid.Nil || completion.DownloadID == uuid.Nil {
		return fmt.Errorf("download enqueue completion requires operation and download IDs")
	}
	if strings.TrimSpace(completion.TorrentHash) == "" || strings.TrimSpace(completion.SavePath) == "" {
		return fmt.Errorf("download enqueue completion requires torrent hash and save path")
	}
	if len(completion.Files) == 0 {
		return fmt.Errorf("download enqueue completion requires file metadata")
	}
	if completion.Outcome != domain.DownloadManifestResolved && completion.Outcome != domain.DownloadManifestUnresolved && completion.Outcome != domain.DownloadManifestHardRejected {
		return fmt.Errorf("download enqueue completion has an invalid manifest outcome")
	}

	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockDownloadForEnqueue(ctx, repository.UUIDToPG(completion.DownloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock download for enqueue completion: %w", err)
		}
		if locked.Status != string(domain.DownloadEnqueuePending) {
			if strings.EqualFold(stringValue(locked.TorrentHash), completion.TorrentHash) &&
				(locked.Status == string(domain.DownloadFileResolutionPending) || locked.Status == string(domain.DownloadDownloading) || locked.Status == string(domain.DownloadCompleted) || locked.Status == string(domain.DownloadSelectingFiles) || locked.Status == string(domain.DownloadMaterialized) || (locked.Status == string(domain.DownloadFailed) && stringValue(locked.FailureStage) == "file_resolution")) {
				return nil
			}
			return fmt.Errorf("persist download manifest: %w", &domain.TransitionError{
				Machine: "download", From: locked.Status, To: string(domain.DownloadFileResolutionPending),
			})
		}
		if err := domain.ValidateDownloadTransition(domain.DownloadState(locked.Status), domain.DownloadFileResolutionPending); err != nil {
			return err
		}

		torrentHash := strings.ToLower(completion.TorrentHash)
		savePath := completion.SavePath
		var resolutionSource *string
		if completion.Outcome == domain.DownloadManifestResolved {
			source := string(domain.DecisionSourceDeterministic)
			resolutionSource = &source
		}
		updated, err := scope.Queries.MarkDownloadManifestPending(ctx, db.MarkDownloadManifestPendingParams{
			TorrentHash: &torrentHash, SavePath: &savePath, FileResolutionSource: resolutionSource,
			ID: repository.UUIDToPG(completion.DownloadID),
		})
		if err != nil {
			var databaseError *pgconn.PgError
			if errors.As(err, &databaseError) && databaseError.ConstraintName == "downloads_torrent_hash_unique" {
				return fmt.Errorf("%w: %s", domain.ErrDuplicateTorrent, torrentHash)
			}
			return fmt.Errorf("persist download manifest: %w", err)
		}
		selectedCount := 0
		for _, file := range completion.Files {
			if file.Index > math.MaxInt32 || file.SourceSeason > math.MaxInt32 || file.SourceEpisode > math.MaxInt32 {
				return fmt.Errorf("download file metadata exceeds database integer range")
			}
			if file.Selected {
				selectedCount++
			}
			if _, err := scope.Queries.CreateDownloadFile(ctx, db.CreateDownloadFileParams{
				ID: repository.UUIDToPG(uuid.New()), DownloadID: repository.UUIDToPG(completion.DownloadID),
				FileIndex: int32(file.Index), RelativePath: file.RelativePath, SizeBytes: file.SizeBytes,
				MediaKind: string(file.Kind), Selected: file.Selected,
				SourceSeason: optionalInt32(file.SourceSeason), SourceEpisode: optionalInt32(file.SourceEpisode),
				Language: optionalString(file.Language),
			}); err != nil {
				return fmt.Errorf("persist download file %d: %w", file.Index, err)
			}
		}
		if _, err := scope.Queries.MarkDownloadRSSEntryEnqueued(ctx, repository.UUIDToPG(completion.DownloadID)); err != nil {
			return fmt.Errorf("mark RSS entry enqueued: %w", err)
		}

		if err := appendResourceEvent(ctx, scope.Queries, "download", completion.DownloadID, completion.OperationID, uuid.Nil, "download.manifest_persisted", map[string]any{
			"status": updated.Status, "fileCount": len(completion.Files), "selectedCount": selectedCount,
			"outcome": completion.Outcome, "reasonCode": completion.ReasonCode,
		}); err != nil {
			return err
		}

		switch completion.Outcome {
		case domain.DownloadManifestResolved:
			if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
				Kind: appqueue.KindDownloadSelectionApply, ResourceType: "download", ResourceID: completion.DownloadID,
				IdempotencyKey: "download.selection.apply:" + completion.DownloadID.String() + ":" + torrentHash,
				MaxAttempts:    5, Timeout: time.Minute, Payload: map[string]any{},
			}); err != nil {
				return fmt.Errorf("schedule download selection apply: %w", err)
			}
		case domain.DownloadManifestHardRejected:
			code := completion.ReasonCode
			if code == "" {
				code = "download_file_resolution_invalid"
			}
			message := "the torrent manifest has no supported main video"
			if _, err := scope.Queries.MarkDownloadFileResolutionTerminalFailure(ctx, db.MarkDownloadFileResolutionTerminalFailureParams{
				ErrorCode: &code, ErrorMessage: &message, ID: repository.UUIDToPG(completion.DownloadID),
			}); err != nil {
				return fmt.Errorf("reject download manifest: %w", err)
			}
			if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
				Kind: appqueue.KindDownloadCancel, ResourceType: "download", ResourceID: completion.DownloadID,
				IdempotencyKey: "download.cancel:hard-rejected:" + completion.DownloadID.String() + ":" + torrentHash + ":" + completion.OperationID.String(),
				MaxAttempts:    3, Timeout: 2 * time.Minute,
				Payload: map[string]any{"command": "file_resolution_rejected", "deleteFiles": false},
			}); err != nil {
				return fmt.Errorf("schedule rejected torrent removal: %w", err)
			}
		}
		return nil
	})
}

func (workflow *DownloadWorkflow) CompleteLegacyEnqueue(ctx context.Context, completion domain.DownloadEnqueueCompletion) error {
	if completion.OperationID == uuid.Nil || completion.DownloadID == uuid.Nil {
		return fmt.Errorf("legacy download enqueue completion requires operation and download IDs")
	}
	if strings.TrimSpace(completion.TorrentHash) == "" || strings.TrimSpace(completion.SavePath) == "" || len(completion.Files) == 0 {
		return fmt.Errorf("legacy download enqueue completion requires torrent metadata and files")
	}
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockDownloadForEnqueue(ctx, repository.UUIDToPG(completion.DownloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock legacy download enqueue: %w", err)
		}
		if locked.Status != string(domain.DownloadEnqueuePending) {
			if locked.Status == string(domain.DownloadDownloading) && strings.EqualFold(stringValue(locked.TorrentHash), completion.TorrentHash) {
				return nil
			}
			return fmt.Errorf("complete legacy download enqueue: %w", &domain.TransitionError{
				Machine: "download", From: locked.Status, To: string(domain.DownloadDownloading),
			})
		}
		if err := domain.ValidateDownloadTransition(domain.DownloadState(locked.Status), domain.DownloadDownloading); err != nil {
			return err
		}
		torrentHash := strings.ToLower(completion.TorrentHash)
		savePath := completion.SavePath
		if _, err := scope.Queries.MarkDownloadLegacyEnqueued(ctx, db.MarkDownloadLegacyEnqueuedParams{
			TorrentHash: &torrentHash, SavePath: &savePath, ID: repository.UUIDToPG(completion.DownloadID),
		}); err != nil {
			var databaseError *pgconn.PgError
			if errors.As(err, &databaseError) && databaseError.ConstraintName == "downloads_torrent_hash_unique" {
				return fmt.Errorf("%w: %s", domain.ErrDuplicateTorrent, torrentHash)
			}
			return fmt.Errorf("mark legacy download enqueued: %w", err)
		}
		selectedCount := 0
		for _, file := range completion.Files {
			if file.Index > math.MaxInt32 || file.SourceSeason > math.MaxInt32 || file.SourceEpisode > math.MaxInt32 {
				return fmt.Errorf("download file metadata exceeds database integer range")
			}
			if file.Selected {
				selectedCount++
			}
			if _, err := scope.Queries.CreateDownloadFile(ctx, db.CreateDownloadFileParams{
				ID: repository.UUIDToPG(uuid.New()), DownloadID: repository.UUIDToPG(completion.DownloadID),
				FileIndex: int32(file.Index), RelativePath: file.RelativePath, SizeBytes: file.SizeBytes,
				MediaKind: string(file.Kind), Selected: file.Selected,
				SourceSeason: optionalInt32(file.SourceSeason), SourceEpisode: optionalInt32(file.SourceEpisode),
				Language: optionalString(file.Language),
			}); err != nil {
				return fmt.Errorf("persist legacy download file %d: %w", file.Index, err)
			}
		}
		if _, err := scope.Queries.MarkDownloadRSSEntryEnqueued(ctx, repository.UUIDToPG(completion.DownloadID)); err != nil {
			return fmt.Errorf("mark legacy RSS entry enqueued: %w", err)
		}
		if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind: appqueue.KindDownloadSync, ResourceType: "download", ResourceID: completion.DownloadID,
			IdempotencyKey: "download.sync:" + completion.DownloadID.String() + ":" + torrentHash,
			MaxAttempts:    5, Timeout: 30 * time.Second, Payload: map[string]any{},
		}); err != nil {
			return fmt.Errorf("schedule legacy download sync: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "download", completion.DownloadID, completion.OperationID, uuid.Nil, "download.enqueued", map[string]any{
			"status": domain.DownloadDownloading, "fileCount": len(completion.Files), "selectedCount": selectedCount,
			"resolutionSource": domain.DecisionSourceDeterministic,
		})
	})
}

func (workflow *DownloadWorkflow) LoadSelectionApplyCommand(ctx context.Context, downloadID uuid.UUID) (domain.DownloadSelectionApplyCommand, error) {
	download, err := workflow.queries.GetDownloadByID(ctx, repository.UUIDToPG(downloadID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DownloadSelectionApplyCommand{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.DownloadSelectionApplyCommand{}, fmt.Errorf("load download selection apply command: %w", err)
	}
	files, err := workflow.queries.ListDownloadFiles(ctx, download.ID)
	if err != nil {
		return domain.DownloadSelectionApplyCommand{}, fmt.Errorf("list selection apply files: %w", err)
	}
	command := domain.DownloadSelectionApplyCommand{
		DownloadID: repository.UUIDFromPG(download.ID), AcquisitionID: repository.UUIDFromPG(download.AcquisitionID),
		Status:      domain.DownloadState(download.Status),
		TorrentHash: stringValue(download.TorrentHash), AllFileIndexes: make([]int, 0, len(files)),
	}
	for _, file := range files {
		command.AllFileIndexes = append(command.AllFileIndexes, int(file.FileIndex))
		if file.Selected {
			command.SelectedFileIndexes = append(command.SelectedFileIndexes, int(file.FileIndex))
		}
	}
	return command, nil
}

func (workflow *DownloadWorkflow) CompleteSelectionApply(ctx context.Context, downloadID, operationID uuid.UUID) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockDownloadForEnqueue(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock selection apply download: %w", err)
		}
		if locked.Status == string(domain.DownloadDownloading) || locked.Status == string(domain.DownloadCompleted) || locked.Status == string(domain.DownloadSelectingFiles) || locked.Status == string(domain.DownloadMaterialized) {
			return nil
		}
		if err := domain.ValidateDownloadTransition(domain.DownloadState(locked.Status), domain.DownloadDownloading); err != nil {
			return err
		}
		updated, err := scope.Queries.MarkDownloadSelectionApplied(ctx, repository.UUIDToPG(downloadID))
		if err != nil {
			return fmt.Errorf("mark download selection applied: %w", err)
		}
		if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind: appqueue.KindDownloadSync, ResourceType: "download", ResourceID: downloadID,
			IdempotencyKey: "download.sync:" + downloadID.String() + ":" + stringValue(updated.TorrentHash),
			MaxAttempts:    5, Timeout: 30 * time.Second, Payload: map[string]any{},
		}); err != nil {
			return fmt.Errorf("schedule download sync: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "download", downloadID, operationID, uuid.Nil, "download.selection_applied", map[string]any{
			"status": domain.DownloadDownloading,
		})
	})
}

func (workflow *DownloadWorkflow) LoadSyncCommand(
	ctx context.Context,
	downloadID uuid.UUID,
) (domain.DownloadSyncCommand, error) {
	row, err := workflow.queries.GetDownloadSyncCommand(ctx, repository.UUIDToPG(downloadID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DownloadSyncCommand{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.DownloadSyncCommand{}, fmt.Errorf("load download sync command: %w", err)
	}
	command := domain.DownloadSyncCommand{
		DownloadID:  repository.UUIDFromPG(row.ID),
		Status:      domain.DownloadState(row.Status),
		TorrentHash: stringValue(row.TorrentHash),
		ClientState: stringValue(row.ClientState),
	}
	if row.LastSyncedAt.Valid {
		value := row.LastSyncedAt.Time
		command.LastSyncedAt = &value
	}
	return command, nil
}

func (workflow *DownloadWorkflow) RecordProgress(
	ctx context.Context,
	downloadID uuid.UUID,
	operationID uuid.UUID,
	progress float64,
	clientState string,
) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	scaled := int64(progress * 100_000)
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		updated, err := scope.Queries.UpdateDownloadProgress(ctx, db.UpdateDownloadProgressParams{
			ProgressScaled: scaled,
			ClientState:    optionalString(clientState),
			ID:             repository.UUIDToPG(downloadID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("update download progress: %w", err)
		}
		eventData, err := json.Marshal(map[string]any{"progress": progress, "clientState": clientState})
		if err != nil {
			return fmt.Errorf("encode download progress event: %w", err)
		}
		resourceType := "download"
		if _, err := scope.Queries.AppendEvent(ctx, db.AppendEventParams{
			ID:           repository.UUIDToPG(uuid.New()),
			Topic:        "download.progressed",
			ResourceType: &resourceType,
			ResourceID:   updated.ID,
			OperationID:  repository.UUIDToPG(operationID),
			Data:         eventData,
		}); err != nil {
			return fmt.Errorf("append download progress event: %w", err)
		}
		return nil
	})
}

func (workflow *DownloadWorkflow) CompleteDownload(
	ctx context.Context,
	downloadID uuid.UUID,
	operationID uuid.UUID,
	clientState string,
) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockDownloadForEnqueue(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock completed download: %w", err)
		}
		if locked.Status == string(domain.DownloadCompleted) || locked.Status == string(domain.DownloadSelectingFiles) || locked.Status == string(domain.DownloadMaterialized) {
			return nil
		}
		if err := domain.ValidateDownloadTransition(domain.DownloadState(locked.Status), domain.DownloadCompleted); err != nil {
			return err
		}
		updated, err := scope.Queries.MarkDownloadCompleted(ctx, db.MarkDownloadCompletedParams{
			ClientState: optionalString(clientState),
			ID:          repository.UUIDToPG(downloadID),
		})
		if err != nil {
			return fmt.Errorf("mark download completed: %w", err)
		}
		eventData, err := json.Marshal(map[string]any{"status": domain.DownloadCompleted, "progress": 1, "clientState": clientState})
		if err != nil {
			return fmt.Errorf("encode download completed event: %w", err)
		}
		resourceType := "download"
		if _, err := scope.Queries.AppendEvent(ctx, db.AppendEventParams{
			ID:           repository.UUIDToPG(uuid.New()),
			Topic:        "download.completed",
			ResourceType: &resourceType,
			ResourceID:   updated.ID,
			OperationID:  repository.UUIDToPG(operationID),
			Data:         eventData,
		}); err != nil {
			return fmt.Errorf("append download completed event: %w", err)
		}
		if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindDownloadMaterialize,
			ResourceType:   "download",
			ResourceID:     downloadID,
			IdempotencyKey: "download.materialize:" + downloadID.String(),
			MaxAttempts:    3,
			Timeout:        2 * time.Minute,
			Payload:        map[string]any{},
		}); err != nil {
			return fmt.Errorf("schedule download materialization: %w", err)
		}
		return nil
	})
}

func (workflow *DownloadWorkflow) CompleteRemoval(ctx context.Context, downloadID, operationID uuid.UUID) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		removed, err := scope.Queries.MarkDownloadRemoved(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			existing, loadErr := scope.Queries.GetDownloadByIDIncludingDeleted(ctx, repository.UUIDToPG(downloadID))
			if loadErr == nil && existing.DeletedAt.Valid {
				return nil
			}
			if errors.Is(loadErr, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return loadErr
		}
		if err != nil {
			return fmt.Errorf("mark download removed: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "download", downloadID, operationID, uuid.Nil, "download.removed", map[string]any{
			"deletedAt": removed.DeletedAt.Time,
		})
	})
}

func (workflow *DownloadWorkflow) DownloadCancellationReady(ctx context.Context, downloadID, operationID uuid.UUID) (bool, error) {
	resourceType := "download"
	count, err := workflow.queries.CountOtherActiveResourceOperations(ctx, db.CountOtherActiveResourceOperationsParams{
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(downloadID),
		OperationID:  repository.UUIDToPG(operationID),
	})
	if err != nil {
		return false, fmt.Errorf("count active download operations: %w", err)
	}
	return count == 0, nil
}

func optionalInt32(value int) *int32 {
	if value <= 0 {
		return nil
	}
	converted := int32(value)
	return &converted
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
