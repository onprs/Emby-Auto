package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type RSSWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
	operations *OperationScheduler
}

func NewRSSWorkflow(
	queries *db.Queries,
	transactor *database.Transactor,
	operations *OperationScheduler,
) *RSSWorkflow {
	return &RSSWorkflow{queries: queries, transactor: transactor, operations: operations}
}

func (workflow *RSSWorkflow) LoadPollCommand(
	ctx context.Context,
	subscriptionID uuid.UUID,
) (domain.RSSPollCommand, error) {
	row, err := workflow.queries.GetRSSPollCommand(ctx, repository.UUIDToPG(subscriptionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RSSPollCommand{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.RSSPollCommand{}, fmt.Errorf("load RSS poll command: %w", err)
	}
	return domain.RSSPollCommand{
		SubscriptionID:     repository.UUIDFromPG(row.ID),
		FeedURL:            row.FeedUrl,
		IncludeKeywords:    row.IncludeKeywords,
		ExcludeKeywords:    row.ExcludeKeywords,
		Enabled:            row.Enabled,
		AutoEpisodeMapping: row.AutoEpisodeMapping,
		Deleted:            row.DeletedAt.Valid,
		Completed:          row.CompletedAt.Valid,
		SourceSeason:       int(row.SourceSeason),
		PollInterval:       time.Duration(row.PollIntervalSeconds) * time.Second,
		Version:            row.Version,
	}, nil
}

// PreparePollMapping moves the established exact same-season, same-episode
// mapping path ahead of acquisition creation. Non-standard coordinates are
// persisted as a bounded Agent scope without allowing target verification or
// acquisition creation to run early.
func (workflow *RSSWorkflow) PreparePollMapping(
	ctx context.Context,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	feed domain.RSSFeed,
) (domain.RSSPollMappingPreparation, error) {
	if subscriptionID == uuid.Nil {
		return domain.RSSPollMappingPreparation{}, domain.ErrNotFound
	}
	result := domain.RSSPollMappingPreparation{}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		mappingContext, err := scope.Queries.LockRSSPollMappingContext(ctx, repository.UUIDToPG(subscriptionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock RSS poll mapping context: %w", err)
		}
		if mappingContext.MappingProfileID.Valid {
			result.Ready = true
			return nil
		}
		if !mappingContext.Enabled || !mappingContext.AutoEpisodeMapping || mappingContext.DeletedAt.Valid || mappingContext.CompletedAt.Valid {
			return nil
		}
		if _, err := scope.Queries.LockMediaSeries(ctx, mappingContext.SeriesID); err != nil {
			return fmt.Errorf("lock RSS mapping series: %w", err)
		}
		seriesID := repository.UUIDFromPG(mappingContext.SeriesID)
		seasons, targetIDs, err := loadSeriesMappingCatalog(ctx, scope.Queries, seriesID)
		if err != nil {
			var serviceErr *Error
			if errors.As(err, &serviceErr) && serviceErr.Code == "tmdb_catalog_missing" {
				return nil
			}
			return err
		}
		anchorSource, anchorTarget, ok := deterministicRSSPollMappingAnchor(
			feed,
			int(mappingContext.SourceSeason),
			mappingContext.IncludeKeywords,
			mappingContext.ExcludeKeywords,
			targetIDs,
		)
		if !ok {
			return nil
		}
		profileRows, err := mappingProfileRowsFromAnchor(seasons, targetIDs, anchorSource, anchorTarget)
		if err != nil {
			return err
		}
		anchorResolution := domain.ResolveEpisodeMapping(domain.EpisodeMappingRequest{
			Source: anchorSource, AnchorSource: anchorSource, AnchorTarget: anchorTarget, TMDbSeasons: seasons,
		})
		anchorTargetID := targetIDs[anchorTarget]
		if anchorResolution.Status != domain.MappingMapped || anchorTargetID == uuid.Nil {
			return nil
		}

		name := mappingProfileName(uuid.Nil, subscriptionID)
		version, err := scope.Queries.NextMappingProfileVersion(ctx, db.NextMappingProfileVersionParams{
			SeriesID: mappingContext.SeriesID, Name: name,
		})
		if err != nil {
			return fmt.Errorf("select deterministic RSS mapping profile version: %w", err)
		}
		if _, err := scope.Queries.DeactivateMappingProfiles(ctx, db.DeactivateMappingProfilesParams{
			SeriesID: mappingContext.SeriesID, Name: name,
		}); err != nil {
			return fmt.Errorf("deactivate deterministic RSS mapping profile: %w", err)
		}
		profileID := uuid.New()
		if _, err := scope.Queries.CreateMappingProfile(ctx, db.CreateMappingProfileParams{
			ID:                    repository.UUIDToPG(profileID),
			SeriesID:              mappingContext.SeriesID,
			Name:                  name,
			Version:               version,
			AnchorSourceSeason:    mappingInt32(anchorSource.Season),
			AnchorSourceEpisode:   mappingInt32(anchorSource.Episode),
			AnchorTargetEpisodeID: repository.UUIDToPG(anchorTargetID),
			TargetEpisodeOffset:   mappingInt32(anchorResolution.AbsoluteEpisode - anchorSource.Episode),
			DecisionSource:        string(domain.DecisionSourceDeterministic),
		}); err != nil {
			return fmt.Errorf("create deterministic RSS mapping profile: %w", err)
		}
		for _, row := range profileRows {
			absoluteEpisode := int32(row.AbsoluteEpisode)
			if _, err := scope.Queries.CreateEpisodeMapping(ctx, db.CreateEpisodeMappingParams{
				ID:              repository.UUIDToPG(uuid.New()),
				ProfileID:       repository.UUIDToPG(profileID),
				SourceSeason:    int32(row.SourceSeason),
				SourceEpisode:   int32(row.SourceEpisode),
				AbsoluteEpisode: &absoluteEpisode,
				TargetEpisodeID: repository.UUIDToPG(row.TargetEpisodeID),
				MappingStatus:   string(row.Status),
				MatchSource:     string(row.MatchSource),
			}); err != nil {
				return fmt.Errorf("create deterministic RSS episode mapping: %w", err)
			}
		}
		newVersion, err := scope.Queries.ApplyDeterministicRSSPollMappingProfile(ctx, db.ApplyDeterministicRSSPollMappingProfileParams{
			MappingProfileID: repository.UUIDToPG(profileID),
			ID:               mappingContext.ID,
			ExpectedVersion:  mappingContext.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return NewError("rss_mapping_conflict", "the RSS subscription changed while automatic mapping was prepared", ErrStateConflict, map[string]any{})
		}
		if err != nil {
			return fmt.Errorf("apply deterministic RSS mapping profile: %w", err)
		}
		if _, err := scope.Queries.ExpireRSSPreacquisitionMappingScopes(ctx, db.ExpireRSSPreacquisitionMappingScopesParams{
			SubscriptionID: repository.UUIDToPG(subscriptionID), SubscriptionVersion: newVersion, SourceFingerprint: []byte{},
		}); err != nil {
			return fmt.Errorf("expire pre-acquisition scopes after deterministic RSS mapping: %w", err)
		}
		if workflow.operations == nil {
			return fmt.Errorf("schedule mapped RSS poll: operation scheduler is unavailable")
		}
		if err := workflow.scheduleContinuousPoll(ctx, scope, domain.RSSSubscription{ID: subscriptionID, Version: newVersion}); err != nil {
			return err
		}
		eventData, err := json.Marshal(map[string]any{
			"profileId": profileID, "version": version, "decisionSource": domain.DecisionSourceDeterministic,
			"mapped": len(profileRows), "subscriptionVersion": newVersion,
		})
		if err != nil {
			return fmt.Errorf("encode deterministic RSS mapping event: %w", err)
		}
		if err := appendRSSEvent(ctx, scope.Queries, subscriptionID, operationID, "rss.mapping_profile_applied", eventData); err != nil {
			return err
		}
		result.Ready = true
		result.Applied = true
		return nil
	})
	if err != nil {
		return domain.RSSPollMappingPreparation{}, err
	}
	if result.Ready {
		return result, nil
	}
	return workflow.prepareAgentPollMapping(ctx, operationID, subscriptionID, feed)
}

// EnsureDeterministicPollMapping remains the narrow compatibility surface used
// by focused deterministic tests and callers that only need readiness.
func (workflow *RSSWorkflow) EnsureDeterministicPollMapping(
	ctx context.Context,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	feed domain.RSSFeed,
) (bool, error) {
	result, err := workflow.PreparePollMapping(ctx, operationID, subscriptionID, feed)
	return result.Ready, err
}

func (workflow *RSSWorkflow) prepareAgentPollMapping(
	ctx context.Context,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	feed domain.RSSFeed,
) (domain.RSSPollMappingPreparation, error) {
	result := domain.RSSPollMappingPreparation{}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		mappingContext, err := scope.Queries.LockRSSPollMappingContext(ctx, repository.UUIDToPG(subscriptionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock RSS Agent mapping context: %w", err)
		}
		if mappingContext.MappingProfileID.Valid {
			result.Ready = true
			return nil
		}
		if !mappingContext.Enabled || !mappingContext.AutoEpisodeMapping || mappingContext.DeletedAt.Valid || mappingContext.CompletedAt.Valid {
			return nil
		}

		discovered := 0
		for _, item := range feed.Entries {
			inserted, _, _, err := workflow.persistFeedEntry(
				ctx, scope, operationID, subscriptionID, uuid.Nil, int(mappingContext.SourceSeason),
				mappingContext.IncludeKeywords, mappingContext.ExcludeKeywords, item, rssTargetOccupancy{}, uuid.Nil, true,
			)
			if err != nil {
				return err
			}
			if inserted {
				discovered++
			}
		}

		sources, err := scope.Queries.ListRSSPreacquisitionMappingSourceCandidates(ctx, repository.UUIDToPG(subscriptionID))
		if err != nil {
			return fmt.Errorf("list RSS pre-acquisition mapping sources: %w", err)
		}
		if len(sources) == 0 {
			candidateRows, err := scope.Queries.ListAgentResolvableRSSEntries(ctx, repository.UUIDToPG(subscriptionID))
			if err != nil {
				return fmt.Errorf("list RSS coordinate fallback candidates: %w", err)
			}
			result.AgentCoordinateCandidates = make([]uuid.UUID, 0, len(candidateRows))
			for _, id := range candidateRows {
				result.AgentCoordinateCandidates = append(result.AgentCoordinateCandidates, repository.UUIDFromPG(id))
			}
		} else {
			type fingerprintSource struct {
				EntryID uuid.UUID `json:"entryId"`
				Season  int32     `json:"season"`
				Episode int32     `json:"episode"`
			}
			values := make([]fingerprintSource, 0, len(sources))
			for _, source := range sources {
				if source.SourceSeason == nil || source.SourceEpisode == nil {
					continue
				}
				values = append(values, fingerprintSource{
					EntryID: repository.UUIDFromPG(source.ID), Season: *source.SourceSeason, Episode: *source.SourceEpisode,
				})
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				return fmt.Errorf("encode RSS pre-acquisition mapping source identity: %w", err)
			}
			fingerprint := sha256.Sum256(encoded)
			if _, err := scope.Queries.ExpireRSSPreacquisitionMappingScopes(ctx, db.ExpireRSSPreacquisitionMappingScopesParams{
				SubscriptionID: repository.UUIDToPG(subscriptionID), SubscriptionVersion: mappingContext.Version,
				SourceFingerprint: fingerprint[:],
			}); err != nil {
				return fmt.Errorf("expire superseded RSS pre-acquisition mapping scopes: %w", err)
			}
			scopeID := rssPreacquisitionMappingScopeID(subscriptionID, mappingContext.Version, fingerprint)
			created, err := scope.Queries.CreateRSSPreacquisitionMappingScope(ctx, db.CreateRSSPreacquisitionMappingScopeParams{
				ID: repository.UUIDToPG(scopeID), SubscriptionID: repository.UUIDToPG(subscriptionID),
				SubscriptionVersion: mappingContext.Version, SourceFingerprint: fingerprint[:],
			})
			if err != nil {
				return fmt.Errorf("create RSS pre-acquisition mapping scope: %w", err)
			}
			for _, source := range values {
				if err := scope.Queries.CreateRSSPreacquisitionMappingSource(ctx, db.CreateRSSPreacquisitionMappingSourceParams{
					ScopeID: created.ID, EntryID: repository.UUIDToPG(source.EntryID),
					SourceSeason: source.Season, SourceEpisode: source.Episode,
				}); err != nil {
					return fmt.Errorf("create RSS pre-acquisition mapping source: %w", err)
				}
			}
			result.ScopeID = scopeID
		}
		if _, err := scope.Queries.RecordRSSPoll(ctx, repository.UUIDToPG(subscriptionID)); err != nil {
			return fmt.Errorf("record RSS mapping discovery poll: %w", err)
		}
		eventData, err := json.Marshal(map[string]any{
			"fetchedCount": len(feed.Entries), "discoveredCount": discovered,
			"mappingSourceCount": len(sources), "coordinateCandidateCount": len(result.AgentCoordinateCandidates),
			"mappingScopeId": nullableEventUUID(result.ScopeID),
		})
		if err != nil {
			return fmt.Errorf("encode RSS mapping discovery event: %w", err)
		}
		return appendRSSEvent(ctx, scope.Queries, subscriptionID, operationID, "rss.mapping_discovery_recorded", eventData)
	})
	if err != nil {
		return domain.RSSPollMappingPreparation{}, err
	}
	return result, nil
}

func rssPreacquisitionMappingScopeID(subscriptionID uuid.UUID, version int32, fingerprint [sha256.Size]byte) uuid.UUID {
	seed := []byte(fmt.Sprintf("%s:%d:%x", subscriptionID, version, fingerprint))
	return uuid.NewSHA1(uuid.NameSpaceOID, seed)
}

func nullableEventUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}

