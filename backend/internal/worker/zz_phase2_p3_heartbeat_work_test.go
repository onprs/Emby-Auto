package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/subtitlebinding"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type phase2HeartbeatWorkLifecycle struct {
	operation domain.Operation
	alive     bool
	err       error
	failed    bool
	retryable bool
	cancelled bool
	succeeded bool
}

func (stub *phase2HeartbeatWorkLifecycle) StartAttempt(context.Context, uuid.UUID, int, string) (domain.Operation, error) {
	return stub.operation, nil
}
func (*phase2HeartbeatWorkLifecycle) Heartbeat(context.Context, uuid.UUID, int) (bool, error) {
	return true, nil
}
func (*phase2HeartbeatWorkLifecycle) SucceedAttempt(context.Context, uuid.UUID, int) error {
	return nil
}
func (*phase2HeartbeatWorkLifecycle) FailAttempt(context.Context, uuid.UUID, int, string, string, bool) error {
	return nil
}
func (*phase2HeartbeatWorkLifecycle) CancelAttempt(context.Context, uuid.UUID, int) error {
	return nil
}
func (*phase2HeartbeatWorkLifecycle) SnoozeAttempt(context.Context, uuid.UUID, int) error {
	return nil
}
func (stub *phase2HeartbeatWorkLifecycle) HeartbeatSubtitleAttempt(context.Context, subtitlebinding.AttemptHandle) (bool, error) {
	return stub.alive, stub.err
}
func (stub *phase2HeartbeatWorkLifecycle) SucceedSubtitleAttempt(context.Context, subtitlebinding.AttemptHandle) error {
	stub.succeeded = true
	return nil
}
func (stub *phase2HeartbeatWorkLifecycle) FailSubtitleAttempt(_ context.Context, _ subtitlebinding.AttemptHandle, _ string, _ string, retryable bool) error {
	stub.failed = true
	stub.retryable = retryable
	return nil
}
func (stub *phase2HeartbeatWorkLifecycle) CancelSubtitleAttempt(context.Context, subtitlebinding.AttemptHandle) error {
	stub.cancelled = true
	return nil
}
func (stub *phase2HeartbeatWorkLifecycle) SnoozeSubtitleAttempt(context.Context, subtitlebinding.AttemptHandle) error {
	return nil
}

func phase2HeartbeatOperation() domain.Operation {
	operationID, taskID := uuid.New(), uuid.New()
	handle := subtitlebinding.AttemptHandle{
		OperationID: operationID, JobAttempt: 1, AttemptID: uuid.New(), OperationAttemptCount: 1,
		ResourceType: "episode_task", ResourceID: taskID,
	}
	return domain.Operation{
		ID: operationID, Kind: "subtitle.prepare", ResourceType: "episode_task", ResourceID: taskID,
		Status: "running", AttemptCount: 1, MaxAttempts: 3, SubtitleAttempt: &handle,
	}
}

func runPhase2HeartbeatWork(t *testing.T, lifecycle *phase2HeartbeatWorkLifecycle) error {
	t.Helper()
	operation := lifecycle.operation
	handler := HandlerFunc(func(ctx context.Context, _ domain.Operation) error {
		<-ctx.Done()
		return ctx.Err()
	})
	worker := newOperationWorker[appqueue.SubtitlePrepareArgs](lifecycle, handler, time.Millisecond, "phase2-heartbeat-work")
	return worker.Work(context.Background(), &river.Job[appqueue.SubtitlePrepareArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args: appqueue.SubtitlePrepareArgs{OperationArgs: appqueue.OperationArgs{
			OperationID: operation.ID, TimeoutSecond: 30,
		}},
	})
}

func TestPhase2HeartbeatWorkErrorIsRetryable(t *testing.T) {
	heartbeatErr := errors.New("temporary heartbeat db failure")
	lifecycle := &phase2HeartbeatWorkLifecycle{operation: phase2HeartbeatOperation(), err: heartbeatErr}
	err := runPhase2HeartbeatWork(t, lifecycle)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("Work error = %v, want retryable *Failure", err)
	}
	if !failure.Retryable || !lifecycle.failed || !lifecycle.retryable {
		t.Fatalf("heartbeat error path failure=%+v failed=%t retryable=%t, want retryable", failure, lifecycle.failed, lifecycle.retryable)
	}
	if lifecycle.cancelled || lifecycle.succeeded {
		t.Fatalf("heartbeat error path cancelled=%t succeeded=%t, want neither", lifecycle.cancelled, lifecycle.succeeded)
	}
	var jobCancel *river.JobCancelError
	if errors.As(err, &jobCancel) {
		t.Fatalf("heartbeat error returned *river.JobCancelError: %v", err)
	}
	if !errors.Is(err, heartbeatErr) {
		t.Fatalf("heartbeat error cause lost: %v", err)
	}
	if errors.Is(err, ErrCancellationRequested) {
		t.Fatal("heartbeat error was reported as business cancellation")
	}
}

func TestPhase2HeartbeatWorkFalseIsCancellation(t *testing.T) {
	lifecycle := &phase2HeartbeatWorkLifecycle{operation: phase2HeartbeatOperation(), alive: false}
	err := runPhase2HeartbeatWork(t, lifecycle)
	if !lifecycle.cancelled || lifecycle.failed || lifecycle.succeeded {
		t.Fatalf("heartbeat false path cancelled=%t failed=%t succeeded=%t, want cancel only", lifecycle.cancelled, lifecycle.failed, lifecycle.succeeded)
	}
	var jobCancel *river.JobCancelError
	if !errors.As(err, &jobCancel) {
		t.Fatalf("heartbeat false error = %v, want *river.JobCancelError", err)
	}
	if !errors.Is(err, ErrCancellationRequested) {
		t.Fatalf("Work error = %v, want cancellation cause", err)
	}
}
