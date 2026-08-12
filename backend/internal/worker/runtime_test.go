package worker

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestNewWorkersRegistersAdministrativeJobs(t *testing.T) {
	workers := NewWorkers(nil, nil, time.Second, "worker-test")
	registry := reflect.ValueOf(workers).Elem().FieldByName("workersMap")
	if !registry.IsValid() {
		t.Fatal("River workers registry is unavailable")
	}
	for _, kind := range []string{
		appqueue.KindRSSSubscriptionComplete,
		appqueue.KindRSSSubscriptionDelete,
	} {
		if !registry.MapIndex(reflect.ValueOf(kind)).IsValid() {
			t.Fatalf("worker kind %q is not registered", kind)
		}
	}
}

type lifecycleStub struct {
	mutex          sync.Mutex
	operation      domain.Operation
	startedAttempt int
	heartbeatAlive bool
	succeeded      bool
	failed         bool
	failureCode    string
	retryable      bool
	cancelled      bool
	snoozed        bool
}

func (stub *lifecycleStub) StartAttempt(_ context.Context, _ uuid.UUID, attempt int, _ string) (domain.Operation, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.startedAttempt = attempt
	return stub.operation, nil
}
func (stub *lifecycleStub) Heartbeat(context.Context, uuid.UUID, int) (bool, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return stub.heartbeatAlive, nil
}
func (stub *lifecycleStub) SucceedAttempt(context.Context, uuid.UUID, int) error {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.succeeded = true
	return nil
}
func (stub *lifecycleStub) FailAttempt(_ context.Context, _ uuid.UUID, _ int, code, _ string, retryable bool) error {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.failed = true
	stub.failureCode = code
	stub.retryable = retryable
	return nil
}
func (stub *lifecycleStub) CancelAttempt(context.Context, uuid.UUID, int) error {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.cancelled = true
	return nil
}
func (stub *lifecycleStub) SnoozeAttempt(context.Context, uuid.UUID, int) error {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.snoozed = true
	return nil
}

func TestOperationWorkerRecordsSuccessfulAttempt(t *testing.T) {
	operationID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	lifecycle := &lifecycleStub{operation: domain.Operation{ID: operationID}, heartbeatAlive: true}
	handled := false
	worker := newOperationWorker[appqueue.RSSPollArgs](lifecycle, HandlerFunc(func(_ context.Context, operation domain.Operation) error {
		handled = operation.ID == operationID
		return nil
	}), time.Hour, "worker-test")

	err := worker.Work(context.Background(), &river.Job[appqueue.RSSPollArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args: appqueue.RSSPollArgs{OperationArgs: appqueue.OperationArgs{
			OperationID:   operationID,
			TimeoutSecond: 30,
		}},
	})
	if err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	if !handled || !lifecycle.succeeded || lifecycle.failed || lifecycle.cancelled {
		t.Fatalf("worker state = handled=%t succeeded=%t failed=%t cancelled=%t", handled, lifecycle.succeeded, lifecycle.failed, lifecycle.cancelled)
	}
	if lifecycle.startedAttempt != 1 {
		t.Fatalf("started attempt = %d, want 1", lifecycle.startedAttempt)
	}
}

func TestOperationWorkerRecordsRetryableAndPermanentFailures(t *testing.T) {
	operationID := uuid.MustParse("10000000-0000-0000-0000-000000000002")
	tests := []struct {
		name          string
		handlerError  error
		wantCode      string
		wantRetryable bool
		wantCancelled bool
	}{
		{name: "ordinary error retries", handlerError: errors.New("temporary upstream error"), wantCode: "operation_failed", wantRetryable: true},
		{name: "classified permanent error cancels River retry", handlerError: &Failure{Code: "invalid_media", Message: "media is invalid", Retryable: false}, wantCode: "invalid_media", wantRetryable: false, wantCancelled: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &lifecycleStub{operation: domain.Operation{ID: operationID}, heartbeatAlive: true}
			worker := newOperationWorker[appqueue.MediaFinalizeArgs](lifecycle, HandlerFunc(func(context.Context, domain.Operation) error {
				return test.handlerError
			}), time.Hour, "worker-test")
			err := worker.Work(context.Background(), &river.Job[appqueue.MediaFinalizeArgs]{
				JobRow: &rivertype.JobRow{Attempt: 2},
				Args:   appqueue.MediaFinalizeArgs{OperationArgs: appqueue.OperationArgs{OperationID: operationID, TimeoutSecond: 60}},
			})
			if err == nil {
				t.Fatal("Work() error = nil")
			}
			var cancelErr *river.JobCancelError
			if errors.As(err, &cancelErr) != test.wantCancelled {
				t.Fatalf("JobCancelError = %t, want %t (error %v)", errors.As(err, &cancelErr), test.wantCancelled, err)
			}
			if !lifecycle.failed || lifecycle.failureCode != test.wantCode || lifecycle.retryable != test.wantRetryable {
				t.Fatalf("failure audit = failed=%t code=%q retryable=%t", lifecycle.failed, lifecycle.failureCode, lifecycle.retryable)
			}
		})
	}
}

