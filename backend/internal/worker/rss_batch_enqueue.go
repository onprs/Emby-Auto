package worker

import (
	"context"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type rssRealtimeBatchVerifier interface {
	VerifyEntries(context.Context, []uuid.UUID) (uuid.UUID, error)
}

type rssRealtimeBatchStore interface {
	ScheduleRSSDownloadsWithRealtimeCheck(context.Context, []domain.RSSEnqueueCandidate, uuid.UUID) (service.RSSBatchResult, error)
}

type rssRealtimeBatchEntryScheduler struct {
	*rssRealtimeEntryScheduler
	verifier       rssRealtimeBatchVerifier
	store          rssRealtimeBatchStore
	maxConcurrency int
}

func (scheduler *rssRealtimeBatchEntryScheduler) ScheduleRSSDownloads(ctx context.Context, candidates []domain.RSSEnqueueCandidate) (service.RSSBatchResult, error) {
	result := service.RSSBatchResult{Outcomes: make([]service.RSSScheduleOutcome, 0, len(candidates))}
	for start := 0; start < len(candidates); start += service.RSSScheduleBatchSize {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		end := min(start+service.RSSScheduleBatchSize, len(candidates))
		batch := candidates[start:end]
		entryIDs := make([]uuid.UUID, len(batch))
		for index, candidate := range batch {
			entryIDs[index] = candidate.EntryID
		}
		// 每个入队批次都在事务外重新获取证据；仅复用同一批次内的查询结果。
		checkID, err := scheduler.verifier.VerifyEntries(ctx, entryIDs)
		if err != nil || checkID == uuid.Nil {
			// 单项映射失效等异常仍使用逐项路径，不能阻断同批的其它资源。
			fallback, fallbackErr := service.ScheduleRSSBatch(ctx, batch, scheduler.maxConcurrency, scheduler.rssRealtimeEntryScheduler)
			result.Outcomes = append(result.Outcomes, fallback.Outcomes...)
			if fallbackErr != nil {
				return result, fallbackErr
			}
			continue
		}
		scheduled, err := scheduler.store.ScheduleRSSDownloadsWithRealtimeCheck(ctx, batch, checkID)
		if err != nil {
			return result, err
		}
		result.Outcomes = append(result.Outcomes, scheduled.Outcomes...)
	}
	return result, nil
}
