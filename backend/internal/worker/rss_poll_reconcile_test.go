package worker

import (
	"context"
	"errors"
	"testing"

	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

type rssDuePollReconcilerStub struct {
	calls int
	err   error
}

func (stub *rssDuePollReconcilerStub) ReconcileDuePolls(context.Context) (int, error) {
	stub.calls++
	return 1, stub.err
}

func TestRSSPollReconcileWorkerRunsDuePollRecovery(t *testing.T) {
	stub := &rssDuePollReconcilerStub{}
	worker := NewRSSPollReconcileWorker(stub)
	if err := worker.Work(context.Background(), &river.Job[appqueue.RSSPollReconcileArgs]{}); err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", stub.calls)
	}
}

func TestRSSPollReconcileWorkerPropagatesUnavailableAndStoreErrors(t *testing.T) {
	if err := NewRSSPollReconcileWorker(nil).Work(context.Background(), &river.Job[appqueue.RSSPollReconcileArgs]{}); err == nil {
		t.Fatal("Work(nil) error = nil")
	}
	want := errors.New("storage unavailable")
	stub := &rssDuePollReconcilerStub{err: want}
	if err := NewRSSPollReconcileWorker(stub).Work(context.Background(), &river.Job[appqueue.RSSPollReconcileArgs]{}); !errors.Is(err, want) {
		t.Fatalf("Work() error = %v, want %v", err, want)
	}
}
