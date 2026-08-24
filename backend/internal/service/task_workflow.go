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
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type TaskWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
	operations *OperationScheduler
}

func NewTaskWorkflow(queries *db.Queries, transactor *database.Transactor, operations *OperationScheduler) *TaskWorkflow {
	return &TaskWorkflow{queries: queries, transactor: transactor, operations: operations}
}

func (workflow *TaskWorkflow) GetTask(ctx context.Context, id uuid.UUID) (domain.EpisodeTask, error) {
	if id == uuid.Nil {
		return domain.EpisodeTask{}, domain.ErrNotFound
	}
	row, err := workflow.queries.GetTaskView(ctx, repository.UUIDToPG(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EpisodeTask{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.EpisodeTask{}, fmt.Errorf("get episode task: %w", err)
	}
	task := taskFromGetRow(row)
	if err := workflow.attachOperationSummaries(ctx, []*domain.EpisodeTask{&task}); err != nil {
		return domain.EpisodeTask{}, err
	}
	return task, nil
}

func (workflow *TaskWorkflow) ListTasks(
	ctx context.Context,
	cursor *uuid.UUID,
	limit int,
	state *domain.TaskState,
	phase *string,
) (domain.EpisodeTaskPage, error) {
	if limit <= 0 || limit > 100 {
		return domain.EpisodeTaskPage{}, invalidTaskCommand("limit", "must be between 1 and 100")
	}
	var stateFilter *string
	if state != nil {
		if !validTaskState(*state) {
			return domain.EpisodeTaskPage{}, invalidTaskCommand("state", "is not a recognized task state")
		}
		value := string(*state)
		stateFilter = &value
	}
	cursorID := pgtypeUUID(cursor)
	if cursor != nil {
		if _, err := workflow.queries.GetTaskView(ctx, cursorID); errors.Is(err, pgx.ErrNoRows) {
			return domain.EpisodeTaskPage{}, domain.ErrNotFound
		} else if err != nil {
			return domain.EpisodeTaskPage{}, fmt.Errorf("validate task cursor: %w", err)
		}
	}
	if phase != nil {
		switch *phase {
		case "processing", "awaiting_review", "importing", "failed", "cleanup_failed":
		default:
			return domain.EpisodeTaskPage{}, invalidTaskCommand("phase", "is not a recognized task phase")
		}
	}
	rows, err := workflow.queries.ListTaskViews(ctx, db.ListTaskViewsParams{
		StateFilter: stateFilter,
		PhaseFilter: phase,
		CursorID:    cursorID,
		PageSize:    int32(limit + 1),
	})
	if err != nil {
		return domain.EpisodeTaskPage{}, fmt.Errorf("list episode tasks: %w", err)
	}
	page := domain.EpisodeTaskPage{Items: make([]domain.EpisodeTask, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			cursorValue := page.Items[len(page.Items)-1].ID
			page.NextCursor = &cursorValue
			break
		}
		page.Items = append(page.Items, taskFromListRow(row))
	}
	taskPointers := make([]*domain.EpisodeTask, 0, len(page.Items))
	for index := range page.Items {
		taskPointers = append(taskPointers, &page.Items[index])
	}
	if err := workflow.attachOperationSummaries(ctx, taskPointers); err != nil {
		return domain.EpisodeTaskPage{}, err
	}
	return page, nil
}

func (workflow *TaskWorkflow) attachOperationSummaries(ctx context.Context, tasks []*domain.EpisodeTask) error {
	if len(tasks) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, 0, len(tasks))
	byID := make(map[uuid.UUID]*domain.EpisodeTask, len(tasks))
	for _, task := range tasks {
		ids = append(ids, repository.UUIDToPG(task.ID))
		byID[task.ID] = task
		task.Operations = []domain.OperationSummary{}
	}
	rows, err := workflow.queries.ListTaskOperationSummaries(ctx, ids)
	if err != nil {
		return fmt.Errorf("list task operation summaries: %w", err)
	}
	for _, row := range rows {
		task := byID[repository.UUIDFromPG(row.ResourceID)]
		if task == nil || len(task.Operations) >= 10 {
			continue
		}
		task.Operations = append(task.Operations, domain.OperationSummary{
			ID: repository.UUIDFromPG(row.ID), Kind: row.Kind, Status: row.Status,
			MaxAttempts: int(row.MaxAttempts), AttemptCount: int(row.AttemptCount),
			ErrorCode: valueOrEmpty(row.ErrorCode), ErrorMessage: valueOrEmpty(row.ErrorMessage),
			StartedAt: timePointer(row.StartedAt), FinishedAt: timePointer(row.FinishedAt), UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return nil
}

func (workflow *TaskWorkflow) ReviewTask(ctx context.Context, input domain.ReviewTask) (domain.EpisodeTask, error) {
	if err := validateReviewTask(input); err != nil {
		return domain.EpisodeTask{}, err
	}
	idempotencyKey := "task.review:" + input.TaskID.String() + ":" + strings.TrimSpace(input.IdempotencyKey)
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(input.TaskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock task for review: %w", err)
		}
		existing, err := scope.Queries.GetTaskReviewByTask(ctx, repository.UUIDToPG(input.TaskID))
		if err == nil {
			if existing.IdempotencyKey == idempotencyKey &&
				existing.ExpectedTaskVersion == input.ExpectedVersion &&
				existing.Decision == string(input.Decision) &&
				existing.Notes == strings.TrimSpace(input.Notes) {
				return nil
			}
			return taskStateConflict("the task was already reviewed", input.TaskID, locked.State, locked.Version)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load existing task review: %w", err)
		}
		if locked.Version != input.ExpectedVersion {
			return taskStateConflict("the task was modified by another request", input.TaskID, locked.State, locked.Version)
		}
		if err := domain.ValidateTaskTransition(domain.TaskState(locked.State), input.Decision); err != nil {
			return taskStateConflict("the task is not awaiting review", input.TaskID, locked.State, locked.Version)
		}
		if _, err := scope.Queries.GetArtifactSetForTask(ctx, repository.UUIDToPG(input.TaskID)); errors.Is(err, pgx.ErrNoRows) {
			return taskStateConflict("the task does not have a valid paired artifact set", input.TaskID, locked.State, locked.Version)
		} else if err != nil {
			return fmt.Errorf("load review artifact set: %w", err)
		}
		reviewID := uuid.New()
		if _, err := scope.Queries.CreateTaskReview(ctx, db.CreateTaskReviewParams{
			ID:                  repository.UUIDToPG(reviewID),
			TaskID:              repository.UUIDToPG(input.TaskID),
			Decision:            string(input.Decision),
			Notes:               strings.TrimSpace(input.Notes),
			ReviewedBy:          repository.UUIDToPG(input.ActorUserID),
			IdempotencyKey:      idempotencyKey,
			ExpectedTaskVersion: input.ExpectedVersion,
		}); err != nil {
			return fmt.Errorf("create task review: %w", err)
		}
		updated, err := scope.Queries.MarkTaskReviewed(ctx, db.MarkTaskReviewedParams{
			Decision:        string(input.Decision),
			ID:              repository.UUIDToPG(input.TaskID),
			ExpectedVersion: input.ExpectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return taskStateConflict("the task is not reviewable", input.TaskID, locked.State, locked.Version)
		}
		if err != nil {
			return fmt.Errorf("mark task reviewed: %w", err)
		}
		if err := appendResourceEvent(ctx, scope.Queries, "episode_task", input.TaskID, uuid.Nil, input.ActorUserID, "task.reviewed", map[string]any{
			"decision": input.Decision,
			"version":  updated.Version,
		}); err != nil {
			return err
		}
		if input.Decision != domain.TaskApproved {
			return nil
		}

		importID := uuid.New()
		if _, err := scope.Queries.CreateTaskImport(ctx, db.CreateTaskImportParams{
			ID:     repository.UUIDToPG(importID),
			TaskID: repository.UUIDToPG(input.TaskID),
		}); err != nil {
			return fmt.Errorf("create approved task import: %w", err)
		}
		queued, err := scope.Queries.MarkTaskImportQueued(ctx, db.MarkTaskImportQueuedParams{
			ID:              repository.UUIDToPG(input.TaskID),
			ExpectedVersion: updated.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return taskStateConflict("the approved task could not be queued for import", input.TaskID, string(input.Decision), updated.Version)
		}
		if err != nil {
			return fmt.Errorf("mark approved task import queued: %w", err)
		}
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindEmbyImport,
			ResourceType:   "episode_task",
			ResourceID:     input.TaskID,
			IdempotencyKey: "emby.import:auto:" + reviewID.String(),
			MaxAttempts:    3,
			Timeout:        30 * time.Minute,
			Payload: map[string]any{
				"importId":        importID,
				"expectedVersion": updated.Version,
			},
			ActorUserID: input.ActorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule approved task import: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "episode_task", input.TaskID, scheduled.Operation.ID, input.ActorUserID, "task.import_queued", map[string]any{
			"automatic": true,
			"importId":  importID,
			"reviewId":  reviewID,
			"version":   queued.Version,
		})
	})
	if err != nil {
		return domain.EpisodeTask{}, err
	}
	return workflow.GetTask(ctx, input.TaskID)
}

func (workflow *TaskWorkflow) QueueImport(ctx context.Context, input domain.QueueTaskImport) (domain.TaskImportResult, error) {
	if err := validateQueueTaskImport(input); err != nil {
		return domain.TaskImportResult{}, err
	}
	idempotencyKey := "emby.import:" + input.TaskID.String() + ":" + strings.TrimSpace(input.IdempotencyKey)
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(input.TaskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock task for import: %w", err)
		}
		if existing, err := scope.Queries.GetOperationByIdempotencyKey(ctx, idempotencyKey); err == nil {
			if existing.Kind != appqueue.KindEmbyImport || repository.UUIDFromPG(existing.ResourceID) != input.TaskID {
				return taskStateConflict("the idempotency key belongs to another command", input.TaskID, locked.State, locked.Version)
			}
			var payload taskImportOperationPayload
			if json.Unmarshal(existing.Payload, &payload) != nil || payload.ExpectedVersion != input.ExpectedVersion || payload.ImportID == uuid.Nil {
				return NewError("idempotency_conflict", "the idempotency key was already used for different import parameters", ErrStateConflict, map[string]any{"idempotencyKey": input.IdempotencyKey})
			}
			operation = operationFromDB(existing)
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load idempotent import operation: %w", err)
		}
		if locked.Version != input.ExpectedVersion {
			return taskStateConflict("the task was modified by another request", input.TaskID, locked.State, locked.Version)
		}
		if err := domain.ValidateTaskTransition(domain.TaskState(locked.State), domain.TaskImportQueued); err != nil {
			return taskStateConflict("only an approved task can be queued for import", input.TaskID, locked.State, locked.Version)
		}
		if _, err := scope.Queries.GetArtifactSetForTask(ctx, repository.UUIDToPG(input.TaskID)); errors.Is(err, pgx.ErrNoRows) {
			return taskStateConflict("the task does not have a valid paired artifact set", input.TaskID, locked.State, locked.Version)
		} else if err != nil {
			return fmt.Errorf("load import artifact set: %w", err)
		}

		importID := uuid.New()
		if _, err := scope.Queries.CreateTaskImport(ctx, db.CreateTaskImportParams{
			ID:     repository.UUIDToPG(importID),
			TaskID: repository.UUIDToPG(input.TaskID),
		}); err != nil {
			return fmt.Errorf("create task import: %w", err)
		}
		updated, err := scope.Queries.MarkTaskImportQueued(ctx, db.MarkTaskImportQueuedParams{
			ID:              repository.UUIDToPG(input.TaskID),
			ExpectedVersion: input.ExpectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return taskStateConflict("the task import guard rejected the command", input.TaskID, locked.State, locked.Version)
		}
		if err != nil {
			return fmt.Errorf("mark task import queued: %w", err)
		}
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindEmbyImport,
			ResourceType:   "episode_task",
			ResourceID:     input.TaskID,
			IdempotencyKey: idempotencyKey,
			MaxAttempts:    3,
			Timeout:        30 * time.Minute,
			Payload: map[string]any{
				"importId":        importID,
				"expectedVersion": input.ExpectedVersion,
			},
			ActorUserID: input.ActorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule task import: %w", err)
		}
		operation = scheduled.Operation
		return appendResourceEvent(ctx, scope.Queries, "episode_task", input.TaskID, operation.ID, input.ActorUserID, "task.import_queued", map[string]any{
			"importId": importID,
			"version":  updated.Version,
		})
	})
	if err != nil {
		return domain.TaskImportResult{}, err
	}
	task, err := workflow.GetTask(ctx, input.TaskID)
	if err != nil {
		return domain.TaskImportResult{}, err
	}
	return domain.TaskImportResult{Task: task, Operation: operation}, nil
}

type taskImportOperationPayload struct {
	ImportID        uuid.UUID `json:"importId"`
	ExpectedVersion int32     `json:"expectedVersion"`
}

func (workflow *TaskWorkflow) BeginImport(ctx context.Context, taskID, importID uuid.UUID) (domain.ImportCommand, error) {
	var command domain.ImportCommand
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock task import: %w", err)
		}
		row, err := scope.Queries.GetTaskImportCommand(ctx, db.GetTaskImportCommandParams{
			TaskID:   repository.UUIDToPG(taskID),
			ImportID: repository.UUIDToPG(importID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load task import command: %w", err)
		}
		command = importCommandFromRow(row)
		if locked.State == string(domain.TaskImported) && row.ImportStatus == "succeeded" {
			return nil
		}
		if locked.State != string(domain.TaskImportQueued) && locked.State != string(domain.TaskImporting) {
			return mediaWorkflowError("task_import_state_conflict", fmt.Sprintf("task cannot import from state %q", locked.State), false)
		}
		if row.ImportStatus != "queued" && row.ImportStatus != "running" {
			return mediaWorkflowError("import_state_conflict", fmt.Sprintf("import cannot start from state %q", row.ImportStatus), false)
		}
		if _, err := scope.Queries.StartImport(ctx, repository.UUIDToPG(importID)); err != nil {
			return fmt.Errorf("start import record: %w", err)
		}
		started, err := scope.Queries.StartTaskImport(ctx, repository.UUIDToPG(taskID))
		if err != nil {
			return fmt.Errorf("start task import: %w", err)
		}
		command.TaskState = domain.TaskState(started.State)
		command.ImportState = "running"
		return nil
	})
	if err != nil {
		return domain.ImportCommand{}, err
	}
	return command, nil
}

