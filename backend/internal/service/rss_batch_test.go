package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type rssEntrySchedulerFunc func(context.Context, domain.RSSEnqueueCandidate) error

func (fn rssEntrySchedulerFunc) ScheduleRSSDownload(ctx context.Context, candidate domain.RSSEnqueueCandidate) error {
	return fn(ctx, candidate)
}

func TestScheduleRSSBatchAttemptsEveryDownloadableEntryWhenSomeFail(t *testing.T) {
	candidates := []domain.RSSEnqueueCandidate{
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000001"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000002"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000003"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000004"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000005"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000006"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000007"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000008"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000009"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("10000000-0000-0000-0000-000000000010"), Status: domain.RSSDiscovered, Downloadable: true},
	}
	failed := map[uuid.UUID]bool{
		candidates[1].EntryID: true,
		candidates[5].EntryID: true,
		candidates[8].EntryID: true,
	}

	var active atomic.Int32
	var maximumActive atomic.Int32
	var callsMu sync.Mutex
	calls := make(map[uuid.UUID]int)
	scheduler := rssEntrySchedulerFunc(func(_ context.Context, candidate domain.RSSEnqueueCandidate) error {
		callsMu.Lock()
		calls[candidate.EntryID]++
		callsMu.Unlock()

		current := active.Add(1)
		for current > maximumActive.Load() && !maximumActive.CompareAndSwap(maximumActive.Load(), current) {
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)

		if failed[candidate.EntryID] {
			return errors.New("qBittorrent rejected torrent")
		}
		return nil
	})

	result, err := ScheduleRSSBatch(context.Background(), candidates, 4, scheduler)
	if err != nil {
		t.Fatalf("ScheduleRSSBatch() error = %v", err)
	}
	if len(result.Outcomes) != 10 {
		t.Fatalf("outcome count = %d, want 10", len(result.Outcomes))
	}

	succeededCount := 0
	failedCount := 0
	for index, outcome := range result.Outcomes {
		if outcome.EntryID != candidates[index].EntryID {
			t.Fatalf("outcome %d entry = %s, want %s", index, outcome.EntryID, candidates[index].EntryID)
		}
		if outcome.Err == nil {
			succeededCount++
		} else {
			failedCount++
		}
	}
	if succeededCount != 7 || failedCount != 3 {
		t.Fatalf("result counts = succeeded %d failed %d, want 7/3", succeededCount, failedCount)
	}
	if len(calls) != 10 {
		t.Fatalf("scheduled entry count = %d, want 10", len(calls))
	}
	for _, candidate := range candidates {
		if calls[candidate.EntryID] != 1 {
			t.Fatalf("entry %s call count = %d, want 1", candidate.EntryID, calls[candidate.EntryID])
		}
	}
	if maximumActive.Load() < 2 {
		t.Fatalf("maximum concurrent schedules = %d, want at least 2", maximumActive.Load())
	}
}

func TestScheduleRSSBatchRetriesOnlyFailedEntries(t *testing.T) {
	candidates := []domain.RSSEnqueueCandidate{
		{EntryID: uuid.MustParse("20000000-0000-0000-0000-000000000001"), Status: domain.RSSEnqueued, Downloadable: true},
		{EntryID: uuid.MustParse("20000000-0000-0000-0000-000000000002"), Status: domain.RSSEnqueueFailed, Downloadable: true},
		{EntryID: uuid.MustParse("20000000-0000-0000-0000-000000000003"), Status: domain.RSSEnqueued, Downloadable: true},
		{EntryID: uuid.MustParse("20000000-0000-0000-0000-000000000004"), Status: domain.RSSEnqueueFailed, Downloadable: true},
		{EntryID: uuid.MustParse("20000000-0000-0000-0000-000000000005"), Status: domain.RSSEnqueueFailed, Downloadable: true},
		{EntryID: uuid.MustParse("20000000-0000-0000-0000-000000000006"), Status: domain.RSSDiscovered, Downloadable: false},
	}

	var callsMu sync.Mutex
	calls := make(map[uuid.UUID]int)
	scheduler := rssEntrySchedulerFunc(func(_ context.Context, candidate domain.RSSEnqueueCandidate) error {
		callsMu.Lock()
		defer callsMu.Unlock()
		calls[candidate.EntryID]++
		return nil
	})

	result, err := ScheduleRSSBatch(context.Background(), candidates, 2, scheduler)
	if err != nil {
		t.Fatalf("ScheduleRSSBatch() error = %v", err)
	}
	if len(result.Outcomes) != 3 {
		t.Fatalf("outcome count = %d, want 3", len(result.Outcomes))
	}
	wantIDs := []uuid.UUID{candidates[1].EntryID, candidates[3].EntryID, candidates[4].EntryID}
	for index, wantID := range wantIDs {
		if result.Outcomes[index].EntryID != wantID || result.Outcomes[index].Err != nil {
			t.Fatalf("outcome %d = %#v, want successful %s", index, result.Outcomes[index], wantID)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("scheduled entry count = %d, want 3", len(calls))
	}
}
