package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

type OperationLifecycle struct {
	queries    *db.Queries
	transactor *database.Transactor
}

func NewOperationLifecycle(queries *db.Queries, transactor *database.Transactor) *OperationLifecycle {
	return &OperationLifecycle{queries: queries, transactor: transactor}
}

func (lifecycle *OperationLifecycle) StartAttempt(
	ctx context.Context,
	operationID uuid.UUID,
	attempt int,
	workerID string,
) (domain.Operation, error) {
	var operation db.Operation
	err := lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Queries.AbandonRunningOperationAttempts(ctx, UUIDToPG(operationID)); err != nil {
			return fmt.Errorf("close interrupted operation attempts: %w", err)
		}
		started, err := scope.Queries.StartOperationAttempt(ctx, UUIDToPG(operationID))
		if errors.Is(err, pgx.ErrNoRows) {
			locked, lockErr := scope.Queries.LockOperation(ctx, UUIDToPG(operationID))
			if lockErr != nil {
				return fmt.Errorf("lock non-runnable operation: %w", lockErr)
			}
			if locked.CancelRequestedAt.Valid && (locked.Status == "queued" || locked.Status == "running") {
				cancelled, cancelErr := scope.Queries.CancelOperation(ctx, UUIDToPG(operationID))
				if cancelErr != nil {
					return fmt.Errorf("cancel operation before attempt: %w", cancelErr)
				}
				operation = cancelled
				if err := cancelOperationResource(ctx, scope.Queries, cancelled); err != nil {
					return err
				}
				return appendOperationEvent(ctx, scope.Queries, cancelled, "operation.cancelled", map[string]any{"beforeAttempt": true})
			}
			return domain.ErrOperationNotRunnable
		}
		if err != nil {
			return fmt.Errorf("start operation attempt: %w", err)
		}
		operation = started
		if _, err := scope.Queries.CreateOperationAttempt(ctx, db.CreateOperationAttemptParams{
			ID:          UUIDToPG(uuid.New()),
			OperationID: UUIDToPG(operationID),
			Attempt:     int32(attempt),
			WorkerID:    &workerID,
		}); err != nil {
			return fmt.Errorf("create operation attempt audit: %w", err)
		}
		return appendOperationEvent(ctx, scope.Queries, operation, "operation.started", map[string]any{"attempt": attempt})
	})
	if err != nil {
		return domain.Operation{}, err
	}
	return mapOperation(operation), nil
}

func (lifecycle *OperationLifecycle) Heartbeat(
	ctx context.Context,
	operationID uuid.UUID,
	attempt int,
) (bool, error) {
	operationRows, err := lifecycle.queries.HeartbeatOperation(ctx, UUIDToPG(operationID))
	if err != nil {
		return false, fmt.Errorf("heartbeat operation: %w", err)
	}
	if operationRows == 0 {
		return false, nil
	}
	attemptRows, err := lifecycle.queries.HeartbeatOperationAttempt(ctx, db.HeartbeatOperationAttemptParams{
		OperationID: UUIDToPG(operationID),
		Attempt:     int32(attempt),
	})
	if err != nil {
		return false, fmt.Errorf("heartbeat operation attempt: %w", err)
	}
	return attemptRows == 1, nil
}

func (lifecycle *OperationLifecycle) SucceedAttempt(
	ctx context.Context,
	operationID uuid.UUID,
	attempt int,
) error {
	return lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Queries.FinishOperationAttempt(ctx, db.FinishOperationAttemptParams{
			Status:      "succeeded",
			OperationID: UUIDToPG(operationID),
			Attempt:     int32(attempt),
		}); err != nil {
			return fmt.Errorf("finish successful operation attempt: %w", err)
		}
		operation, err := scope.Queries.CompleteOperation(ctx, UUIDToPG(operationID))
		if err != nil {
			return fmt.Errorf("complete operation: %w", err)
		}
		return appendOperationEvent(ctx, scope.Queries, operation, "operation.succeeded", map[string]any{"attempt": attempt})
	})
}