func TestOperationWorkerSnoozesWithoutCompletingOrFailingAttempt(t *testing.T) {
	operationID := uuid.MustParse("10000000-0000-0000-0000-000000000005")
	lifecycle := &lifecycleStub{operation: domain.Operation{ID: operationID}, heartbeatAlive: true}
	worker := newOperationWorker[appqueue.DownloadSyncArgs](lifecycle, HandlerFunc(func(context.Context, domain.Operation) error {
		return river.JobSnooze(30 * time.Second)
	}), time.Hour, "worker-test")

	err := worker.Work(context.Background(), &river.Job[appqueue.DownloadSyncArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   appqueue.DownloadSyncArgs{OperationArgs: appqueue.OperationArgs{OperationID: operationID, TimeoutSecond: 30}},
	})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) || snoozeErr.Duration != 30*time.Second {
		t.Fatalf("Work() error = %v, want 30s JobSnoozeError", err)
	}
	if !lifecycle.snoozed || lifecycle.succeeded || lifecycle.failed || lifecycle.cancelled {
		t.Fatalf("lifecycle = snoozed=%t succeeded=%t failed=%t cancelled=%t", lifecycle.snoozed, lifecycle.succeeded, lifecycle.failed, lifecycle.cancelled)
	}
}

func TestOperationWorkerSnoozesShutdownCancellationWithoutConsumingRetry(t *testing.T) {
	operationID := uuid.MustParse("10000000-0000-0000-0000-000000000006")
	lifecycle := &lifecycleStub{operation: domain.Operation{ID: operationID}, heartbeatAlive: true}
	ctx, cancel := context.WithCancel(context.Background())
	worker := newOperationWorker[appqueue.TranscodeRunArgs](lifecycle, HandlerFunc(func(workCtx context.Context, _ domain.Operation) error {
		cancel()
		<-workCtx.Done()
		return workCtx.Err()
	}), time.Hour, "worker-test")

	err := worker.Work(ctx, &river.Job[appqueue.TranscodeRunArgs]{
		JobRow: &rivertype.JobRow{Attempt: 3},
		Args:   appqueue.TranscodeRunArgs{OperationArgs: appqueue.OperationArgs{OperationID: operationID, TimeoutSecond: 3600}},
	})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) || snoozeErr.Duration != time.Second {
		t.Fatalf("Work() error = %v, want 1s JobSnoozeError", err)
	}
	if !lifecycle.snoozed || lifecycle.succeeded || lifecycle.failed || lifecycle.cancelled {
		t.Fatalf("lifecycle = snoozed=%t succeeded=%t failed=%t cancelled=%t", lifecycle.snoozed, lifecycle.succeeded, lifecycle.failed, lifecycle.cancelled)
	}
}

func TestOperationWorkerDiscardsJobCancelledBeforeFirstAttempt(t *testing.T) {
	operationID := uuid.MustParse("10000000-0000-0000-0000-000000000004")
	lifecycle := &lifecycleStub{operation: domain.Operation{ID: operationID, Status: "cancelled"}}
	handled := false
	worker := newOperationWorker[appqueue.RSSPollArgs](lifecycle, HandlerFunc(func(context.Context, domain.Operation) error {
		handled = true
		return nil
	}), time.Hour, "worker-test")

	err := worker.Work(context.Background(), &river.Job[appqueue.RSSPollArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   appqueue.RSSPollArgs{OperationArgs: appqueue.OperationArgs{OperationID: operationID, TimeoutSecond: 30}},
	})
	var cancelErr *river.JobCancelError
	if !errors.As(err, &cancelErr) {
		t.Fatalf("Work() error = %v, want JobCancelError", err)
	}
	if handled || lifecycle.succeeded || lifecycle.failed || lifecycle.cancelled {
		t.Fatalf("cancelled operation ran handler or attempt finalizer: handled=%t succeeded=%t failed=%t cancelled=%t", handled, lifecycle.succeeded, lifecycle.failed, lifecycle.cancelled)
	}
}

func TestOperationWorkerHeartbeatObservesCancellation(t *testing.T) {
	operationID := uuid.MustParse("10000000-0000-0000-0000-000000000003")
	lifecycle := &lifecycleStub{operation: domain.Operation{ID: operationID}, heartbeatAlive: false}
	worker := newOperationWorker[appqueue.TranscodeRunArgs](lifecycle, HandlerFunc(func(ctx context.Context, _ domain.Operation) error {
		<-ctx.Done()
		return ctx.Err()
	}), time.Millisecond, "worker-test")

	err := worker.Work(context.Background(), &river.Job[appqueue.TranscodeRunArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   appqueue.TranscodeRunArgs{OperationArgs: appqueue.OperationArgs{OperationID: operationID, TimeoutSecond: 120}},
	})
	var cancelErr *river.JobCancelError
	if !errors.As(err, &cancelErr) {
		t.Fatalf("Work() error = %v, want JobCancelError", err)
	}
	if !lifecycle.cancelled || lifecycle.failed || lifecycle.succeeded {
		t.Fatalf("lifecycle = cancelled=%t failed=%t succeeded=%t", lifecycle.cancelled, lifecycle.failed, lifecycle.succeeded)
	}
}

func TestOperationWorkerUsesOperationTimeout(t *testing.T) {
	worker := newOperationWorker[appqueue.CleanupRunArgs](&lifecycleStub{}, HandlerFunc(func(context.Context, domain.Operation) error { return nil }), time.Second, "worker-test")
	job := &river.Job[appqueue.CleanupRunArgs]{Args: appqueue.CleanupRunArgs{OperationArgs: appqueue.OperationArgs{TimeoutSecond: 75}}}
	if got := worker.Timeout(job); got != 75*time.Second {
		t.Fatalf("Timeout() = %v, want 75s", got)
	}
}
