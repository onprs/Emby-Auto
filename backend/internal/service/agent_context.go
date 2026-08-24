package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/agentharness"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type agentContextSnapshot struct {
	ResourceType           string
	ResourceVersion        *int
	Resource               json.RawMessage
	Fingerprint            [32]byte
	Tools                  []agentharness.Tool
	AllowedCatalogQueries  map[string]map[int64]struct{}
	Files                  map[uuid.UUID]scopedFile
	DefaultSourceSeason    int
	DefaultSourceEpisode   *int
	RSSAdjudicationEntries map[uuid.UUID]scopedRSSReleaseEntry
	RSSAdjudicationHistory map[uuid.UUID]scopedRSSReleaseHistory
	RSSMappingSources      map[domain.EpisodeCoordinate]struct{}
	RSSMappingTargets      map[domain.EpisodeCoordinate]struct{}
	SubtitleVideoMatch     *scopedSubtitleVideoMatch
}

type scopedSubtitleVideoMatch struct {
	TaskID          uuid.UUID                 `json:"taskId"`
	SeriesTitle     string                    `json:"seriesTitle"`
	TargetSeason    int                       `json:"targetSeason"`
	TargetEpisode   int                       `json:"targetEpisode"`
	TargetTitle     string                    `json:"targetTitle"`
	VideoPath       string                    `json:"videoPath"`
	Candidates      []scopedSubtitleCandidate `json:"candidates"`
}

type scopedSubtitleCandidate struct {
	ID          string `json:"candidateId"`
	Source      string `json:"source"`
	StreamIndex int    `json:"streamIndex,omitempty"`
	Format      string `json:"format,omitempty"`
	Language    string `json:"language,omitempty"`
	Title       string `json:"title,omitempty"`
	Path        string `json:"path,omitempty"`
}

type scopedRSSReleaseEntry struct {
	EntryID          uuid.UUID                 `json:"entryId"`
	Title            string                    `json:"title"`
	PublishedAt      *string                   `json:"publishedAt,omitempty"`
	Status           string                    `json:"status"`
	SourceSeason     *int                      `json:"sourceSeason,omitempty"`
	SourceEpisode    *int                      `json:"sourceEpisode,omitempty"`
	Deterministic    domain.RSSReleaseAnalysis `json:"deterministicAnalysis"`
	RejectionReasons []string                  `json:"rejectionReasons"`
}

type scopedRSSReleaseHistory struct {
	EntryID           uuid.UUID `json:"entryId"`
	Title             string    `json:"title"`
	PublishedAt       *string   `json:"publishedAt,omitempty"`
	WorkflowStatus    string    `json:"workflowStatus"`
	AdjudicationState string    `json:"adjudicationState"`
	SourceSeason      *int      `json:"sourceSeason,omitempty"`
	SourceEpisode     *int      `json:"sourceEpisode,omitempty"`
	Imported          bool      `json:"imported"`
}

type scopedRSSMappingSource struct {
	EntryID       uuid.UUID `json:"entryId"`
	Title         string    `json:"title"`
	SourceSeason  int       `json:"sourceSeason"`
	SourceEpisode int       `json:"sourceEpisode"`
}

type scopedFile struct {
	ID            uuid.UUID `json:"fileId"`
	RelativePath  string    `json:"relativePath"`
	SizeBytes     int64     `json:"sizeBytes,omitempty"`
	MediaKind     string    `json:"mediaKind,omitempty"`
	Selected      bool      `json:"selected,omitempty"`
	SourceSeason  *int      `json:"sourceSeason,omitempty"`
	SourceEpisode *int      `json:"sourceEpisode,omitempty"`
	Language      string    `json:"language,omitempty"`
}

type scopedEpisode struct {
	ID      uuid.UUID `json:"episodeId"`
	Season  int       `json:"season"`
	Episode int       `json:"episode"`
	Title   string    `json:"title"`
}

func (service *AgentResolutionService) buildAgentContext(
	ctx context.Context,
	capability domain.AgentCapability,
	resourceID uuid.UUID,
) (agentContextSnapshot, error) {
	switch capability {
	case domain.AgentCapabilityRSSCoordinate:
		return service.buildRSSAgentContext(ctx, resourceID)
	case domain.AgentCapabilityRSSReleaseAdjudication:
		return service.buildRSSAdjudicationContext(ctx, resourceID)
	case domain.AgentCapabilityRSSPreacquisitionMapping:
		return service.buildRSSPreacquisitionMappingAgentContext(ctx, resourceID)
	case domain.AgentCapabilityDownloadFileResolution:
		return service.buildDownloadAgentContext(ctx, resourceID)
	case domain.AgentCapabilityCatalogCandidate:
		return service.buildCatalogAgentContext(ctx, resourceID)
	case domain.AgentCapabilityEpisodeMapping:
		return service.buildMappingAgentContext(ctx, resourceID)
	case domain.AgentCapabilitySubtitleVideoMatch:
		return service.buildSubtitleVideoMatchAgentContext(ctx, resourceID)
	default:
		return agentContextSnapshot{}, invalidAgentResolution("capability", "is unsupported")
	}
}

