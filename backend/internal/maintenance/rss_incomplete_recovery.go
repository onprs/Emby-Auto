package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type IncompleteRSSRecoveryRequest struct {
	SubscriptionID uuid.UUID
	SourceEpisodes []int32
}

type IncompleteRSSRecoveryResult struct {
	SubscriptionID uuid.UUID
	RequestedCount int
	ScheduledCount int
	ExistingCount  int
}

type rssEntryScheduler interface {
	ScheduleRSSRecoveryDownload(context.Context, domain.RSSEnqueueCandidate) error
}

type IncompleteRSSRecovery struct {
	queries    *db.Queries
	transactor *database.Transactor
	scheduler  rssEntryScheduler
}

func NewIncompleteRSSRecovery(queries *db.Queries, transactor *database.Transactor, scheduler rssEntryScheduler) *IncompleteRSSRecovery {
	return &IncompleteRSSRecovery{queries: queries, transactor: transactor, scheduler: scheduler}
}

func ValidateIncompleteRSSRecoveryRequest(request IncompleteRSSRecoveryRequest) error {
	if request.SubscriptionID == uuid.Nil {
		return fmt.Errorf("RSS subscription ID is required")
	}
	if len(request.SourceEpisodes) == 0 {
		return fmt.Errorf("at least one source episode is required")
	}
	seen := make(map[int32]struct{}, len(request.SourceEpisodes))
	for _, episode := range request.SourceEpisodes {
		if episode <= 0 {
			return fmt.Errorf("source episodes must be positive")
		}
		if _, exists := seen[episode]; exists {
			return fmt.Errorf("source episode %d is duplicated", episode)
		}
		seen[episode] = struct{}{}
	}
	return nil
}

func (recovery *IncompleteRSSRecovery) Recover(ctx context.Context, request IncompleteRSSRecoveryRequest) (IncompleteRSSRecoveryResult, error) {
	result := IncompleteRSSRecoveryResult{SubscriptionID: request.SubscriptionID, RequestedCount: len(request.SourceEpisodes)}
	if recovery == nil || recovery.queries == nil || recovery.transactor == nil || recovery.scheduler == nil {
		return result, fmt.Errorf("incomplete RSS recovery is not configured")
	}
	if err := ValidateIncompleteRSSRecoveryRequest(request); err != nil {
		return result, err
	}
	episodes := append([]int32(nil), request.SourceEpisodes...)
	sort.Slice(episodes, func(left, right int) bool { return episodes[left] < episodes[right] })

	var candidates []uuid.UUID
	err := recovery.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockRSSIncompleteRecoveryEntries(ctx, db.LockRSSIncompleteRecoveryEntriesParams{
			SubscriptionID: repository.UUIDToPG(request.SubscriptionID),
			SourceEpisodes: episodes,
		})
		if err != nil {
			return fmt.Errorf("lock RSS recovery entries: %w", err)
		}
		if len(locked) != len(episodes) {
			return fmt.Errorf("found %d recovery entries, expected %d", len(locked), len(episodes))
		}
		byEpisode := make(map[int32]db.LockRSSIncompleteRecoveryEntriesRow, len(locked))
		for _, entry := range locked {
			if entry.SourceEpisode == nil {
				return fmt.Errorf("RSS recovery entry %s has no source episode", repository.UUIDFromPG(entry.ID))
			}
			byEpisode[*entry.SourceEpisode] = entry
		}
		for _, episode := range episodes {
			entry, exists := byEpisode[episode]
			if !exists {
				return fmt.Errorf("source episode %d is missing from the subscription", episode)
			}
			if entry.ImportedAt.Valid {
				return fmt.Errorf("source episode %d is already imported", episode)
			}
			if !entry.Downloadable {
				return fmt.Errorf("source episode %d is not downloadable", episode)
			}
			if entry.AcquisitionID.Valid {
				if entry.Status != string(domain.RSSEnqueueing) && entry.Status != string(domain.RSSEnqueued) {
					return fmt.Errorf("source episode %d has an active acquisition in unexpected entry state %q", episode, entry.Status)
				}
				result.ExistingCount++
				continue
			}
			if entry.Status != string(domain.RSSEnqueued) && entry.Status != string(domain.RSSEnqueueFailed) {
				return fmt.Errorf("source episode %d cannot recover from entry state %q", episode, entry.Status)
			}
		}

		reset, err := scope.Queries.ResetRSSIncompleteRecoveryEntries(ctx, db.ResetRSSIncompleteRecoveryEntriesParams{
			SubscriptionID: repository.UUIDToPG(request.SubscriptionID),
			SourceEpisodes: episodes,
		})
		if err != nil {
			return fmt.Errorf("reset incomplete RSS entries: %w", err)
		}
		if len(reset)+result.ExistingCount != len(episodes) {
			return fmt.Errorf("prepared %d recovery entries and found %d existing, expected %d", len(reset), result.ExistingCount, len(episodes))
		}
		for _, entry := range reset {
			candidates = append(candidates, repository.UUIDFromPG(entry.ID))
		}
		return appendRecoveryEvent(ctx, scope.Queries, request.SubscriptionID, "rss.subscription.incomplete_recovery_started", map[string]any{
			"sourceEpisodes": episodes,
			"candidateCount": len(candidates),
			"existingCount":  result.ExistingCount,
		})
	})
	if err != nil {
		return result, err
	}

	finish := func(status string, cause error) error {
		return recovery.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			data := map[string]any{
				"sourceEpisodes": episodes,
				"scheduledCount": result.ScheduledCount,
				"existingCount":  result.ExistingCount,
				"status":         status,
			}
			if cause != nil {
				data["error"] = cause.Error()
			}
			return appendRecoveryEvent(ctx, scope.Queries, request.SubscriptionID, "rss.subscription.incomplete_recovery_"+status, data)
		})
	}

	for _, entryID := range candidates {
		if err := recovery.scheduler.ScheduleRSSRecoveryDownload(ctx, domain.RSSEnqueueCandidate{EntryID: entryID}); err != nil {
			finishErr := finish("failed", err)
			return result, errors.Join(fmt.Errorf("schedule RSS recovery entry: %w", err), finishErr)
		}
		if _, err := recovery.queries.GetRSSRecoveryScheduleState(ctx, repository.UUIDToPG(entryID)); err != nil {
			finishErr := finish("failed", err)
			return result, errors.Join(fmt.Errorf("verify RSS recovery schedule: %w", err), finishErr)
		}
		result.ScheduledCount++
	}
	if err := finish("scheduled", nil); err != nil {
		return result, err
	}
	return result, nil
}

func appendRecoveryEvent(ctx context.Context, queries *db.Queries, subscriptionID uuid.UUID, topic string, data map[string]any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode RSS recovery event: %w", err)
	}
	resourceType := "rss_subscription"
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           repository.UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(subscriptionID),
		Data:         encoded,
	}); err != nil {
		return fmt.Errorf("append RSS recovery event: %w", err)
	}
	return nil
}
