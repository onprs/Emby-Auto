package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// AcquisitionDeletionWorkflow owns the command and persistence boundary for
// deleting one complete lifecycle task. Files are removed by the Worker before
// CompleteDeletion physically removes the database graph.
type AcquisitionDeletionWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
	operations *OperationScheduler
}

func NewAcquisitionDeletionWorkflow(queries *db.Queries, transactor *database.Transactor, operations *OperationScheduler) *AcquisitionDeletionWorkflow {
	return &AcquisitionDeletionWorkflow{queries: queries, transactor: transactor, operations: operations}
}

func (workflow *AcquisitionDeletionWorkflow) RequestDeletion(
	ctx context.Context,
	acquisitionID uuid.UUID,
	idempotencyKey string,
	actorUserID uuid.UUID,
) (domain.Operation, error) {
	if acquisitionID == uuid.Nil || strings.TrimSpace(idempotencyKey) == "" {
		return domain.Operation{}, NewError("invalid_acquisition_deletion", "acquisition and idempotency key are required", ErrInvalidInput, map[string]any{})
	}
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, replayed, err := findIdempotentResourceCommand(
			ctx, scope, idempotencyKey, "acquisition", acquisitionID, "delete", appqueue.KindAcquisitionDelete,
		)
		if err != nil {
			return err
		}
		if replayed {
			operation = existing
			return nil
		}
		if err := prepareAcquisitionDeletionInTx(ctx, scope, acquisitionID, false, uuid.Nil); err != nil {
			return err
		}
		operation, err = workflow.scheduleDeletionInTx(ctx, scope, acquisitionID, idempotencyKey, actorUserID, map[string]any{})
		return err
	})
	if err != nil {
		var serviceErr *Error
		if errors.As(err, &serviceErr) || errors.Is(err, domain.ErrNotFound) {
			return domain.Operation{}, err
		}
		return domain.Operation{}, fmt.Errorf("request acquisition deletion: %w", err)
	}
	return operation, nil
}

// RequestDownloadDeletion keeps the legacy download delete endpoint aligned
// with the acquisition-owned lifecycle boundary.
func (workflow *AcquisitionDeletionWorkflow) RequestDownloadDeletion(
	ctx context.Context,
	downloadID uuid.UUID,
	expectedVersion int32,
	idempotencyKey string,
	actorUserID uuid.UUID,
) (domain.Operation, error) {
	if downloadID == uuid.Nil || expectedVersion <= 0 || strings.TrimSpace(idempotencyKey) == "" {
		return domain.Operation{}, NewError("invalid_download_deletion", "download, version and idempotency key are required", ErrInvalidInput, map[string]any{})
	}
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, err := scope.Queries.GetOperationByIdempotencyKey(ctx, strings.TrimSpace(idempotencyKey))
		if err == nil {
			var payload struct {
				Command          string    `json:"command"`
				OriginDownloadID uuid.UUID `json:"originDownloadId"`
			}
			matches := json.Unmarshal(existing.Payload, &payload) == nil &&
				existing.Kind == appqueue.KindAcquisitionDelete && payload.Command == "delete" && payload.OriginDownloadID == downloadID
			if !matches {
				return NewError("idempotency_conflict", "the idempotency key was already used for a different command", ErrStateConflict, map[string]any{"idempotencyKey": idempotencyKey})
			}
			operation = operationFromDB(existing)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load idempotent download deletion: %w", err)
		}

		acquisitionRow, err := scope.Queries.GetDownloadAcquisitionDeletionTarget(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load download deletion target: %w", err)
		}
		acquisitionID := repository.UUIDFromPG(acquisitionRow)
		locked, err := scope.Queries.LockAcquisitionForDeletion(ctx, repository.UUIDToPG(acquisitionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock download acquisition for deletion: %w", err)
		}
		download, err := scope.Queries.LockDownloadAcquisitionDeletionTarget(ctx, db.LockDownloadAcquisitionDeletionTargetParams{
			ID: repository.UUIDToPG(downloadID), AcquisitionID: repository.UUIDToPG(acquisitionID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock download deletion target: %w", err)
		}
		if download.Version != expectedVersion {
			return NewError("version_conflict", "the download changed before deletion", ErrStateConflict, map[string]any{
				"downloadId": downloadID, "expectedVersion": expectedVersion, "actualVersion": download.Version,
			})
		}
		if err := prepareLockedAcquisitionDeletionInTx(ctx, scope, acquisitionID, locked.DeletionRequestedAt.Valid, false, uuid.Nil); err != nil {
			return err
		}
		operation, err = workflow.scheduleDeletionInTx(ctx, scope, acquisitionID, idempotencyKey, actorUserID, map[string]any{"originDownloadId": downloadID})
		return err
	})
	if err != nil {
		var serviceErr *Error
		if errors.As(err, &serviceErr) || errors.Is(err, domain.ErrNotFound) {
			return domain.Operation{}, err
		}
		return domain.Operation{}, fmt.Errorf("request download lifecycle deletion: %w", err)
	}
	return operation, nil
}

func (workflow *AcquisitionDeletionWorkflow) scheduleDeletionInTx(
	ctx context.Context,
	scope database.TxScope,
	acquisitionID uuid.UUID,
	idempotencyKey string,
	actorUserID uuid.UUID,
	extraPayload map[string]any,
) (domain.Operation, error) {
	payload := map[string]any{"command": "delete", "actorUserId": actorUserID}
	for key, value := range extraPayload {
		payload[key] = value
	}
	scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
		Kind: appqueue.KindAcquisitionDelete, ResourceType: "acquisition", ResourceID: acquisitionID,
		IdempotencyKey: idempotencyKey, MaxAttempts: 5, Timeout: 30 * time.Minute,
		Payload: payload, ActorUserID: actorUserID,
	})
	if err != nil {
		return domain.Operation{}, fmt.Errorf("schedule acquisition deletion: %w", err)
	}
	operation := scheduled.Operation
	if err := appendResourceEvent(ctx, scope.Queries, "acquisition", acquisitionID, operation.ID, actorUserID, "acquisition.delete_requested", map[string]any{}); err != nil {
		return domain.Operation{}, err
	}
	return operation, nil
}

