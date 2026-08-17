package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

type rssSubscriptionProgressReconcilerStub struct {
	calls int
	count int
	err   error
}

func (stub *rssSubscriptionProgressReconcilerStub) ReconcileSubscriptionProgress(context.Context) (int, error) {
	stub.calls++
	return stub.count, stub.err
}

func TestRSSSubscriptionProgressReconcileWorkerRunsUnifiedReconciler(t *testing.T) {
	stub := &rssSubscriptionProgressReconcilerStub{count: 3}
	worker := NewRSSSubscriptionProgressReconcileWorker(stub)
	job := &river.Job[appqueue.RSSSubscriptionProgressReconcileArgs]{}
	if timeout := worker.Timeout(job); timeout != 5*time.Minute {
		t.Fatalf("Timeout() = %v, want 5m", timeout)
	}
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("reconciler calls = %d, want 1", stub.calls)
	}
}

func TestRSSSubscriptionProgressReconcileWorkerPropagatesRetryableError(t *testing.T) {
	wantErr := errors.New("database unavailable")
	worker := NewRSSSubscriptionProgressReconcileWorker(&rssSubscriptionProgressReconcilerStub{err: wantErr})
	if err := worker.Work(context.Background(), &river.Job[appqueue.RSSSubscriptionProgressReconcileArgs]{}); !errors.Is(err, wantErr) {
		t.Fatalf("Work() error = %v, want %v", err, wantErr)
	}
}

func TestRSSSubscriptionProgressReconcileWorkerRejectsMissingDependency(t *testing.T) {
	worker := NewRSSSubscriptionProgressReconcileWorker(nil)
	if err := worker.Work(context.Background(), &river.Job[appqueue.RSSSubscriptionProgressReconcileArgs]{}); err == nil {
		t.Fatal("Work() error = nil, want missing reconciler error")
	}
}