func deterministicRSSPollMappingAnchor(
	feed domain.RSSFeed,
	sourceSeason int,
	includeKeywords []string,
	excludeKeywords []string,
	targetIDs map[domain.EpisodeCoordinate]uuid.UUID,
) (domain.EpisodeCoordinate, domain.EpisodeCoordinate, bool) {
	anchor := domain.EpisodeCoordinate{}
	seen := make(map[domain.EpisodeCoordinate]struct{})
	for _, item := range feed.Entries {
		analysis := domain.AnalyzeRSSRelease(
			item.Title, strings.TrimSpace(item.DownloadURI), sourceSeason, includeKeywords, excludeKeywords,
		)
		if !analysis.Downloadable {
			continue
		}
		source := domain.EpisodeCoordinate{Season: analysis.SourceSeason, Episode: analysis.SourceEpisode}
		if source.Season != sourceSeason || source.Episode <= 0 || targetIDs[source] == uuid.Nil {
			return domain.EpisodeCoordinate{}, domain.EpisodeCoordinate{}, false
		}
		if _, duplicate := seen[source]; duplicate {
			continue
		}
		seen[source] = struct{}{}
		if anchor.Episode == 0 || source.Episode < anchor.Episode {
			anchor = source
		}
	}
	if anchor.Season == 0 {
		return domain.EpisodeCoordinate{}, domain.EpisodeCoordinate{}, false
	}
	return anchor, anchor, true
}