func (service *AgentResolutionService) buildRSSAgentContext(ctx context.Context, entryID uuid.UUID) (agentContextSnapshot, error) {
	row, err := service.queries.GetAgentRSSContext(ctx, repository.UUIDToPG(entryID))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentContextSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("load Agent RSS context: %w", err)
	}
	softReason := false
	for _, reason := range row.RejectionReasons {
		switch reason {
		case "episode_not_detected", "episode_ambiguous":
			softReason = true
		default:
			return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "RSS hard rejection cannot be escalated to Agent", ErrStateConflict, map[string]any{"reasonCode": reason})
		}
	}
	if !softReason || !row.HasDownloadUri {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "the RSS entry has no Agent-resolvable coordinate ambiguity", ErrStateConflict, nil)
	}
	neighbors, err := service.queries.ListAgentRSSNeighbors(ctx, db.ListAgentRSSNeighborsParams{
		SubscriptionID: row.SubscriptionID, EntryID: row.ID, DiscoveredAt: row.DiscoveredAt,
	})
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("list Agent RSS neighbors: %w", err)
	}
	type neighbor struct {
		EntryID uuid.UUID `json:"entryId"`
		Title   string    `json:"title"`
		Season  int       `json:"sourceSeason"`
		Episode int       `json:"sourceEpisode"`
	}
	neighborValues := make([]neighbor, 0, len(neighbors))
	for _, item := range neighbors {
		if item.SourceSeason == nil || item.SourceEpisode == nil {
			continue
		}
		neighborValues = append(neighborValues, neighbor{
			EntryID: repository.UUIDFromPG(item.ID), Title: item.Title, Season: int(*item.SourceSeason), Episode: int(*item.SourceEpisode),
		})
	}
	resource := struct {
		EntryID             uuid.UUID `json:"entryId"`
		Title               string    `json:"title"`
		DefaultSourceSeason int       `json:"defaultSourceSeason"`
		ReasonCodes         []string  `json:"reasonCodes"`
		NeighborCount       int       `json:"neighborCount"`
	}{entryID, row.Title, int(row.DefaultSourceSeason), row.RejectionReasons, len(neighborValues)}
	resourceJSON, _ := json.Marshal(resource)
	fingerprintInput, _ := json.Marshal(struct {
		Resource  any        `json:"resource"`
		Neighbors []neighbor `json:"neighbors"`
	}{resource, neighborValues})
	analysis := domain.AnalyzeRSSRelease(row.Title, "magnet:?xt=urn:btih:"+strings.Repeat("0", 40), int(row.DefaultSourceSeason), row.IncludeKeywords, row.ExcludeKeywords)
	tools := []agentharness.Tool{
		staticAgentTool("analyze_release_title", "Return the deterministic parser diagnostics for this scoped title.", map[string]any{}, analysis),
		staticAgentTool("list_neighbor_release_evidence", "Return bounded successfully parsed neighboring RSS titles.", map[string]any{}, neighborValues),
	}
	return snapshot("rss_entry", nil, resourceJSON, fingerprintInput, tools), nil
}

func (service *AgentResolutionService) buildRSSAdjudicationContext(ctx context.Context, batchID uuid.UUID) (agentContextSnapshot, error) {
	batch, err := service.queries.GetAgentRSSAdjudicationBatch(ctx, repository.UUIDToPG(batchID))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentContextSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("load Agent RSS adjudication batch: %w", err)
	}
	if batch.Status != "pending" || !batch.SubscriptionEnabled || batch.SubscriptionCompletedAt.Valid || batch.SubscriptionDeletedAt.Valid {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "the RSS adjudication batch is not pending on an active subscription", ErrStateConflict, nil)
	}
	entryRows, err := service.queries.ListAgentRSSAdjudicationEntries(ctx, repository.UUIDToPG(batchID))
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("list Agent RSS adjudication entries: %w", err)
	}
	if len(entryRows) == 0 || len(entryRows) != int(batch.EntryCount) {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "the RSS adjudication batch is empty or incomplete", ErrStateConflict, nil)
	}
	historyRows, err := service.queries.ListAgentRSSAdjudicationHistory(ctx, db.ListAgentRSSAdjudicationHistoryParams{
		SubscriptionID: batch.SubscriptionID, BatchID: repository.UUIDToPG(batchID), DiscoveredBefore: batch.CreatedAt,
	})
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("list Agent RSS adjudication history: %w", err)
	}
	entries := make([]scopedRSSReleaseEntry, 0, len(entryRows))
	entriesByID := make(map[uuid.UUID]scopedRSSReleaseEntry, len(entryRows))
	for _, row := range entryRows {
		entryID := repository.UUIDFromPG(row.ID)
		publishedAt := pgTimeRFC3339(row.PublishedAt)
		analysis := domain.AnalyzeRSSRelease(
			row.Title, "magnet:?xt=urn:btih:"+strings.Repeat("0", 40), int(batch.DefaultSourceSeason), batch.IncludeKeywords, batch.ExcludeKeywords,
		)
		value := scopedRSSReleaseEntry{
			EntryID: entryID, Title: boundedAgentText(row.Title, 2048), PublishedAt: publishedAt, Status: row.Status,
			SourceSeason: int32PointerToInt(row.SourceSeason), SourceEpisode: int32PointerToInt(row.SourceEpisode),
			Deterministic: analysis, RejectionReasons: row.RejectionReasons,
		}
		entries = append(entries, value)
		entriesByID[entryID] = value
	}
	history := make([]scopedRSSReleaseHistory, 0, len(historyRows))
	historyByID := make(map[uuid.UUID]scopedRSSReleaseHistory, len(historyRows))
	for _, row := range historyRows {
		entryID := repository.UUIDFromPG(row.ID)
		value := scopedRSSReleaseHistory{
			EntryID: entryID, Title: boundedAgentText(row.Title, 2048), PublishedAt: pgTimeRFC3339(row.PublishedAt), WorkflowStatus: row.Status,
			AdjudicationState: row.AdjudicationState, SourceSeason: int32PointerToInt(row.SourceSeason),
			SourceEpisode: int32PointerToInt(row.SourceEpisode), Imported: row.ImportedAt.Valid,
		}
		history = append(history, value)
		historyByID[entryID] = value
	}
	resource := struct {
		BatchID             uuid.UUID `json:"batchId"`
		DefaultSourceSeason int       `json:"defaultSourceSeason"`
		EntryCount          int       `json:"entryCount"`
		HistoryCount        int       `json:"historyCount"`
	}{batchID, int(batch.DefaultSourceSeason), len(entries), len(history)}
	resourceJSON, _ := json.Marshal(resource)
	fingerprintInput, _ := json.Marshal(struct {
		Resource any                       `json:"resource"`
		Entries  []scopedRSSReleaseEntry   `json:"entries"`
		History  []scopedRSSReleaseHistory `json:"history"`
	}{resource, entries, history})
	subscriptionContext := map[string]any{
		"defaultSourceSeason": int(batch.DefaultSourceSeason),
		"includeKeywords":     batch.IncludeKeywords,
		"excludeKeywords":     batch.ExcludeKeywords,
	}
	tools := []agentharness.Tool{
		staticAgentTool("get_subscription_context", "Return the scoped RSS subscription policy.", map[string]any{}, subscriptionContext),
		staticAgentTool("list_new_release_entries", "Return every untrusted release title in this adjudication batch.", map[string]any{}, entries),
		staticAgentTool("list_release_history", "Return bounded release history discovered before this batch.", map[string]any{}, history),
		rssReleaseAnalysisTool(entriesByID),
	}
	result := snapshot("rss_adjudication_batch", nil, resourceJSON, fingerprintInput, tools)
	result.RSSAdjudicationEntries = entriesByID
	result.RSSAdjudicationHistory = historyByID
	return result, nil
}