func (lifecycle *OperationLifecycle) FailAttempt(
	ctx context.Context,
	operationID uuid.UUID,
	attempt int,
	code string,
	message string,
	retryable bool,
) error {
	return lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockOperation(ctx, UUIDToPG(operationID))
		if err != nil {
			return fmt.Errorf("lock failed operation: %w", err)
		}
		if locked.CancelRequestedAt.Valid {
			if _, err := scope.Queries.FinishOperationAttempt(ctx, db.FinishOperationAttemptParams{
				Status:      "cancelled",
				OperationID: UUIDToPG(operationID),
				Attempt:     int32(attempt),
			}); err != nil {
				return fmt.Errorf("finish cancelled operation attempt: %w", err)
			}
			operation, err := scope.Queries.CancelOperation(ctx, UUIDToPG(operationID))
			if err != nil {
				return fmt.Errorf("cancel operation after concurrent request: %w", err)
			}
			if err := cancelOperationResource(ctx, scope.Queries, operation); err != nil {
				return err
			}
			return appendOperationEvent(ctx, scope.Queries, operation, "operation.cancelled", map[string]any{"attempt": attempt})
		}

		if _, err := scope.Queries.FinishOperationAttempt(ctx, db.FinishOperationAttemptParams{
			Status:       "failed",
			ErrorCode:    &code,
			ErrorMessage: &message,
			OperationID:  UUIDToPG(operationID),
			Attempt:      int32(attempt),
		}); err != nil {
			return fmt.Errorf("finish failed operation attempt: %w", err)
		}
		if retryable && locked.AttemptCount < locked.MaxAttempts {
			operation, retryErr := scope.Queries.RetryOperation(ctx, db.RetryOperationParams{
				ErrorCode:    &code,
				ErrorMessage: &message,
				ID:           UUIDToPG(operationID),
			})
			if retryErr != nil {
				return fmt.Errorf("schedule operation retry: %w", retryErr)
			}
			return appendOperationEvent(ctx, scope.Queries, operation, "operation.retry_scheduled", map[string]any{
				"attempt":   attempt,
				"errorCode": code,
			})
		}

		switch {
		case locked.Kind == "agent.resolve" && valueOrEmpty(locked.ResourceType) == "agent_resolution":
			if err := failAgentResolutionResource(ctx, scope.Queries, locked, "failed", code, message); err != nil {
				return err
			}
		case locked.Kind == "download.enqueue" && valueOrEmpty(locked.ResourceType) == "download":
			if err := failDownloadEnqueueResource(ctx, scope.Queries, locked, code, message, retryable); err != nil {
				return err
			}
		case (locked.Kind == "download.selection.apply" || locked.Kind == "download.sync" || locked.Kind == "download.materialize") && valueOrEmpty(locked.ResourceType) == "download":
			if err := failDownloadPostEnqueueResource(ctx, scope.Queries, locked, code, message); err != nil {
				return err
			}
		case locked.Kind == "search.run" && valueOrEmpty(locked.ResourceType) == "search_run":
			if err := failSearchRunResource(ctx, scope.Queries, locked, code, message); err != nil {
				return err
			}
		case valueOrEmpty(locked.ResourceType) == "episode_task" && (locked.Kind == "transcode.run" || locked.Kind == "subtitle.prepare" || locked.Kind == "media.finalize"):
			if err := failTaskMediaResource(ctx, scope.Queries, locked, code, message); err != nil {
				return err
			}
		case valueOrEmpty(locked.ResourceType) == "episode_task" && locked.Kind == "emby.import":
			if err := failTaskImportResource(ctx, scope.Queries, locked, code, message); err != nil {
				return err
			}
		case valueOrEmpty(locked.ResourceType) == "episode_task" && locked.Kind == "cleanup.run":
			if err := failTaskCleanupResource(ctx, scope.Queries, locked, code, message); err != nil {
				return err
			}
		case valueOrEmpty(locked.ResourceType) == "emby_scan" && locked.Kind == "emby.scan":
			if err := failEmbyScanResource(ctx, scope.Queries, locked, "failed", code, message); err != nil {
				return err
			}
		}
		operation, err := scope.Queries.FailOperation(ctx, db.FailOperationParams{
			ErrorCode:    &code,
			ErrorMessage: &message,
			ID:           UUIDToPG(operationID),
		})
		if err != nil {
			return fmt.Errorf("fail operation: %w", err)
		}
		return appendOperationEvent(ctx, scope.Queries, operation, "operation.failed", map[string]any{
			"attempt":   attempt,
			"errorCode": code,
		})
	})
}

