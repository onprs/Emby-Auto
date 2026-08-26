package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

const (
	rssTargetInLibraryReason  = "target_episode_in_library"
	rssTargetImportedReason   = "target_episode_imported"
	rssTargetProcessingReason = "target_episode_processing"

	rssFulfillmentManagedImport = "managed_import"
	rssFulfillmentEmbyCatalog   = "emby_catalog"
)

type rssTargetOccupancy struct {
	TargetEpisodeID   uuid.UUID
	TargetSeason      int
	TargetEpisode     int
	Reason            string
	Fulfilled         bool
	FulfillmentSource string
	RealtimeCheckID   uuid.UUID
	CheckedAt         time.Time
}

func loadRSSMappedTargetOccupancyWithRealtimeCheck(
	ctx context.Context,
	queries *db.Queries,
	subscriptionID uuid.UUID,
	sourceSeason int,
	sourceEpisode int,
	excludedEntryID uuid.UUID,
	realtimeCheckID uuid.UUID,
) (rssTargetOccupancy, error) {
	if subscriptionID == uuid.Nil || sourceSeason <= 0 || sourceEpisode <= 0 {
		return rssTargetOccupancy{}, nil
	}
	excluded := pgtype.UUID{}
	if excludedEntryID != uuid.Nil {
		excluded = repository.UUIDToPG(excludedEntryID)
	}
	realtimeCheck := pgtype.UUID{}
	if realtimeCheckID != uuid.Nil {
		realtimeCheck = repository.UUIDToPG(realtimeCheckID)
	}
	row, err := queries.GetRSSMappedTargetOccupancy(ctx, db.GetRSSMappedTargetOccupancyParams{
		RealtimeCheckID:    realtimeCheck,
		ExcludedRssEntryID: excluded,
		SourceSeason:       int32(sourceSeason),
		SourceEpisode:      int32(sourceEpisode),
		SubscriptionID:     repository.UUIDToPG(subscriptionID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if realtimeCheckID != uuid.Nil {
			return rssTargetOccupancy{}, NewError(
				"rss_realtime_mapping_unavailable",
				"the RSS source coordinate does not have a mapped target for real-time verification",
				ErrStateConflict,
				nil,
			)
		}
		return rssTargetOccupancy{}, nil
	}
	if err != nil {
		return rssTargetOccupancy{}, fmt.Errorf("load RSS target occupancy: %w", err)
	}
	if realtimeCheckID != uuid.Nil && !row.RealtimeCheckValid {
		return rssTargetOccupancy{}, NewError(
			"rss_realtime_check_expired",
			"the real-time Emby target check is missing or expired",
			ErrStateConflict,
			nil,
		)
	}
	catalogPresent := row.CatalogPresent
	if realtimeCheckID != uuid.Nil {
		catalogPresent = row.RealtimeCatalogPresent
	}
	occupancy := rssTargetOccupancy{
		TargetEpisodeID: repository.UUIDFromPG(row.TargetEpisodeID),
		TargetSeason:    int(row.TargetSeason),
		TargetEpisode:   int(row.TargetEpisode),
		RealtimeCheckID: realtimeCheckID,
	}
	if row.RealtimeCheckedAt.Valid {
		occupancy.CheckedAt = row.RealtimeCheckedAt.Time
	}
	switch {
	case row.ManagedImportPresent:
		occupancy.Reason = rssTargetImportedReason
		occupancy.Fulfilled = true
		occupancy.FulfillmentSource = rssFulfillmentManagedImport
	case catalogPresent:
		occupancy.Reason = rssTargetInLibraryReason
		occupancy.Fulfilled = true
		occupancy.FulfillmentSource = rssFulfillmentEmbyCatalog
	case row.ProcessingPresent:
		occupancy.Reason = rssTargetProcessingReason
	}
	return occupancy, nil
}

type rssTargetOccupancyRequest struct {
	subscriptionID  uuid.UUID
	sourceSeason    int
	sourceEpisode   int
	excludedEntryID uuid.UUID
	realtimeCheckID uuid.UUID
}

func lockEpisodeMappingTargets(
	ctx context.Context,
	scope database.TxScope,
	targetEpisodeIDs []uuid.UUID,
) error {
	unique := make(map[uuid.UUID]struct{}, len(targetEpisodeIDs))
	ordered := make([]uuid.UUID, 0, len(targetEpisodeIDs))
	for _, targetEpisodeID := range targetEpisodeIDs {
		if targetEpisodeID == uuid.Nil {
			continue
		}
		if _, exists := unique[targetEpisodeID]; exists {
			continue
		}
		unique[targetEpisodeID] = struct{}{}
		ordered = append(ordered, targetEpisodeID)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].String() < ordered[right].String()
	})
	for _, targetEpisodeID := range ordered {
		if _, err := scope.Tx.Exec(
			ctx,
			"SELECT pg_advisory_xact_lock(hashtextextended('rss-target:' || $1::text, 0))",
			targetEpisodeID,
		); err != nil {
			return fmt.Errorf("lock episode Mapping target: %w", err)
		}
	}
	return nil
}