func (service *AgentResolutionService) buildDownloadAgentContext(ctx context.Context, downloadID uuid.UUID) (agentContextSnapshot, error) {
	row, err := service.queries.GetAgentDownloadContext(ctx, repository.UUIDToPG(downloadID))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentContextSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("load Agent download context: %w", err)
	}
	if row.Status != string(domain.DownloadFileResolutionPending) && (row.Status != string(domain.DownloadFailed) || stringValue(row.FailureStage) != "file_resolution" || row.FileResolutionSource != nil) {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "the download is not waiting for file resolution", ErrStateConflict, nil)
	}
	if row.FileResolutionSource != nil {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "the download already has a validated file resolution", ErrStateConflict, nil)
	}
	files, err := service.queries.ListAgentDownloadFiles(ctx, row.ID)
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("list Agent download files: %w", err)
	}
	values := make([]scopedFile, 0, len(files))
	for _, file := range files {
		values = append(values, scopedFile{
			ID: repository.UUIDFromPG(file.ID), RelativePath: file.RelativePath, SizeBytes: file.SizeBytes,
			MediaKind: file.MediaKind, Selected: file.Selected, SourceSeason: int32PointerToInt(file.SourceSeason),
			SourceEpisode: int32PointerToInt(file.SourceEpisode), Language: stringValue(file.Language),
		})
	}
	resource := struct {
		DownloadID           uuid.UUID `json:"downloadId"`
		DefaultSourceSeason  int       `json:"defaultSourceSeason"`
		DefaultSourceEpisode *int      `json:"defaultSourceEpisode,omitempty"`
		FileCount            int       `json:"fileCount"`
	}{downloadID, int(row.DefaultSourceSeason), int32PointerToInt(row.DefaultSourceEpisode), len(values)}
	resourceJSON, _ := json.Marshal(resource)
	fingerprintInput, _ := json.Marshal(struct {
		Resource any          `json:"resource"`
		Files    []scopedFile `json:"files"`
		Version  int32        `json:"version"`
	}{resource, values, row.Version})
	filesByID := make(map[uuid.UUID]scopedFile, len(values))
	for _, file := range values {
		filesByID[file.ID] = file
	}
	tools := []agentharness.Tool{
		staticAgentTool("analyze_download_manifest", "Return the safe scoped download manifest.", map[string]any{}, values),
		parseFileCoordinateTool(filesByID, int(row.DefaultSourceSeason)),
	}
	version := int(row.Version)
	result := snapshot("download", &version, resourceJSON, fingerprintInput, tools)
	result.Files = filesByID
	result.DefaultSourceSeason = int(row.DefaultSourceSeason)
	result.DefaultSourceEpisode = int32PointerToInt(row.DefaultSourceEpisode)
	return result, nil
}

