//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type automaticEpisodeMappingReconciliationCatalogStub struct {
	targetID uuid.UUID
	visited  []uuid.UUID
}

func (*automaticEpisodeMappingReconciliationCatalogStub) AutomaticEpisodeMappingEnabled(context.Context, uuid.UUID) (bool, error) {
	return true, nil
}

func (stub *automaticEpisodeMappingReconciliationCatalogStub) TryDeterministicEpisodeMapping(_ context.Context, acquisitionID uuid.UUID) (bool, error) {
	stub.visited = append(stub.visited, acquisitionID)
	return acquisitionID == stub.targetID, nil
}

func (*automaticEpisodeMappingReconciliationCatalogStub) PreviewEpisodeMapping(context.Context, domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error) {
	return domain.EpisodeMappingPreview{}, nil
}

func (*automaticEpisodeMappingReconciliationCatalogStub) ApplyAgentEpisodeMapping(context.Context, domain.AgentResolution, domain.AgentEpisodeMappingProposal, domain.AgentProposalValidation) (domain.SavedEpisodeMapping, error) {
	return domain.SavedEpisodeMapping{}, nil
}

func TestAutomaticEpisodeMappingReconciliationIncludesSearchAcquisitionsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	catalog := NewCatalogWorkflow(queries, transactor, NewOperationScheduler(transactor, &integrationJobInserter{}))

	seriesID, seasonID, episodeID := uuid.New(), uuid.New(), uuid.New()
	searchID, candidateID, acquisitionID, downloadID, fileID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Search Mapping Fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 1)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, 1, 'Episode 1')`, episodeID, seasonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO search_runs (id, query, status) VALUES ($1, 'search mapping fixture', 'completed')`, searchID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO release_candidates (id, search_run_id, provider, identity_key, title) VALUES ($1, $2, 'fixture', $3, 'Search Mapping Fixture S01E01')`, candidateID, searchID, candidateID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, release_candidate_id) VALUES ($1, $2, 'search', $3)`, acquisitionID, seriesID, candidateID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status, progress, failure_stage, error_code) VALUES ($1, $2, 'failed', 1, 'materialize', 'mapping_profile_required')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Search Mapping Fixture S01E01.mkv', 1000, 'video', true, 1, 1)`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}

	resolutions := NewAgentResolutionService(
		queries, transactor, NewOperationScheduler(transactor, &integrationJobInserter{}),
		deterministicAgentConfigurationStub{configuration: domain.Configuration{}}, catalog, nil,
	)
	snapshot, err := resolutions.buildAgentContext(ctx, domain.AgentCapabilityEpisodeMapping, acquisitionID)
	if err != nil {
		t.Fatalf("buildAgentContext(search mapping) error = %v", err)
	}
	var resource struct {
		SourceTitle string `json:"sourceTitle"`
	}
	if err := json.Unmarshal(snapshot.Resource, &resource); err != nil || resource.SourceTitle != "Search Mapping Fixture S01E01" {
		t.Fatalf("search mapping Agent resource = %s, error = %v", snapshot.Resource, err)
	}
	reconciled, err := resolutions.ReconcileAutomaticEpisodeMappings(ctx)
	if err != nil || reconciled != 1 {
		t.Fatalf("ReconcileAutomaticEpisodeMappings() = %d, %v, want 1, nil", reconciled, err)
	}
	var decisionSource string
	if err := pool.QueryRow(ctx, `
SELECT profile.decision_source
FROM acquisitions AS acquisition
JOIN episode_mapping_profiles AS profile ON profile.id = acquisition.mapping_profile_id
WHERE acquisition.id = $1`, acquisitionID).Scan(&decisionSource); err != nil {
		t.Fatal(err)
	}
	var resolutionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_resolutions WHERE resource_id = $1`, acquisitionID).Scan(&resolutionCount); err != nil {
		t.Fatal(err)
	}
	if decisionSource != string(domain.DecisionSourceDeterministic) || resolutionCount != 0 {
		t.Fatalf("search mapping decision/resolutions = %q/%d", decisionSource, resolutionCount)
	}
}

func TestAutomaticEpisodeMappingReconciliationPagesPastHardTerminalCandidatesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})

	seriesID := uuid.MustParse("01000000-0000-4000-8000-000000000001")
	seasonID := uuid.MustParse("01000000-0000-4000-8000-000000000002")
	targetID := uuid.MustParse("f0000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, 900001, 'Paged Mapping Fixture')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 1)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, 1, 'Episode 1')`, uuid.New(), seasonID); err != nil {
		t.Fatal(err)
	}

	terminalIDs := make([]uuid.UUID, 0, 101)
	candidateIDs := make([]uuid.UUID, 0, 102)
	for index := 0; index < 101; index++ {
		acquisitionID := uuid.MustParse(fmt.Sprintf("10000000-0000-4000-8000-%012x", index+1))
		terminalIDs = append(terminalIDs, acquisitionID)
		candidateIDs = append(candidateIDs, acquisitionID)
	}
	candidateIDs = append(candidateIDs, targetID)
	batch := &pgx.Batch{}
	for index, acquisitionID := range candidateIDs {
		downloadID, fileID := uuid.New(), uuid.New()
		sourceURI := fmt.Sprintf("https://example.test/manual/paged-mapping/%03d.torrent", index)
		batch.Queue(`INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', $3)`, acquisitionID, seriesID, sourceURI)
		batch.Queue(`INSERT INTO downloads (id, acquisition_id, status, progress, failure_stage, error_code) VALUES ($1, $2, 'failed', 1, 'materialize', 'mapping_profile_required')`, downloadID, acquisitionID)
		batch.Queue(`
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, $3, 1000, 'video', true, 1, 2)`, fileID, downloadID, fmt.Sprintf("Paged Mapping Fixture %03d S01E02.mkv", index))
	}
	if err := pool.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}

	settings := domain.DefaultAgentSettings()
	settings.Enabled = true
	settings.EpisodeMappingEnabled = true
	settings.BaseURL = "https://provider.example/v1"
	settings.Model = "fixture-model"
	configuration := deterministicAgentConfigurationStub{configuration: domain.Configuration{
		Version:  1,
		Settings: domain.RuntimeSettings{Agent: settings},
	}}
	catalog := &automaticEpisodeMappingReconciliationCatalogStub{targetID: targetID}
	service := NewAgentResolutionService(queries, transactor, scheduler, configuration, catalog, nil)
	resolutionIDs := make([]uuid.UUID, 0, len(terminalIDs))
	for _, acquisitionID := range terminalIDs {
		result, err := service.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{
			Capability: domain.AgentCapabilityEpisodeMapping,
			ResourceID: acquisitionID,
		})
		if err != nil {
			t.Fatalf("seed terminal resolution for %s: %v", acquisitionID, err)
		}
		resolutionIDs = append(resolutionIDs, result.Resolution.ID)
	}
	if _, err := pool.Exec(ctx, `
UPDATE agent_resolutions
SET status = 'failed',
    version = 10,
    error_code = 'agent_provider_unavailable',
    error_message = 'fixture terminal failure',
    completed_at = now(),
    updated_at = now()
WHERE id = ANY($1::uuid[])`, resolutionIDs); err != nil {
		t.Fatal(err)
	}

	catalog.visited = nil
	reconciled, err := service.ReconcileAutomaticEpisodeMappings(ctx)
	if err != nil {
		t.Fatalf("ReconcileAutomaticEpisodeMappings() error = %v", err)
	}
	if reconciled != len(candidateIDs) {
		t.Fatalf("ReconcileAutomaticEpisodeMappings() = %d, want %d", reconciled, len(candidateIDs))
	}
	if len(catalog.visited) != len(candidateIDs) || catalog.visited[len(catalog.visited)-1] != targetID {
		t.Fatalf("visited %d candidates, last = %v, want %d candidates ending with %s", len(catalog.visited), catalog.visited[len(catalog.visited)-1], len(candidateIDs), targetID)
	}
	var terminalCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_resolutions WHERE id = ANY($1::uuid[]) AND status = 'failed' AND version = 10`, resolutionIDs).Scan(&terminalCount); err != nil {
		t.Fatal(err)
	}
	if terminalCount != len(terminalIDs) {
		t.Fatalf("hard terminal resolutions = %d, want %d", terminalCount, len(terminalIDs))
	}
}
