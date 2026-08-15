package service

import (
	"context"
	"errors"
	"fmt"

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

func lockRSSMappedTargetOccupancyWithRealtimeCheck(
	ctx context.Context,
	scope database.TxScope,
	subscriptionID uuid.UUID,
	sourceSeason int,
	sourceEpisode int,
	excludedEntryID uuid.UUID,
	realtimeCheckID uuid.UUID,
) (rssTargetOccupancy, error) {
	occupancy, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(
		ctx, scope.Queries, subscriptionID, sourceSeason, sourceEpisode, excludedEntryID, realtimeCheckID,
	)
	if err != nil || occupancy.TargetEpisodeID == uuid.Nil {
		return occupancy, err
	}
	if _, err := scope.Tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended('rss-target:' || $1::text, 0))",
		occupancy.TargetEpisodeID,
	); err != nil {
		return rssTargetOccupancy{}, fmt.Errorf("lock RSS target episode: %w", err)
	}
	return loadRSSMappedTargetOccupancyWithRealtimeCheck(
		ctx, scope.Queries, subscriptionID, sourceSeason, sourceEpisode, excludedEntryID, realtimeCheckID,
	)
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
	_, err := scope.Queries.MarkRSSEntryTargetOccupied(ctx, db.MarkRSSEntryTargetOccupiedParams{
		RejectionReason:   occupancy.Reason,
		Fulfilled:         occupancy.Fulfilled,
		FulfillmentSource: occupancy.FulfillmentSource,
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