func (service *AgentResolutionService) buildRSSPreacquisitionMappingAgentContext(ctx context.Context, scopeID uuid.UUID) (agentContextSnapshot, error) {
	row, err := service.queries.GetAgentRSSPreacquisitionMappingContext(ctx, repository.UUIDToPG(scopeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentContextSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("load Agent RSS pre-acquisition Mapping context: %w", err)
	}
	if row.Status != "pending" || !row.SubscriptionEnabled || !row.AutoEpisodeMapping || row.MappingProfileID.Valid ||
		row.SubscriptionDeletedAt.Valid || row.SubscriptionCompletedAt.Valid {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "the RSS pre-acquisition Mapping scope is not active", ErrStateConflict, nil)
	}
	sourceRows, err := service.queries.ListAgentRSSPreacquisitionMappingSources(ctx, repository.UUIDToPG(scopeID))
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("list Agent RSS pre-acquisition Mapping sources: %w", err)
	}
	episodeRows, err := service.queries.ListAgentRSSPreacquisitionTMDbEpisodes(ctx, repository.UUIDToPG(scopeID))
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("list Agent RSS pre-acquisition TMDb episodes: %w", err)
	}
	if len(sourceRows) == 0 {
		return agentContextSnapshot{}, NewError("rss_mapping_scope_empty", "the RSS pre-acquisition Mapping scope has no source coordinates", ErrStateConflict, nil)
	}
	if len(episodeRows) == 0 {
		return agentContextSnapshot{}, NewError("tmdb_catalog_missing", "the TMDb series catalog has not been synchronized", ErrStateConflict, nil)
	}
	sources := make([]scopedRSSMappingSource, 0, len(sourceRows))
	sourceCoordinates := make(map[domain.EpisodeCoordinate]struct{}, len(sourceRows))
	for _, source := range sourceRows {
		coordinate := domain.EpisodeCoordinate{Season: int(source.SourceSeason), Episode: int(source.SourceEpisode)}
		if coordinate.Season <= 0 || coordinate.Episode <= 0 {
			return agentContextSnapshot{}, NewError("mapping_source_invalid", "the RSS Mapping scope contains an invalid source coordinate", ErrStateConflict, nil)
		}
		sources = append(sources, scopedRSSMappingSource{
			EntryID: repository.UUIDFromPG(source.EntryID), Title: boundedAgentText(source.Title, 2048),
			SourceSeason: coordinate.Season, SourceEpisode: coordinate.Episode,
		})
		sourceCoordinates[coordinate] = struct{}{}
	}
	episodes := make([]scopedEpisode, 0, len(episodeRows))
	targetCoordinates := make(map[domain.EpisodeCoordinate]struct{}, len(episodeRows))
	for _, episode := range episodeRows {
		coordinate := domain.EpisodeCoordinate{Season: int(episode.SeasonNumber), Episode: int(episode.EpisodeNumber)}
		episodes = append(episodes, scopedEpisode{
			ID: repository.UUIDFromPG(episode.ID), Season: coordinate.Season, Episode: coordinate.Episode, Title: boundedAgentText(episode.Title, 2048),
		})
		targetCoordinates[coordinate] = struct{}{}
	}
	resource := struct {
		ScopeID      uuid.UUID `json:"scopeId"`
		SeriesTitle  string    `json:"seriesTitle"`
		SourceCount  int       `json:"sourceCount"`
		EpisodeCount int       `json:"regularEpisodeCount"`
	}{scopeID, row.SeriesTitle, len(sources), len(episodes)}
	resourceJSON, _ := json.Marshal(resource)
	fingerprintInput, _ := json.Marshal(struct {
		Resource    any                      `json:"resource"`
		Sources     []scopedRSSMappingSource `json:"sources"`
		Episodes    []scopedEpisode          `json:"episodes"`
		Fingerprint []byte                   `json:"scopeFingerprint"`
	}{resource, sources, episodes, row.SourceFingerprint})
	tools := []agentharness.Tool{
		staticAgentTool("list_rss_mapping_sources", "Return scoped RSS release titles and their backend-resolved source coordinates.", map[string]any{}, sources),
		staticAgentTool("list_tmdb_regular_episodes", "Return synchronized regular TMDb episodes for this RSS subscription.", map[string]any{}, episodes),
		service.previewRSSPreacquisitionMappingTool(scopeID, sourceCoordinates),
	}
	version := int(row.SubscriptionVersion)
	result := snapshot("rss_preacquisition_mapping_scope", &version, resourceJSON, fingerprintInput, tools)
	result.RSSMappingSources = sourceCoordinates
	result.RSSMappingTargets = targetCoordinates
	return result, nil
}