// prepareAcquisitionDeletionInTx prevents new list reads, requests cancellation
// of all related workers, and moves active state machines to cancelled in one
// transaction. RSS deletion calls the same helper for every owned acquisition.
func prepareAcquisitionDeletionInTx(ctx context.Context, scope database.TxScope, acquisitionID uuid.UUID, allowExisting bool, excludedOperationID uuid.UUID) error {
	locked, err := scope.Queries.LockAcquisitionForDeletion(ctx, repository.UUIDToPG(acquisitionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock acquisition for deletion: %w", err)
	}
	return prepareLockedAcquisitionDeletionInTx(ctx, scope, acquisitionID, locked.DeletionRequestedAt.Valid, allowExisting, excludedOperationID)
}

func prepareLockedAcquisitionDeletionInTx(ctx context.Context, scope database.TxScope, acquisitionID uuid.UUID, alreadyRequested, allowExisting bool, excludedOperationID uuid.UUID) error {
	if alreadyRequested && !allowExisting {
		return NewError("deletion_in_progress", "the task is already being deleted", ErrStateConflict, map[string]any{"acquisitionId": acquisitionID})
	}
	if _, err := scope.Queries.RequestAcquisitionOperationCancellations(ctx, db.RequestAcquisitionOperationCancellationsParams{
		ExcludedOperationID: repository.UUIDToPG(excludedOperationID),
		AcquisitionID:       repository.UUIDToPG(acquisitionID),
	}); err != nil {
		return fmt.Errorf("request acquisition operation cancellations: %w", err)
	}
	if _, err := scope.Queries.CancelAcquisitionTasksForDeletion(ctx, repository.UUIDToPG(acquisitionID)); err != nil {
		return fmt.Errorf("cancel acquisition tasks for deletion: %w", err)
	}
	if _, err := scope.Queries.CancelAcquisitionDownloadsForDeletion(ctx, repository.UUIDToPG(acquisitionID)); err != nil {
		return fmt.Errorf("cancel acquisition downloads for deletion: %w", err)
	}
	if !alreadyRequested {
		if _, err := scope.Queries.MarkAcquisitionDeletionRequested(ctx, repository.UUIDToPG(acquisitionID)); err != nil {
			return fmt.Errorf("mark acquisition deletion requested: %w", err)
		}
	}
	return nil
}

func (workflow *AcquisitionDeletionWorkflow) DeletionReady(ctx context.Context, acquisitionID, operationID uuid.UUID) (bool, error) {
	count, err := workflow.queries.CountAcquisitionDeletionActiveOperations(ctx, db.CountAcquisitionDeletionActiveOperationsParams{
		OperationID: repository.UUIDToPG(operationID), AcquisitionID: repository.UUIDToPG(acquisitionID),
	})
	if err != nil {
		return false, fmt.Errorf("count active acquisition operations: %w", err)
	}
	return count == 0, nil
}

func (workflow *AcquisitionDeletionWorkflow) LoadDeletionCommand(ctx context.Context, acquisitionID uuid.UUID) (domain.AcquisitionDeletionCommand, error) {
	command := domain.AcquisitionDeletionCommand{AcquisitionID: acquisitionID}
	taskRows, err := workflow.queries.ListAcquisitionDeletionTaskIDs(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return domain.AcquisitionDeletionCommand{}, fmt.Errorf("list acquisition deletion tasks: %w", err)
	}
	command.TaskIDs = make([]uuid.UUID, 0, len(taskRows))
	for _, taskID := range taskRows {
		command.TaskIDs = append(command.TaskIDs, repository.UUIDFromPG(taskID))
	}
	command.ArtifactPaths, err = workflow.queries.ListAcquisitionDeletionArtifactPaths(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return domain.AcquisitionDeletionCommand{}, fmt.Errorf("list acquisition deletion artifacts: %w", err)
	}
	libraryRows, err := workflow.queries.ListAcquisitionDeletionLibraryFiles(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return domain.AcquisitionDeletionCommand{}, fmt.Errorf("list acquisition deletion library files: %w", err)
	}
	command.LibraryFiles = make([]domain.AcquisitionDeletionLibraryFile, 0, len(libraryRows))
	for _, row := range libraryRows {
		command.LibraryFiles = append(command.LibraryFiles, domain.AcquisitionDeletionLibraryFile{FilePath: row.FilePath, Preserve: row.Preserve})
	}
	downloadRows, err := workflow.queries.ListAcquisitionDeletionDownloads(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return domain.AcquisitionDeletionCommand{}, fmt.Errorf("list acquisition deletion downloads: %w", err)
	}
	command.Downloads = make([]domain.AcquisitionDeletionDownload, 0, len(downloadRows))
	for _, row := range downloadRows {
		command.Downloads = append(command.Downloads, domain.AcquisitionDeletionDownload{
			ID: repository.UUIDFromPG(row.ID), TorrentHash: valueOrEmpty(row.TorrentHash), SavePath: valueOrEmpty(row.SavePath),
			PreserveTorrent: row.PreserveTorrent, PreservePath: row.PreservePath,
		})
	}
	return command, nil
}

func (workflow *AcquisitionDeletionWorkflow) CompleteDeletion(ctx context.Context, acquisitionID, operationID uuid.UUID) (domain.AcquisitionDeletionResult, error) {
	result := domain.AcquisitionDeletionResult{AcquisitionID: acquisitionID}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockAcquisitionForDeletion(ctx, repository.UUIDToPG(acquisitionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock completed acquisition deletion: %w", err)
		}
		if !locked.DeletionRequestedAt.Valid {
			return NewError("deletion_not_requested", "the task deletion was not requested", ErrStateConflict, map[string]any{"acquisitionId": acquisitionID})
		}
		tasks, err := scope.Queries.ListAcquisitionDeletionTaskIDs(ctx, repository.UUIDToPG(acquisitionID))
		if err != nil {
			return fmt.Errorf("count deleted acquisition tasks: %w", err)
		}
		downloads, err := scope.Queries.ListAcquisitionDeletionDownloads(ctx, repository.UUIDToPG(acquisitionID))
		if err != nil {
			return fmt.Errorf("count deleted acquisition downloads: %w", err)
		}
		artifacts, err := scope.Queries.ListAcquisitionDeletionArtifactPaths(ctx, repository.UUIDToPG(acquisitionID))
		if err != nil {
			return fmt.Errorf("count deleted acquisition artifacts: %w", err)
		}
		result.TasksRemoved = int64(len(tasks))
		result.Downloads = len(downloads)
		result.Artifacts = len(artifacts)
		if err := appendResourceEvent(ctx, scope.Queries, "acquisition", acquisitionID, operationID, uuid.Nil, "acquisition.delete_completed", map[string]any{
			"tasksRemoved": result.TasksRemoved, "downloadsRemoved": result.Downloads, "artifactsRemoved": result.Artifacts,
		}); err != nil {
			return err
		}
		if _, err := scope.Queries.DeleteArtifactSetsForAcquisition(ctx, repository.UUIDToPG(acquisitionID)); err != nil {
			return fmt.Errorf("delete acquisition artifact sets: %w", err)
		}
		if _, err := scope.Queries.DeleteMediaArtifactsForAcquisition(ctx, repository.UUIDToPG(acquisitionID)); err != nil {
			return fmt.Errorf("delete acquisition artifacts: %w", err)
		}
		if _, err := scope.Queries.DeleteEpisodeTasksForAcquisition(ctx, repository.UUIDToPG(acquisitionID)); err != nil {
			return fmt.Errorf("delete acquisition tasks: %w", err)
		}
		deleted, err := scope.Queries.DeleteAcquisitionWorkflow(ctx, repository.UUIDToPG(acquisitionID))
		if err != nil {
			return fmt.Errorf("delete acquisition workflow: %w", err)
		}
		if deleted != 1 {
			return fmt.Errorf("delete acquisition workflow: expected one row, got %d", deleted)
		}
		return nil
	})
	if err != nil {
		return domain.AcquisitionDeletionResult{}, err
	}
	return result, nil
}