const rssPreAcquisitionMappingRecoveryVersion = "v1"

// ReconcilePreAcquisitionMappingPolls narrowly restores poll operations that
// exhausted retries because the pre-acquisition mapping step was missing.
func (workflow *RSSWorkflow) ReconcilePreAcquisitionMappingPolls(ctx context.Context) (int, error) {
	if workflow == nil || workflow.queries == nil || workflow.operations == nil {
		return 0, fmt.Errorf("RSS pre-acquisition mapping recovery is unavailable")
	}
	rows, err := workflow.queries.ListRSSPreAcquisitionMappingRecoveryCandidates(ctx)
	if err != nil {
		return 0, fmt.Errorf("list RSS pre-acquisition mapping recovery candidates: %w", err)
	}
	reconciled := 0
	for _, row := range rows {
		subscriptionID := repository.UUIDFromPG(row.ID)
		result, err := workflow.operations.Schedule(ctx, ScheduleOperationRequest{
			Kind:           appqueue.KindRSSPoll,
			ResourceType:   "rss_subscription",
			ResourceID:     subscriptionID,
			IdempotencyKey: "rss.poll:recovery:preacquisition-mapping-" + rssPreAcquisitionMappingRecoveryVersion + ":" + subscriptionID.String(),
			MaxAttempts:    5,
			Timeout:        30 * time.Second,
			Payload: map[string]any{
				"continuous":          true,
				"subscriptionVersion": row.Version,
			},
		})
		if err != nil {
			return reconciled, fmt.Errorf("schedule RSS pre-acquisition mapping recovery: %w", err)
		}
		if result.Created {
			reconciled++
		}
	}
	return reconciled, nil
}

const rssAdjudicationBatchSize = 50

func (workflow *RSSWorkflow) ListAgentMappingAcquisitions(
	ctx context.Context,
	subscriptionID uuid.UUID,
) ([]uuid.UUID, error) {
	rows, err := workflow.queries.ListAgentMappingAcquisitionsByRSSSubscription(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return nil, fmt.Errorf("list RSS Agent Mapping acquisitions: %w", err)
	}
	result := make([]uuid.UUID, 0, len(rows))
	for _, id := range rows {
		result = append(result, repository.UUIDFromPG(id))
	}
	return result, nil
}

