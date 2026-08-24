package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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
	trimmedKey := strings.TrimSpace(idempotencyKey)
	if trimmedKey == "" {
		return domain.EpisodeTask{}, domain.Operation{}, NewError("invalid_task_command", "the task command is invalid", ErrInvalidInput, map[string]any{"field": "idempotencyKey", "reason": "must not be blank"})
	}
	if !utf8.ValidString(trimmedKey) {
		return domain.EpisodeTask{}, domain.Operation{}, NewError("invalid_task_command", "the task command is invalid", ErrInvalidInput, map[string]any{"field": "idempotencyKey", "reason": "must be valid utf8"})
	}
	if utf8.RuneCountInString(trimmedKey) > 256 {
		return domain.EpisodeTask{}, domain.Operation{}, NewError("invalid_task_command", "the task command is invalid", ErrInvalidInput, map[string]any{"field": "idempotencyKey", "reason": "must not exceed 256 characters"})
	}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, replayed, err := findIdempotentResourceCommand(
			ctx, scope, trimmedKey, "episode_task", taskID, "retry",
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
		videoFailed := task.VideoState == string(domain.VideoFailed)
		subtitleFailed := task.SubtitleState == string(domain.SubtitleFailed)
		hasMediaFailed := videoFailed || subtitleFailed
		if task.State == string(domain.TaskImported) {
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
			schedule := ScheduleOperationRequest{
				ResourceType: "episode_task", ResourceID: taskID, IdempotencyKey: trimmedKey,
				Kind: appqueue.KindCleanupRun, MaxAttempts: 5, Timeout: 30 * time.Minute,
				Payload: map[string]any{"command": "retry", "cleanupId": repository.UUIDFromPG(cleanup.ID)}, ActorUserID: actorUserID,
			}
			scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, schedule)
			if err != nil {
				return fmt.Errorf("schedule task retry: %w", err)
			}
			operation = scheduled.Operation
			return appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, scheduled.Operation.ID, actorUserID, "task.retry_requested", map[string]any{"failureStage": failureStage})
		}
		isFailedState := task.State == string(domain.TaskFailed)
		isProcessingStuck := task.State == string(domain.TaskProcessing) && hasMediaFailed
		isCancelledRecoverable := isCancelledMediaRecoverable(task.State, task.VideoState, task.SubtitleState)
		if isFailedState || isProcessingStuck || isCancelledRecoverable {
			if hasMediaFailed {
				var primaryBranch, secondaryBranch string
				if videoFailed && subtitleFailed {
					switch failureStage {
					case "video":
						primaryBranch = "video"
						secondaryBranch = "subtitle"
					case "subtitle":
						primaryBranch = "subtitle"
						secondaryBranch = "video"
					default:
						primaryBranch = "video"
						secondaryBranch = "subtitle"
					}
				} else if videoFailed {
					primaryBranch = "video"
				} else {
					primaryBranch = "subtitle"
				}
				if _, err := scope.Queries.RequeueTaskFailedMediaBranches(ctx, db.RequeueTaskFailedMediaBranchesParams{
					ID:              repository.UUIDToPG(taskID),
					ExpectedVersion: expectedVersion,
				}); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return NewError("state_conflict", "the task is not retryable", ErrStateConflict, map[string]any{"state": task.State, "videoState": task.VideoState, "subtitleState": task.SubtitleState})
					}
					return fmt.Errorf("requeue task media branches: %w", err)
				}
				primaryKind, primaryTimeout, primaryAttempts := mediaBranchSchedule(primaryBranch)
				schedulePrimary := ScheduleOperationRequest{
					ResourceType: "episode_task", ResourceID: taskID, IdempotencyKey: trimmedKey,
					Kind: primaryKind, MaxAttempts: primaryAttempts, Timeout: primaryTimeout,
					Payload: map[string]any{"command": "retry"}, ActorUserID: actorUserID,
				}
				scheduledPrimary, err := workflow.operations.ScheduleInTx(ctx, scope, schedulePrimary)
				if err != nil {
					return fmt.Errorf("schedule task retry %s: %w", primaryBranch, err)
				}
				operation = scheduledPrimary.Operation
				var secondaryOperation domain.Operation
				var secondaryKind string
				if secondaryBranch != "" {
					var secondaryTimeout time.Duration
					var secondaryAttempts int
					secondaryKind, secondaryTimeout, secondaryAttempts = mediaBranchSchedule(secondaryBranch)
					derivedKey := deriveSecondaryIdempotencyKey(trimmedKey, secondaryKind, taskID)
					scheduleSecondary := ScheduleOperationRequest{
						ResourceType: "episode_task", ResourceID: taskID, IdempotencyKey: derivedKey,
						Kind: secondaryKind, MaxAttempts: secondaryAttempts, Timeout: secondaryTimeout,
						Payload: map[string]any{"command": "retry"}, ActorUserID: actorUserID,
					}
					secondaryScheduled, err := workflow.operations.ScheduleInTx(ctx, scope, scheduleSecondary)
					if err != nil {
						return fmt.Errorf("schedule task retry %s: %w", secondaryBranch, err)
					}
					secondaryOperation = secondaryScheduled.Operation
				}
				eventFailureStage := failureStage
				if eventFailureStage == "" {
					eventFailureStage = primaryBranch
				}
				// 构建兼容且确定顺序的审计事件：保留 failureStage，新增有序的 failureStages 与分支 operation 关联（不含敏感值）
				eventData := map[string]any{"failureStage": eventFailureStage}
				if secondaryBranch != "" {
					stageToOp := map[string]struct {
						kind string
						opID uuid.UUID
					}{
						primaryBranch:   {kind: primaryKind, opID: operation.ID},
						secondaryBranch: {kind: secondaryKind, opID: secondaryOperation.ID},
					}
					stages := []string{primaryBranch, secondaryBranch}
					sort.Strings(stages)
					eventData["failureStages"] = stages
					branches := make([]map[string]any, 0, 2)
					for _, stage := range stages {
						entry := stageToOp[stage]
						branches = append(branches, map[string]any{
							"stage":       stage,
							"kind":        entry.kind,
							"operationId": entry.opID.String(),
						})
					}
					eventData["branches"] = branches
				} else {
					eventData["failureStages"] = []string{primaryBranch}
					eventData["branches"] = []map[string]any{
						{"stage": primaryBranch, "kind": primaryKind, "operationId": operation.ID.String()},
					}
				}
				return appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, operation.ID, actorUserID, "task.retry_requested", eventData)
			}
			// No media branch failed, handle finalize/import for failed tasks only.
			if !isFailedState {
				return NewError("invalid_state", "only a failed task or failed imported cleanup can be retried", ErrStateConflict, map[string]any{"state": task.State})
			}
			switch failureStage {
			case "finalize":
				if _, err := scope.Queries.RequeueTaskFinalizeBranch(ctx, repository.UUIDToPG(taskID)); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return NewError("state_conflict", "the failed task has no retryable finalize branch", ErrStateConflict, map[string]any{"failureStage": failureStage})
					}
					return fmt.Errorf("requeue task finalize: %w", err)
				}
				schedule := ScheduleOperationRequest{
					ResourceType: "episode_task", ResourceID: taskID, IdempotencyKey: trimmedKey,
					Kind: appqueue.KindMediaFinalize, MaxAttempts: 3, Timeout: 5 * time.Minute,
					Payload: map[string]any{"command": "retry"}, ActorUserID: actorUserID,
				}
				scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, schedule)
				if err != nil {
					return fmt.Errorf("schedule task retry: %w", err)
				}
				operation = scheduled.Operation
				return appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, scheduled.Operation.ID, actorUserID, "task.retry_requested", map[string]any{"failureStage": failureStage})
			case "import":
				if _, err := scope.Queries.RequeueTaskImportBranch(ctx, repository.UUIDToPG(taskID)); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return NewError("state_conflict", "the failed task has no retryable import branch", ErrStateConflict, map[string]any{"failureStage": failureStage})
					}
					return fmt.Errorf("requeue task import: %w", err)
				}
				latestImport, err := scope.Queries.RequeueLatestFailedTaskImport(ctx, repository.UUIDToPG(taskID))
				if err != nil {
					return fmt.Errorf("requeue task import record: %w", err)
				}
				schedule := ScheduleOperationRequest{
					ResourceType: "episode_task", ResourceID: taskID, IdempotencyKey: trimmedKey,
					Kind: appqueue.KindEmbyImport, MaxAttempts: 3, Timeout: 10 * time.Minute,
					Payload: map[string]any{"command": "retry", "importId": repository.UUIDFromPG(latestImport.ID)}, ActorUserID: actorUserID,
				}
				scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, schedule)
				if err != nil {
					return fmt.Errorf("schedule task retry: %w", err)
				}
				operation = scheduled.Operation
				return appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, scheduled.Operation.ID, actorUserID, "task.retry_requested", map[string]any{"failureStage": failureStage})
			default:
				return NewError("invalid_state", "the failed task has no retryable branch", ErrStateConflict, map[string]any{"failureStage": failureStage})
			}
		}
		return NewError("invalid_state", "only a failed task or failed imported cleanup can be retried", ErrStateConflict, map[string]any{"state": task.State})
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

func mediaBranchSchedule(branch string) (string, time.Duration, int) {
	switch branch {
	case "video":
		return appqueue.KindTranscodeRun, 24 * time.Hour, 3
	case "subtitle":
		return appqueue.KindSubtitlePrepare, 30 * time.Minute, 3
	default:
		return "", 0, 0
	}
}

func deriveSecondaryIdempotencyKey(primaryKey, secondaryKind string, taskID uuid.UUID) string {
	digest := sha256.Sum256([]byte(primaryKey))
	hexDigest := hex.EncodeToString(digest[:])
	return "task-retry:" + taskID.String() + ":" + secondaryKind + ":" + hexDigest
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