func (lifecycle *OperationLifecycle) CancelAttempt(
	ctx context.Context,
	operationID uuid.UUID,
	attempt int,
) error {
	return lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Queries.FinishOperationAttempt(ctx, db.FinishOperationAttemptParams{
			Status:      "cancelled",
			OperationID: UUIDToPG(operationID),
			Attempt:     int32(attempt),
		}); err != nil {
			return fmt.Errorf("finish cancelled operation attempt: %w", err)
		}
		operation, err := scope.Queries.CancelOperation(ctx, UUIDToPG(operationID))
		if err != nil {
			return fmt.Errorf("cancel operation: %w", err)
		}
		if err := cancelOperationResource(ctx, scope.Queries, operation); err != nil {
			return err
		}
		return appendOperationEvent(ctx, scope.Queries, operation, "operation.cancelled", map[string]any{"attempt": attempt})
	})
}

func (lifecycle *OperationLifecycle) SnoozeAttempt(
	ctx context.Context,
	operationID uuid.UUID,
	attempt int,
) error {
	return lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		deleted, err := scope.Queries.DeleteSnoozedOperationAttempt(ctx, db.DeleteSnoozedOperationAttemptParams{
			OperationID: UUIDToPG(operationID),
			Attempt:     int32(attempt),
		})
		if err != nil {
			return fmt.Errorf("delete snoozed operation attempt: %w", err)
		}
		if deleted != 1 {
			return fmt.Errorf("delete snoozed operation attempt: expected one running attempt, got %d", deleted)
		}
		if _, err := scope.Queries.SnoozeOperation(ctx, UUIDToPG(operationID)); err != nil {
			return fmt.Errorf("snooze operation: %w", err)
		}
		return nil
	})
}

func failDownloadEnqueueResource(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	code string,
	message string,
	retryable bool,
) error {
	download, err := queries.MarkDownloadEnqueueTerminalFailure(ctx, db.MarkDownloadEnqueueTerminalFailureParams{
		ErrorCode:    &code,
		ErrorMessage: &message,
		ID:           operation.ResourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail download enqueue resource: %w", err)
	}
	if _, err := queries.MarkDownloadRSSEntryEnqueueFailed(ctx, db.MarkDownloadRSSEntryEnqueueFailedParams{
		ErrorCode:      &code,
		ErrorMessage:   &message,
		ErrorRetryable: retryable,
		DownloadID:     download.ID,
	}); err != nil {
		return fmt.Errorf("mark download RSS entry enqueue failed: %w", err)
	}
	resourceType := "download"
	data, err := json.Marshal(map[string]any{"status": "failed", "errorCode": code})
	if err != nil {
		return fmt.Errorf("encode download failure event: %w", err)
	}
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           UUIDToPG(uuid.New()),
		Topic:        "download.enqueue_failed",
		ResourceType: &resourceType,
		ResourceID:   download.ID,
		OperationID:  operation.ID,
		Data:         data,
	}); err != nil {
		return fmt.Errorf("append download failure event: %w", err)
	}
	return nil
}

func failDownloadPostEnqueueResource(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	code string,
	message string,
) error {
	var download db.Download
	var err error
	switch operation.Kind {
	case "download.selection.apply":
		download, err = queries.MarkDownloadFileResolutionTerminalFailure(ctx, db.MarkDownloadFileResolutionTerminalFailureParams{
			ErrorCode: &code, ErrorMessage: &message, ID: operation.ResourceID,
		})
	case "download.sync":
		download, err = queries.MarkDownloadSyncTerminalFailure(ctx, db.MarkDownloadSyncTerminalFailureParams{
			ErrorCode: &code, ErrorMessage: &message, ID: operation.ResourceID,
		})
	case "download.materialize":
		download, err = queries.MarkDownloadMaterializeTerminalFailure(ctx, db.MarkDownloadMaterializeTerminalFailureParams{
			ErrorCode: &code, ErrorMessage: &message, ID: operation.ResourceID,
		})
	default:
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail download %s resource: %w", operation.Kind, err)
	}
	stage := "sync"
	if operation.Kind == "download.materialize" {
		stage = "materialize"
	}
	resourceType := "download"
	data, err := json.Marshal(map[string]any{"status": "failed", "failureStage": stage, "errorCode": code})
	if err != nil {
		return fmt.Errorf("encode download failure event: %w", err)
	}
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID: UUIDToPG(uuid.New()), Topic: "download." + stage + "_failed", ResourceType: &resourceType,
		ResourceID: download.ID, OperationID: operation.ID, Data: data,
	}); err != nil {
		return fmt.Errorf("append download failure event: %w", err)
	}
	return nil
}

