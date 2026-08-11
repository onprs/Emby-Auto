package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/riverqueue/river"
)

type taskCancelStoreStub struct {
	ready bool
	err   error
}

func (stub taskCancelStoreStub) TaskCancellationReady(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return stub.ready, stub.err
}

func TestTaskCancelHandlerWaitsForEarlierOperations(t *testing.T) {
	operation := domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: uuid.New()}
	err := NewTaskCancelHandler(taskCancelStoreStub{}).Handle(context.Background(), operation)
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != cancellationReconcileInterval {
		t.Fatalf("Handle() error = %v", err)
	}
	if err := NewTaskCancelHandler(taskCancelStoreStub{ready: true}).Handle(context.Background(), operation); err != nil {
		t.Fatalf("ready Handle() error = %v", err)
	}
}
