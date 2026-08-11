//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestEmbyCatalogSnapshotMarksMissingItemsAbsentIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	workflow := NewEmbyCatalogWorkflow(queries, database.NewTransactor(pool), nil)
	actorID := uuid.New()
	scan1ID, scan2ID := uuid.New(), uuid.New()
	operation1ID, operation2ID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "emby-scan-test-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM emby_library_items WHERE last_scan_run_id IN ($1, $2)`, scan1ID, scan2ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM emby_libraries WHERE last_scan_run_id IN ($1, $2)`, scan1ID, scan2ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM emby_scan_runs WHERE id IN ($1, $2)`, scan1ID, scan2ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM operations WHERE id IN ($1, $2)`, operation1ID, operation2ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM admin_users WHERE id = $1`, actorID)
	})
	createScan := func(scanID, operationID uuid.UUID) domain.Operation {
		if _, err := pool.Exec(ctx, `
			INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
			VALUES ($1, 'emby.scan', 'emby_scan', $2, $3, 'running', 3, 60)`, operationID, scanID, "test-"+operationID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := queries.CreateEmbyScanRun(ctx, db.CreateEmbyScanRunParams{ID: repository.UUIDToPG(scanID), OperationID: repository.UUIDToPG(operationID), CreatedBy: repository.UUIDToPG(actorID)}); err != nil {
			t.Fatal(err)
		}
		operation := domain.Operation{ID: operationID, Kind: "emby.scan", ResourceType: "emby_scan", ResourceID: scanID}
		if _, err := workflow.BeginScan(ctx, operation); err != nil {
			t.Fatal(err)
		}
		return operation
	}
	firstOperation := createScan(scan1ID, operation1ID)
	seasonNumber, episodeNumber := 1, 1
	if err := workflow.CompleteScan(ctx, firstOperation, []domain.EmbyLibrarySnapshot{{
		Library: domain.EmbyLibraryCatalog{EmbyID: "integration-library-" + actorID.String(), Name: "Anime", Payload: []byte(`{"ItemId":"library"}`)},
		Items: []domain.EmbyLibraryItemCatalog{
			{EmbyID: "integration-item-" + actorID.String(), ItemType: "Episode", Name: "Pilot", ProviderIDs: map[string]string{"Tmdb": "42"}, SeasonNumber: &seasonNumber, EpisodeNumber: &episodeNumber, Payload: []byte(`{"Id":"episode"}`)},
			{EmbyID: "integration-movie-" + actorID.String(), ItemType: "Movie", Name: "Fixture Movie", Path: "/media/movies/Fixture Movie(2024)/Fixture Movie(2024).mp4", ProviderIDs: map[string]string{"Tmdb": "12345"}, Payload: []byte(`{"Id":"movie"}`)},
		},
	}}); err != nil {
		t.Fatalf("first CompleteScan() error = %v", err)
	}
	libraries, err := workflow.ListLibraries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var libraryID uuid.UUID
	for _, library := range libraries {
		if library.EmbyID == "integration-library-"+actorID.String() {
			libraryID = library.ID
		}
	}
	if libraryID == uuid.Nil {
		t.Fatal("persisted Emby library was not found")
	}
	items, err := workflow.ListLibraryItems(ctx, libraryID, domain.EmbyLibraryItemFilter{}, nil, 10)
	if err != nil || len(items.Items) != 2 || !items.Items[0].Present || !items.Items[1].Present {
		t.Fatalf("first items = %#v, %v", items, err)
	}
	movieItems, err := workflow.ListLibraryItems(ctx, libraryID, domain.EmbyLibraryItemFilter{ItemType: "Movie"}, nil, 10)
	if err != nil || len(movieItems.Items) != 1 || movieItems.Items[0].Name != "Fixture Movie" {
		t.Fatalf("movie items = %#v, %v", movieItems, err)
	}
	secondOperation := createScan(scan2ID, operation2ID)
	if err := workflow.CompleteScan(ctx, secondOperation, []domain.EmbyLibrarySnapshot{{
		Library: domain.EmbyLibraryCatalog{EmbyID: "integration-library-" + actorID.String(), Name: "Anime", Payload: []byte(`{"ItemId":"library"}`)},
		Items:   []domain.EmbyLibraryItemCatalog{},
	}}); err != nil {
		t.Fatalf("second CompleteScan() error = %v", err)
	}
	items, err = workflow.ListLibraryItems(ctx, libraryID, domain.EmbyLibraryItemFilter{}, nil, 10)
	if err != nil || len(items.Items) != 2 || items.Items[0].Present || items.Items[1].Present {
		t.Fatalf("second items = %#v, %v", items, err)
	}
}