func (service *AgentResolutionService) buildMappingAgentContext(ctx context.Context, acquisitionID uuid.UUID) (agentContextSnapshot, error) {
	row, err := service.queries.GetAgentMappingContext(ctx, repository.UUIDToPG(acquisitionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentContextSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("load Agent Mapping context: %w", err)
	}
	if row.MappingProfileID.Valid {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "the acquisition already has an episode Mapping profile", ErrStateConflict, nil)
	}
	if row.TmdbSeriesID == nil {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "the acquisition has no TMDb series", ErrStateConflict, nil)
	}
	fileRows, err := service.queries.ListAgentMappingFiles(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("list Agent Mapping files: %w", err)
	}
	episodeRows, err := service.queries.ListAgentTMDbEpisodes(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("list Agent Mapping episodes: %w", err)
	}
	if len(fileRows) == 0 || len(episodeRows) == 0 {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "selected files and synchronized TMDb episodes are required", ErrStateConflict, nil)
	}
	files := make([]scopedFile, 0, len(fileRows))
	filesByID := make(map[uuid.UUID]scopedFile, len(fileRows))
	seen := map[[2]int]struct{}{}
	for _, file := range fileRows {
		value := scopedFile{
			ID: repository.UUIDFromPG(file.ID), RelativePath: file.RelativePath,
			SourceSeason: int32PointerToInt(file.SourceSeason), SourceEpisode: int32PointerToInt(file.SourceEpisode),
		}
		if season, episode, ok := domain.ParseSourceCoordinate(value.RelativePath, pointerIntValue(value.SourceSeason)); ok {
			value.SourceSeason, value.SourceEpisode = &season, &episode
		}
		if value.SourceSeason == nil || value.SourceEpisode == nil || *value.SourceSeason <= 0 || *value.SourceEpisode <= 0 {
			return agentContextSnapshot{}, NewError("mapping_source_invalid", "every selected video needs a valid source coordinate before Agent Mapping", ErrStateConflict, map[string]any{"fileId": value.ID})
		}
		coordinate := [2]int{*value.SourceSeason, *value.SourceEpisode}
		if _, duplicate := seen[coordinate]; duplicate {
			return agentContextSnapshot{}, NewError("mapping_source_duplicate", "selected videos have duplicate source coordinates", ErrStateConflict, nil)
		}
		seen[coordinate] = struct{}{}
		files = append(files, value)
		filesByID[value.ID] = value
	}
	episodes := make([]scopedEpisode, 0, len(episodeRows))
	for _, episode := range episodeRows {
		episodes = append(episodes, scopedEpisode{
			ID: repository.UUIDFromPG(episode.ID), Season: int(episode.SeasonNumber), Episode: int(episode.EpisodeNumber), Title: episode.Title,
		})
	}
	resource := struct {
		AcquisitionID uuid.UUID `json:"acquisitionId"`
		SeriesTitle   string    `json:"seriesTitle"`
		FileCount     int       `json:"fileCount"`
		EpisodeCount  int       `json:"regularEpisodeCount"`
	}{acquisitionID, row.Title, len(files), len(episodes)}
	resourceJSON, _ := json.Marshal(resource)
	fingerprintInput, _ := json.Marshal(struct {
		Resource any             `json:"resource"`
		Files    []scopedFile    `json:"files"`
		Episodes []scopedEpisode `json:"episodes"`
		Updated  any             `json:"updatedAt"`
	}{resource, files, episodes, row.UpdatedAt.Time.UTC()})
	tools := []agentharness.Tool{
		staticAgentTool("analyze_download_manifest", "Return selected video filenames and source coordinates in this acquisition.", map[string]any{}, files),
		staticAgentTool("list_tmdb_regular_episodes", "Return synchronized regular TMDb episodes for this acquisition.", map[string]any{}, episodes),
		parseFileCoordinateTool(filesByID, 1),
		service.previewMappingTool(acquisitionID, filesByID),
	}
	result := snapshot("acquisition", nil, resourceJSON, fingerprintInput, tools)
	result.Files = filesByID
	return result, nil
}

