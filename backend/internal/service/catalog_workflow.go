package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type CatalogWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
	operations *OperationScheduler
}

func NewCatalogWorkflow(
	queries *db.Queries,
	transactor *database.Transactor,
	operations *OperationScheduler,
) *CatalogWorkflow {
	return &CatalogWorkflow{queries: queries, transactor: transactor, operations: operations}
}

func (workflow *CatalogWorkflow) ScheduleTMDbSync(
	ctx context.Context,
	input domain.SyncTMDbSeries,
) (domain.CatalogCommandResult, error) {
	title := strings.Join(strings.Fields(input.SeriesTitle), " ")
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if input.TMDbSeriesID <= 0 {
		return domain.CatalogCommandResult{}, invalidCatalog("tmdbSeriesId", "must be positive")
	}
	if title == "" || len(title) > 512 {
		return domain.CatalogCommandResult{}, invalidCatalog("seriesTitle", "must contain between 1 and 512 characters")
	}
	if idempotencyKey == "" || len(idempotencyKey) > 256 {
		return domain.CatalogCommandResult{}, invalidCatalog("idempotencyKey", "must contain between 1 and 256 characters")
	}
	if input.ActorUserID == uuid.Nil {
		return domain.CatalogCommandResult{}, invalidCatalog("actorUserId", "must be present")
	}

	commandKey := "tmdb.sync:" + input.ActorUserID.String() + ":" + idempotencyKey
	result := domain.CatalogCommandResult{}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		tmdbID := input.TMDbSeriesID
		series, err := scope.Queries.UpsertSearchMediaSeries(ctx, db.UpsertSearchMediaSeriesParams{
			ID:           repository.UUIDToPG(deterministicResourceID("tmdb.series:" + fmt.Sprint(tmdbID))),
			TmdbSeriesID: &tmdbID,
			Title:        title,
		})
		if err != nil {
			return fmt.Errorf("ensure TMDb series: %w", err)
		}
		seriesID := repository.UUIDFromPG(series.ID)
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindTMDbSync,
			ResourceType:   "media_series",
			ResourceID:     seriesID,
			IdempotencyKey: commandKey,
			MaxAttempts:    4,
			Timeout:        10 * time.Minute,
			Payload:        map[string]any{"tmdbSeriesId": input.TMDbSeriesID},
			ActorUserID:    input.ActorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule TMDb sync: %w", err)
		}
		result = domain.CatalogCommandResult{SeriesID: seriesID, Operation: scheduled.Operation}
		return nil
	})
	if err != nil {
		return domain.CatalogCommandResult{}, catalogCommandError("schedule TMDb synchronization", err)
	}
	return result, nil
}

func (workflow *CatalogWorkflow) ListAgentMappingAcquisitions(ctx context.Context, seriesID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := workflow.queries.ListAgentMappingAcquisitionsBySeries(ctx, repository.UUIDToPG(seriesID))
	if err != nil {
		return nil, fmt.Errorf("list Agent Mapping acquisitions: %w", err)
	}
	result := make([]uuid.UUID, 0, len(rows))
	for _, id := range rows {
		result = append(result, repository.UUIDFromPG(id))
	}
	return result, nil
}