func failSearchRunResource(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	code string,
	message string,
) error {
	search, err := queries.MarkSearchRunTerminalFailure(ctx, db.MarkSearchRunTerminalFailureParams{
		ErrorCode:    &code,
		ErrorMessage: &message,
		ID:           operation.ResourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail search run resource: %w", err)
	}
	return appendSearchLifecycleEvent(ctx, queries, search.ID, operation.ID, "search.failed", map[string]any{
		"status":    "failed",
		"errorCode": code,
	})
}

func failTaskMediaResource(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	code string,
	message string,
) error {
	params := db.MarkTaskVideoTerminalFailureParams{ErrorCode: &code, ErrorMessage: &message, ID: operation.ResourceID}
	var task db.EpisodeTask
	var err error
	switch operation.Kind {
	case "transcode.run":
		task, err = queries.MarkTaskVideoTerminalFailure(ctx, params)
	case "subtitle.prepare":
		task, err = queries.MarkTaskSubtitleTerminalFailure(ctx, db.MarkTaskSubtitleTerminalFailureParams(params))
	case "media.finalize":
		task, err = queries.MarkTaskFinalizeTerminalFailure(ctx, db.MarkTaskFinalizeTerminalFailureParams(params))
	default:
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail task media resource: %w", err)
	}
	return appendTaskLifecycleEvent(ctx, queries, task.ID, operation.ID, "task.media_failed", map[string]any{
		"kind":      operation.Kind,
		"status":    "failed",
		"errorCode": code,
	})
}

func failTaskImportResource(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	code string,
	message string,
) error {
	if _, err := queries.MarkActiveImportTerminalFailure(ctx, db.MarkActiveImportTerminalFailureParams{
		ErrorCode: &code, ErrorMessage: &message, TaskID: operation.ResourceID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return fmt.Errorf("fail active task import: %w", err)
	}
	task, err := queries.MarkTaskImportTerminalFailure(ctx, db.MarkTaskImportTerminalFailureParams{
		ErrorCode: &code, ErrorMessage: &message, ID: operation.ResourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail imported task resource: %w", err)
	}
	return appendTaskLifecycleEvent(ctx, queries, task.ID, operation.ID, "task.import_failed", map[string]any{
		"status": "failed", "errorCode": code,
	})
}

func failTaskCleanupResource(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	code string,
	message string,
) error {
	cleanup, err := queries.MarkActiveCleanupTerminalFailure(ctx, db.MarkActiveCleanupTerminalFailureParams{
		ErrorCode: &code, ErrorMessage: &message, TaskID: operation.ResourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail active task cleanup: %w", err)
	}
	return appendTaskLifecycleEvent(ctx, queries, cleanup.TaskID, operation.ID, "task.cleanup_failed", map[string]any{
		"status": "failed", "errorCode": code,
	})
}

func cancelOperationResource(ctx context.Context, queries *db.Queries, operation db.Operation) error {
	if operation.Kind == "agent.resolve" && valueOrEmpty(operation.ResourceType) == "agent_resolution" {
		return failAgentResolutionResource(ctx, queries, operation, "cancelled", "operation_cancelled", "the Agent resolution was cancelled")
	}
	if operation.Kind == "emby.scan" && valueOrEmpty(operation.ResourceType) == "emby_scan" {
		return failEmbyScanResource(ctx, queries, operation, "cancelled", "operation_cancelled", "the Emby scan was cancelled")
	}
	if operation.Kind == "search.run" && valueOrEmpty(operation.ResourceType) == "search_run" {
		search, err := queries.MarkSearchRunCancelled(ctx, operation.ResourceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("cancel search run resource: %w", err)
		}
		return appendSearchLifecycleEvent(ctx, queries, search.ID, operation.ID, "search.cancelled", map[string]any{"status": "cancelled"})
	}
	if valueOrEmpty(operation.ResourceType) != "episode_task" {
		return nil
	}
	if operation.Kind == "cleanup.run" {
		cleanup, err := queries.CancelActiveCleanup(ctx, operation.ResourceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("cancel task cleanup resource: %w", err)
		}
		return appendTaskLifecycleEvent(ctx, queries, cleanup.TaskID, operation.ID, "task.cleanup_cancelled", map[string]any{"status": "cancelled"})
	}
	if operation.Kind == "emby.import" {
		if _, err := queries.CancelActiveImport(ctx, operation.ResourceID); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("cancel active task import: %w", err)
		}
		task, err := queries.CancelTaskImport(ctx, operation.ResourceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("cancel task import resource: %w", err)
		}
		return appendTaskLifecycleEvent(ctx, queries, task.ID, operation.ID, "task.import_cancelled", map[string]any{"status": "cancelled"})
	}
	var task db.EpisodeTask
	var err error
	switch operation.Kind {
	case "transcode.run":
		task, err = queries.MarkTaskVideoCancelled(ctx, operation.ResourceID)
	case "subtitle.prepare":
		task, err = queries.MarkTaskSubtitleCancelled(ctx, operation.ResourceID)
	case "media.finalize":
		task, err = queries.MarkTaskFinalizeCancelled(ctx, operation.ResourceID)
	default:
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cancel task media resource: %w", err)
	}
	return appendTaskLifecycleEvent(ctx, queries, task.ID, operation.ID, "task.media_cancelled", map[string]any{
		"kind":   operation.Kind,
		"status": "cancelled",
	})
}

func failAgentResolutionResource(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	status string,
	code string,
	message string,
) error {
	resolution, err := queries.MarkAgentResolutionOperationTerminal(ctx, db.MarkAgentResolutionOperationTerminalParams{
		Status: status, ErrorCode: &code, ErrorMessage: &message, ID: operation.ResourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("finish Agent resolution resource: %w", err)
	}
	topic := "agent.resolution_failed"
	if status == "cancelled" {
		topic = "agent.resolution_cancelled"
	}
	resourceType := "agent_resolution"
	data, _ := json.Marshal(map[string]any{"status": status, "errorCode": code, "capability": resolution.Capability})
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID: UUIDToPG(uuid.New()), Topic: topic, ResourceType: &resourceType,
		ResourceID: resolution.ID, OperationID: operation.ID, Data: data,
	}); err != nil {
		return fmt.Errorf("append Agent resolution terminal event: %w", err)
	}
	return nil
}

func failEmbyScanResource(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	status string,
	code string,
	message string,
) error {
	scan, err := queries.FailEmbyScanRun(ctx, db.FailEmbyScanRunParams{
		Status:       status,
		ErrorCode:    &code,
		ErrorMessage: &message,
		ID:           operation.ResourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail Emby scan resource: %w", err)
	}
	topic := "emby.scan_failed"
	if status == "cancelled" {
		topic = "emby.scan_cancelled"
	}
	data, err := json.Marshal(map[string]any{"status": status, "errorCode": code})
	if err != nil {
		return fmt.Errorf("encode Emby scan lifecycle event: %w", err)
	}
	resourceType := "emby_scan"
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   scan.ID,
		OperationID:  operation.ID,
		Data:         data,
	}); err != nil {
		return fmt.Errorf("append Emby scan lifecycle event: %w", err)
	}
	return nil
}

func appendTaskLifecycleEvent(
	ctx context.Context,
	queries *db.Queries,
	taskID pgtype.UUID,
	operationID pgtype.UUID,
	topic string,
	data map[string]any,
) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode task lifecycle event: %w", err)
	}
	resourceType := "episode_task"
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   taskID,
		OperationID:  operationID,
		Data:         encoded,
	}); err != nil {
		return fmt.Errorf("append task lifecycle event: %w", err)
	}
	return nil
}

