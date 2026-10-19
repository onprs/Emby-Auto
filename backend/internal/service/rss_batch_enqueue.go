package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

const RSSScheduleBatchSize = 50

// ScheduleRSSDownloadsWithRealtimeCheck 在一个有界事务中入队，提交前只重算一次订阅进度。
// 条目各自使用保存点，保留单项失败不阻塞其它条目的行为。
func (workflow *RSSWorkflow) ScheduleRSSDownloadsWithRealtimeCheck(
	ctx context.Context,
	candidates []domain.RSSEnqueueCandidate,
	checkID uuid.UUID,
) (RSSBatchResult, error) {
	result := RSSBatchResult{Outcomes: make([]RSSScheduleOutcome, len(candidates))}
	if len(candidates) == 0 {
		return result, nil
	}
	if checkID == uuid.Nil || len(candidates) > RSSScheduleBatchSize {
		return RSSBatchResult{}, NewError("invalid_rss_batch", "the RSS enqueue batch requires a current target check and a bounded size", ErrInvalidInput, nil)
	}
	entryIDs := make([]pgtype.UUID, 0, len(candidates))
	byID := make(map[uuid.UUID]domain.RSSEnqueueCandidate, len(candidates))
	for index, candidate := range candidates {
		if candidate.EntryID == uuid.Nil {
			return RSSBatchResult{}, fmt.Errorf("RSS entry ID is required")
		}
		if _, exists := byID[candidate.EntryID]; exists {
			return RSSBatchResult{}, fmt.Errorf("duplicate RSS entry ID %s", candidate.EntryID)
		}
		entryIDs = append(entryIDs, repository.UUIDToPG(candidate.EntryID))
		byID[candidate.EntryID] = candidate
		result.Outcomes[index].EntryID = candidate.EntryID
	}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		subscriptions, err := scope.Queries.LockRSSSubscriptionsForEntries(ctx, entryIDs)
		if err != nil {
			return fmt.Errorf("lock RSS enqueue batch subscription: %w", err)
		}
		if len(subscriptions) == 0 {
			return nil
		}
		if len(subscriptions) != 1 {
			return NewError("invalid_rss_batch", "the RSS enqueue batch must belong to one subscription", ErrInvalidInput, nil)
		}
		subscription := subscriptions[0]
		entries, err := scope.Queries.ListRSSEntriesByIDs(ctx, entryIDs)
		if err != nil {
			return fmt.Errorf("load RSS enqueue batch entries: %w", err)
		}
		targets := make(map[uuid.UUID]uuid.UUID, len(entries))
		preparationErrors := make(map[uuid.UUID]error)
		lockTargets := make([]uuid.UUID, 0, len(entries))
		for _, entry := range entries {
			entryID := repository.UUIDFromPG(entry.ID)
			candidate := byID[entryID]
			if !subscription.Enabled || subscription.DeletedAt.Valid || subscription.CompletedAt.Valid ||
				(candidate.ExpectedSubscriptionVersion > 0 && candidate.ExpectedSubscriptionVersion != subscription.Version) ||
				!entry.Downloadable || (entry.Status != string(domain.RSSDiscovered) && entry.Status != string(domain.RSSEnqueueFailed)) {
				continue
			}
			analysis := domain.AnalyzeRSSRelease(entry.Title, valueOrEmpty(entry.DownloadUri), int(subscription.SourceSeason), subscription.IncludeKeywords, subscription.ExcludeKeywords)
			if !analysis.Downloadable && !rssOnlySoftCoordinateReasons(analysis.RejectionReasons) {
				continue
			}
			targetID, err := scope.Queries.GetRSSMappedTargetEpisodeID(ctx, db.GetRSSMappedTargetEpisodeIDParams{
				SubscriptionID: subscription.ID, SourceSeason: int32(valueInt32(entry.SourceSeason)), SourceEpisode: int32(valueInt32(entry.SourceEpisode)),
			})
			if errors.Is(err, pgx.ErrNoRows) {
				preparationErrors[entryID] = NewError("rss_realtime_mapping_unavailable", "the RSS source coordinate does not have a mapped target for real-time verification", ErrStateConflict, nil)
				continue
			}
			if err != nil {
				return fmt.Errorf("load RSS enqueue batch target: %w", err)
			}
			targets[entryID] = repository.UUIDFromPG(targetID)
			lockTargets = append(lockTargets, repository.UUIDFromPG(targetID))
		}
		// 跨订阅竞争也统一按目标顺序加锁，避免批次持有相反顺序的目标锁。
		if err := lockEpisodeMappingTargets(ctx, scope, lockTargets); err != nil {
			return err
		}
		for index, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := preparationErrors[candidate.EntryID]; err != nil {
				result.Outcomes[index].Err = err
				continue
			}
			tx, err := scope.Tx.Begin(ctx)
			if err != nil {
				return err
			}
			var expectedTarget *uuid.UUID
			if target, exists := targets[candidate.EntryID]; exists {
				expectedTarget = &target
			}
			err = workflow.scheduleRSSDownloadInTx(ctx, database.TxScope{Tx: tx, Queries: scope.Queries.WithTx(tx)}, candidate, false, checkID, expectedTarget)
			if err != nil {
				if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
				result.Outcomes[index].Err = err
				continue
			}
			if err := tx.Commit(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return RSSBatchResult{}, err
	}
	for index, candidate := range candidates {
		result.Outcomes[index].Err = workflow.finishRSSSchedule(ctx, candidate, result.Outcomes[index].Err)
	}
	return result, nil
}