func TestEpisodeMappingPreviewAndIdempotentSaveIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	actorID := uuid.New()
	seriesID := uuid.New()
	season1ID := uuid.New()
	season2ID := uuid.New()
	acquisitionID := uuid.New()
	downloadID := uuid.New()
	cancelledRetryID := uuid.New()
	fileID := uuid.New()
	username := "mapping-test-" + actorID.String()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, username); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM acquisitions WHERE id = $1`, acquisitionID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_series WHERE id = $1`, seriesID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM admin_users WHERE id = $1`, actorID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Canonical Show')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES
		($1, $3, 1, 28), ($2, $3, 2, 12)`, season1ID, season2ID, seriesID); err != nil {
		t.Fatal(err)
	}
	for season, count := range map[int]int{1: 28, 2: 12} {
		seasonID := season1ID
		if season == 2 {
			seasonID = season2ID
		}
		for episode := 1; episode <= count; episode++ {
			if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, $3, $4)`, uuid.New(), seasonID, episode, fmt.Sprintf("S%d Episode %d", season, episode)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by)
		VALUES ($1, $2, 'manual', 'magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567', $3)`, acquisitionID, seriesID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'downloading')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
		VALUES ($1, $2, 0, 'Show.S02E01.mkv', 1000, 'video', true, 2, 1)`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (id, acquisition_id, attempt, status)
		VALUES ($1, $2, 2, 'cancelled')`, cancelledRetryID, acquisitionID); err != nil {
		t.Fatal(err)
	}

	queries := db.New(pool)
	currentDownload, err := queries.GetLatestDownloadByAcquisition(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil || repository.UUIDFromPG(currentDownload.ID) != downloadID {
		t.Fatalf("current download = %#v, error=%v; want non-cancelled attempt", currentDownload, err)
	}
	mappingCompleteness, err := queries.GetAcquisitionMappingCompleteness(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil || mappingCompleteness.SelectedVideoCount != 1 {
		t.Fatalf("mapping completeness = %#v, error=%v; want selected video from non-cancelled attempt", mappingCompleteness, err)
	}

	workflow := NewCatalogWorkflow(queries, database.NewTransactor(pool), nil)
	input := domain.EpisodeMappingPlanInput{
		AcquisitionID: acquisitionID,
		Anchor: domain.EpisodeMappingAnchorInput{
			SourceFileID: fileID,
			Target:       domain.EpisodeCoordinate{Season: 2, Episode: 1},
		},
	}
	preview, err := workflow.PreviewEpisodeMapping(ctx, input)
	if err != nil {
		t.Fatalf("PreviewEpisodeMapping() error = %v", err)
	}
	if len(preview.Rows) != 1 || preview.Rows[0].AbsoluteEpisode != 29 || preview.Rows[0].TargetSeason != 2 || preview.Rows[0].TargetEpisode != 1 || preview.Rows[0].TargetTitle != "S2 Episode 1" {
		t.Fatalf("preview = %#v", preview)
	}

	input.IdempotencyKey = "fixture-save"
	input.ActorUserID = actorID
	first, err := workflow.SaveEpisodeMapping(ctx, input)
	if err != nil {
		t.Fatalf("SaveEpisodeMapping() error = %v", err)
	}
	second, err := workflow.SaveEpisodeMapping(ctx, input)
	if err != nil {
		t.Fatalf("replayed SaveEpisodeMapping() error = %v", err)
	}
	if first.ProfileID != second.ProfileID || first.Version != 1 || second.Version != 1 {
		t.Fatalf("saved mappings = %#v, %#v", first, second)
	}
	var profileMappingCount, reusableMappingCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mappings WHERE profile_id = $1`, first.ProfileID).Scan(&profileMappingCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM episode_mappings AS mapping
JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE mapping.profile_id = $1
  AND mapping.source_season = 2
  AND mapping.source_episode = 2
  AND season.season_number = 2
  AND episode.episode_number = 2
  AND mapping.mapping_status = 'mapped'`, first.ProfileID).Scan(&reusableMappingCount); err != nil {
		t.Fatal(err)
	}
	if profileMappingCount != 12 || reusableMappingCount != 1 {
		t.Fatalf("profile mapping counts = total %d reusable %d, want 12/1", profileMappingCount, reusableMappingCount)
	}
	changed := input
	changed.Anchor.Target = domain.EpisodeCoordinate{Season: 2, Episode: 2}
	if _, err := workflow.SaveEpisodeMapping(ctx, changed); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("changed idempotent save error = %v", err)
	}
}

