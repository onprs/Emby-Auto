package worker

import (
	"context"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/riverqueue/river"
)

type TaskCancelStore interface {
	TaskCancellationReady(context.Context, uuid.UUID, uuid.UUID) (bool, error)
}

type TaskCancelHandler struct {
	store TaskCancelStore
}

func NewTaskCancelHandler(store TaskCancelStore) *TaskCancelHandler {
	return &TaskCancelHandler{store: store}
}

func (handler *TaskCancelHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "episode_task" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_task_operation", "task.cancel requires an episode task resource", nil)
	}
	if handler.store == nil {
		return permanentFailure("task_cancel_handler_not_configured", "task cancellation storage is unavailable", nil)
	}
	ready, err := handler.store.TaskCancellationReady(ctx, operation.ResourceID, operation.ID)
	if err != nil {
		return retryableFailure("task_storage_unavailable", "task cancellation could not be reconciled", err)
	}
	if !ready {
		return river.JobSnooze(cancellationReconcileInterval)
	}
	return nil
}