func (workflow *TaskWorkflow) CompleteImport(ctx context.Context, completion domain.ImportCompletion) error {
	if completion.TaskID == uuid.Nil || completion.ImportID == uuid.Nil || completion.OperationID == uuid.Nil ||
		strings.TrimSpace(completion.DestinationVideoPath) == "" || strings.TrimSpace(completion.DestinationSubtitlePath) == "" {
		return fmt.Errorf("import completion requires task, import, operation, and destination paths")
	}
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(completion.TaskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock completed task import: %w", err)
		}
		command, err := scope.Queries.GetTaskImportCommand(ctx, db.GetTaskImportCommandParams{
			TaskID:   repository.UUIDToPG(completion.TaskID),
			ImportID: repository.UUIDToPG(completion.ImportID),
		})
		if err != nil {
			return fmt.Errorf("load completed task import: %w", err)
		}
		if locked.State == string(domain.TaskImported) && command.ImportStatus == "succeeded" {
			return nil
		}
		if locked.State != string(domain.TaskImporting) || command.ImportStatus != "running" {
			return mediaWorkflowError("task_import_state_conflict", "the task import is not running", false)
		}
		videoPath := completion.DestinationVideoPath
		subtitlePath := completion.DestinationSubtitlePath
		if _, err := scope.Queries.CompleteImport(ctx, db.CompleteImportParams{
			DestinationVideoPath:    &videoPath,
			DestinationSubtitlePath: &subtitlePath,
			ID:                      repository.UUIDToPG(completion.ImportID),
		}); err != nil {
			return fmt.Errorf("complete import record: %w", err)
		}
		updated, err := scope.Queries.MarkTaskImported(ctx, repository.UUIDToPG(completion.TaskID))
		if err != nil {
			return fmt.Errorf("mark task imported: %w", err)
		}
		_, rssErr := scope.Queries.LockRSSSubscriptionForTaskImport(ctx, repository.UUIDToPG(completion.TaskID))
		isRSSTask := rssErr == nil
		if rssErr != nil && !errors.Is(rssErr, pgx.ErrNoRows) {
			return fmt.Errorf("lock RSS subscription for task import: %w", rssErr)
		}
		if !isRSSTask {
			downloadIDValue, err := scope.Queries.GetTaskDownloadID(ctx, repository.UUIDToPG(completion.TaskID))
			if err != nil {
				return fmt.Errorf("load task download for cleanup: %w", err)
			}
			if err := workflow.scheduleTaskCleanupInTx(
				ctx, scope, completion.TaskID, completion.ImportID, repository.UUIDFromPG(downloadIDValue),
			); err != nil {
				return err
			}
		}
		if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindEmbyRefresh,
			ResourceType:   "episode_task",
			ResourceID:     completion.TaskID,
			IdempotencyKey: "emby.refresh:" + completion.TaskID.String() + ":" + completion.ImportID.String(),
			MaxAttempts:    5,
			Timeout:        2 * time.Minute,
			Payload:        map[string]any{},
		}); err != nil {
			return fmt.Errorf("schedule %s: %w", appqueue.KindEmbyRefresh, err)
		}
		if err := appendResourceEvent(ctx, scope.Queries, "episode_task", completion.TaskID, completion.OperationID, uuid.Nil, "task.imported", map[string]any{
			"importId":                completion.ImportID,
			"destinationVideoPath":    videoPath,
			"destinationSubtitlePath": subtitlePath,
			"version":                 updated.Version,
		}); err != nil {
			return err
		}
		if _, err := scope.Queries.MarkRSSEntryImportedForTask(ctx, repository.UUIDToPG(completion.TaskID)); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("mark RSS entry imported: %w", err)
		}
		return workflow.scheduleRSSCompletionInTx(ctx, scope, completion)
	})
}