func (workflow *RSSWorkflow) PersistPoll(
	ctx context.Context,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	feed domain.RSSFeed,
	options domain.RSSPollPersistOptions,
) (domain.RSSPollPersistResult, error) {
	result := domain.RSSPollPersistResult{FetchedCount: len(feed.Entries)}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		command, err := scope.Queries.GetRSSPollCommand(ctx, repository.UUIDToPG(subscriptionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load RSS subscription for poll: %w", err)
		}
		if !command.Enabled || command.DeletedAt.Valid || command.CompletedAt.Valid {
			return nil
		}
		if err := workflow.refreshRSSEmbyCatalogFulfillmentsInTx(
			ctx, scope, operationID, subscriptionID, options.RealtimeCheckID,
		); err != nil {
			return err
		}

		discoveredCount := 0
		skippedIdentityCount := 0
		stagedCount := 0
		batchIDs := make([]uuid.UUID, 0)
		batchEntryCounts := make([]int, 0)
		currentBatchAttempts := rssAdjudicationBatchSize
		adjudicationPlan := make([]bool, len(feed.Entries))
		occupancyPlan := make([]rssTargetOccupancy, len(feed.Entries))
		if options.AdjudicateReleases {
			adjudicationPlan = domain.PlanRSSReleaseAdjudication(
				feed.Entries, int(command.SourceSeason), command.IncludeKeywords, command.ExcludeKeywords,
			)
		} else {
			if _, err := scope.Queries.ExpirePendingRSSAdjudicationBatches(ctx, repository.UUIDToPG(subscriptionID)); err != nil {
				return fmt.Errorf("expire disabled RSS adjudication batches: %w", err)
			}
		}
		for index, item := range feed.Entries {
			analysis := domain.AnalyzeRSSRelease(
				item.Title, strings.TrimSpace(item.DownloadURI), int(command.SourceSeason), command.IncludeKeywords, command.ExcludeKeywords,
			)
			if analysis.SourceSeason <= 0 || analysis.SourceEpisode <= 0 {
				continue
			}
			occupancy, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(
				ctx, scope.Queries, subscriptionID, analysis.SourceSeason, analysis.SourceEpisode, uuid.Nil, options.RealtimeCheckID,
			)
			if err != nil {
				return err
			}
			occupancyPlan[index] = occupancy
			if occupancy.Reason != "" {
				adjudicationPlan[index] = false
			}
		}
		for index, item := range feed.Entries {
			batchID := uuid.Nil
			if adjudicationPlan[index] {
				if currentBatchAttempts >= rssAdjudicationBatchSize {
					batchID = uuid.New()
					if _, err := scope.Queries.CreateRSSAdjudicationBatch(ctx, db.CreateRSSAdjudicationBatchParams{
						ID: repository.UUIDToPG(batchID), SubscriptionID: repository.UUIDToPG(subscriptionID),
					}); err != nil {
						return fmt.Errorf("create RSS adjudication batch: %w", err)
					}
					batchIDs = append(batchIDs, batchID)
					batchEntryCounts = append(batchEntryCounts, 0)
					currentBatchAttempts = 0
				} else {
					batchID = batchIDs[len(batchIDs)-1]
				}
				currentBatchAttempts++
			}
			inserted, skipped, staged, err := workflow.persistFeedEntry(
				ctx, scope, operationID, subscriptionID, batchID, int(command.SourceSeason), command.IncludeKeywords,
				command.ExcludeKeywords, item, occupancyPlan[index], options.RealtimeCheckID, false,
			)
			if err != nil {
				return err
			}
			if inserted {
				discoveredCount++
			}
			if skipped {
				skippedIdentityCount++
			}
			if staged {
				stagedCount++
				batchEntryCounts[len(batchEntryCounts)-1]++
			}
		}
		for index, batchID := range batchIDs {
			entryCount := batchEntryCounts[index]
			if entryCount == 0 {
				if _, err := scope.Queries.DeleteEmptyRSSAdjudicationBatch(ctx, repository.UUIDToPG(batchID)); err != nil {
					return fmt.Errorf("delete empty RSS adjudication batch: %w", err)
				}
				continue
			}
			if _, err := scope.Queries.FinalizeRSSAdjudicationBatch(ctx, db.FinalizeRSSAdjudicationBatchParams{
				EntryCount: int32(entryCount), BatchID: repository.UUIDToPG(batchID),
			}); err != nil {
				return fmt.Errorf("finalize RSS adjudication batch: %w", err)
			}
		}
		reconciledConflictCount, err := workflow.reconcileRSSImportConflictsInTx(
			ctx, scope, operationID, subscriptionID, options.RealtimeCheckID,
		)
		if err != nil {
			return err
		}
		if _, err := scope.Queries.RecordRSSPoll(ctx, repository.UUIDToPG(subscriptionID)); err != nil {
			return fmt.Errorf("record RSS poll time: %w", err)
		}
		if err := workflow.completeRSSSubscriptionAtFulfillmentInTx(ctx, scope, subscriptionID, operationID, "emby_catalog"); err != nil {
			return err
		}
		if options.AdjudicateReleases {
			batchRows, err := scope.Queries.ListUnresolvedRSSAdjudicationBatches(ctx, repository.UUIDToPG(subscriptionID))
			if err != nil {
				return fmt.Errorf("list unresolved RSS adjudication batches: %w", err)
			}
			result.AgentAdjudicationBatchIDs = make([]uuid.UUID, 0, len(batchRows))
			for _, id := range batchRows {
				result.AgentAdjudicationBatchIDs = append(result.AgentAdjudicationBatchIDs, repository.UUIDFromPG(id))
			}
		}
		rows, err := scope.Queries.ListEligibleRSSEntries(ctx, repository.UUIDToPG(subscriptionID))
		if err != nil {
			return fmt.Errorf("list eligible RSS entries: %w", err)
		}
		result.Candidates = make([]domain.RSSEnqueueCandidate, 0, len(rows))
		for _, row := range rows {
			result.Candidates = append(result.Candidates, domain.RSSEnqueueCandidate{
				EntryID:      repository.UUIDFromPG(row.ID),
				Status:       domain.RSSState(row.Status),
				Downloadable: row.Downloadable,
			})
		}
		agentRows, err := scope.Queries.ListAgentResolvableRSSEntries(ctx, repository.UUIDToPG(subscriptionID))
		if err != nil {
			return fmt.Errorf("list Agent-resolvable RSS entries: %w", err)
		}
		result.AgentCoordinateCandidates = make([]uuid.UUID, 0, len(agentRows))
		for _, id := range agentRows {
			result.AgentCoordinateCandidates = append(result.AgentCoordinateCandidates, repository.UUIDFromPG(id))
		}

		eventData, err := json.Marshal(map[string]any{
			"feedTitle":               feed.Title,
			"fetchedCount":            len(feed.Entries),
			"discoveredCount":         discoveredCount,
			"eligibleCount":           len(result.Candidates),
			"skippedIdentityCount":    skippedIdentityCount,
			"adjudicationBatchCount":  len(result.AgentAdjudicationBatchIDs),
			"adjudicationEntryCount":  stagedCount,
			"reconciledConflictCount": reconciledConflictCount,
		})
		if err != nil {
			return fmt.Errorf("encode RSS poll event: %w", err)
		}
		return appendRSSEvent(ctx, scope.Queries, subscriptionID, operationID, "rss.polled", eventData)
	})
	if err != nil {
		return domain.RSSPollPersistResult{}, err
	}
	return result, nil
}