func (workflow *CatalogWorkflow) AutomaticEpisodeMappingEnabled(ctx context.Context, acquisitionID uuid.UUID) (bool, error) {
	enabled, err := workflow.queries.IsAutomaticEpisodeMappingEnabled(ctx, repository.UUIDToPG(acquisitionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("load automatic episode Mapping policy: %w", err)
	}
	return enabled, nil
}

// TryDeterministicEpisodeMapping resolves the ordinary case where every
// selected source video maps directly to the unique TMDb episode with the
// same season and episode number. Any uncertainty is left for Agent or user
// anchor selection.
func (workflow *CatalogWorkflow) TryDeterministicEpisodeMapping(ctx context.Context, acquisitionID uuid.UUID) (bool, error) {
	mappingContext, err := workflow.queries.GetAgentMappingContext(ctx, repository.UUIDToPG(acquisitionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("load deterministic Mapping context: %w", err)
	}
	if mappingContext.MappingProfileID.Valid {
		return true, nil
	}
	files, err := workflow.queries.ListAgentMappingFiles(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return false, fmt.Errorf("list deterministic Mapping files: %w", err)
	}
	episodes, err := workflow.queries.ListAgentTMDbEpisodes(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return false, fmt.Errorf("list deterministic Mapping episodes: %w", err)
	}
	anchor, ok := deterministicMappingAnchor(files, episodes)
	if !ok {
		return false, nil
	}
	_, err = workflow.saveEpisodeMapping(ctx, domain.EpisodeMappingPlanInput{
		AcquisitionID:  acquisitionID,
		Anchor:         anchor,
		IdempotencyKey: "deterministic-exact:" + acquisitionID.String(),
	}, mappingSaveOrigin{DecisionSource: domain.DecisionSourceDeterministic})
	if err != nil {
		return false, err
	}
	return true, nil
}

func deterministicMappingAnchor(
	files []db.ListAgentMappingFilesRow,
	episodes []db.ListAgentTMDbEpisodesRow,
) (domain.EpisodeMappingAnchorInput, bool) {
	if len(files) == 0 || len(episodes) == 0 {
		return domain.EpisodeMappingAnchorInput{}, false
	}
	targetCounts := make(map[domain.EpisodeCoordinate]int, len(episodes))
	for _, episode := range episodes {
		coordinate := domain.EpisodeCoordinate{Season: int(episode.SeasonNumber), Episode: int(episode.EpisodeNumber)}
		targetCounts[coordinate]++
	}
	sourceSeason := 0
	seen := make(map[domain.EpisodeCoordinate]struct{}, len(files))
	for _, file := range files {
		if file.SourceSeason == nil || file.SourceEpisode == nil || *file.SourceSeason <= 0 || *file.SourceEpisode <= 0 {
			return domain.EpisodeMappingAnchorInput{}, false
		}
		coordinate := domain.EpisodeCoordinate{Season: int(*file.SourceSeason), Episode: int(*file.SourceEpisode)}
		if sourceSeason == 0 {
			sourceSeason = coordinate.Season
		}
		if coordinate.Season != sourceSeason || targetCounts[coordinate] != 1 {
			return domain.EpisodeMappingAnchorInput{}, false
		}
		if _, duplicate := seen[coordinate]; duplicate {
			return domain.EpisodeMappingAnchorInput{}, false
		}
		seen[coordinate] = struct{}{}
	}
	first := files[0]
	return domain.EpisodeMappingAnchorInput{
		SourceFileID: repository.UUIDFromPG(first.ID),
		Target:       domain.EpisodeCoordinate{Season: int(*first.SourceSeason), Episode: int(*first.SourceEpisode)},
	}, true
}

func (workflow *CatalogWorkflow) SaveTMDbCatalog(
	ctx context.Context,
	operation domain.Operation,
	catalog domain.TMDbSeriesCatalog,
) error {
	if operation.ResourceType != "media_series" || operation.ResourceID == uuid.Nil || catalog.TMDbSeriesID <= 0 {
		return fmt.Errorf("TMDb sync requires a media series operation and catalog")
	}
	if strings.TrimSpace(catalog.Name) == "" || !json.Valid(catalog.Payload) {
		return fmt.Errorf("TMDb series catalog is incomplete")
	}
	fetchedAt := time.Now().UTC()
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, err := scope.Queries.LockMediaSeries(ctx, repository.UUIDToPG(operation.ResourceID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock TMDb series: %w", err)
		}
		if existing.TmdbSeriesID == nil || *existing.TmdbSeriesID != catalog.TMDbSeriesID {
			return fmt.Errorf("TMDb series operation does not match catalog ID")
		}
		tmdbID := catalog.TMDbSeriesID
		series, err := scope.Queries.UpsertTMDbSeries(ctx, db.UpsertTMDbSeriesParams{
			ID:            existing.ID,
			TmdbSeriesID:  &tmdbID,
			Title:         strings.TrimSpace(catalog.Name),
			OriginalTitle: optionalString(catalog.OriginalName),
			Metadata:      catalog.Payload,
		})
		if err != nil {
			return fmt.Errorf("persist TMDb series: %w", err)
		}
		seasonNumbers := make([]int32, 0, len(catalog.Seasons))
		episodeCount := 0
		for _, seasonCatalog := range catalog.Seasons {
			if seasonCatalog.SeasonNumber < 0 || seasonCatalog.SeasonNumber > math.MaxInt32 || seasonCatalog.TMDbSeasonID <= 0 || !json.Valid(seasonCatalog.Payload) {
				return fmt.Errorf("TMDb season catalog is invalid")
			}
			seasonNumber := int32(seasonCatalog.SeasonNumber)
			seasonNumbers = append(seasonNumbers, seasonNumber)
			seasonTMDbID := seasonCatalog.TMDbSeasonID
			season, err := scope.Queries.UpsertTMDbSeason(ctx, db.UpsertTMDbSeasonParams{
				ID:              repository.UUIDToPG(deterministicResourceID(fmt.Sprintf("tmdb.season:%d", seasonTMDbID))),
				SeriesID:        series.ID,
				TmdbSeasonID:    &seasonTMDbID,
				SeasonNumber:    seasonNumber,
				Name:            optionalString(seasonCatalog.Name),
				EpisodeCount:    int32(len(seasonCatalog.Episodes)),
				FetchedAt:       pgtype.Timestamptz{Time: fetchedAt, Valid: true},
				UpstreamPayload: seasonCatalog.Payload,
			})
			if err != nil {
				return fmt.Errorf("persist TMDb season %d: %w", seasonCatalog.SeasonNumber, err)
			}
			episodeNumbers := make([]int32, 0, len(seasonCatalog.Episodes))
			for _, episodeCatalog := range seasonCatalog.Episodes {
				if episodeCatalog.EpisodeNumber <= 0 || episodeCatalog.EpisodeNumber > math.MaxInt32 || episodeCatalog.TMDbEpisodeID <= 0 || strings.TrimSpace(episodeCatalog.Name) == "" || !json.Valid(episodeCatalog.Payload) {
					return fmt.Errorf("TMDb season %d contains an invalid episode", seasonCatalog.SeasonNumber)
				}
				episodeNumber := int32(episodeCatalog.EpisodeNumber)
				episodeNumbers = append(episodeNumbers, episodeNumber)
				episodeTMDbID := episodeCatalog.TMDbEpisodeID
				airDate := pgtype.Date{}
				if episodeCatalog.AirDate != nil {
					airDate = pgtype.Date{Time: *episodeCatalog.AirDate, Valid: true}
				}
				if _, err := scope.Queries.UpsertTMDbEpisode(ctx, db.UpsertTMDbEpisodeParams{
					ID:              repository.UUIDToPG(deterministicResourceID(fmt.Sprintf("tmdb.episode:%d", episodeTMDbID))),
					SeasonID:        season.ID,
					TmdbEpisodeID:   &episodeTMDbID,
					EpisodeNumber:   episodeNumber,
					Title:           strings.TrimSpace(episodeCatalog.Name),
					AirDate:         airDate,
					UpstreamPayload: episodeCatalog.Payload,
				}); err != nil {
					return fmt.Errorf("persist TMDb episode S%02dE%02d: %w", seasonCatalog.SeasonNumber, episodeCatalog.EpisodeNumber, err)
				}
			}
			episodeCount += len(episodeNumbers)
			if _, err := scope.Queries.DeleteStaleTMDbEpisodes(ctx, db.DeleteStaleTMDbEpisodesParams{
				SeasonID:       season.ID,
				EpisodeNumbers: episodeNumbers,
			}); err != nil {
				return fmt.Errorf("delete stale TMDb episodes: %w", err)
			}
		}
		if _, err := scope.Queries.DeleteStaleTMDbSeasons(ctx, db.DeleteStaleTMDbSeasonsParams{
			SeriesID:      series.ID,
			SeasonNumbers: seasonNumbers,
		}); err != nil {
			return fmt.Errorf("delete stale TMDb seasons: %w", err)
		}
		return appendCatalogEvent(ctx, scope.Queries, "tmdb.series_synchronized", "media_series", series.ID, repository.UUIDToPG(operation.ID), uuid.Nil, map[string]any{
			"tmdbSeriesId": catalog.TMDbSeriesID,
			"seasonCount":  len(seasonNumbers),
			"episodeCount": episodeCount,
		})
	})
}

func (workflow *CatalogWorkflow) PreviewEpisodeMapping(
	ctx context.Context,
	input domain.EpisodeMappingPlanInput,
) (domain.EpisodeMappingPreview, error) {
	if err := validateMappingInput(input, false); err != nil {
		return domain.EpisodeMappingPreview{}, err
	}
	acquisition, err := workflow.queries.GetAcquisitionMappingContext(ctx, repository.UUIDToPG(input.AcquisitionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EpisodeMappingPreview{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.EpisodeMappingPreview{}, fmt.Errorf("load acquisition mapping context: %w", err)
	}
	preview, err := workflow.buildMappingPreview(ctx, workflow.queries, input, repository.UUIDFromPG(acquisition.SeriesID))
	if err != nil {
		return domain.EpisodeMappingPreview{}, catalogCommandError("preview episode mapping", err)
	}
	return preview, nil
}

func (workflow *CatalogWorkflow) SaveEpisodeMapping(
	ctx context.Context,
	input domain.EpisodeMappingPlanInput,
) (domain.SavedEpisodeMapping, error) {
	if err := validateMappingInput(input, true); err != nil {
		return domain.SavedEpisodeMapping{}, err
	}
	return workflow.saveEpisodeMapping(ctx, input, mappingSaveOrigin{
		DecisionSource: domain.DecisionSourceUser,
	})
}

type mappingSaveOrigin struct {
	DecisionSource  domain.DecisionSource
	Resolution      *domain.AgentResolution
	Validation      domain.AgentProposalValidation
	ExpectedVersion int
}

func (workflow *CatalogWorkflow) ApplyAgentEpisodeMapping(
	ctx context.Context,
	resolution domain.AgentResolution,
	proposal domain.AgentEpisodeMappingProposal,
	validation domain.AgentProposalValidation,
) (domain.SavedEpisodeMapping, error) {
	input := domain.EpisodeMappingPlanInput{
		AcquisitionID: proposal.AcquisitionID,
		Anchor: domain.EpisodeMappingAnchorInput{
			SourceFileID: proposal.SourceFileID,
			Target:       domain.EpisodeCoordinate{Season: proposal.TargetSeason, Episode: proposal.TargetEpisode},
		},
		ActorUserID: uuid.Nil,
	}
	if err := validateMappingInput(input, false); err != nil {
		return domain.SavedEpisodeMapping{}, err
	}
	return workflow.saveEpisodeMapping(ctx, input, mappingSaveOrigin{
		DecisionSource: domain.DecisionSourceAgentAuto, Resolution: &resolution, Validation: validation, ExpectedVersion: resolution.Version,
	})
}

func (workflow *CatalogWorkflow) saveEpisodeMapping(
	ctx context.Context,
	input domain.EpisodeMappingPlanInput,
	origin mappingSaveOrigin,
) (domain.SavedEpisodeMapping, error) {
	commandKey := "mapping.save:" + input.ActorUserID.String() + ":" + strings.TrimSpace(input.IdempotencyKey)
	if origin.Resolution != nil {
		commandKey = "mapping.save:agent:" + origin.Resolution.ID.String()
	}
	fingerprint, err := mappingFingerprint(input)
	if err != nil {
		return domain.SavedEpisodeMapping{}, fmt.Errorf("fingerprint mapping request: %w", err)
	}
	result := domain.SavedEpisodeMapping{}
	err = workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if origin.Resolution != nil {
			lockedResolution, lockErr := scope.Queries.LockAgentResolution(ctx, repository.UUIDToPG(origin.Resolution.ID))
			if errors.Is(lockErr, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			if lockErr != nil {
				return fmt.Errorf("lock Agent Mapping resolution: %w", lockErr)
			}
			if lockedResolution.Capability != string(domain.AgentCapabilityEpisodeMapping) || lockedResolution.ResourceType != "acquisition" || repository.UUIDFromPG(lockedResolution.ResourceID) != input.AcquisitionID {
				return NewError("agent_tool_scope_violation", "the Agent Mapping proposal is outside its resolution scope", ErrStateConflict, nil)
			}
			if lockedResolution.Status != string(domain.AgentResolutionApplied) {
				if int(lockedResolution.Version) != origin.ExpectedVersion || (lockedResolution.Status != string(domain.AgentResolutionProposed) && lockedResolution.Status != string(domain.AgentResolutionReviewRequired)) {
					return NewError("agent_resolution_stale", "the Agent Mapping proposal is no longer current", ErrStateConflict, nil)
				}
				setting, settingErr := scope.Queries.GetAppSetting(ctx, domain.RuntimeSettingsName)
				if settingErr != nil {
					return fmt.Errorf("load current Agent settings: %w", settingErr)
				}
				var runtimeSettings domain.RuntimeSettings
				runtimeSettings.Agent = domain.DefaultAgentSettings()
				if err := json.Unmarshal(setting.Value, &runtimeSettings); err != nil {
					return fmt.Errorf("decode current Agent settings: %w", err)
				}
				agentSettings := runtimeSettings.Agent.WithDefaults()
				if setting.Version != lockedResolution.ConfigurationVersion || !agentSettings.Enabled || !agentSettings.EpisodeMappingEnabled || agentSettings.Model != lockedResolution.Model || agentSettings.Protocol != lockedResolution.Protocol || providerOrigin(agentSettings.BaseURL) != lockedResolution.ProviderOrigin {
					return NewError("agent_resolution_stale", "the Agent configuration changed after this proposal was created", ErrStateConflict, nil)
				}
				if origin.DecisionSource == domain.DecisionSourceAgentAuto && !agentCapabilityAutomatic(agentSettings, domain.AgentCapabilityEpisodeMapping) {
					return NewError("agent_auto_apply_disabled", "automatic Agent episode Mapping is disabled", ErrStateConflict, nil)
				}
			}
		}
		existing, existingErr := scope.Queries.GetMappingSaveByIdempotencyKey(ctx, commandKey)
		switch {
		case existingErr == nil:
			if existing.AcquisitionID != repository.UUIDToPG(input.AcquisitionID) || !bytes.Equal(existing.RequestFingerprint, fingerprint[:]) {
				return idempotencyConflict(input.IdempotencyKey)
			}
			if err := json.Unmarshal(existing.ResultPayload, &result); err != nil {
				return fmt.Errorf("decode saved mapping result: %w", err)
			}
			return nil
		case !errors.Is(existingErr, pgx.ErrNoRows):
			return fmt.Errorf("load mapping idempotency record: %w", existingErr)
		}

		acquisition, err := scope.Queries.LockAcquisitionForMapping(ctx, repository.UUIDToPG(input.AcquisitionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock acquisition mapping context: %w", err)
		}
		if origin.Resolution != nil && acquisition.MappingProfileID.Valid {
			return NewError("agent_mapping_conflict", "an episode Mapping profile already exists for this acquisition", ErrStateConflict, map[string]any{
				"mappingProfileId": repository.UUIDFromPG(acquisition.MappingProfileID),
			})
		}
		if _, err := scope.Queries.LockMediaSeries(ctx, acquisition.SeriesID); err != nil {
			return fmt.Errorf("lock mapping series: %w", err)
		}
		seriesID := repository.UUIDFromPG(acquisition.SeriesID)
		preview, err := workflow.buildMappingPreview(ctx, scope.Queries, input, seriesID)
		if err != nil {
			return err
		}
		if pending := len(preview.Rows) - mappedRowCount(preview.Rows); pending > 0 {
			return NewError("mapping_incomplete", "all selected video files must have a completed episode mapping", ErrStateConflict, map[string]any{
				"pendingRows": pending,
			})
		}
		profileRows, err := workflow.buildMappingProfileRows(ctx, scope.Queries, input, seriesID, preview.Rows)
		if err != nil {
			return err
		}
		anchorRow, ok := mappingAnchorRow(preview.Rows, input.Anchor.SourceFileID)
		if !ok || anchorRow.TargetEpisodeID == uuid.Nil {
			return NewError("mapping_anchor_invalid", "the selected mapping anchor is unavailable", ErrStateConflict, map[string]any{
				"sourceFileId": input.Anchor.SourceFileID,
			})
		}
		name := mappingProfileName(input.AcquisitionID, repository.UUIDFromPG(acquisition.RssSubscriptionID))
		version, err := scope.Queries.NextMappingProfileVersion(ctx, db.NextMappingProfileVersionParams{
			SeriesID: acquisition.SeriesID,
			Name:     name,
		})
		if err != nil {
			return fmt.Errorf("select mapping profile version: %w", err)
		}
		if _, err := scope.Queries.DeactivateMappingProfiles(ctx, db.DeactivateMappingProfilesParams{
			SeriesID: acquisition.SeriesID,
			Name:     name,
		}); err != nil {
			return fmt.Errorf("deactivate mapping profile: %w", err)
		}
		profileID := uuid.New()
		createdBy := nullableMappingActor(input.ActorUserID)
		agentResolutionID := pgtype.UUID{}
		var eventAgentResolutionID any
		if origin.Resolution != nil {
			agentResolutionID = repository.UUIDToPG(origin.Resolution.ID)
			eventAgentResolutionID = origin.Resolution.ID
		}
		if _, err := scope.Queries.CreateMappingProfile(ctx, db.CreateMappingProfileParams{
			ID:                    repository.UUIDToPG(profileID),
			SeriesID:              acquisition.SeriesID,
			Name:                  name,
			Version:               version,
			AnchorSourceSeason:    mappingInt32(anchorRow.SourceSeason),
			AnchorSourceEpisode:   mappingInt32(anchorRow.SourceEpisode),
			AnchorTargetEpisodeID: repository.UUIDToPG(anchorRow.TargetEpisodeID),
			TargetEpisodeOffset:   mappingInt32(anchorRow.AbsoluteEpisode - anchorRow.SourceEpisode),
			CreatedBy:             createdBy,
			DecisionSource:        string(origin.DecisionSource),
			AgentResolutionID:     agentResolutionID,
		}); err != nil {
			return fmt.Errorf("create mapping profile: %w", err)
		}
		for _, row := range profileRows {
			absoluteEpisode := (*int32)(nil)
			if row.AbsoluteEpisode > 0 {
				value := int32(row.AbsoluteEpisode)
				absoluteEpisode = &value
			}
			targetEpisodeID := pgtype.UUID{}
			if row.TargetEpisodeID != uuid.Nil {
				targetEpisodeID = repository.UUIDToPG(row.TargetEpisodeID)
			}
			if _, err := scope.Queries.CreateEpisodeMapping(ctx, db.CreateEpisodeMappingParams{
				ID:              repository.UUIDToPG(uuid.New()),
				ProfileID:       repository.UUIDToPG(profileID),
				SourceSeason:    int32(row.SourceSeason),
				SourceEpisode:   int32(row.SourceEpisode),
				AbsoluteEpisode: absoluteEpisode,
				TargetEpisodeID: targetEpisodeID,
				MappingStatus:   string(row.Status),
				MatchSource:     string(row.MatchSource),
				ErrorCode:       optionalString(row.ErrorCode),
			}); err != nil {
				return fmt.Errorf("create episode mapping: %w", err)
			}
		}
		if _, err := scope.Queries.UpdateAcquisitionMappingProfile(ctx, db.UpdateAcquisitionMappingProfileParams{
			MappingProfileID: repository.UUIDToPG(profileID),
			ID:               acquisition.ID,
		}); err != nil {
			return fmt.Errorf("activate acquisition mapping profile: %w", err)
		}
		subscriptionID, propagated, err := applyMappingProfileToRSSScope(ctx, scope.Queries, input.AcquisitionID, profileID)
		if err != nil {
			return err
		}
		corrected, err := repairMappingScopeCoordinates(ctx, scope.Queries, input.AcquisitionID)
		if err != nil {
			return err
		}
		scheduled, err := scheduleMappingMaterializations(ctx, scope, workflow.operations, input.AcquisitionID, profileID, input.ActorUserID)
		if err != nil {
			return err
		}

		result = domain.SavedEpisodeMapping{ProfileID: profileID, Version: int(version), Preview: preview}
		resultPayload, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode saved mapping result: %w", err)
		}
		if _, err := scope.Queries.CreateMappingSave(ctx, db.CreateMappingSaveParams{
			ID:                 repository.UUIDToPG(uuid.New()),
			AcquisitionID:      acquisition.ID,
			ProfileID:          repository.UUIDToPG(profileID),
			IdempotencyKey:     commandKey,
			RequestFingerprint: fingerprint[:],
			ResultPayload:      resultPayload,
			CreatedBy:          createdBy,
			DecisionSource:     string(origin.DecisionSource),
			AgentResolutionID:  agentResolutionID,
		}); err != nil {
			return fmt.Errorf("record mapping save: %w", err)
		}
		if err := appendCatalogEvent(ctx, scope.Queries, "mapping.profile_saved", "acquisition", acquisition.ID, pgtype.UUID{}, input.ActorUserID, map[string]any{
			"profileId":                 profileID,
			"version":                   version,
			"decisionSource":            origin.DecisionSource,
			"agentResolutionId":         eventAgentResolutionID,
			"mapped":                    mappedRowCount(profileRows),
			"pending":                   len(profileRows) - mappedRowCount(profileRows),
			"correctedFiles":            corrected,
			"propagatedAcquisitions":    propagated,
			"materializationOperations": scheduled,
		}); err != nil {
			return err
		}
		if subscriptionID != uuid.Nil {
			if err := appendCatalogEvent(ctx, scope.Queries, "rss.mapping_profile_applied", "rss_subscription", repository.UUIDToPG(subscriptionID), pgtype.UUID{}, input.ActorUserID, map[string]any{
				"profileId": profileID, "sourceAcquisitionId": input.AcquisitionID, "propagatedAcquisitions": propagated,
			}); err != nil {
				return err
			}
		}
		if origin.Resolution != nil {
			validationJSON, err := json.Marshal(origin.Validation)
			if err != nil {
				return fmt.Errorf("encode Agent Mapping validation: %w", err)
			}
			if _, err := scope.Queries.CompleteAgentResolution(ctx, db.CompleteAgentResolutionParams{
				Status: string(domain.AgentResolutionApplied), Validation: validationJSON, ID: repository.UUIDToPG(origin.Resolution.ID),
			}); err != nil {
				return fmt.Errorf("complete automatic Agent Mapping resolution: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return domain.SavedEpisodeMapping{}, catalogCommandError("save episode mapping", err)
	}
	return result, nil
}

func applyMappingProfileToRSSScope(
	ctx context.Context,
	queries *db.Queries,
	acquisitionID uuid.UUID,
	profileID uuid.UUID,
) (uuid.UUID, int64, error) {
	subscription, err := queries.ApplyMappingProfileToRSSSubscription(ctx, db.ApplyMappingProfileToRSSSubscriptionParams{
		MappingProfileID: repository.UUIDToPG(profileID),
		AcquisitionID:    repository.UUIDToPG(acquisitionID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, 0, nil
	}
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("apply mapping profile to RSS subscription: %w", err)
	}
	propagated, err := queries.PropagateRSSMappingProfile(ctx, db.PropagateRSSMappingProfileParams{
		MappingProfileID: repository.UUIDToPG(profileID),
		SubscriptionID:   subscription,
	})
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("propagate RSS mapping profile: %w", err)
	}
	return repository.UUIDFromPG(subscription), propagated, nil
}

func repairMappingScopeCoordinates(ctx context.Context, queries *db.Queries, acquisitionID uuid.UUID) (int64, error) {
	files, err := queries.ListMappingScopeSelectedVideos(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return 0, fmt.Errorf("list mapping scope selected videos: %w", err)
	}
	var corrected int64
	for _, file := range files {
		coordinate := mappingSourceCoordinate(file.RelativePath, file.SourceSeason, file.SourceEpisode)
		if coordinate.Season <= 0 || coordinate.Episode <= 0 {
			continue
		}
		season, episode := int32(coordinate.Season), int32(coordinate.Episode)
		changed, err := queries.UpdateSelectedFileCoordinateFamily(ctx, db.UpdateSelectedFileCoordinateFamilyParams{
			NewSourceSeason:  &season,
			NewSourceEpisode: &episode,
			SourceFileID:     file.ID,
		})
		if err != nil {
			return 0, fmt.Errorf("update selected file source coordinate: %w", err)
		}
		corrected += changed
	}
	if _, err := queries.SyncMappingScopeSourceFacts(ctx, repository.UUIDToPG(acquisitionID)); err != nil {
		return 0, fmt.Errorf("synchronize mapping source facts: %w", err)
	}
	return corrected, nil
}

func scheduleMappingMaterializations(
	ctx context.Context,
	scope database.TxScope,
	operations *OperationScheduler,
	acquisitionID uuid.UUID,
	profileID uuid.UUID,
	actorUserID uuid.UUID,
) (int, error) {
	candidates, err := scope.Queries.ListMappingMaterializationCandidates(ctx, db.ListMappingMaterializationCandidatesParams{
		AcquisitionID:    repository.UUIDToPG(acquisitionID),
		MappingProfileID: repository.UUIDToPG(profileID),
	})
	if err != nil {
		return 0, fmt.Errorf("list mapped download materialization candidates: %w", err)
	}
	if len(candidates) > 0 && operations == nil {
		return 0, fmt.Errorf("schedule mapped download materialization: operation scheduler is unavailable")
	}
	scheduledCount := 0
	for _, candidate := range candidates {
		downloadID := repository.UUIDFromPG(candidate.ID)
		recovered := false
		if candidate.Status == string(domain.DownloadFailed) {
			if _, err := scope.Queries.RequeueMappingMaterialization(ctx, db.RequeueMappingMaterializationParams{
				ID: candidate.ID, ExpectedVersion: candidate.Version,
			}); errors.Is(err, pgx.ErrNoRows) {
				continue
			} else if err != nil {
				return 0, fmt.Errorf("requeue mapped download materialization: %w", err)
			}
			recovered = true
		}
		scheduled, err := operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindDownloadMaterialize,
			ResourceType:   "download",
			ResourceID:     downloadID,
			IdempotencyKey: "download.materialize:" + downloadID.String() + ":mapping:" + profileID.String(),
			MaxAttempts:    3,
			Timeout:        10 * time.Minute,
			Payload:        map[string]any{"mappingProfileId": profileID},
			ActorUserID:    actorUserID,
		})
		if err != nil {
			return 0, fmt.Errorf("schedule mapped download materialization: %w", err)
		}
		if recovered {
			if err := appendCatalogEvent(ctx, scope.Queries, "download.mapping_recovered", "download", candidate.ID, repository.UUIDToPG(scheduled.Operation.ID), actorUserID, map[string]any{
				"profileId": profileID,
			}); err != nil {
				return 0, err
			}
		}
		scheduledCount++
	}
	return scheduledCount, nil
}

func mappingSourceCoordinate(relativePath string, sourceSeason, sourceEpisode *int32) domain.EpisodeCoordinate {
	coordinate := domain.EpisodeCoordinate{}
	if sourceSeason != nil {
		coordinate.Season = int(*sourceSeason)
	}
	if sourceEpisode != nil {
		coordinate.Episode = int(*sourceEpisode)
	}
	if season, episode, ok := domain.ParseSourceCoordinate(relativePath, coordinate.Season); ok {
		coordinate.Season = season
		coordinate.Episode = episode
	}
	return coordinate
}

type mappingPreviewSource struct {
	id           uuid.UUID
	relativePath string
	coordinate   domain.EpisodeCoordinate
}

func (workflow *CatalogWorkflow) buildMappingPreview(
	ctx context.Context,
	queries *db.Queries,
	input domain.EpisodeMappingPlanInput,
	seriesID uuid.UUID,
) (domain.EpisodeMappingPreview, error) {
	files, err := queries.ListAcquisitionSelectedVideos(ctx, repository.UUIDToPG(input.AcquisitionID))
	if err != nil {
		return domain.EpisodeMappingPreview{}, fmt.Errorf("list selected video files: %w", err)
	}
	if len(files) == 0 {
		return domain.EpisodeMappingPreview{}, NewError("mapping_files_unavailable", "the acquisition has no selected video files", ErrStateConflict, map[string]any{"acquisitionId": input.AcquisitionID})
	}
	seasons, targetIDs, err := loadSeriesMappingCatalog(ctx, queries, seriesID)
	if err != nil {
		return domain.EpisodeMappingPreview{}, err
	}

	sources := make([]mappingPreviewSource, 0, len(files))
	seenCoordinates := map[domain.EpisodeCoordinate]uuid.UUID{}
	anchorSource := domain.EpisodeCoordinate{}
	for _, file := range files {
		fileID := repository.UUIDFromPG(file.ID)
		source := mappingSourceCoordinate(file.RelativePath, file.SourceSeason, file.SourceEpisode)
		if source.Season <= 0 || source.Episode <= 0 {
			return domain.EpisodeMappingPreview{}, NewError("mapping_source_invalid", "a selected video file has no valid episode coordinate", ErrStateConflict, map[string]any{
				"fileId": fileID, "relativePath": file.RelativePath,
			})
		}
		if previous, duplicate := seenCoordinates[source]; duplicate {
			return domain.EpisodeMappingPreview{}, NewError("mapping_source_duplicate", "selected video files have duplicate episode coordinates", ErrStateConflict, map[string]any{
				"sourceSeason": source.Season, "sourceEpisode": source.Episode, "fileIds": []uuid.UUID{previous, fileID},
			})
		}
		seenCoordinates[source] = fileID
		sources = append(sources, mappingPreviewSource{id: fileID, relativePath: file.RelativePath, coordinate: source})
		if fileID == input.Anchor.SourceFileID {
			anchorSource = source
		}
	}
	if anchorSource.Season == 0 {
		return domain.EpisodeMappingPreview{}, NewError("mapping_anchor_source_invalid", "the anchor must reference a selected video file", ErrStateConflict, map[string]any{
			"sourceFileId": input.Anchor.SourceFileID,
		})
	}

	preview := domain.EpisodeMappingPreview{
		AcquisitionID: input.AcquisitionID,
		SeriesID:      seriesID,
		Anchor:        input.Anchor,
		Rows:          make([]domain.EpisodeMappingRow, 0, len(sources)),
	}
	for _, file := range sources {
		resolved := domain.ResolveEpisodeMapping(domain.EpisodeMappingRequest{
			Source:       file.coordinate,
			AnchorSource: anchorSource,
			AnchorTarget: input.Anchor.Target,
			TMDbSeasons:  seasons,
		})
		preview.Rows = append(preview.Rows, domain.EpisodeMappingRow{
			SourceFileID:    file.id,
			RelativePath:    file.relativePath,
			SourceSeason:    file.coordinate.Season,
			SourceEpisode:   file.coordinate.Episode,
			AbsoluteEpisode: resolved.AbsoluteEpisode,
			Status:          resolved.Status,
			TargetSeason:    resolved.Target.Season,
			TargetEpisode:   resolved.Target.Episode,
			TargetEpisodeID: targetIDs[resolved.Target],
			TargetTitle:     resolved.TargetTitle,
			MatchSource:     resolved.MatchSource,
			ErrorCode:       resolved.ErrorCode,
		})
	}
	return preview, nil
}

func (workflow *CatalogWorkflow) buildMappingProfileRows(
	ctx context.Context,
	queries *db.Queries,
	input domain.EpisodeMappingPlanInput,
	seriesID uuid.UUID,
	previewRows []domain.EpisodeMappingRow,
) ([]domain.EpisodeMappingRow, error) {
	anchorRow, ok := mappingAnchorRow(previewRows, input.Anchor.SourceFileID)
	if !ok || anchorRow.Status != domain.MappingMapped {
		return nil, NewError("mapping_anchor_invalid", "the selected mapping anchor is unavailable", ErrStateConflict, map[string]any{
			"sourceFileId": input.Anchor.SourceFileID,
		})
	}
	seasons, targetIDs, err := loadSeriesMappingCatalog(ctx, queries, seriesID)
	if err != nil {
		return nil, err
	}
	return mappingProfileRowsFromAnchor(
		seasons,
		targetIDs,
		domain.EpisodeCoordinate{Season: anchorRow.SourceSeason, Episode: anchorRow.SourceEpisode},
		input.Anchor.Target,
	)
}

func mappingProfileRowsFromAnchor(
	seasons []domain.TMDbSeason,
	targetIDs map[domain.EpisodeCoordinate]uuid.UUID,
	anchorSource domain.EpisodeCoordinate,
	anchorTarget domain.EpisodeCoordinate,
) ([]domain.EpisodeMappingRow, error) {
	total := domain.RegularEpisodeCount(seasons)
	if total <= 0 || total > 10000 {
		return nil, NewError("mapping_catalog_invalid", "the TMDb catalog cannot be expanded into an automatic mapping", ErrStateConflict, map[string]any{
			"regularEpisodeCount": total,
		})
	}
	anchor := domain.ResolveEpisodeMapping(domain.EpisodeMappingRequest{
		Source:       anchorSource,
		AnchorSource: anchorSource,
		AnchorTarget: anchorTarget,
		TMDbSeasons:  seasons,
	})
	if anchor.Status != domain.MappingMapped || targetIDs[anchorTarget] == uuid.Nil {
		return nil, NewError("mapping_anchor_invalid", "the selected mapping anchor is unavailable", ErrStateConflict, map[string]any{})
	}
	offset := anchor.AbsoluteEpisode - anchorSource.Episode
	rows := make([]domain.EpisodeMappingRow, 0, total)
	for absolute := 1; absolute <= total; absolute++ {
		sourceEpisode := absolute - offset
		if sourceEpisode <= 0 || sourceEpisode > math.MaxInt32 {
			continue
		}
		source := domain.EpisodeCoordinate{Season: anchorSource.Season, Episode: sourceEpisode}
		resolved := domain.ResolveEpisodeMapping(domain.EpisodeMappingRequest{
			Source:       source,
			AnchorSource: anchorSource,
			AnchorTarget: anchorTarget,
			TMDbSeasons:  seasons,
		})
		row := mappingRowFromResolution(source, resolved, targetIDs)
		if row.Status != domain.MappingMapped || row.TargetEpisodeID == uuid.Nil {
			return nil, NewError("mapping_catalog_incomplete", "the TMDb catalog is missing an episode required by the automatic mapping", ErrStateConflict, map[string]any{
				"absoluteEpisode": absolute, "errorCode": row.ErrorCode,
			})
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, NewError("mapping_catalog_invalid", "the automatic mapping contains no usable source episodes", ErrStateConflict, map[string]any{})
	}
	return rows, nil
}

func mappingAnchorRow(rows []domain.EpisodeMappingRow, sourceFileID uuid.UUID) (domain.EpisodeMappingRow, bool) {
	for _, row := range rows {
		if row.SourceFileID == sourceFileID {
			return row, true
		}
	}
	return domain.EpisodeMappingRow{}, false
}

func mappingProfileName(acquisitionID, subscriptionID uuid.UUID) string {
	if subscriptionID != uuid.Nil {
		return "rss:" + subscriptionID.String()
	}
	return "acquisition:" + acquisitionID.String()
}

func mappingInt32(value int) *int32 {
	converted := int32(value)
	return &converted
}

func loadSeriesMappingCatalog(
	ctx context.Context,
	queries *db.Queries,
	seriesID uuid.UUID,
) ([]domain.TMDbSeason, map[domain.EpisodeCoordinate]uuid.UUID, error) {
	catalogRows, err := queries.ListSeriesMappingCatalog(ctx, repository.UUIDToPG(seriesID))
	if err != nil {
		return nil, nil, fmt.Errorf("load TMDb mapping catalog: %w", err)
	}
	if len(catalogRows) == 0 {
		return nil, nil, NewError("tmdb_catalog_missing", "the TMDb series catalog has not been synchronized", ErrStateConflict, map[string]any{"seriesId": seriesID})
	}
	seasonsByNumber := map[int]*domain.TMDbSeason{}
	targetIDs := map[domain.EpisodeCoordinate]uuid.UUID{}
	for _, row := range catalogRows {
		seasonNumber := int(row.SeasonNumber)
		season := seasonsByNumber[seasonNumber]
		if season == nil {
			season = &domain.TMDbSeason{Season: seasonNumber, EpisodeCount: int(row.EpisodeCount), Titles: map[int]string{}}
			seasonsByNumber[seasonNumber] = season
		}
		if row.EpisodeNumber != nil && row.Title != nil {
			episodeNumber := int(*row.EpisodeNumber)
			season.Titles[episodeNumber] = *row.Title
			targetIDs[domain.EpisodeCoordinate{Season: seasonNumber, Episode: episodeNumber}] = repository.UUIDFromPG(row.EpisodeID)
		}
	}
	seasons := make([]domain.TMDbSeason, 0, len(seasonsByNumber))
	for _, season := range seasonsByNumber {
		seasons = append(seasons, *season)
	}
	return seasons, targetIDs, nil
}

func mappingRowFromResolution(
	source domain.EpisodeCoordinate,
	resolved domain.EpisodeMappingResult,
	targetIDs map[domain.EpisodeCoordinate]uuid.UUID,
) domain.EpisodeMappingRow {
	return domain.EpisodeMappingRow{
		SourceSeason:    source.Season,
		SourceEpisode:   source.Episode,
		AbsoluteEpisode: resolved.AbsoluteEpisode,
		Status:          resolved.Status,
		TargetSeason:    resolved.Target.Season,
		TargetEpisode:   resolved.Target.Episode,
		TargetEpisodeID: targetIDs[resolved.Target],
		TargetTitle:     resolved.TargetTitle,
		MatchSource:     resolved.MatchSource,
		ErrorCode:       resolved.ErrorCode,
	}
}

func providerOrigin(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host)
}

func validateMappingInput(input domain.EpisodeMappingPlanInput, save bool) error {
	if input.AcquisitionID == uuid.Nil {
		return invalidCatalog("acquisitionId", "must be present")
	}
	if input.Anchor.SourceFileID == uuid.Nil {
		return invalidCatalog("anchor.sourceFileId", "must be present")
	}
	if input.Anchor.Target.Season <= 0 || input.Anchor.Target.Season > math.MaxInt32 {
		return invalidCatalog("anchor.targetSeason", "must be a positive 32-bit season number")
	}
	if input.Anchor.Target.Episode <= 0 || input.Anchor.Target.Episode > math.MaxInt32 {
		return invalidCatalog("anchor.targetEpisode", "must be a positive 32-bit episode number")
	}
	if !save {
		return nil
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" || len(key) > 256 {
		return invalidCatalog("idempotencyKey", "must contain between 1 and 256 characters")
	}
	if input.ActorUserID == uuid.Nil {
		return invalidCatalog("actorUserId", "must be present")
	}
	return nil
}

func mappingFingerprint(input domain.EpisodeMappingPlanInput) ([32]byte, error) {
	payload, err := json.Marshal(struct {
		AcquisitionID uuid.UUID                        `json:"acquisitionId"`
		Anchor        domain.EpisodeMappingAnchorInput `json:"anchor"`
	}{
		AcquisitionID: input.AcquisitionID,
		Anchor:        input.Anchor,
	})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func mappedRowCount(rows []domain.EpisodeMappingRow) int {
	count := 0
	for _, row := range rows {
		if row.Status == domain.MappingMapped {
			count++
		}
	}
	return count
}

func appendCatalogEvent(
	ctx context.Context,
	queries *db.Queries,
	topic string,
	resourceType string,
	resourceID pgtype.UUID,
	operationID pgtype.UUID,
	actorUserID uuid.UUID,
	data map[string]any,
) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode catalog event: %w", err)
	}
	actor := pgtype.UUID{}
	if actorUserID != uuid.Nil {
		actor = repository.UUIDToPG(actorUserID)
	}
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           repository.UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   resourceID,
		OperationID:  operationID,
		ActorUserID:  actor,
		Data:         encoded,
	}); err != nil {
		return fmt.Errorf("append catalog event: %w", err)
	}
	return nil
}

func invalidCatalog(field, message string) error {
	return NewError("invalid_request", "catalog request is invalid", ErrInvalidInput, map[string]any{"field": field, "reason": message})
}

func nullableMappingActor(actor uuid.UUID) pgtype.UUID {
	if actor == uuid.Nil {
		return pgtype.UUID{}
	}
	return repository.UUIDToPG(actor)
}

func catalogCommandError(action string, err error) error {
	var serviceErr *Error
	if errors.As(err, &serviceErr) || errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return NewError("service_unavailable", "catalog storage is unavailable", fmt.Errorf("%s: %w", action, err), map[string]any{"dependency": "postgresql"})
}