// LockRSSTargetEpisodes serializes real-time observations and target-level
// fulfillment writes in the shared RSS occupancy lock domain.
func LockRSSTargetEpisodes(
	ctx context.Context,
	scope database.TxScope,
	targetEpisodeIDs []uuid.UUID,
) error {
	return lockEpisodeMappingTargets(ctx, scope, targetEpisodeIDs)
}

func prepareRSSMappedTargetLocks(
	ctx context.Context,
	scope database.TxScope,
	requests []rssTargetOccupancyRequest,
) ([]uuid.UUID, error) {
	expectedTargets := make([]uuid.UUID, len(requests))
	for index, request := range requests {
		occupancy, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(
			ctx,
			scope.Queries,
			request.subscriptionID,
			request.sourceSeason,
			request.sourceEpisode,
			request.excludedEntryID,
			request.realtimeCheckID,
		)
		if err != nil {
			return nil, err
		}
		expectedTargets[index] = occupancy.TargetEpisodeID
	}
	if err := lockEpisodeMappingTargets(ctx, scope, expectedTargets); err != nil {
		return nil, err
	}
	return expectedTargets, nil
}

func loadRSSMappedTargetOccupancyAfterLock(
	ctx context.Context,
	scope database.TxScope,
	request rssTargetOccupancyRequest,
	expectedTarget uuid.UUID,
) (rssTargetOccupancy, error) {
	occupancy, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(
		ctx,
		scope.Queries,
		request.subscriptionID,
		request.sourceSeason,
		request.sourceEpisode,
		request.excludedEntryID,
		request.realtimeCheckID,
	)
	if err != nil {
		return rssTargetOccupancy{}, err
	}
	if occupancy.TargetEpisodeID != expectedTarget {
		return rssTargetOccupancy{}, NewError(
			"rss_target_mapping_changed",
			"the RSS target Mapping changed while target occupancy was locked",
			ErrStateConflict,
			nil,
		)
	}
	return occupancy, nil
}

func lockRSSMappedTargetOccupancyWithRealtimeCheck(
	ctx context.Context,
	scope database.TxScope,
	subscriptionID uuid.UUID,
	sourceSeason int,
	sourceEpisode int,
	excludedEntryID uuid.UUID,
	realtimeCheckID uuid.UUID,
) (rssTargetOccupancy, error) {
	request := rssTargetOccupancyRequest{
		subscriptionID:  subscriptionID,
		sourceSeason:    sourceSeason,
		sourceEpisode:   sourceEpisode,
		excludedEntryID: excludedEntryID,
		realtimeCheckID: realtimeCheckID,
	}
	expectedTargets, err := prepareRSSMappedTargetLocks(ctx, scope, []rssTargetOccupancyRequest{request})
	if err != nil {
		return rssTargetOccupancy{}, err
	}
	return loadRSSMappedTargetOccupancyAfterLock(ctx, scope, request, expectedTargets[0])
}

func markRSSEntryTargetOccupiedInTx(
	ctx context.Context,
	scope database.TxScope,
	entryID uuid.UUID,
	operationID uuid.UUID,
	occupancy rssTargetOccupancy,
) (bool, error) {
	if entryID == uuid.Nil || occupancy.Reason == "" {
		return false, nil
	}
	if occupancy.FulfillmentSource == rssFulfillmentEmbyCatalog && occupancy.RealtimeCheckID != uuid.Nil {
		if err := lockEpisodeMappingTargets(ctx, scope, []uuid.UUID{occupancy.TargetEpisodeID}); err != nil {
			return false, err
		}
		authoritative, err := scope.Queries.IsRSSRealtimeTargetCheckAuthoritative(ctx, db.IsRSSRealtimeTargetCheckAuthoritativeParams{
			TargetEpisodeID: repository.UUIDToPG(occupancy.TargetEpisodeID),
			CheckID:         repository.UUIDToPG(occupancy.RealtimeCheckID),
		})
		if err != nil {
			return false, fmt.Errorf("verify authoritative RSS real-time check: %w", err)
		}
		if !authoritative {
			return false, NewError(
				"rss_realtime_check_superseded",
				"the real-time Emby target check was superseded before fulfillment could be recorded",
				ErrStateConflict,
				nil,
			)
		}
	}
	var verifiedAt pgtype.Timestamptz
	if !occupancy.CheckedAt.IsZero() {
		verifiedAt = pgtype.Timestamptz{Time: occupancy.CheckedAt, Valid: true}
	}
	_, err := scope.Queries.MarkRSSEntryTargetOccupied(ctx, db.MarkRSSEntryTargetOccupiedParams{
		RejectionReason:   occupancy.Reason,
		Fulfilled:         occupancy.Fulfilled,
		FulfillmentSource: occupancy.FulfillmentSource,
		TargetEpisodeID:   repository.UUIDToPG(occupancy.TargetEpisodeID),
		VerifiedAt:        verifiedAt,
		ID:                repository.UUIDToPG(entryID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark occupied RSS target: %w", err)
	}
	if err := appendResourceEvent(ctx, scope.Queries, "rss_entry", entryID, operationID, uuid.Nil, "rss.entry.target_occupied", map[string]any{
		"reasonCode":    occupancy.Reason,
		"targetSeason":  occupancy.TargetSeason,
		"targetEpisode": occupancy.TargetEpisode,
		"fulfilled":     occupancy.Fulfilled,
	}); err != nil {
		return false, err
	}
	return true, nil
}