func (service *AgentResolutionService) buildCatalogAgentContext(ctx context.Context, resourceID uuid.UUID) (agentContextSnapshot, error) {
	row, err := service.queries.GetAgentCatalogContext(ctx, repository.UUIDToPG(resourceID))
	if err == nil {
		resource := struct {
			AcquisitionID uuid.UUID `json:"acquisitionId"`
			SourceTitle   string    `json:"sourceTitle"`
			CurrentTitle  string    `json:"currentTitle"`
		}{resourceID, row.SourceTitle, row.CurrentTitle}
		resourceJSON, _ := json.Marshal(resource)
		fingerprintInput, _ := json.Marshal(struct {
			Resource any `json:"resource"`
			Updated  any `json:"updatedAt"`
		}{resource, row.UpdatedAt.Time.UTC()})
		return service.catalogSearchSnapshot("acquisition", resourceJSON, fingerprintInput), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return agentContextSnapshot{}, fmt.Errorf("load Agent catalog context: %w", err)
	}

	lookup, err := service.queries.GetAgentRSSFeedCatalogLookup(ctx, repository.UUIDToPG(resourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentContextSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("load Agent RSS feed catalog context: %w", err)
	}
	resource := struct {
		LookupID         uuid.UUID `json:"lookupId"`
		FeedTitle        string    `json:"feedTitle,omitempty"`
		SuggestedQueries []string  `json:"suggestedQueries"`
		SampleTitles     []string  `json:"sampleTitles"`
	}{resourceID, boundedAgentText(lookup.FeedTitle, 512), lookup.SuggestedQueries, lookup.SampleTitles}
	resourceJSON, _ := json.Marshal(resource)
	fingerprintInput, _ := json.Marshal(struct {
		Resource  any       `json:"resource"`
		ExpiresAt time.Time `json:"expiresAt"`
	}{resource, lookup.ExpiresAt.Time.UTC()})
	return service.catalogSearchSnapshot("rss_feed_lookup", resourceJSON, fingerprintInput), nil
}

func (service *AgentResolutionService) catalogSearchSnapshot(resourceType string, resourceJSON, fingerprintInput []byte) agentContextSnapshot {
	allowedQueries := map[string]map[int64]struct{}{}
	searchTool := agentharness.Tool{
		Definition: agentapi.ToolDefinition{
			Name: "search_tmdb_catalog", Description: "Search bounded TMDb series candidates using one title query.",
			Parameters: strictObject(map[string]any{"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 512}}, "query"),
		},
		Execute: func(callCtx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
			var arguments struct {
				Query string `json:"query"`
			}
			if err := strictJSON(raw, &arguments); err != nil || strings.TrimSpace(arguments.Query) == "" {
				return nil, fmt.Errorf("invalid TMDb search arguments")
			}
			if service.tmdbSearch == nil {
				return nil, fmt.Errorf("TMDb search is unavailable")
			}
			query := strings.TrimSpace(arguments.Query)
			results, err := service.tmdbSearch.SearchTV(callCtx, query)
			if err != nil {
				return nil, err
			}
			if len(results) > 20 {
				results = results[:20]
			}
			type candidate struct {
				ID           int64  `json:"id"`
				Name         string `json:"name"`
				OriginalName string `json:"originalName,omitempty"`
			}
			queryKey := strings.ToLower(query)
			queryIDs := allowedQueries[queryKey]
			if queryIDs == nil {
				queryIDs = map[int64]struct{}{}
				allowedQueries[queryKey] = queryIDs
			}
			values := make([]candidate, 0, len(results))
			for _, result := range results {
				queryIDs[result.TMDbSeriesID] = struct{}{}
				values = append(values, candidate{ID: result.TMDbSeriesID, Name: result.Name, OriginalName: result.OriginalName})
			}
			return json.Marshal(values)
		},
	}
	result := snapshot(resourceType, nil, resourceJSON, fingerprintInput, []agentharness.Tool{searchTool})
	result.AllowedCatalogQueries = allowedQueries
	return result
}

func (service *AgentResolutionService) previewRSSPreacquisitionMappingTool(
	scopeID uuid.UUID,
	sources map[domain.EpisodeCoordinate]struct{},
) agentharness.Tool {
	return agentharness.Tool{
		Definition: agentapi.ToolDefinition{
			Name: "preview_rss_episode_mapping", Description: "Preview the complete backend-authoritative RSS Mapping for one scoped source and target anchor.",
			Parameters: strictObject(map[string]any{
				"sourceSeason":  map[string]any{"type": "integer", "minimum": 1, "maximum": math.MaxInt32},
				"sourceEpisode": map[string]any{"type": "integer", "minimum": 1, "maximum": math.MaxInt32},
				"targetSeason":  map[string]any{"type": "integer", "minimum": 1, "maximum": math.MaxInt32},
				"targetEpisode": map[string]any{"type": "integer", "minimum": 1, "maximum": math.MaxInt32},
			}, "sourceSeason", "sourceEpisode", "targetSeason", "targetEpisode"),
		},
		Execute: func(callCtx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
			var arguments struct {
				SourceSeason  int `json:"sourceSeason"`
				SourceEpisode int `json:"sourceEpisode"`
				TargetSeason  int `json:"targetSeason"`
				TargetEpisode int `json:"targetEpisode"`
			}
			if err := strictJSON(raw, &arguments); err != nil {
				return nil, err
			}
			source := domain.EpisodeCoordinate{Season: arguments.SourceSeason, Episode: arguments.SourceEpisode}
			if _, ok := sources[source]; !ok {
				return nil, fmt.Errorf("source coordinate is outside this RSS Mapping scope")
			}
			if service.rssMapping == nil {
				return nil, fmt.Errorf("RSS pre-acquisition Mapping preview is unavailable")
			}
			rows, err := service.rssMapping.PreviewRSSPreacquisitionMapping(
				callCtx, scopeID, source,
				domain.EpisodeCoordinate{Season: arguments.TargetSeason, Episode: arguments.TargetEpisode},
			)
			if err != nil {
				return nil, err
			}
			type previewRow struct {
				SourceSeason  int    `json:"sourceSeason"`
				SourceEpisode int    `json:"sourceEpisode"`
				Status        string `json:"status"`
				TargetSeason  int    `json:"targetSeason,omitempty"`
				TargetEpisode int    `json:"targetEpisode,omitempty"`
				ErrorCode     string `json:"errorCode,omitempty"`
			}
			values := make([]previewRow, 0, len(rows))
			for _, row := range rows {
				values = append(values, previewRow{
					SourceSeason: row.SourceSeason, SourceEpisode: row.SourceEpisode, Status: string(row.Status),
					TargetSeason: row.TargetSeason, TargetEpisode: row.TargetEpisode, ErrorCode: row.ErrorCode,
				})
			}
			return json.Marshal(map[string]any{"rows": values})
		},
	}
}

func (service *AgentResolutionService) previewMappingTool(acquisitionID uuid.UUID, files map[uuid.UUID]scopedFile) agentharness.Tool {
	return agentharness.Tool{
		Definition: agentapi.ToolDefinition{
			Name: "preview_episode_mapping", Description: "Preview the full backend-authoritative Mapping for one scoped anchor.",
			Parameters: strictObject(map[string]any{
				"sourceFileId":  map[string]any{"type": "string", "format": "uuid"},
				"targetSeason":  map[string]any{"type": "integer", "minimum": 1, "maximum": math.MaxInt32},
				"targetEpisode": map[string]any{"type": "integer", "minimum": 1, "maximum": math.MaxInt32},
			}, "sourceFileId", "targetSeason", "targetEpisode"),
		},
		Execute: func(callCtx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
			var arguments struct {
				SourceFileID  uuid.UUID `json:"sourceFileId"`
				TargetSeason  int       `json:"targetSeason"`
				TargetEpisode int       `json:"targetEpisode"`
			}
			if err := strictJSON(raw, &arguments); err != nil {
				return nil, err
			}
			if _, ok := files[arguments.SourceFileID]; !ok {
				return nil, fmt.Errorf("source file is outside this acquisition")
			}
			if service.catalog == nil {
				return nil, fmt.Errorf("mapping preview is unavailable")
			}
			preview, err := service.catalog.PreviewEpisodeMapping(callCtx, domain.EpisodeMappingPlanInput{
				AcquisitionID: acquisitionID,
				Anchor: domain.EpisodeMappingAnchorInput{
					SourceFileID: arguments.SourceFileID,
					Target:       domain.EpisodeCoordinate{Season: arguments.TargetSeason, Episode: arguments.TargetEpisode},
				},
			})
			if err != nil {
				return nil, err
			}
			type row struct {
				SourceFileID  uuid.UUID `json:"sourceFileId"`
				Status        string    `json:"status"`
				TargetSeason  int       `json:"targetSeason,omitempty"`
				TargetEpisode int       `json:"targetEpisode,omitempty"`
				ErrorCode     string    `json:"errorCode,omitempty"`
			}
			rows := make([]row, 0, len(preview.Rows))
			for _, item := range preview.Rows {
				rows = append(rows, row{item.SourceFileID, string(item.Status), item.TargetSeason, item.TargetEpisode, item.ErrorCode})
			}
			return json.Marshal(map[string]any{"rows": rows})
		},
	}
}

func rssReleaseAnalysisTool(entries map[uuid.UUID]scopedRSSReleaseEntry) agentharness.Tool {
	return agentharness.Tool{
		Definition: agentapi.ToolDefinition{
			Name: "analyze_release_title", Description: "Return deterministic parser diagnostics for one scoped untrusted release title.",
			Parameters: strictObject(map[string]any{"entryId": map[string]any{"type": "string", "format": "uuid"}}, "entryId"),
		},
		Execute: func(_ context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
			var arguments struct {
				EntryID uuid.UUID `json:"entryId"`
			}
			if err := strictJSON(raw, &arguments); err != nil {
				return nil, err
			}
			entry, ok := entries[arguments.EntryID]
			if !ok {
				return nil, fmt.Errorf("RSS entry is outside this adjudication batch")
			}
			return json.Marshal(entry.Deterministic)
		},
	}
}

func boundedAgentText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func pgTimeRFC3339(value pgtype.Timestamptz) *string {
	if !value.Valid {
		return nil
	}
	formatted := value.Time.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func staticAgentTool(name, description string, properties map[string]any, value any) agentharness.Tool {
	encoded, _ := json.Marshal(value)
	return agentharness.Tool{
		Definition: agentapi.ToolDefinition{Name: name, Description: description, Parameters: strictObject(properties)},
		Execute: func(_ context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
			var arguments map[string]any
			if err := strictJSON(raw, &arguments); err != nil || len(arguments) != 0 {
				return nil, fmt.Errorf("tool takes no arguments")
			}
			return encoded, nil
		},
	}
}

func parseFileCoordinateTool(files map[uuid.UUID]scopedFile, defaultSeason int) agentharness.Tool {
	return agentharness.Tool{
		Definition: agentapi.ToolDefinition{
			Name: "parse_file_coordinate", Description: "Run the deterministic coordinate parser for one scoped file.",
			Parameters: strictObject(map[string]any{"fileId": map[string]any{"type": "string", "format": "uuid"}}, "fileId"),
		},
		Execute: func(_ context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
			var arguments struct {
				FileID uuid.UUID `json:"fileId"`
			}
			if err := strictJSON(raw, &arguments); err != nil {
				return nil, err
			}
			file, ok := files[arguments.FileID]
			if !ok {
				return nil, fmt.Errorf("file is outside this resolution scope")
			}
			season, episode, matched := domain.ParseSourceCoordinate(file.RelativePath, defaultSeason)
			return json.Marshal(map[string]any{"matched": matched, "sourceSeason": season, "sourceEpisode": episode})
		},
	}
}

func snapshot(resourceType string, resourceVersion *int, resource, fingerprintInput json.RawMessage, tools []agentharness.Tool) agentContextSnapshot {
	return agentContextSnapshot{
		ResourceType: resourceType, ResourceVersion: resourceVersion, Resource: resource,
		Fingerprint: sha256.Sum256(fingerprintInput), Tools: tools,
	}
}

func strictObject(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
}

func strictJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}

func int32PointerToInt(value *int32) *int {
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}

func pointerIntValue(value *int) int {
	if value == nil {
		return 1
	}
	return *value
}

func (service *AgentResolutionService) buildSubtitleVideoMatchAgentContext(ctx context.Context, scopeID uuid.UUID) (agentContextSnapshot, error) {
	row, err := service.queries.GetSubtitleVideoMatchContext(ctx, repository.UUIDToPG(scopeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentContextSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("load subtitle video match context: %w", err)
	}
	if row.ScopeStatus != "pending" {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "the subtitle video match scope is no longer pending", ErrStateConflict, nil)
	}
	candidateRows, err := service.queries.ListSubtitleVideoMatchCandidates(ctx, repository.UUIDToPG(scopeID))
	if err != nil {
		return agentContextSnapshot{}, fmt.Errorf("list subtitle video match candidates: %w", err)
	}
	if len(candidateRows) < 2 {
		return agentContextSnapshot{}, NewError("agent_resolution_not_allowed", "subtitle video match requires at least two candidates", ErrStateConflict, nil)
	}
	videoPath, err := securePreviewPath(row.SavePath, row.SourceVideoRelativePath)
	if err != nil {
		return agentContextSnapshot{}, NewError("subtitle_video_path_invalid", "the source video path is unsafe", ErrStateConflict, nil)
	}
	candidates := make([]scopedSubtitleCandidate, 0, len(candidateRows))
	for _, candidate := range candidateRows {
		candidates = append(candidates, scopedSubtitleCandidate{
			ID: candidate.CandidateID, Source: candidate.Source, StreamIndex: intFromInt32(candidate.StreamIndex),
			Format: stringValue(candidate.Format), Language: stringValue(candidate.Language),
			Title: stringValue(candidate.Title), Path: stringValue(candidate.Path),
		})
	}
	match := &scopedSubtitleVideoMatch{
		TaskID: repository.UUIDFromPG(row.TaskID), SeriesTitle: row.SeriesTitle,
		TargetSeason:  intFromInt32(row.TargetSeasonNumber),
		TargetEpisode: intFromInt32(row.TargetEpisodeNumber),
		TargetTitle: stringValue(row.TargetEpisodeTitle), VideoPath: videoPath, Candidates: candidates,
	}
	resourceJSON, _ := json.Marshal(struct {
		TaskID        uuid.UUID                   `json:"taskId"`
		SeriesTitle   string                      `json:"seriesTitle"`
		TargetSeason  int                         `json:"targetSeason"`
		TargetEpisode int                         `json:"targetEpisode"`
		TargetTitle   string                      `json:"targetTitle"`
		VideoPath     string                      `json:"videoPath"`
		Candidates    []scopedSubtitleCandidate   `json:"candidates"`
	}{match.TaskID, match.SeriesTitle, match.TargetSeason, match.TargetEpisode, match.TargetTitle, match.VideoPath, candidates})
	fingerprintInput, _ := json.Marshal(struct {
		Resource any `json:"resource"`
		Updated  any `json:"updatedAt"`
	}{resourceJSON, row.TaskVersion})
	tools := []agentharness.Tool{
		staticAgentTool("list_subtitle_candidates", "Return the bounded subtitle candidates in this scope with their metadata.", map[string]any{}, candidates),
		service.inspectSubtitleCandidateTool(match),
	}
	result := snapshot("episode_task", int32Pointer(row.TaskVersion), resourceJSON, fingerprintInput, tools)
	result.SubtitleVideoMatch = match
	return result, nil
}

func (service *AgentResolutionService) inspectSubtitleCandidateTool(match *scopedSubtitleVideoMatch) agentharness.Tool {
	return agentharness.Tool{
		Definition: agentapi.ToolDefinition{
			Name: "inspect_subtitle_candidate", Description: "Read a subtitle text sample from one scoped candidate to judge whether it matches the video episode.",
			Parameters: strictObject(map[string]any{"candidateId": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "candidateId"),
		},
		Execute: func(callCtx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
			var arguments struct {
				CandidateID string `json:"candidateId"`
			}
			if err := strictJSON(raw, &arguments); err != nil {
				return nil, err
			}
			if service.subtitleReader == nil {
				return nil, fmt.Errorf("subtitle inspection is unavailable")
			}
			var target *scopedSubtitleCandidate
			for index := range match.Candidates {
				if match.Candidates[index].ID == arguments.CandidateID {
					target = &match.Candidates[index]
					break
				}
			}
			if target == nil {
				return nil, fmt.Errorf("candidate is outside this resolution scope")
			}
			inspection, err := service.subtitleReader.InspectSubtitleText(callCtx, domain.SubtitleInspectionRequest{
				VideoPath: match.VideoPath, CandidateID: arguments.CandidateID,
				Source: domain.SubtitleSource(target.Source), StreamIndex: target.StreamIndex,
				Format: domain.SubtitleFormat(target.Format), Path: target.Path,
			})
			if err != nil {
				return nil, err
			}
			return json.Marshal(inspection)
		},
	}
}

func intFromInt32(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}

func int32Pointer(value int32) *int {
	converted := int(value)
	return &converted
}

func securePreviewPath(base *string, relative string) (string, error) {
	if base == nil || strings.TrimSpace(*base) == "" || strings.TrimSpace(relative) == "" {
		return "", fmt.Errorf("base path and relative path are required")
	}
	cleanBase := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(*base, `\`, "/")))
	cleanRel := filepath.Clean(filepath.ToSlash(strings.ReplaceAll(relative, `\`, "/")))
	if filepath.IsAbs(cleanRel) || strings.HasPrefix(cleanRel, "..") {
		return "", fmt.Errorf("relative path is unsafe")
	}
	return filepath.Join(cleanBase, cleanRel), nil
}