func (workflow *AcquisitionDeletionWorkflow) SubscriptionDeletionReady(ctx context.Context, subscriptionID, operationID uuid.UUID) (bool, error) {
	count, err := workflow.queries.CountSubscriptionDeletionActiveOperations(ctx, db.CountSubscriptionDeletionActiveOperationsParams{
		OperationID: repository.UUIDToPG(operationID), SubscriptionID: repository.UUIDToPG(subscriptionID),
	})
	if err != nil {
		return false, fmt.Errorf("count active RSS subscription operations: %w", err)
	}
	return count == 0, nil
}

func (workflow *AcquisitionDeletionWorkflow) ListSubscriptionAcquisitions(ctx context.Context, subscriptionID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := workflow.queries.ListSubscriptionDeletionAcquisitionIDs(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return nil, fmt.Errorf("list subscription acquisitions for deletion: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, repository.UUIDFromPG(row))
	}
	return ids, nil
}

func (workflow *AcquisitionDeletionWorkflow) CompleteSubscriptionCleanup(ctx context.Context, subscriptionID, operationID uuid.UUID) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		subscription, err := scope.Queries.GetRSSPollCommand(ctx, repository.UUIDToPG(subscriptionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load completed RSS cleanup: %w", err)
		}
		if subscription.Enabled {
			return NewError("state_conflict", "completed RSS cleanup requires a disabled subscription", ErrStateConflict, map[string]any{"subscriptionId": subscriptionID})
		}
		if subscription.DeletedAt.Valid {
			if _, err := scope.Queries.RetainArchivedRSSSubscriptionCompletion(ctx, repository.UUIDToPG(subscriptionID)); errors.Is(err, pgx.ErrNoRows) {
				return NewError("state_conflict", "archived RSS subscription could not be retained after completion", ErrStateConflict, map[string]any{"subscriptionId": subscriptionID})
			} else if err != nil {
				return fmt.Errorf("retain archived RSS subscription completion: %w", err)
			}
		}
		return appendRSSEvent(ctx, scope.Queries, subscriptionID, operationID, "rss.subscription.completion_retained", []byte(`{"summary":"订阅已完成，订阅与任务历史已保留"}`))
	})
}

func (workflow *AcquisitionDeletionWorkflow) CompleteSubscriptionDeletion(ctx context.Context, subscriptionID, operationID uuid.UUID) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Queries.GetRSSPollCommand(ctx, repository.UUIDToPG(subscriptionID)); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("load completed RSS deletion: %w", err)
		}
		if err := appendRSSEvent(ctx, scope.Queries, subscriptionID, operationID, "rss.subscription.delete_completed", []byte(`{"summary":"订阅、关联任务、下载与临时文件已删除"}`)); err != nil {
			return err
		}
		deleted, err := scope.Queries.DeleteArchivedRSSSubscription(ctx, repository.UUIDToPG(subscriptionID))
		if err != nil {
			return fmt.Errorf("delete archived RSS subscription: %w", err)
		}
		if deleted != 1 {
			return fmt.Errorf("delete archived RSS subscription: related acquisitions remain")
		}
		return nil
	})
}
