package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type RSSPreacquisitionMappingAgent interface {
	AutomaticRSSPreacquisitionMappingEnabled(context.Context, uuid.UUID) (bool, error)
	TryDeterministicRSSPreacquisitionMapping(context.Context, uuid.UUID) (bool, error)
	PreviewRSSPreacquisitionMapping(context.Context, uuid.UUID, domain.EpisodeCoordinate, domain.EpisodeCoordinate) ([]domain.EpisodeMappingRow, error)
	ApplyAgentRSSPreacquisitionMapping(context.Context, domain.AgentResolution, domain.AgentRSSPreacquisitionMappingProposal, domain.AgentProposalValidation) error
}

func (workflow *RSSWorkflow) AutomaticRSSPreacquisitionMappingEnabled(ctx context.Context, scopeID uuid.UUID) (bool, error) {
	enabled, err := workflow.queries.IsRSSPreacquisitionMappingEnabled(ctx, repository.UUIDToPG(scopeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("load RSS pre-acquisition mapping policy: %w", err)
	}
	return enabled != nil && *enabled, nil
}

func (workflow *RSSWorkflow) TryDeterministicRSSPreacquisitionMapping(ctx context.Context, scopeID uuid.UUID) (bool, error) {
	resolved := false
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		mappingContext, err := scope.Queries.LockRSSPreacquisitionMappingContext(ctx, repository.UUIDToPG(scopeID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock deterministic RSS pre-acquisition mapping scope: %w", err)
		}
		if mappingContext.MappingProfileID.Valid {
			resolved = true
			return nil
		}
		if mappingContext.ScopeStatus != "pending" || !mappingContext.Enabled || !mappingContext.AutoEpisodeMapping ||
			mappingContext.DeletedAt.Valid || mappingContext.CompletedAt.Valid ||
			mappingContext.SubscriptionVersion != mappingContext.CurrentSubscriptionVersion {
			return NewError("agent_resolution_stale", "the RSS pre-acquisition mapping scope is no longer current", ErrStateConflict, nil)
		}
		if _, err := scope.Queries.LockMediaSeries(ctx, mappingContext.SeriesID); err != nil {
			return fmt.Errorf("lock deterministic RSS pre-acquisition mapping series: %w", err)
		}
		sources, err := scope.Queries.ListRSSPreacquisitionMappingSources(ctx, repository.UUIDToPG(scopeID))
		if err != nil {
			return fmt.Errorf("list deterministic RSS pre-acquisition mapping sources: %w", err)
		}
		if len(sources) == 0 {
			return NewError("rss_mapping_scope_empty", "the RSS pre-acquisition Mapping scope has no source coordinates", ErrStateConflict, nil)
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
		anchorSource := domain.EpisodeCoordinate{}
		for _, source := range sources {
			coordinate := domain.EpisodeCoordinate{Season: int(source.SourceSeason), Episode: int(source.SourceEpisode)}
			if coordinate.Season != int(mappingContext.SourceSeason) || coordinate.Episode <= 0 || targetIDs[coordinate] == uuid.Nil {
				return nil
			}
			if anchorSource.Episode == 0 || coordinate.Episode < anchorSource.Episode {
				anchorSource = coordinate
			}
		}
		anchorTarget := anchorSource
		rows, err := mappingProfileRowsFromAnchor(seasons, targetIDs, anchorSource, anchorTarget)
		if err != nil {
			return err
		}
		anchorTargetID := targetIDs[anchorTarget]
		anchorAbsolute := 0
		for _, row := range rows {
			if row.SourceSeason == anchorSource.Season && row.SourceEpisode == anchorSource.Episode {
				anchorAbsolute = row.AbsoluteEpisode
				break
			}
		}
		if anchorTargetID == uuid.Nil || anchorAbsolute <= 0 {
			return NewError("mapping_anchor_invalid", "the deterministic RSS mapping anchor is unavailable", ErrStateConflict, nil)
		}

		name := mappingProfileName(uuid.Nil, repository.UUIDFromPG(mappingContext.SubscriptionID))
		version, err := scope.Queries.NextMappingProfileVersion(ctx, db.NextMappingProfileVersionParams{
			SeriesID: mappingContext.SeriesID, Name: name,
		})
		if err != nil {
			return fmt.Errorf("select deterministic RSS pre-acquisition mapping profile version: %w", err)
		}
		if _, err := scope.Queries.DeactivateMappingProfiles(ctx, db.DeactivateMappingProfilesParams{
			SeriesID: mappingContext.SeriesID, Name: name,
		}); err != nil {
			return fmt.Errorf("deactivate deterministic RSS pre-acquisition mapping profiles: %w", err)
		}
		profileID := uuid.New()
		if _, err := scope.Queries.CreateMappingProfile(ctx, db.CreateMappingProfileParams{
			ID: repository.UUIDToPG(profileID), SeriesID: mappingContext.SeriesID, Name: name, Version: version,
			AnchorSourceSeason: mappingInt32(anchorSource.Season), AnchorSourceEpisode: mappingInt32(anchorSource.Episode),
			AnchorTargetEpisodeID: repository.UUIDToPG(anchorTargetID), TargetEpisodeOffset: mappingInt32(anchorAbsolute - anchorSource.Episode),
			DecisionSource: string(domain.DecisionSourceDeterministic),
		}); err != nil {
			return fmt.Errorf("create deterministic RSS pre-acquisition mapping profile: %w", err)
		}
		for _, row := range rows {
			absoluteEpisode := int32(row.AbsoluteEpisode)
			if _, err := scope.Queries.CreateEpisodeMapping(ctx, db.CreateEpisodeMappingParams{
				ID: repository.UUIDToPG(uuid.New()), ProfileID: repository.UUIDToPG(profileID),
				SourceSeason: int32(row.SourceSeason), SourceEpisode: int32(row.SourceEpisode), AbsoluteEpisode: &absoluteEpisode,
				TargetEpisodeID: repository.UUIDToPG(row.TargetEpisodeID), MappingStatus: string(row.Status), MatchSource: string(row.MatchSource),
			}); err != nil {
				return fmt.Errorf("create deterministic RSS pre-acquisition episode mapping: %w", err)
			}
		}
		newVersion, err := scope.Queries.ApplyRSSPreacquisitionMappingProfile(ctx, db.ApplyRSSPreacquisitionMappingProfileParams{
			MappingProfileID: repository.UUIDToPG(profileID), ScopeID: repository.UUIDToPG(scopeID),
			ExpectedVersion: mappingContext.CurrentSubscriptionVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return NewError("rss_mapping_conflict", "the RSS subscription changed while deterministic mapping was applied", ErrStateConflict, nil)
		}
		if err != nil {
			return fmt.Errorf("apply deterministic RSS pre-acquisition mapping profile: %w", err)
		}
		if _, err := scope.Queries.CompleteRSSPreacquisitionMappingScope(ctx, db.CompleteRSSPreacquisitionMappingScopeParams{
			AppliedProfileID: repository.UUIDToPG(profileID), ID: repository.UUIDToPG(scopeID),
		}); err != nil {
			return fmt.Errorf("complete deterministic RSS pre-acquisition mapping scope: %w", err)
		}
		if _, err := scope.Queries.ExpireOtherRSSPreacquisitionMappingScopes(ctx, db.ExpireOtherRSSPreacquisitionMappingScopesParams{
			SubscriptionID: mappingContext.SubscriptionID, AppliedScopeID: repository.UUIDToPG(scopeID),
		}); err != nil {
			return fmt.Errorf("expire alternate RSS pre-acquisition mapping scopes: %w", err)
		}
		if workflow.operations == nil {
			return fmt.Errorf("schedule deterministic RSS pre-acquisition poll: operation scheduler is unavailable")
		}
		if err := workflow.scheduleContinuousPoll(ctx, scope, domain.RSSSubscription{
			ID: repository.UUIDFromPG(mappingContext.SubscriptionID), Version: newVersion,
		}); err != nil {
			return err
		}
		if err := appendRSSEvent(ctx, scope.Queries, repository.UUIDFromPG(mappingContext.SubscriptionID), uuid.Nil, "rss.mapping_profile_applied", mustJSON(map[string]any{
			"profileId": profileID, "version": version, "decisionSource": domain.DecisionSourceDeterministic,
			"mappingScopeId": scopeID, "mapped": len(rows), "subscriptionVersion": newVersion,
		})); err != nil {
			return err
		}
		resolved = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return resolved, nil
}

func (workflow *RSSWorkflow) PreviewRSSPreacquisitionMapping(
	ctx context.Context,
	scopeID uuid.UUID,
	anchorSource domain.EpisodeCoordinate,
	anchorTarget domain.EpisodeCoordinate,
) ([]domain.EpisodeMappingRow, error) {
	mappingContext, err := workflow.queries.GetAgentRSSPreacquisitionMappingContext(ctx, repository.UUIDToPG(scopeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load RSS pre-acquisition mapping context: %w", err)
	}
	if mappingContext.Status != "pending" || !mappingContext.SubscriptionEnabled || !mappingContext.AutoEpisodeMapping ||
		mappingContext.MappingProfileID.Valid || mappingContext.SubscriptionDeletedAt.Valid || mappingContext.SubscriptionCompletedAt.Valid {
		return nil, NewError("agent_resolution_not_allowed", "the RSS mapping scope is no longer active", ErrStateConflict, nil)
	}
	return buildRSSPreacquisitionMappingRows(ctx, workflow.queries, scopeID, repository.UUIDFromPG(mappingContext.SeriesID), anchorSource, anchorTarget)
}

func buildRSSPreacquisitionMappingRows(
	ctx context.Context,
	queries *db.Queries,
	scopeID uuid.UUID,
	seriesID uuid.UUID,
	anchorSource domain.EpisodeCoordinate,
	anchorTarget domain.EpisodeCoordinate,
) ([]domain.EpisodeMappingRow, error) {
	if scopeID == uuid.Nil || seriesID == uuid.Nil || anchorSource.Season <= 0 || anchorSource.Episode <= 0 || anchorTarget.Season <= 0 || anchorTarget.Episode <= 0 {
		return nil, NewError("mapping_anchor_invalid", "the RSS pre-acquisition mapping anchor is invalid", ErrInvalidInput, nil)
	}
	sources, err := queries.ListRSSPreacquisitionMappingSources(ctx, repository.UUIDToPG(scopeID))
	if err != nil {
		return nil, fmt.Errorf("list RSS pre-acquisition mapping sources: %w", err)
	}
	anchorOwned := false
	for _, source := range sources {
		coordinate := domain.EpisodeCoordinate{Season: int(source.SourceSeason), Episode: int(source.SourceEpisode)}
		if coordinate == anchorSource {
			anchorOwned = true
		}
	}
	if !anchorOwned {
		return nil, NewError("agent_tool_scope_violation", "the source anchor is outside the RSS mapping scope", ErrStateConflict, nil)
	}
	seasons, targetIDs, err := loadSeriesMappingCatalog(ctx, queries, seriesID)
	if err != nil {
		return nil, err
	}
	rows, err := mappingProfileRowsFromAnchor(seasons, targetIDs, anchorSource, anchorTarget)
	if err != nil {
		return nil, err
	}
	mapped := make(map[domain.EpisodeCoordinate]struct{}, len(rows))
	for _, row := range rows {
		mapped[domain.EpisodeCoordinate{Season: row.SourceSeason, Episode: row.SourceEpisode}] = struct{}{}
	}
	for _, source := range sources {
		coordinate := domain.EpisodeCoordinate{Season: int(source.SourceSeason), Episode: int(source.SourceEpisode)}
		if _, ok := mapped[coordinate]; !ok {
			return nil, NewError("mapping_incomplete", "the Agent anchor does not cover every scoped RSS coordinate", ErrStateConflict, map[string]any{
				"sourceSeason": coordinate.Season, "sourceEpisode": coordinate.Episode,
			})
		}
	}
	return rows, nil
}

func (workflow *RSSWorkflow) ApplyAgentRSSPreacquisitionMapping(
	ctx context.Context,
	resolution domain.AgentResolution,
	proposal domain.AgentRSSPreacquisitionMappingProposal,
	validation domain.AgentProposalValidation,
) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		lockedResolution, err := scope.Queries.LockAgentResolution(ctx, repository.UUIDToPG(resolution.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock RSS pre-acquisition Agent resolution: %w", err)
		}
		if lockedResolution.Status == string(domain.AgentResolutionApplied) {
			return nil
		}
		if lockedResolution.Capability != string(domain.AgentCapabilityRSSPreacquisitionMapping) ||
			lockedResolution.ResourceType != "rss_preacquisition_mapping_scope" ||
			repository.UUIDFromPG(lockedResolution.ResourceID) != proposal.ScopeID ||
			lockedResolution.Version != int32(resolution.Version) ||
			(lockedResolution.Status != string(domain.AgentResolutionProposed) && lockedResolution.Status != string(domain.AgentResolutionReviewRequired)) {
			return NewError("agent_resolution_stale", "the RSS pre-acquisition Agent resolution is no longer current", ErrStateConflict, nil)
		}
		setting, err := scope.Queries.GetAppSetting(ctx, domain.RuntimeSettingsName)
		if err != nil {
			return fmt.Errorf("load current Agent settings: %w", err)
		}
		var runtimeSettings domain.RuntimeSettings
		runtimeSettings.Agent = domain.DefaultAgentSettings()
		if err := json.Unmarshal(setting.Value, &runtimeSettings); err != nil {
			return fmt.Errorf("decode current Agent settings: %w", err)
		}
		agentSettings := runtimeSettings.Agent.WithDefaults()
		if setting.Version != lockedResolution.ConfigurationVersion || !agentSettings.Enabled || !agentSettings.EpisodeMappingEnabled ||
			agentSettings.Model != lockedResolution.Model || agentSettings.Protocol != lockedResolution.Protocol ||
			providerOrigin(agentSettings.BaseURL) != lockedResolution.ProviderOrigin {
			return NewError("agent_resolution_stale", "the Agent configuration changed after this proposal was created", ErrStateConflict, nil)
		}

		mappingContext, err := scope.Queries.LockRSSPreacquisitionMappingContext(ctx, repository.UUIDToPG(proposal.ScopeID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock RSS pre-acquisition mapping scope: %w", err)
		}
		if mappingContext.ScopeStatus != "pending" || !mappingContext.Enabled || !mappingContext.AutoEpisodeMapping ||
			mappingContext.MappingProfileID.Valid || mappingContext.DeletedAt.Valid || mappingContext.CompletedAt.Valid ||
			mappingContext.SubscriptionVersion != mappingContext.CurrentSubscriptionVersion {
			return NewError("agent_resolution_stale", "the RSS pre-acquisition mapping scope changed before apply", ErrStateConflict, nil)
		}
		if _, err := scope.Queries.LockMediaSeries(ctx, mappingContext.SeriesID); err != nil {
			return fmt.Errorf("lock RSS pre-acquisition mapping series: %w", err)
		}
		anchorSource := domain.EpisodeCoordinate{Season: proposal.SourceSeason, Episode: proposal.SourceEpisode}
		anchorTarget := domain.EpisodeCoordinate{Season: proposal.TargetSeason, Episode: proposal.TargetEpisode}
		rows, err := buildRSSPreacquisitionMappingRows(
			ctx, scope.Queries, proposal.ScopeID, repository.UUIDFromPG(mappingContext.SeriesID), anchorSource, anchorTarget,
		)
		if err != nil {
			return err
		}
		anchorTargetID := uuid.Nil
		anchorAbsolute := 0
		for _, row := range rows {
			if row.SourceSeason == anchorSource.Season && row.SourceEpisode == anchorSource.Episode {
				anchorTargetID = row.TargetEpisodeID
				anchorAbsolute = row.AbsoluteEpisode
				break
			}
		}
		if anchorTargetID == uuid.Nil || anchorAbsolute <= 0 {
			return NewError("mapping_anchor_invalid", "the Agent anchor target is unavailable", ErrStateConflict, nil)
		}
		name := mappingProfileName(uuid.Nil, repository.UUIDFromPG(mappingContext.SubscriptionID))
		version, err := scope.Queries.NextMappingProfileVersion(ctx, db.NextMappingProfileVersionParams{
			SeriesID: mappingContext.SeriesID, Name: name,
		})
		if err != nil {
			return fmt.Errorf("select RSS Agent mapping profile version: %w", err)
		}
		if _, err := scope.Queries.DeactivateMappingProfiles(ctx, db.DeactivateMappingProfilesParams{
			SeriesID: mappingContext.SeriesID, Name: name,
		}); err != nil {
			return fmt.Errorf("deactivate RSS Agent mapping profiles: %w", err)
		}
		profileID := uuid.New()
		if _, err := scope.Queries.CreateMappingProfile(ctx, db.CreateMappingProfileParams{
			ID: repository.UUIDToPG(profileID), SeriesID: mappingContext.SeriesID, Name: name, Version: version,
			AnchorSourceSeason: mappingInt32(anchorSource.Season), AnchorSourceEpisode: mappingInt32(anchorSource.Episode),
			AnchorTargetEpisodeID: repository.UUIDToPG(anchorTargetID), TargetEpisodeOffset: mappingInt32(anchorAbsolute - anchorSource.Episode),
			DecisionSource: string(domain.DecisionSourceAgentAuto), AgentResolutionID: repository.UUIDToPG(resolution.ID),
		}); err != nil {
			return fmt.Errorf("create RSS Agent mapping profile: %w", err)
		}
		for _, row := range rows {
			absoluteEpisode := int32(row.AbsoluteEpisode)
			if _, err := scope.Queries.CreateEpisodeMapping(ctx, db.CreateEpisodeMappingParams{
				ID: repository.UUIDToPG(uuid.New()), ProfileID: repository.UUIDToPG(profileID),
				SourceSeason: int32(row.SourceSeason), SourceEpisode: int32(row.SourceEpisode), AbsoluteEpisode: &absoluteEpisode,
				TargetEpisodeID: repository.UUIDToPG(row.TargetEpisodeID), MappingStatus: string(row.Status), MatchSource: string(row.MatchSource),
			}); err != nil {
				return fmt.Errorf("create RSS Agent episode mapping: %w", err)
			}
		}
		newVersion, err := scope.Queries.ApplyRSSPreacquisitionMappingProfile(ctx, db.ApplyRSSPreacquisitionMappingProfileParams{
			MappingProfileID: repository.UUIDToPG(profileID), ScopeID: repository.UUIDToPG(proposal.ScopeID),
			ExpectedVersion: mappingContext.CurrentSubscriptionVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return NewError("agent_resolution_stale", "the RSS subscription changed before Agent mapping apply", ErrStateConflict, nil)
		}
		if err != nil {
			return fmt.Errorf("apply RSS Agent mapping profile: %w", err)
		}
		if _, err := scope.Queries.CompleteRSSPreacquisitionMappingScope(ctx, db.CompleteRSSPreacquisitionMappingScopeParams{
			AppliedProfileID: repository.UUIDToPG(profileID), ID: repository.UUIDToPG(proposal.ScopeID),
		}); err != nil {
			return fmt.Errorf("complete RSS pre-acquisition mapping scope: %w", err)
		}
		if _, err := scope.Queries.ExpireOtherRSSPreacquisitionMappingScopes(ctx, db.ExpireOtherRSSPreacquisitionMappingScopesParams{
			SubscriptionID: mappingContext.SubscriptionID, AppliedScopeID: repository.UUIDToPG(proposal.ScopeID),
		}); err != nil {
			return fmt.Errorf("expire alternate RSS pre-acquisition mapping scopes: %w", err)
		}
		if workflow.operations == nil {
			return fmt.Errorf("schedule Agent-mapped RSS poll: operation scheduler is unavailable")
		}
		if err := workflow.scheduleContinuousPoll(ctx, scope, domain.RSSSubscription{
			ID: repository.UUIDFromPG(mappingContext.SubscriptionID), Version: newVersion,
		}); err != nil {
			return err
		}
		validationJSON, err := json.Marshal(validation)
		if err != nil {
			return fmt.Errorf("encode RSS Agent mapping validation: %w", err)
		}
		if _, err := scope.Queries.CompleteAgentResolution(ctx, db.CompleteAgentResolutionParams{
			Status: string(domain.AgentResolutionApplied), Validation: validationJSON, ID: repository.UUIDToPG(resolution.ID),
		}); err != nil {
			return fmt.Errorf("complete RSS pre-acquisition Agent resolution: %w", err)
		}
		return appendRSSEvent(ctx, scope.Queries, repository.UUIDFromPG(mappingContext.SubscriptionID), resolution.OperationID, "rss.mapping_profile_applied", mustJSON(map[string]any{
			"profileId": profileID, "version": version, "decisionSource": domain.DecisionSourceAgentAuto,
			"agentResolutionId": resolution.ID, "mappingScopeId": proposal.ScopeID,
			"mapped": len(rows), "subscriptionVersion": newVersion,
		}))
	})
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