func TestSaveEpisodeMappingRepairsCoordinatesAndRecoversRSSDownloadsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	workflow := NewCatalogWorkflow(queries, transactor, NewOperationScheduler(transactor, &integrationJobInserter{}))

	actorID, seriesID, seasonID := uuid.New(), uuid.New(), uuid.New()
	subscriptionID := uuid.New()
	entryIDs := []uuid.UUID{uuid.New(), uuid.New()}
	acquisitionIDs := []uuid.UUID{uuid.New(), uuid.New()}
	downloadIDs := []uuid.UUID{uuid.New(), uuid.New()}
	videoIDs := []uuid.UUID{uuid.New(), uuid.New()}
	subtitleID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "mapping-recovery-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM acquisitions WHERE id = ANY($1::uuid[])`, acquisitionIDs)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM rss_subscriptions WHERE id = $1`, subscriptionID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_series WHERE id = $1`, seriesID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM admin_users WHERE id = $1`, actorID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Mapping Recovery Show')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 2)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	for episode := 1; episode <= 2; episode++ {
		if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, $3, $4)`, uuid.New(), seasonID, episode, fmt.Sprintf("Episode %d", episode)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Mapping Recovery RSS', 'https://example.test/mapping-recovery.xml', false, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	for index := range acquisitionIDs {
		if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status)
VALUES ($1, $2, $3, $4, $5, true, ARRAY[]::text[], 1, $6, 'enqueued')`, entryIDs[index], subscriptionID, fmt.Sprintf("mapping-recovery-%d", index), fmt.Sprintf("Show - %02d", index+1), fmt.Sprintf("https://example.test/%d.torrent", index+1), index+1); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, acquisitionIDs[index], seriesID, entryIDs[index]); err != nil {
			t.Fatal(err)
		}
		hash := fmt.Sprintf("%040d", index+1)
		if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status, progress, failure_stage, error_code, error_message)
VALUES ($1, $2, $3, 'failed', 1, 'materialize', 'mapping_profile_required', 'mapping is required')`, downloadIDs[index], acquisitionIDs[index], hash); err != nil {
			t.Fatal(err)
		}
		persistedEpisode := index + 1
		if index == 0 {
			persistedEpisode = 9
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, $3, 1000, 'video', true, 1, $4)`, videoIDs[index], downloadIDs[index], fmt.Sprintf("Show - %02d.mkv", index+1), persistedEpisode); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 1, 'Show - 01.ass', 100, 'subtitle', true, 1, 9)`, subtitleID, downloadIDs[0]); err != nil {
		t.Fatal(err)
	}

	input := domain.EpisodeMappingPlanInput{
		AcquisitionID: acquisitionIDs[0],
		Anchor: domain.EpisodeMappingAnchorInput{
			SourceFileID: videoIDs[0],
			Target:       domain.EpisodeCoordinate{Season: 1, Episode: 1},
		},
		IdempotencyKey: "mapping-recovery-save", ActorUserID: actorID,
	}
	preview, err := workflow.PreviewEpisodeMapping(ctx, input)
	if err != nil {
		t.Fatalf("PreviewEpisodeMapping() error = %v", err)
	}
	if len(preview.Rows) != 1 || preview.Rows[0].SourceEpisode != 1 || preview.Rows[0].TargetEpisode != 1 || preview.Rows[0].Status != domain.MappingMapped {
		t.Fatalf("preview = %#v, want corrected S01E01 mapping", preview)
	}
	incomplete := input
	incomplete.IdempotencyKey = "mapping-incomplete-save"
	incomplete.Anchor.Target = domain.EpisodeCoordinate{Season: 9, Episode: 1}
	if _, err := workflow.SaveEpisodeMapping(ctx, incomplete); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("incomplete SaveEpisodeMapping() error = %v, want state conflict", err)
	}
	var profilesBeforeSave int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mapping_profiles WHERE series_id = $1`, seriesID).Scan(&profilesBeforeSave); err != nil {
		t.Fatal(err)
	}
	if profilesBeforeSave != 0 {
		t.Fatalf("profiles after incomplete save = %d, want 0", profilesBeforeSave)
	}

	saved, err := workflow.SaveEpisodeMapping(ctx, input)
	if err != nil {
		t.Fatalf("SaveEpisodeMapping() error = %v, cause = %v", err, errors.Unwrap(err))
	}

	var subscriptionProfile uuid.UUID
	var subscriptionVersion int
	if err := pool.QueryRow(ctx, `SELECT mapping_profile_id, version FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&subscriptionProfile, &subscriptionVersion); err != nil {
		t.Fatal(err)
	}
	if subscriptionProfile != saved.ProfileID || subscriptionVersion != 2 {
		t.Fatalf("subscription profile/version = %s/%d, want %s/2", subscriptionProfile, subscriptionVersion, saved.ProfileID)
	}
	var propagated int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE id = ANY($1::uuid[]) AND mapping_profile_id = $2`, acquisitionIDs, saved.ProfileID).Scan(&propagated); err != nil {
		t.Fatal(err)
	}
	if propagated != 2 {
		t.Fatalf("propagated acquisitions = %d, want 2", propagated)
	}
	var correctedFiles int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE id = ANY($1::uuid[]) AND source_season = 1 AND source_episode = 1`, []uuid.UUID{videoIDs[0], subtitleID}).Scan(&correctedFiles); err != nil {
		t.Fatal(err)
	}
	if correctedFiles != 2 {
		t.Fatalf("corrected coordinate files = %d, want video and subtitle", correctedFiles)
	}
	var synchronizedSourceFacts int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE acquisition.id = ANY($1::uuid[])
  AND (acquisition.source_payload->>'sourceSeason')::integer = 1
  AND (acquisition.source_payload->>'sourceEpisode')::integer = entry.source_episode
  AND entry.source_episode IN (1, 2)`, acquisitionIDs).Scan(&synchronizedSourceFacts); err != nil {
		t.Fatal(err)
	}
	if synchronizedSourceFacts != 2 {
		t.Fatalf("synchronized source facts = %d, want 2", synchronizedSourceFacts)
	}
	var recoveredDownloads, materializeOperations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM downloads WHERE id = ANY($1::uuid[]) AND status = 'completed' AND failure_stage IS NULL AND error_code IS NULL AND version = 2`, downloadIDs).Scan(&recoveredDownloads); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_id = ANY($1::uuid[]) AND kind = 'download.materialize' AND status = 'queued'`, downloadIDs).Scan(&materializeOperations); err != nil {
		t.Fatal(err)
	}
	if recoveredDownloads != 2 || materializeOperations != 2 {
		t.Fatalf("recovered downloads/operations = %d/%d, want 2/2", recoveredDownloads, materializeOperations)
	}
}

