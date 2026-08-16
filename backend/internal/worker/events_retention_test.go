package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

type eventsRetentionConfigurationStub struct {
	configuration domain.Configuration
	loadErr       error
}

func (stub *eventsRetentionConfigurationStub) Load(context.Context) (domain.Configuration, error) {
	return stub.configuration, stub.loadErr
}

type eventsRetentionStoreStub struct {
	deleted []int64
	err     error
	calls   int
}

func (stub *eventsRetentionStoreStub) DeleteExpired(_ context.Context, before time.Time, maxRows int32) (int64, error) {
	stub.calls++
	if stub.err != nil {
		return 0, stub.err
	}
	if len(stub.deleted) == 0 {
		return 0, nil
	}
	deleted := stub.deleted[0]
	stub.deleted = stub.deleted[1:]
	if deleted > int64(maxRows) {
		deleted = int64(maxRows)
	}
	return deleted, nil
}

func newEventsRetentionWorker(
	configuration EventsRetentionConfiguration,
	store EventsRetentionStore,
	now time.Time,
) *EventsRetentionWorker {
	worker := NewEventsRetentionWorker(configuration, store)
	worker.now = func() time.Time { return now }
	return worker
}

func TestEventsRetentionWorkerSkipsWhenRetentionIsDisabled(t *testing.T) {
	store := &eventsRetentionStoreStub{}
	worker := newEventsRetentionWorker(
		&eventsRetentionConfigurationStub{configuration: domain.Configuration{
			Settings: domain.RuntimeSettings{Events: domain.EventsSettings{RetentionDays: 0}},
		}},
		store,
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
	)
	err := worker.Work(context.Background(), &river.Job[appqueue.EventsRetentionCleanupArgs]{})
	if err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("Work() called DeleteExpired() %d times with retention disabled, want 0", store.calls)
	}
}

func TestEventsRetentionWorkerStopsWhenBatchIsNotFull(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	store := &eventsRetentionStoreStub{deleted: []int64{1000, 1000, 350, 1000}}
	worker := newEventsRetentionWorker(
		&eventsRetentionConfigurationStub{configuration: domain.Configuration{
			Settings: domain.RuntimeSettings{Events: domain.EventsSettings{RetentionDays: 30}},
		}},
		store,
		now,
	)
	if err := worker.Work(context.Background(), &river.Job[appqueue.EventsRetentionCleanupArgs]{}); err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	if store.calls != 3 {
		t.Fatalf("Work() DeleteExpired() calls = %d, want 3", store.calls)
	}
	if len(store.deleted) != 1 {
		t.Fatalf("Work() remaining batches = %d, want 1 after partial batch", len(store.deleted))
	}
}

func TestEventsRetentionWorkerStopsAtPerJobBatchBudget(t *testing.T) {
	store := &eventsRetentionStoreStub{deleted: []int64{
		1000, 1000, 1000, 1000, 1000,
		1000, 1000, 1000, 1000, 1000,
		1000,
	}}
	worker := newEventsRetentionWorker(
		&eventsRetentionConfigurationStub{configuration: domain.Configuration{
			Settings: domain.RuntimeSettings{Events: domain.EventsSettings{RetentionDays: 30}},
		}},
		store,
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
	)
	if err := worker.Work(context.Background(), &river.Job[appqueue.EventsRetentionCleanupArgs]{}); err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	if store.calls != 10 {
		t.Fatalf("Work() DeleteExpired() calls = %d, want 10", store.calls)
	}
	if len(store.deleted) != 1 {
		t.Fatalf("Work() remaining batches = %d, want 1 for the next hourly job", len(store.deleted))
	}
}

func TestEventsRetentionWorkerPropagatesDeletionError(t *testing.T) {
	wantErr := errors.New("database unavailable")
	store := &eventsRetentionStoreStub{err: wantErr}
	worker := newEventsRetentionWorker(
		&eventsRetentionConfigurationStub{configuration: domain.Configuration{
			Settings: domain.RuntimeSettings{Events: domain.EventsSettings{RetentionDays: 30}},
		}},
		store,
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
	)
	err := worker.Work(context.Background(), &river.Job[appqueue.EventsRetentionCleanupArgs]{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Work() error = %v, want %v", err, wantErr)
	}
}

func TestEventsRetentionWorkerPropagatesConfigurationError(t *testing.T) {
	wantErr := errors.New("configuration unavailable")
	worker := newEventsRetentionWorker(
		&eventsRetentionConfigurationStub{loadErr: wantErr},
		&eventsRetentionStoreStub{},
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
	)
	err := worker.Work(context.Background(), &river.Job[appqueue.EventsRetentionCleanupArgs]{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Work() error = %v, want %v", err, wantErr)
	}
}

func TestEventsRetentionWorkerReportsMissingDependencies(t *testing.T) {
	worker := &EventsRetentionWorker{}
	err := worker.Work(context.Background(), &river.Job[appqueue.EventsRetentionCleanupArgs]{})
	if err == nil {
		t.Fatal("Work() error = nil, want missing dependency error")
	}
}

func TestEventsRetentionWorkerUsesConfiguredCutoff(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	var observedCutoff time.Time
	store := &cutoffRecordingStore{onDelete: func(before time.Time) {
		observedCutoff = before
	}}
	worker := newEventsRetentionWorker(
		&eventsRetentionConfigurationStub{configuration: domain.Configuration{
			Settings: domain.RuntimeSettings{Events: domain.EventsSettings{RetentionDays: 7}},
		}},
		store,
		now,
	)
	if err := worker.Work(context.Background(), &river.Job[appqueue.EventsRetentionCleanupArgs]{}); err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	wantCutoff := now.Add(-7 * 24 * time.Hour)
	if !observedCutoff.Equal(wantCutoff) {
		t.Fatalf("Work() cutoff = %v, want %v", observedCutoff, wantCutoff)
	}
}

type cutoffRecordingStore struct {
	onDelete func(time.Time)
}

func (store *cutoffRecordingStore) DeleteExpired(_ context.Context, before time.Time, _ int32) (int64, error) {
	store.onDelete(before)
	return 0, nil
}