func appendSearchLifecycleEvent(
	ctx context.Context,
	queries *db.Queries,
	searchID pgtype.UUID,
	operationID pgtype.UUID,
	topic string,
	data map[string]any,
) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode search lifecycle event: %w", err)
	}
	resourceType := "search_run"
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   searchID,
		OperationID:  operationID,
		Data:         encoded,
	}); err != nil {
		return fmt.Errorf("append search lifecycle event: %w", err)
	}
	return nil
}

func appendOperationEvent(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	topic string,
	data map[string]any,
) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode operation lifecycle event: %w", err)
	}
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: operation.ResourceType,
		ResourceID:   operation.ResourceID,
		OperationID:  operation.ID,
		Data:         encoded,
	}); err != nil {
		return fmt.Errorf("append operation lifecycle event: %w", err)
	}
	return nil
}

func mapOperation(operation db.Operation) domain.Operation {
	return domain.Operation{
		ID:             UUIDFromPG(operation.ID),
		Kind:           operation.Kind,
		ResourceType:   valueOrEmpty(operation.ResourceType),
		ResourceID:     UUIDFromPG(operation.ResourceID),
		IdempotencyKey: operation.IdempotencyKey,
		Status:         operation.Status,
		RiverJobID:     int64Value(operation.RiverJobID),
		MaxAttempts:    int(operation.MaxAttempts),
		AttemptCount:   int(operation.AttemptCount),
		Timeout:        durationSeconds(operation.TimeoutSeconds),
		Payload:        operation.Payload,
	}
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func durationSeconds(value int32) time.Duration {
	return time.Duration(value) * time.Second
}