func TestDeterministicEpisodeMappingResolvesExactRSSCoordinatesWithoutAgentIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	workflow := NewCatalogWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, &integrationJobInserter{}))

	seriesID, seasonID, subscriptionID := uuid.New(), uuid.New(), uuid.New()
	entryID, acquisitionID, downloadID, fileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Deterministic Mapping Show')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 2)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	for episode := 1; episode <= 2; episode++ {
		if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, $3, $4)`, uuid.New(), seasonID, episode, fmt.Sprintf("Episode %d", episode)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, auto_episode_mapping, poll_interval_seconds, source_season) VALUES ($1, $2, 'Deterministic Mapping RSS', 'https://example.test/deterministic-mapping.xml', false, true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status) VALUES ($1, $2, 'deterministic-mapping-1', 'Show S01E01', 'https://example.test/1.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued')`, entryID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, acquisitionID, seriesID, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, torrent_hash, status, progress, failure_stage, error_code, error_message) VALUES ($1, $2, $3, 'failed', 1, 'materialize', 'mapping_profile_required', 'mapping is required')`, downloadID, acquisitionID, fmt.Sprintf("%040d", 91)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode) VALUES ($1, $2, 0, 'Show S01E01.mkv', 1000, 'video', true, 1, 1)`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}

	automatic, err := workflow.AutomaticEpisodeMappingEnabled(ctx, acquisitionID)
	if err != nil || !automatic {
		t.Fatalf("AutomaticEpisodeMappingEnabled() = %t, %v", automatic, err)
	}
	agentService := NewAgentResolutionService(
		db.New(pool), transactor, NewOperationScheduler(transactor, &integrationJobInserter{}),
		deterministicAgentConfigurationStub{configuration: domain.Configuration{}}, workflow, nil,
	)
	reconciled, err := agentService.ReconcileAutomaticEpisodeMappings(ctx)
	if err != nil || reconciled != 1 {
		t.Fatalf("ReconcileAutomaticEpisodeMappings() = %d, %v, want 1, nil", reconciled, err)
	}
	var decisionSource string
	var profileID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT profile.id, profile.decision_source FROM episode_mapping_profiles AS profile JOIN acquisitions AS acquisition ON acquisition.mapping_profile_id = profile.id WHERE acquisition.id = $1`, acquisitionID).Scan(&profileID, &decisionSource); err != nil {
		t.Fatal(err)
	}
	var subscriptionProfile uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT mapping_profile_id FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&subscriptionProfile); err != nil {
		t.Fatal(err)
	}
	var agentResolutions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_resolutions WHERE resource_id = $1`, acquisitionID).Scan(&agentResolutions); err != nil {
		t.Fatal(err)
	}
	if decisionSource != string(domain.DecisionSourceDeterministic) || subscriptionProfile != profileID || agentResolutions != 0 {
		t.Fatalf("deterministic Mapping profile=%s subscription=%s source=%s Agent resolutions=%d", profileID, subscriptionProfile, decisionSource, agentResolutions)
	}
}