func (workflow *TaskWorkflow) scheduleTaskCleanupInTx(
	ctx context.Context,
	scope database.TxScope,
	taskID uuid.UUID,
	importID uuid.UUID,
	downloadID uuid.UUID,
) error {
	return scheduleTaskCleanupInTx(ctx, scope, workflow.operations, taskID, importID, downloadID)
}

func scheduleTaskCleanupInTx(
	ctx context.Context,
	scope database.TxScope,
	operations *OperationScheduler,
	taskID uuid.UUID,
	importID uuid.UUID,
	downloadID uuid.UUID,
) error {
	cleanupID := deterministicResourceID("cleanup:" + taskID.String() + ":" + importID.String())
	if _, err := scope.Queries.CreateTaskCleanup(ctx, db.CreateTaskCleanupParams{
		ID:         repository.UUIDToPG(cleanupID),
		TaskID:     repository.UUIDToPG(taskID),
		DownloadID: repository.UUIDToPG(downloadID),
	}); err != nil {
		return fmt.Errorf("create task cleanup: %w", err)
	}
	if _, err := operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
		Kind:           appqueue.KindCleanupRun,
		ResourceType:   "episode_task",
		ResourceID:     taskID,
		IdempotencyKey: "cleanup.run:" + taskID.String() + ":" + importID.String(),
		MaxAttempts:    5,
		Timeout:        30 * time.Minute,
		Payload:        map[string]any{"cleanupId": cleanupID},
	}); err != nil {
		return fmt.Errorf("schedule %s: %w", appqueue.KindCleanupRun, err)
	}
	return nil
}