func (workflow *RSSWorkflow) persistFeedEntry(
	ctx context.Context,
	scope database.TxScope,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	adjudicationBatchID uuid.UUID,
	defaultSeason int,
	includeKeywords []string,
	excludeKeywords []string,
	item domain.RSSFeedEntry,
	occupancy rssTargetOccupancy,
	realtimeCheckID uuid.UUID,
	preacquisitionMapping bool,
) (inserted bool, skipped bool, staged bool, err error) {
	queries := scope.Queries
	downloadURI := strings.TrimSpace(item.DownloadURI)
	btih := qbittorrent.ExtractBTIH(downloadURI)
	identityURL := strings.TrimSpace(item.URL)
	if identityURL == "" {
		identityURL = downloadURI
	}
	identity, err := domain.BuildRSSIdentity(domain.RSSIdentityInput{
		GUID:        item.GUID,
		BTIH:        btih,
		URL:         identityURL,
		Title:       item.Title,
		PublishedAt: item.PublishedAt,
	})
	if err != nil {
		return false, true, false, nil
	}
	analysis := rssReleaseAnalysisWithOccupancy(
		domain.AnalyzeRSSRelease(item.Title, downloadURI, defaultSeason, includeKeywords, excludeKeywords), occupancy,
	)
	rejectionReasons := analysis.RejectionReasons
	if rejectionReasons == nil {
		rejectionReasons = []string{}
	}
	payload := item.UpstreamPayload
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return false, false, false, fmt.Errorf("encode RSS entry payload: %w", err)
	}
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = "Untitled RSS entry"
	}
	// A non-nil batch ID is the authoritative complete-poll decision. Rechecking
	// one entry here would discard conflicts that only exist across releases.
	adjudicate := adjudicationBatchID != uuid.Nil
	params := db.InsertRSSEntryParams{
		ID:               repository.UUIDToPG(uuid.New()),
		SubscriptionID:   repository.UUIDToPG(subscriptionID),
		IdentityKey:      identity,
		Guid:             optionalString(strings.TrimSpace(item.GUID)),
		Btih:             optionalString(btih),
		CanonicalUrl:     optionalHTTPURL(item.URL),
		DownloadUri:      optionalString(downloadURI),
		Title:            title,
		PublishedAt:      optionalTime(item.PublishedAt),
		Downloadable:     analysis.Downloadable,
		RejectionReasons: rejectionReasons,
		SourceSeason:     optionalInt32(analysis.SourceSeason),
		SourceEpisode:    optionalInt32(analysis.SourceEpisode),
		UpstreamPayload:  payloadJSON,
	}
	row, err := queries.InsertRSSEntry(ctx, params)
	if err == nil {
		if occupancy.Reason != "" {
			if _, err := markRSSEntryTargetOccupiedInTx(ctx, scope, repository.UUIDFromPG(row.ID), operationID, occupancy); err != nil {
				return false, false, false, err
			}
		}
		if adjudicate {
			if _, err := queries.CreatePendingRSSAdjudication(ctx, db.CreatePendingRSSAdjudicationParams{
				EntryID: row.ID, SubscriptionID: row.SubscriptionID, BatchID: repository.UUIDToPG(adjudicationBatchID),
			}); err != nil {
				return false, false, false, fmt.Errorf("stage RSS entry for Agent adjudication: %w", err)
			}
		}
		return true, false, adjudicate, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, false, false, fmt.Errorf("insert RSS entry: %w", err)
	}
	existing, err := queries.GetRSSEntryBySignals(ctx, db.GetRSSEntryBySignalsParams{
		SubscriptionID: repository.UUIDToPG(subscriptionID),
		IdentityKey:    identity,
		Btih:           optionalString(btih),
	})
	if err != nil {
		return false, false, false, fmt.Errorf("load duplicate RSS entry: %w", err)
	}
	existingID := repository.UUIDFromPG(existing.ID)
	importedBySelf := existing.ImportedAt.Valid && valueOrEmpty(existing.FulfillmentSource) == rssFulfillmentManagedImport
	if importedBySelf {
		// 已由本系统入库完成的条目保持完成状态：仅刷新发布元数据，
		// 不重新核验占用，也不覆盖可下载性、拒绝原因与源坐标。
		params.Downloadable = existing.Downloadable
		params.RejectionReasons = existing.RejectionReasons
		params.SourceSeason = existing.SourceSeason
		params.SourceEpisode = existing.SourceEpisode
	} else {
		if existing.SourceSeason != nil && existing.SourceEpisode != nil && existing.CoordinateSource != nil &&
			(*existing.CoordinateSource == string(domain.DecisionSourceAgentAuto) ||
				*existing.CoordinateSource == string(domain.DecisionSourceAgentAccepted) ||
				*existing.CoordinateSource == string(domain.DecisionSourceUser)) &&
			rssOnlySoftCoordinateReasons(analysis.RejectionReasons) {
			analysis.SourceSeason = int(*existing.SourceSeason)
			analysis.SourceEpisode = int(*existing.SourceEpisode)
			analysis.RejectionReasons = []string{}
			analysis.Downloadable = true
			params.SourceSeason = existing.SourceSeason
			params.SourceEpisode = existing.SourceEpisode
			params.RejectionReasons = []string{}
			params.Downloadable = true
		}
		if !preacquisitionMapping && analysis.SourceSeason > 0 && analysis.SourceEpisode > 0 {
			occupancy, err = loadRSSMappedTargetOccupancyWithRealtimeCheck(
				ctx, queries, subscriptionID, analysis.SourceSeason, analysis.SourceEpisode, existingID, realtimeCheckID,
			)
			if err != nil {
				return false, false, false, err
			}
			analysis = rssReleaseAnalysisWithOccupancy(
				domain.AnalyzeRSSRelease(item.Title, downloadURI, defaultSeason, includeKeywords, excludeKeywords), occupancy,
			)
			params.Downloadable = analysis.Downloadable
			params.RejectionReasons = analysis.RejectionReasons
		}
	}
	if _, err := queries.UpdateRSSEntryMetadata(ctx, db.UpdateRSSEntryMetadataParams{
		Guid:             params.Guid,
		Btih:             params.Btih,
		CanonicalUrl:     params.CanonicalUrl,
		DownloadUri:      params.DownloadUri,
		Title:            params.Title,
		PublishedAt:      params.PublishedAt,
		Downloadable:     params.Downloadable,
		RejectionReasons: params.RejectionReasons,
		SourceSeason:     params.SourceSeason,
		SourceEpisode:    params.SourceEpisode,
		UpstreamPayload:  params.UpstreamPayload,
		ID:               existing.ID,
	}); err != nil {
		return false, false, false, fmt.Errorf("update duplicate RSS entry: %w", err)
	}
	if !importedBySelf && occupancy.Reason != "" {
		if _, err := markRSSEntryTargetOccupiedInTx(ctx, scope, existingID, operationID, occupancy); err != nil {
			return false, false, false, err
		}
	}
	return false, false, false, nil
}

