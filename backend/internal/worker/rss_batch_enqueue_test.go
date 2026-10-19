package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type rssBatchVerifierStub struct {
	rssRealtimeVerifierStub
	checks  []uuid.UUID
	batches [][]uuid.UUID
	err     error
}

func (stub *rssBatchVerifierStub) VerifyEntries(_ context.Context, ids []uuid.UUID) (uuid.UUID, error) {
	stub.batches = append(stub.batches, append([]uuid.UUID(nil), ids...))
	if stub.err != nil || len(stub.checks) == 0 {
		return uuid.Nil, stub.err
	}
	return stub.checks[len(stub.batches)-1], nil
}

type rssBatchStoreStub struct {
	rssPollStoreStub
	checks  []uuid.UUID
	batches [][]domain.RSSEnqueueCandidate
}

func (stub *rssBatchStoreStub) ScheduleRSSDownloadsWithRealtimeCheck(_ context.Context, candidates []domain.RSSEnqueueCandidate, checkID uuid.UUID) (service.RSSBatchResult, error) {
	stub.checks = append(stub.checks, checkID)
	stub.batches = append(stub.batches, append([]domain.RSSEnqueueCandidate(nil), candidates...))
	result := service.RSSBatchResult{Outcomes: make([]service.RSSScheduleOutcome, len(candidates))}
	for index, candidate := range candidates {
		result.Outcomes[index].EntryID = candidate.EntryID
	}
	return result, nil
}

func TestRSSBatchSchedulerRefreshesEachBoundedBatch(t *testing.T) {
	verifier := &rssBatchVerifierStub{checks: []uuid.UUID{uuid.New(), uuid.New()}}
	store := &rssBatchStoreStub{}
	scheduler := &rssRealtimeBatchEntryScheduler{
		rssRealtimeEntryScheduler: &rssRealtimeEntryScheduler{verifier: verifier, store: store},
		verifier:                  verifier, store: store, maxConcurrency: 4,
	}
	candidates := make([]domain.RSSEnqueueCandidate, 51)
	for index := range candidates {
		candidates[index] = domain.RSSEnqueueCandidate{EntryID: uuid.New(), Downloadable: true, Status: domain.RSSDiscovered}
	}
	result, err := service.ScheduleRSSBatch(context.Background(), candidates, 4, scheduler)
	if err != nil || len(result.Outcomes) != 51 || len(store.batches) != 2 || len(verifier.batches) != 2 ||
		len(store.batches[0]) != 50 || len(store.batches[1]) != 1 ||
		store.checks[0] != verifier.checks[0] || store.checks[1] != verifier.checks[1] {
		t.Fatalf("批次边界或实时证据复用范围错误：batches=%v checks=%v outcomes=%d err=%v", store.batches, store.checks, len(result.Outcomes), err)
	}
}

func TestRSSBatchVerifierFailureFallsBackWithoutBlockingOtherEntry(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	entryFailure := errors.New("entry mapping unavailable")
	verifier := &rssBatchVerifierStub{
		rssRealtimeVerifierStub: rssRealtimeVerifierStub{entryCheckIDs: map[uuid.UUID]uuid.UUID{second: uuid.New()}, entryErrors: map[uuid.UUID]error{first: entryFailure}},
		err:                     errors.New("batch mapping unavailable"),
	}
	store := &rssBatchStoreStub{}
	scheduler := &rssRealtimeBatchEntryScheduler{
		rssRealtimeEntryScheduler: &rssRealtimeEntryScheduler{verifier: verifier, store: store},
		verifier:                  verifier, store: store, maxConcurrency: 1,
	}
	result, err := scheduler.ScheduleRSSDownloads(context.Background(), []domain.RSSEnqueueCandidate{
		{EntryID: first, Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: second, Status: domain.RSSDiscovered, Downloadable: true},
	})
	if err != nil || len(result.Outcomes) != 2 || !errors.Is(result.Outcomes[0].Err, entryFailure) || result.Outcomes[1].Err != nil ||
		len(store.batches) != 0 || store.scheduleCalls[first] != 0 || store.scheduleCalls[second] != 1 {
		t.Fatalf("批次核验失败阻断了其它条目：outcomes=%+v scheduled=%v err=%v", result.Outcomes, store.scheduleCalls, err)
	}
}

func TestRSSBatchVerifierRejectsMissingConfiguration(t *testing.T) {
	for _, verifier := range []*RSSRealtimeEmbyVerifier{nil, {}} {
		id, err := verifier.VerifyEntries(context.Background(), []uuid.UUID{uuid.New()})
		var verificationErr *service.RSSRealtimeVerificationError
		if id != uuid.Nil || !errors.As(err, &verificationErr) || verificationErr.Code != "rss_realtime_not_configured" || verificationErr.Retryable {
			t.Fatalf("缺失批量核验配置未明确失败：id=%v err=%v", id, err)
		}
	}
}