func (workflow *TaskWorkflow) scheduleRSSCompletionInTx(ctx context.Context, scope database.TxScope, completion domain.ImportCompletion) error {
	final, err := scope.Queries.LockRSSSubscriptionAtCompleteImport(ctx, repository.UUIDToPG(completion.TaskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("detect complete RSS import set: %w", err)
	}
	subscriptionID := repository.UUIDFromPG(final.ID)
	completed, err := scope.Queries.MarkRSSSubscriptionCompleted(ctx, db.MarkRSSSubscriptionCompletedParams{
		ID: final.ID, ExpectedVersion: final.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("complete RSS subscription: %w", err)
	}
	sourceSeason, sourceEpisode := int(final.SourceSeason), int(final.SourceEpisode)
	cleanupOperations := 0
	if final.CleanupSourceOnCompletion {
		candidates, err := scope.Queries.ListRSSCompletionCleanupCandidates(ctx, final.ID)
		if err != nil {
			return fmt.Errorf("list RSS completion cleanup candidates: %w", err)
		}
		for _, candidate := range candidates {
			if err := workflow.scheduleTaskCleanupInTx(
				ctx,
				scope,
				repository.UUIDFromPG(candidate.TaskID),
				repository.UUIDFromPG(candidate.ImportID),
				repository.UUIDFromPG(candidate.DownloadID),
			); err != nil {
				return err
			}
			cleanupOperations++
		}
	}
	eventData, err := json.Marshal(map[string]any{
		"sourceSeason": sourceSeason, "sourceEpisode": sourceEpisode, "version": completed.Version,
		"cleanupSourceOnCompletion": final.CleanupSourceOnCompletion, "cleanupOperations": cleanupOperations,
	})
	if err != nil {
		return fmt.Errorf("encode final RSS import event: %w", err)
	}
	return appendRSSEvent(ctx, scope.Queries, subscriptionID, completion.OperationID, "rss.subscription.final_imported", eventData)
}

func (workflow *TaskWorkflow) BeginCleanup(ctx context.Context, taskID, cleanupID uuid.UUID) (domain.CleanupCommand, error) {
	var command domain.CleanupCommand
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock task cleanup: %w", err)
		}
		row, err := scope.Queries.GetTaskCleanupCommand(ctx, db.GetTaskCleanupCommandParams{
			TaskID:    repository.UUIDToPG(taskID),
			CleanupID: repository.UUIDToPG(cleanupID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load task cleanup command: %w", err)
		}
		command = cleanupCommandFromRow(row)
		if row.CleanupStatus == string(domain.CleanupCompleted) {
			return nil
		}
		if locked.State != string(domain.TaskImported) {
			return mediaWorkflowError("cleanup_import_guard_failed", "cleanup requires an imported task", false)
		}
		if row.CleanupStatus != string(domain.CleanupQueued) && row.CleanupStatus != string(domain.CleanupRunning) {
			return mediaWorkflowError("cleanup_state_conflict", fmt.Sprintf("cleanup cannot start from state %q", row.CleanupStatus), false)
		}
		if _, err := scope.Queries.StartCleanup(ctx, repository.UUIDToPG(cleanupID)); err != nil {
			return fmt.Errorf("start cleanup record: %w", err)
		}
		command.CleanupState = domain.CleanupRunning
		return nil
	})
	if err != nil {
		return domain.CleanupCommand{}, err
	}
	return command, nil
}

func (workflow *TaskWorkflow) CompleteCleanup(ctx context.Context, completion domain.CleanupCompletion) error {
	if completion.TaskID == uuid.Nil || completion.CleanupID == uuid.Nil || completion.OperationID == uuid.Nil {
		return fmt.Errorf("cleanup completion requires task, cleanup, and operation IDs")
	}
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		row, err := scope.Queries.GetTaskCleanupCommand(ctx, db.GetTaskCleanupCommandParams{
			TaskID:    repository.UUIDToPG(completion.TaskID),
			CleanupID: repository.UUIDToPG(completion.CleanupID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load completed cleanup: %w", err)
		}
		if row.CleanupStatus == string(domain.CleanupCompleted) {
			return nil
		}
		if row.TaskState != string(domain.TaskImported) || row.CleanupStatus != string(domain.CleanupRunning) {
			return mediaWorkflowError("cleanup_state_conflict", "the imported task cleanup is not running", false)
		}
		if _, err := scope.Queries.CompleteCleanup(ctx, db.CompleteCleanupParams{
			TorrentRemoved:     completion.TorrentRemoved,
			StagedFilesRemoved: completion.StagedFilesRemoved,
			ID:                 repository.UUIDToPG(completion.CleanupID),
		}); err != nil {
			return fmt.Errorf("complete cleanup record: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "episode_task", completion.TaskID, completion.OperationID, uuid.Nil, "task.cleanup_completed", map[string]any{
			"cleanupId":          completion.CleanupID,
			"torrentRemoved":     completion.TorrentRemoved,
			"stagedFilesRemoved": completion.StagedFilesRemoved,
		})
	})
}

func (workflow *TaskWorkflow) TaskCancellationReady(ctx context.Context, taskID, operationID uuid.UUID) (bool, error) {
	resourceType := "episode_task"
	count, err := workflow.queries.CountOtherActiveResourceOperations(ctx, db.CountOtherActiveResourceOperationsParams{
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(taskID),
		OperationID:  repository.UUIDToPG(operationID),
	})
	if err != nil {
		return false, fmt.Errorf("count active task operations: %w", err)
	}
	return count == 0, nil
}

func validateReviewTask(input domain.ReviewTask) error {
	switch {
	case input.TaskID == uuid.Nil:
		return invalidTaskCommand("taskId", "is required")
	case input.ExpectedVersion <= 0:
		return invalidTaskCommand("expectedVersion", "must be positive")
	case input.Decision != domain.TaskApproved && input.Decision != domain.TaskRejected:
		return invalidTaskCommand("decision", "must be approved or rejected")
	case len(input.Notes) > 4096:
		return invalidTaskCommand("notes", "must not exceed 4096 bytes")
	case strings.TrimSpace(input.IdempotencyKey) == "":
		return invalidTaskCommand("idempotencyKey", "must not be blank")
	case input.ActorUserID == uuid.Nil:
		return invalidTaskCommand("actorUserId", "is required")
	default:
		return nil
	}
}

func validateQueueTaskImport(input domain.QueueTaskImport) error {
	switch {
	case input.TaskID == uuid.Nil:
		return invalidTaskCommand("taskId", "is required")
	case input.ExpectedVersion <= 0:
		return invalidTaskCommand("expectedVersion", "must be positive")
	case strings.TrimSpace(input.IdempotencyKey) == "":
		return invalidTaskCommand("idempotencyKey", "must not be blank")
	case input.ActorUserID == uuid.Nil:
		return invalidTaskCommand("actorUserId", "is required")
	default:
		return nil
	}
}

func validTaskState(state domain.TaskState) bool {
	switch state {
	case domain.TaskMediaQueued, domain.TaskProcessing, domain.TaskFinalizing, domain.TaskAwaitingReview,
		domain.TaskApproved, domain.TaskRejected, domain.TaskImportQueued, domain.TaskImporting,
		domain.TaskImported, domain.TaskFailed, domain.TaskCancelled:
		return true
	default:
		return false
	}
}

func invalidTaskCommand(field, reason string) *Error {
	return NewError("invalid_task_command", "the task command is invalid", ErrInvalidInput, map[string]any{"field": field, "reason": reason})
}

func taskStateConflict(message string, taskID uuid.UUID, state string, version int32) *Error {
	return NewError("state_conflict", message, ErrStateConflict, map[string]any{"taskId": taskID, "state": state, "version": version})
}

func importCommandFromRow(row db.GetTaskImportCommandRow) domain.ImportCommand {
	return domain.ImportCommand{
		TaskID:      repository.UUIDFromPG(row.TaskID),
		ImportID:    repository.UUIDFromPG(row.ImportID),
		TaskState:   domain.TaskState(row.State),
		ImportState: row.ImportStatus,
		MediaType:   domain.TaskMediaType(row.MediaType),
		SeriesTitle: row.SeriesTitle,
		MovieTitle:  movieTitle(row.MediaType, row.SeriesTitle),
		ReleaseYear: intValue(row.ReleaseYear),
		Season:      intValue(row.TargetSeason),
		BaseName:    row.Basename,
		Video: domain.MediaArtifact{
			ID:             repository.UUIDFromPG(row.VideoArtifactID),
			TaskID:         repository.UUIDFromPG(row.TaskID),
			Kind:           domain.MediaVideo,
			BaseName:       row.Basename,
			FilePath:       row.VideoFilePath,
			Format:         row.VideoFormat,
			SizeBytes:      row.VideoSizeBytes,
			ChecksumSHA256: row.VideoChecksumSha256,
		},
		Subtitle: domain.MediaArtifact{
			ID:             repository.UUIDFromPG(row.SubtitleArtifactID),
			TaskID:         repository.UUIDFromPG(row.TaskID),
			Kind:           domain.MediaSubtitle,
			BaseName:       row.Basename,
			FilePath:       row.SubtitleFilePath,
			Format:         row.SubtitleFormat,
			SizeBytes:      row.SubtitleSizeBytes,
			ChecksumSHA256: row.SubtitleChecksumSha256,
		},
	}
}

func movieTitle(mediaType, title string) string {
	if mediaType == string(domain.TaskMediaMovie) {
		return title
	}
	return ""
}

func cleanupCommandFromRow(row db.GetTaskCleanupCommandRow) domain.CleanupCommand {
	return domain.CleanupCommand{
		TaskID:             repository.UUIDFromPG(row.TaskID),
		CleanupID:          repository.UUIDFromPG(row.CleanupID),
		TaskState:          domain.TaskState(row.TaskState),
		CleanupState:       domain.CleanupState(row.CleanupStatus),
		DownloadID:         repository.UUIDFromPG(row.DownloadID),
		TorrentHash:        stringValue(row.TorrentHash),
		DownloadPath:       stringValue(row.SavePath),
		StagedVideoPath:    row.StagedVideoPath,
		StagedSubtitlePath: row.StagedSubtitlePath,
		DownloadRemovable:  row.DownloadRemovable,
	}
}

func taskFromGetRow(row db.GetTaskViewRow) domain.EpisodeTask {
	return buildTaskView(taskViewValues{
		id: row.ID, acquisitionID: row.AcquisitionID, downloadID: row.DownloadID, embyItemID: row.EmbyItemID, embyLibraryID: row.EmbyLibraryID,
		state: row.State, videoState: row.VideoState, subtitleState: row.SubtitleState, mediaType: row.MediaType,
		version: row.Version, failureStage: row.FailureStage, errorCode: row.ErrorCode, errorMessage: row.ErrorMessage, createdAt: row.CreatedAt, updatedAt: row.UpdatedAt,
		releaseYear: row.ReleaseYear, sourceSeason: row.SourceSeason, sourceEpisode: row.SourceEpisode, seriesTitle: row.SeriesTitle, targetSeason: row.TargetSeason,
		targetEpisode: row.TargetEpisode, targetEpisodeTitle: row.TargetEpisodeTitle, artifactSetID: row.ArtifactSetID,
		artifactBasename: row.ArtifactBasename, videoArtifactID: row.VideoArtifactID, videoFilePath: row.VideoFilePath,
		videoFormat: row.VideoFormat, videoSizeBytes: row.VideoSizeBytes, videoChecksum: row.VideoChecksumSha256,
		subtitleArtifactID: row.SubtitleArtifactID, subtitleFilePath: row.SubtitleFilePath, subtitleFormat: row.SubtitleFormat,
		subtitleSizeBytes: row.SubtitleSizeBytes, subtitleChecksum: row.SubtitleChecksumSha256,
		reviewID: row.ReviewID, reviewDecision: row.ReviewDecision, reviewNotes: row.ReviewNotes, reviewedBy: row.ReviewedBy, reviewedAt: row.ReviewedAt,
		importID: row.ImportID, importAttempt: row.ImportAttempt, importStatus: row.ImportStatus, destinationVideoPath: row.DestinationVideoPath,
		destinationSubtitlePath: row.DestinationSubtitlePath, importErrorCode: row.ImportErrorCode, importErrorMessage: row.ImportErrorMessage,
		importStartedAt: row.ImportStartedAt, importCompletedAt: row.ImportCompletedAt, importCreatedAt: row.ImportCreatedAt, importUpdatedAt: row.ImportUpdatedAt,
		cleanupID: row.CleanupID, cleanupAttempt: row.CleanupAttempt, cleanupStatus: row.CleanupStatus, torrentRemoved: row.TorrentRemoved,
		stagedFilesRemoved: row.StagedFilesRemoved, cleanupErrorCode: row.CleanupErrorCode, cleanupErrorMessage: row.CleanupErrorMessage,
		cleanupStartedAt: row.CleanupStartedAt, cleanupCompletedAt: row.CleanupCompletedAt, cleanupCreatedAt: row.CleanupCreatedAt, cleanupUpdatedAt: row.CleanupUpdatedAt,
	})
}

func taskFromListRow(row db.ListTaskViewsRow) domain.EpisodeTask {
	return buildTaskView(taskViewValues{
		id: row.ID, acquisitionID: row.AcquisitionID, downloadID: row.DownloadID, embyItemID: row.EmbyItemID, embyLibraryID: row.EmbyLibraryID,
		state: row.State, videoState: row.VideoState, subtitleState: row.SubtitleState, mediaType: row.MediaType,
		version: row.Version, failureStage: row.FailureStage, errorCode: row.ErrorCode, errorMessage: row.ErrorMessage, createdAt: row.CreatedAt, updatedAt: row.UpdatedAt,
		releaseYear: row.ReleaseYear, sourceSeason: row.SourceSeason, sourceEpisode: row.SourceEpisode, seriesTitle: row.SeriesTitle, targetSeason: row.TargetSeason,
		targetEpisode: row.TargetEpisode, targetEpisodeTitle: row.TargetEpisodeTitle, artifactSetID: row.ArtifactSetID,
		artifactBasename: row.ArtifactBasename, videoArtifactID: row.VideoArtifactID, videoFilePath: row.VideoFilePath,
		videoFormat: row.VideoFormat, videoSizeBytes: row.VideoSizeBytes, videoChecksum: row.VideoChecksumSha256,
		subtitleArtifactID: row.SubtitleArtifactID, subtitleFilePath: row.SubtitleFilePath, subtitleFormat: row.SubtitleFormat,
		subtitleSizeBytes: row.SubtitleSizeBytes, subtitleChecksum: row.SubtitleChecksumSha256,
		reviewID: row.ReviewID, reviewDecision: row.ReviewDecision, reviewNotes: row.ReviewNotes, reviewedBy: row.ReviewedBy, reviewedAt: row.ReviewedAt,
		importID: row.ImportID, importAttempt: row.ImportAttempt, importStatus: row.ImportStatus, destinationVideoPath: row.DestinationVideoPath,
		destinationSubtitlePath: row.DestinationSubtitlePath, importErrorCode: row.ImportErrorCode, importErrorMessage: row.ImportErrorMessage,
		importStartedAt: row.ImportStartedAt, importCompletedAt: row.ImportCompletedAt, importCreatedAt: row.ImportCreatedAt, importUpdatedAt: row.ImportUpdatedAt,
		cleanupID: row.CleanupID, cleanupAttempt: row.CleanupAttempt, cleanupStatus: row.CleanupStatus, torrentRemoved: row.TorrentRemoved,
		stagedFilesRemoved: row.StagedFilesRemoved, cleanupErrorCode: row.CleanupErrorCode, cleanupErrorMessage: row.CleanupErrorMessage,
		cleanupStartedAt: row.CleanupStartedAt, cleanupCompletedAt: row.CleanupCompletedAt, cleanupCreatedAt: row.CleanupCreatedAt, cleanupUpdatedAt: row.CleanupUpdatedAt,
	})
}

type taskViewValues struct {
	id, acquisitionID, downloadID, embyItemID, embyLibraryID, artifactSetID, videoArtifactID, subtitleArtifactID, reviewID, reviewedBy, importID, cleanupID pgtype.UUID
	state, videoState, subtitleState, importStatus, cleanupStatus, mediaType                                                                                string
	version, importAttempt, cleanupAttempt                                                                                                                  int32
	failureStage, errorCode, errorMessage, seriesTitle, artifactBasename, videoFilePath, videoFormat, subtitleFilePath, subtitleFormat                      *string
	sourceSeason, sourceEpisode, targetSeason, targetEpisode, releaseYear                                                                                   *int32
	targetEpisodeTitle, reviewDecision, reviewNotes, destinationVideoPath, destinationSubtitlePath                                                          *string
	videoSizeBytes, subtitleSizeBytes                                                                                                                       *int64
	videoChecksum, subtitleChecksum                                                                                                                         []byte
	importErrorCode, importErrorMessage, cleanupErrorCode, cleanupErrorMessage                                                                              *string
	createdAt, updatedAt, reviewedAt, importStartedAt, importCompletedAt, importCreatedAt, importUpdatedAt                                                  pgtype.Timestamptz
	cleanupStartedAt, cleanupCompletedAt, cleanupCreatedAt, cleanupUpdatedAt                                                                                pgtype.Timestamptz
	torrentRemoved, stagedFilesRemoved                                                                                                                      bool
}

func buildTaskView(value taskViewValues) domain.EpisodeTask {
	task := domain.EpisodeTask{
		ID: repository.UUIDFromPG(value.id), AcquisitionID: repository.UUIDFromPG(value.acquisitionID), DownloadID: repository.UUIDFromPG(value.downloadID),
		MediaType: domain.TaskMediaType(value.mediaType), SeriesTitle: stringValue(value.seriesTitle),
		MovieTitle: movieTitle(value.mediaType, stringValue(value.seriesTitle)), ReleaseYear: intValue(value.releaseYear),
		SourceSeason: intValue(value.sourceSeason), SourceEpisode: intValue(value.sourceEpisode),
		TargetSeason: intValue(value.targetSeason), TargetEpisode: intValue(value.targetEpisode), TargetEpisodeTitle: stringValue(value.targetEpisodeTitle),
		State: domain.TaskState(value.state), VideoState: domain.VideoState(value.videoState), SubtitleState: domain.SubtitleState(value.subtitleState),
		Version: value.version, FailureStage: stringValue(value.failureStage), ErrorCode: stringValue(value.errorCode), ErrorMessage: stringValue(value.errorMessage),
		CreatedAt: value.createdAt.Time, UpdatedAt: value.updatedAt.Time,
	}
	if value.artifactSetID.Valid {
		task.Artifacts = &domain.TaskArtifactSet{
			ID: repository.UUIDFromPG(value.artifactSetID), BaseName: stringValue(value.artifactBasename),
			Video:    domain.MediaArtifact{ID: repository.UUIDFromPG(value.videoArtifactID), TaskID: task.ID, Kind: domain.MediaVideo, BaseName: stringValue(value.artifactBasename), FilePath: stringValue(value.videoFilePath), Format: stringValue(value.videoFormat), SizeBytes: int64ValuePointer(value.videoSizeBytes), ChecksumSHA256: value.videoChecksum},
			Subtitle: domain.MediaArtifact{ID: repository.UUIDFromPG(value.subtitleArtifactID), TaskID: task.ID, Kind: domain.MediaSubtitle, BaseName: stringValue(value.artifactBasename), FilePath: stringValue(value.subtitleFilePath), Format: stringValue(value.subtitleFormat), SizeBytes: int64ValuePointer(value.subtitleSizeBytes), ChecksumSHA256: value.subtitleChecksum},
		}
	}
	if value.reviewID.Valid {
		task.Review = &domain.TaskReview{ID: repository.UUIDFromPG(value.reviewID), Decision: domain.TaskState(stringValue(value.reviewDecision)), Notes: stringValue(value.reviewNotes), ReviewedBy: repository.UUIDFromPG(value.reviewedBy), ReviewedAt: value.reviewedAt.Time}
	}
	if value.importID.Valid {
		task.Import = &domain.TaskImport{ID: repository.UUIDFromPG(value.importID), Attempt: int(value.importAttempt), Status: value.importStatus,
			DestinationVideoPath: stringValue(value.destinationVideoPath), DestinationSubtitlePath: stringValue(value.destinationSubtitlePath),
			ErrorCode: stringValue(value.importErrorCode), ErrorMessage: stringValue(value.importErrorMessage), StartedAt: optionalTimePointer(value.importStartedAt),
			CompletedAt: optionalTimePointer(value.importCompletedAt), CreatedAt: value.importCreatedAt.Time, UpdatedAt: value.importUpdatedAt.Time}
	}
	if value.cleanupID.Valid {
		task.Cleanup = &domain.TaskCleanup{ID: repository.UUIDFromPG(value.cleanupID), Attempt: int(value.cleanupAttempt), Status: domain.CleanupState(value.cleanupStatus),
			TorrentRemoved: value.torrentRemoved, StagedFilesRemoved: value.stagedFilesRemoved, ErrorCode: stringValue(value.cleanupErrorCode),
			ErrorMessage: stringValue(value.cleanupErrorMessage), StartedAt: optionalTimePointer(value.cleanupStartedAt), CompletedAt: optionalTimePointer(value.cleanupCompletedAt),
			CreatedAt: value.cleanupCreatedAt.Time, UpdatedAt: value.cleanupUpdatedAt.Time}
	}
	if value.embyItemID.Valid {
		itemID := repository.UUIDFromPG(value.embyItemID)
		task.EmbyItemID = &itemID
	}
	if value.embyLibraryID.Valid {
		libraryID := repository.UUIDFromPG(value.embyLibraryID)
		task.EmbyLibraryID = &libraryID
	}
	task.Actions = domain.TaskActions{
		CanRetry: (task.State == domain.TaskFailed && (task.FailureStage != "" || task.VideoState == domain.VideoFailed || task.SubtitleState == domain.SubtitleFailed)) ||
			(task.State == domain.TaskProcessing && (task.VideoState == domain.VideoFailed || task.SubtitleState == domain.SubtitleFailed)) ||
			(task.State == domain.TaskImported && task.Cleanup != nil && task.Cleanup.Status == domain.CleanupFailed),
		CanCancel: task.State != domain.TaskImported && task.State != domain.TaskFailed && task.State != domain.TaskCancelled && task.State != domain.TaskRejected,
		CanReview: task.State == domain.TaskAwaitingReview && task.Artifacts != nil,
		CanImport: task.State == domain.TaskApproved && task.Artifacts != nil && task.Review != nil && task.Review.Decision == domain.TaskApproved,
	}
	return task
}

func intValue(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}

func int64ValuePointer(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