func rssOnlySoftCoordinateReasons(reasons []string) bool {
	if len(reasons) == 0 {
		return false
	}
	for _, reason := range reasons {
		if reason != "episode_not_detected" && reason != "episode_ambiguous" {
			return false
		}
	}
	return true
}

func rssReleaseAnalysisWithOccupancy(analysis domain.RSSReleaseAnalysis, occupancy rssTargetOccupancy) domain.RSSReleaseAnalysis {
	analysis.RejectionReasons = append([]string{}, analysis.RejectionReasons...)
	if occupancy.Reason == "" {
		return analysis
	}
	for _, reason := range analysis.RejectionReasons {
		if reason == occupancy.Reason {
			analysis.Downloadable = false
			return analysis
		}
	}
	analysis.RejectionReasons = append(analysis.RejectionReasons, occupancy.Reason)
	analysis.Downloadable = false
	return analysis
}

func (workflow *RSSWorkflow) refreshRSSEmbyCatalogFulfillmentsInTx(
	ctx context.Context,
	scope database.TxScope,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	realtimeCheckID uuid.UUID,
) error {
	rows, err := scope.Queries.ListRSSEmbyCatalogFulfilledEntries(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return fmt.Errorf("list RSS Emby catalog fulfillments: %w", err)
	}
	for _, row := range rows {
		if row.SourceSeason == nil || row.SourceEpisode == nil {
			continue
		}
		entryID := repository.UUIDFromPG(row.ID)
		occupancy, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(
			ctx, scope.Queries, subscriptionID, int(*row.SourceSeason), int(*row.SourceEpisode), entryID, realtimeCheckID,
		)
		if err != nil {
			return err
		}
		if occupancy.Fulfilled {
			continue
		}
		if _, err := scope.Queries.ClearRSSEmbyCatalogFulfillment(ctx, row.ID); errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return fmt.Errorf("clear stale RSS Emby catalog fulfillment: %w", err)
		}
		if err := appendResourceEvent(ctx, scope.Queries, "rss_entry", entryID, operationID, uuid.Nil, "rss.entry.fulfillment_expired", map[string]any{
			"fulfillmentSource": rssFulfillmentEmbyCatalog,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (workflow *RSSWorkflow) reconcileRSSImportConflictsInTx(
	ctx context.Context,
	scope database.TxScope,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	realtimeCheckID uuid.UUID,
) (int, error) {
	rows, err := scope.Queries.ListRSSImportConflictReconciliationCandidates(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return 0, fmt.Errorf("list RSS import conflict reconciliation candidates: %w", err)
	}
	deletions := NewAcquisitionDeletionWorkflow(workflow.queries, workflow.transactor, workflow.operations)
	reconciled := 0
	for _, row := range rows {
		if row.SourceSeason == nil || row.SourceEpisode == nil {
			continue
		}
		entryID := repository.UUIDFromPG(row.EntryID)
		occupancy, err := lockRSSMappedTargetOccupancyWithRealtimeCheck(
			ctx, scope, subscriptionID, int(*row.SourceSeason), int(*row.SourceEpisode), entryID, realtimeCheckID,
		)
		if err != nil {
			return 0, err
		}
		if !occupancy.Fulfilled {
			continue
		}
		acquisitionID := repository.UUIDFromPG(row.AcquisitionID)
		if err := prepareAcquisitionDeletionInTx(ctx, scope, acquisitionID, false, uuid.Nil); err != nil {
			return 0, fmt.Errorf("prepare occupied RSS acquisition deletion: %w", err)
		}
		if _, err := markRSSEntryTargetOccupiedInTx(ctx, scope, entryID, operationID, occupancy); err != nil {
			return 0, err
		}
		if _, err := deletions.scheduleDeletionInTx(
			ctx,
			scope,
			acquisitionID,
			"acquisition.delete:rss-target-occupied:"+acquisitionID.String(),
			uuid.Nil,
			map[string]any{"reasonCode": occupancy.Reason},
		); err != nil {
			return 0, err
		}
		reconciled++
	}
	return reconciled, nil
}

func (workflow *RSSWorkflow) completeRSSSubscriptionAtFulfillmentInTx(
	ctx context.Context,
	scope database.TxScope,
	subscriptionID uuid.UUID,
	operationID uuid.UUID,
	trigger string,
) error {
	final, err := scope.Queries.LockRSSSubscriptionAtFulfillment(ctx, repository.UUIDToPG(subscriptionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("detect complete RSS fulfillment set: %w", err)
	}
	if _, err := scope.Queries.ExpirePendingRSSAdjudicationBatches(ctx, repository.UUIDToPG(subscriptionID)); err != nil {
		return fmt.Errorf("expire fulfilled RSS adjudication batches: %w", err)
	}
	completed, err := scope.Queries.MarkRSSSubscriptionCompleted(ctx, db.MarkRSSSubscriptionCompletedParams{
		ID: final.ID, ExpectedVersion: final.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("complete fulfilled RSS subscription: %w", err)
	}
	cleanupOperations := 0
	if final.CleanupSourceOnCompletion {
		candidates, err := scope.Queries.ListRSSCompletionCleanupCandidates(ctx, final.ID)
		if err != nil {
			return fmt.Errorf("list fulfilled RSS cleanup candidates: %w", err)
		}
		for _, candidate := range candidates {
			if err := scheduleTaskCleanupInTx(
				ctx,
				scope,
				workflow.operations,
				repository.UUIDFromPG(candidate.TaskID),
				repository.UUIDFromPG(candidate.ImportID),
				repository.UUIDFromPG(candidate.DownloadID),
			); err != nil {
				return err
			}
			cleanupOperations++
		}
	}
	eventData, err := json.Marshal(map[string]any{
		"sourceSeason": int(final.SourceSeason), "sourceEpisode": int(final.SourceEpisode), "version": completed.Version,
		"trigger": trigger, "cleanupSourceOnCompletion": final.CleanupSourceOnCompletion, "cleanupOperations": cleanupOperations,
	})
	if err != nil {
		return fmt.Errorf("encode fulfilled RSS subscription event: %w", err)
	}
	return appendRSSEvent(ctx, scope.Queries, subscriptionID, operationID, "rss.subscription.fulfilled", eventData)
}

func (workflow *RSSWorkflow) RecordPollFailure(
	ctx context.Context,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	code string,
	message string,
) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Queries.RecordRSSPollFailure(ctx, repository.UUIDToPG(subscriptionID)); err != nil {
			return fmt.Errorf("record RSS poll failure time: %w", err)
		}
		data, err := json.Marshal(map[string]any{"errorCode": code, "message": truncate(message, 2000)})
		if err != nil {
			return fmt.Errorf("encode RSS poll failure event: %w", err)
		}
		return appendRSSEvent(ctx, scope.Queries, subscriptionID, operationID, "rss.poll_failed", data)
	})
}

func (workflow *RSSWorkflow) RecordPollBatch(
	ctx context.Context,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	summary domain.RSSPollBatchSummary,
) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		data, err := json.Marshal(map[string]any{
			"fetchedCount":   summary.FetchedCount,
			"eligibleCount":  summary.EligibleCount,
			"scheduledCount": summary.ScheduledCount,
			"failedCount":    summary.FailedCount,
		})
		if err != nil {
			return fmt.Errorf("encode RSS poll summary event: %w", err)
		}
		return appendRSSEvent(ctx, scope.Queries, subscriptionID, operationID, "rss.poll_completed", data)
	})
}

