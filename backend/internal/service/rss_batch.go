package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type RSSEntryScheduler interface {
	ScheduleRSSDownload(context.Context, domain.RSSEnqueueCandidate) error
}

type RSSScheduleOutcome struct {
	EntryID uuid.UUID
	Err     error
}

type RSSBatchResult struct {
	Outcomes []RSSScheduleOutcome
}

// ScheduleRSSBatch schedules every eligible entry independently. One entry
// failing must not stop the rest of a poll from producing download jobs.
func ScheduleRSSBatch(
	ctx context.Context,
	candidates []domain.RSSEnqueueCandidate,
	maxConcurrency int,
	scheduler RSSEntryScheduler,
) (RSSBatchResult, error) {
	if maxConcurrency <= 0 {
		return RSSBatchResult{}, errors.New("RSS schedule concurrency must be positive")
	}
	if scheduler == nil {
		return RSSBatchResult{}, errors.New("RSS entry scheduler is required")
	}

	eligible := make([]domain.RSSEnqueueCandidate, 0, len(candidates))
	seen := make(map[uuid.UUID]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.EntryID == uuid.Nil {
			return RSSBatchResult{}, errors.New("RSS entry ID is required")
		}
		if _, exists := seen[candidate.EntryID]; exists {
			return RSSBatchResult{}, fmt.Errorf("duplicate RSS entry ID %s", candidate.EntryID)
		}
		seen[candidate.EntryID] = struct{}{}
		if !candidate.Downloadable || (candidate.Status != domain.RSSDiscovered && candidate.Status != domain.RSSEnqueueFailed) {
			continue
		}
		eligible = append(eligible, candidate)
	}

	result := RSSBatchResult{Outcomes: make([]RSSScheduleOutcome, len(eligible))}
	if len(eligible) == 0 {
		return result, nil
	}
	if maxConcurrency > len(eligible) {
		maxConcurrency = len(eligible)
	}

	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(maxConcurrency)
	for range maxConcurrency {
		go func() {
			defer workers.Done()
			for index := range jobs {
				candidate := eligible[index]
				result.Outcomes[index] = RSSScheduleOutcome{
					EntryID: candidate.EntryID,
					Err:     scheduler.ScheduleRSSDownload(ctx, candidate),
				}
			}
		}()
	}

	dispatched := 0
dispatchLoop:
	for index := range eligible {
		select {
		case jobs <- index:
			dispatched++
		case <-ctx.Done():
			break dispatchLoop
		}
	}
	close(jobs)
	workers.Wait()

	if dispatched < len(eligible) {
		for index := dispatched; index < len(eligible); index++ {
			result.Outcomes[index] = RSSScheduleOutcome{EntryID: eligible[index].EntryID, Err: ctx.Err()}
		}
		return result, ctx.Err()
	}
	return result, nil
}
