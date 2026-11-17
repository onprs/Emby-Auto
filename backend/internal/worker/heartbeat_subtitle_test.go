package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/subtitlebinding"
)

type subtitleHeartbeatLifecycleStub struct {
	ok  bool
	err error
}

func (stub *subtitleHeartbeatLifecycleStub) HeartbeatSubtitleAttempt(context.Context, subtitlebinding.AttemptHandle) (bool, error) {
	return stub.ok, stub.err
}
func (*subtitleHeartbeatLifecycleStub) SucceedSubtitleAttempt(context.Context, subtitlebinding.AttemptHandle) error {
	return nil
}
func (*subtitleHeartbeatLifecycleStub) FailSubtitleAttempt(context.Context, subtitlebinding.AttemptHandle, string, string, bool) error {
	return nil
}
func (*subtitleHeartbeatLifecycleStub) CancelSubtitleAttempt(context.Context, subtitlebinding.AttemptHandle) error {
	return nil
}
func (*subtitleHeartbeatLifecycleStub) SnoozeSubtitleAttempt(context.Context, subtitlebinding.AttemptHandle) error {
	return nil
}

func TestSubtitleHeartbeatErrorIsRetryableNotCancellation(t *testing.T) {
	heartbeatErr := errors.New("temporary heartbeat database failure")
	lifecycle := &subtitleHeartbeatLifecycleStub{err: heartbeatErr}
	worker := &operationWorker[appqueue.SubtitlePrepareArgs]{heartbeatInterval: time.Millisecond}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan struct{})
	go worker.heartbeatSubtitle(ctx, cancel, subtitlebinding.AttemptHandle{OperationID: uuid.New()}, lifecycle, done)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeatSubtitle did not stop")
	}
	cause := context.Cause(ctx)
	if errors.Is(cause, ErrCancellationRequested) {
		t.Fatalf("heartbeat error was treated as business cancellation: %v", cause)
	}
	if !errors.Is(cause, heartbeatErr) {
		t.Fatalf("heartbeat cause = %v, want wrapped heartbeat error", cause)
	}
}

func TestSubtitleHeartbeatFalseIsCancellation(t *testing.T) {
	lifecycle := &subtitleHeartbeatLifecycleStub{ok: false}
	worker := &operationWorker[appqueue.SubtitlePrepareArgs]{heartbeatInterval: time.Millisecond}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan struct{})
	go worker.heartbeatSubtitle(ctx, cancel, subtitlebinding.AttemptHandle{OperationID: uuid.New()}, lifecycle, done)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeatSubtitle did not stop")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, ErrCancellationRequested) {
		t.Fatalf("heartbeat false cause = %v, want cancellation", cause)
	}
}