func (workflow *RSSWorkflow) ScheduleRSSDownload(
	ctx context.Context,
	candidate domain.RSSEnqueueCandidate,
) error {
	return workflow.scheduleRSSDownload(ctx, candidate, false, uuid.Nil)
}

func (workflow *RSSWorkflow) ScheduleRSSDownloadWithRealtimeCheck(
	ctx context.Context,
	candidate domain.RSSEnqueueCandidate,
	realtimeCheckID uuid.UUID,
) error {
	if realtimeCheckID == uuid.Nil {
		return NewError("rss_realtime_check_required", "a real-time Emby target check is required before RSS enqueue", ErrStateConflict, nil)
	}
	return workflow.scheduleRSSDownload(ctx, candidate, false, realtimeCheckID)
}

// ScheduleRSSRecoveryDownload schedules one explicitly validated entry while
// its incomplete subscription remains disabled.
func (workflow *RSSWorkflow) ScheduleRSSRecoveryDownload(
	ctx context.Context,
	candidate domain.RSSEnqueueCandidate,
) error {
	return workflow.scheduleRSSDownload(ctx, candidate, true, uuid.Nil)
}

func (workflow *RSSWorkflow) ScheduleRSSRecoveryDownloadWithRealtimeCheck(
	ctx context.Context,
	candidate domain.RSSEnqueueCandidate,
	realtimeCheckID uuid.UUID,
) error {
	if realtimeCheckID == uuid.Nil {
		return NewError("rss_realtime_check_required", "a real-time Emby target check is required before RSS recovery enqueue", ErrStateConflict, nil)
	}
	return workflow.scheduleRSSDownload(ctx, candidate, true, realtimeCheckID)
}

