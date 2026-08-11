package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// TaskCommandWorkflow executes explicit task recovery commands.
type TaskCommandWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
	operations *OperationScheduler
	tasks      *TaskWorkflow
}

func NewTaskCommandWorkflow(queries *db.Queries, transactor *database.Transactor, operations *OperationScheduler, tasks *TaskWorkflow) *TaskCommandWorkflow {
	return &TaskCommandWorkflow{queries: queries, transactor: transactor, operations: operations, tasks: tasks}
}

func (workflow *TaskCommandWorkflow) Retry(ctx context.Context, taskID uuid.UUID, expectedVersion int32, idempotencyKey string, actorUserID uuid.UUID) (domain.EpisodeTask, domain.Operation, error) {
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, replayed, err := findIdempotentResourceCommand(
			ctx, scope, idempotencyKey, "episode_task", taskID, "retry",
			appqueue.KindTranscodeRun, appqueue.KindSubtitlePrepare, appqueue.KindMediaFinalize, appqueue.KindEmbyImport, appqueue.KindCleanupRun,
		)
		if err != nil {
			return err
		}
		if replayed {
			operation = existing
			return nil
		}
		task, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock task: %w", err)
		}
		if task.Version != expectedVersion {
			return NewError("state_conflict", "the task was modified by another request", ErrStateConflict, map[string]any{"expectedVersion": expectedVersion})
		}
		failureStage := valueOrEmpty(task.FailureStage)
		schedule := ScheduleOperationRequest{
			ResourceType: "episode_task", ResourceID: taskID, IdempotencyKey: idempotencyKey,
			MaxAttempts: 3, Payload: map[string]any{"command": "retry"}, ActorUserID: actorUserID,
		}
		if task.State == "imported" {
			failureStage = "cleanup"
			if _, err := scope.Queries.MarkTaskCleanupRetryRequested(ctx, db.MarkTaskCleanupRetryRequestedParams{
				ID: repository.UUIDToPG(taskID), ExpectedVersion: expectedVersion,
			}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return NewError("invalid_state", "the imported task has no failed cleanup to retry", ErrStateConflict, map[string]any{"state": task.State})
				}
				return fmt.Errorf("mark task cleanup retry: %w", err)
			}
			cleanup, err := scope.Queries.RequeueLatestFailedTaskCleanup(ctx, repository.UUIDToPG(taskID))
			if err != nil {
				return fmt.Errorf("requeue task cleanup record: %w", err)
			}
			schedule.Kind = appqueue.KindCleanupRun
			schedule.MaxAttempts = 5
			schedule.Timeout = 30 * time.Minute
			schedule.Payload = map[string]any{"command": "retry", "cleanupId": repository.UUIDFromPG(cleanup.ID)}
		} else {
			if task.State != "failed" {
				return NewError("invalid_state", "only a failed task or failed imported cleanup can be retried", ErrStateConflict, map[string]any{"state": task.State})
			}
			switch failureStage {
			case "video":
				if _, err := scope.Queries.RequeueTaskVideoBranch(ctx, repository.UUIDToPG(taskID)); err != nil {
					return fmt.Errorf("requeue task video: %w", err)
				}
				schedule.Kind = appqueue.KindTranscodeRun
				schedule.Timeout = 24 * time.Hour
			case "subtitle":
				if _, err := scope.Queries.RequeueTaskSubtitleBranch(ctx, repository.UUIDToPG(taskID)); err != nil {
					return fmt.Errorf("requeue task subtitle: %w", err)
				}
				schedule.Kind = appqueue.KindSubtitlePrepare
				schedule.Timeout = 30 * time.Minute
			case "finalize":
				if _, err := scope.Queries.RequeueTaskFinalizeBranch(ctx, repository.UUIDToPG(taskID)); err != nil {
					return fmt.Errorf("requeue task finalize: %w", err)
				}
				schedule.Kind = appqueue.KindMediaFinalize
				schedule.Timeout = 5 * time.Minute
			case "import":
				if _, err := scope.Queries.RequeueTaskImportBranch(ctx, repository.UUIDToPG(taskID)); err != nil {
					return fmt.Errorf("requeue task import: %w", err)
				}
				latestImport, err := scope.Queries.RequeueLatestFailedTaskImport(ctx, repository.UUIDToPG(taskID))
				if err != nil {
					return fmt.Errorf("requeue task import record: %w", err)
				}
				schedule.Kind = appqueue.KindEmbyImport
				schedule.Timeout = 10 * time.Minute
				schedule.Payload = map[string]any{"command": "retry", "importId": repository.UUIDFromPG(latestImport.ID)}
			default:
				return NewError("invalid_state", "the failed task has no retryable branch", ErrStateConflict, map[string]any{"failureStage": failureStage})
			}
		}

		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, schedule)
		if err != nil {
			return fmt.Errorf("schedule task retry: %w", err)
		}
		operation = scheduled.Operation
		return appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, scheduled.Operation.ID, actorUserID, "task.retry_requested", map[string]any{"failureStage": failureStage})
	})
	if err != nil {
		return domain.EpisodeTask{}, domain.Operation{}, err
	}
	task, err := workflow.tasks.GetTask(ctx, taskID)
	if err != nil {
		return domain.EpisodeTask{}, domain.Operation{}, err
	}
	return task, operation, nil
}

func (workflow *TaskCommandWorkflow) Cancel(ctx context.Context, taskID uuid.UUID, expectedVersion int32, idempotencyKey string, actorUserID uuid.UUID) (domain.EpisodeTask, domain.Operation, error) {
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, replayed, err := findIdempotentResourceCommand(
			ctx, scope, idempotencyKey, "episode_task", taskID, "cancel", appqueue.KindTaskCancel,
		)
		if err != nil {
			return err
		}
		if replayed {
			operation = existing
			return nil
		}
		if err := requestResourceOperationCancellations(ctx, scope, "episode_task", taskID, actorUserID); err != nil {
			return err
		}
		task, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock task: %w", err)
		}
		if task.Version != expectedVersion {
			return NewError("state_conflict", "the task was modified by another request", ErrStateConflict, map[string]any{"expectedVersion": expectedVersion})
		}
		cancelled, err := scope.Queries.CancelEpisodeTaskIfActive(ctx, db.CancelEpisodeTaskIfActiveParams{
			ID:              repository.UUIDToPG(taskID),
			ExpectedVersion: expectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return NewError("invalid_state", "only a non-terminal task can be cancelled", ErrStateConflict, map[string]any{"state": task.State})
		}
		if err != nil {
			return fmt.Errorf("cancel task: %w", err)
		}
		_ = cancelled
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindTaskCancel,
			ResourceType:   "episode_task",
			ResourceID:     taskID,
			IdempotencyKey: idempotencyKey,
			MaxAttempts:    1,
			Timeout:        time.Minute,
			Payload:        map[string]any{"command": "cancel"},
			ActorUserID:    actorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule task cancel: %w", err)
		}
		operation = scheduled.Operation
		return appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, scheduled.Operation.ID, actorUserID, "task.cancel_requested", map[string]any{})
	})
	if err != nil {
		return domain.EpisodeTask{}, domain.Operation{}, err
	}
	task, err := workflow.tasks.GetTask(ctx, taskID)
	if err != nil {
		return domain.EpisodeTask{}, domain.Operation{}, err
	}
	return task, operation, nil
}