func (workflow *RSSWorkflow) scheduleRSSDownload(
	ctx context.Context,
	candidate domain.RSSEnqueueCandidate,
	recovery bool,
	realtimeCheckID uuid.UUID,
) error {
	if candidate.EntryID == uuid.Nil {
		return fmt.Errorf("RSS entry ID is required")
	}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		entry, err := scope.Queries.LockRSSEntryForEnqueue(ctx, db.LockRSSEntryForEnqueueParams{
			ID:       repository.UUIDToPG(candidate.EntryID),
			Recovery: recovery,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock RSS entry for enqueue: %w", err)
		}
		if !entry.Downloadable || (entry.Status != string(domain.RSSDiscovered) && entry.Status != string(domain.RSSEnqueueFailed)) {
			return nil
		}
		occupancy, err := lockRSSMappedTargetOccupancyWithRealtimeCheck(
			ctx,
			scope,
			repository.UUIDFromPG(entry.SubscriptionID),
			valueInt32(entry.SourceSeason),
			valueInt32(entry.SourceEpisode),
			candidate.EntryID,
			realtimeCheckID,
		)
		if err != nil {
			return err
		}
		if occupancy.Reason != "" {
			if _, err := markRSSEntryTargetOccupiedInTx(ctx, scope, candidate.EntryID, uuid.Nil, occupancy); err != nil {
				return err
			}
			if occupancy.Fulfilled {
				return workflow.completeRSSSubscriptionAtFulfillmentInTx(ctx, scope, repository.UUIDFromPG(entry.SubscriptionID), uuid.Nil, "enqueue_guard")
			}
			return nil
		}
		if err := domain.ValidateRSSTransition(domain.RSSState(entry.Status), domain.RSSEnqueueing); err != nil {
			return err
		}
		updated, err := scope.Queries.MarkRSSEntryEnqueueing(ctx, entry.ID)
		if err != nil {
			return fmt.Errorf("mark RSS entry enqueueing: %w", err)
		}
		sourcePayload, err := json.Marshal(map[string]any{
			"rssEntryId":    candidate.EntryID,
			"identityKey":   entry.IdentityKey,
			"sourceSeason":  valueInt32(entry.SourceSeason),
			"sourceEpisode": valueInt32(entry.SourceEpisode),
		})
		if err != nil {
			return fmt.Errorf("encode RSS acquisition payload: %w", err)
		}
		acquisition, err := scope.Queries.UpsertRSSAcquisition(ctx, db.UpsertRSSAcquisitionParams{
			ID:               repository.UUIDToPG(uuid.New()),
			SeriesID:         entry.SeriesID,
			MappingProfileID: entry.MappingProfileID,
			RssEntryID:       entry.ID,
			SourcePayload:    sourcePayload,
		})
		if err != nil {
			return fmt.Errorf("create RSS acquisition: %w", err)
		}
		download, err := scope.Queries.CreateRSSDownloadAttempt(ctx, db.CreateRSSDownloadAttemptParams{
			ID:            repository.UUIDToPG(uuid.New()),
			AcquisitionID: acquisition.ID,
		})
		if err != nil {
			return fmt.Errorf("create RSS download attempt: %w", err)
		}
		if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindDownloadEnqueue,
			ResourceType:   "download",
			ResourceID:     repository.UUIDFromPG(download.ID),
			IdempotencyKey: "download.enqueue:" + repository.UUIDFromPG(download.ID).String(),
			MaxAttempts:    5,
			Timeout:        2 * time.Minute,
			Payload: map[string]any{
				"defaultSeason":  valueInt32(entry.SourceSeason),
				"defaultEpisode": valueInt32(entry.SourceEpisode),
				"singleEpisode":  true,
			},
		}); err != nil {
			return fmt.Errorf("schedule RSS download enqueue: %w", err)
		}
		data, err := json.Marshal(map[string]any{
			"status":         domain.RSSEnqueueing,
			"enqueueAttempt": updated.EnqueueAttempts,
			"acquisitionId":  repository.UUIDFromPG(acquisition.ID),
			"downloadId":     repository.UUIDFromPG(download.ID),
		})
		if err != nil {
			return fmt.Errorf("encode RSS enqueue event: %w", err)
		}
		return appendRSSEntryEvent(ctx, scope.Queries, candidate.EntryID, "rss.entry.enqueueing", data)
	})
	if err == nil {
		return nil
	}
	var serviceErr *Error
	if errors.As(err, &serviceErr) && strings.HasPrefix(serviceErr.Code, "rss_realtime_") {
		return err
	}
	if failureErr := workflow.recordScheduleFailure(ctx, candidate.EntryID, "rss_schedule_failed", err.Error()); failureErr != nil {
		return errors.Join(err, failureErr)
	}
	return err
}

func (workflow *RSSWorkflow) recordScheduleFailure(
	ctx context.Context,
	entryID uuid.UUID,
	code string,
	message string,
) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		entry, err := scope.Queries.MarkRSSEntryScheduleFailed(ctx, db.MarkRSSEntryScheduleFailedParams{
			ErrorCode:    &code,
			ErrorMessage: stringPointer(truncate(message, 2000)),
			ID:           repository.UUIDToPG(entryID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("mark RSS schedule failed: %w", err)
		}
		data, err := json.Marshal(map[string]any{"status": entry.Status, "errorCode": code})
		if err != nil {
			return fmt.Errorf("encode RSS schedule failure event: %w", err)
		}
		return appendRSSEntryEvent(ctx, scope.Queries, entryID, "rss.entry.enqueue_failed", data)
	})
}

func appendRSSEvent(
	ctx context.Context,
	queries *db.Queries,
	subscriptionID uuid.UUID,
	operationID uuid.UUID,
	topic string,
	data []byte,
) error {
	resourceType := "rss_subscription"
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           repository.UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(subscriptionID),
		OperationID:  nullableUUID(operationID),
		Data:         data,
	}); err != nil {
		return fmt.Errorf("append RSS event: %w", err)
	}
	return nil
}

func appendRSSEntryEvent(ctx context.Context, queries *db.Queries, entryID uuid.UUID, topic string, data []byte) error {
	resourceType := "rss_entry"
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           repository.UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(entryID),
		Data:         data,
	}); err != nil {
		return fmt.Errorf("append RSS entry event: %w", err)
	}
	return nil
}

func optionalHTTPURL(value string) *string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil
	}
	return optionalString(parsed.String())
}

func optionalTime(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func valueInt32(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}

func stringPointer(value string) *string {
	return &value
}

// truncate 按 rune 数截断文本，保证输出始终为合法 UTF-8：
// 按字节截断可能把多字节字符拦腰切断，非法序列会被 PostgreSQL 拒绝写入；
// []rune 转换还会把输入中的非法 UTF-8 字节替换为 U+FFFD，因此即使不需要截断也要返回转换结果，
// 覆盖上游消息本身含非法字节的路径。
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}
