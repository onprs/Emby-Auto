//go:build integration

package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func waitForBlockedIntegrationQuery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, queryName string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM pg_stat_activity
    WHERE datname = current_database()
      AND pid <> pg_backend_pid()
      AND wait_event_type = 'Lock'
      AND query LIKE '%' || $1 || '%'
)`, queryName).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("query %q did not block on a lock", queryName)
		case <-ctx.Done():
			t.Fatalf("waiting for blocked query %q: %v", queryName, ctx.Err())
		}
	}
}

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
	cancelledFileID := uuid.New()
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
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'completed')`, downloadID, acquisitionID); err != nil {
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
		VALUES ($1, $2, 0, 'Cancelled.S09E09.mkv', 1000, 'video', true, 9, 9)`, cancelledFileID, cancelledRetryID); err != nil {
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
	agentMappingFiles, err := queries.ListAgentMappingFiles(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil || len(agentMappingFiles) != 1 || repository.UUIDFromPG(agentMappingFiles[0].ID) != fileID {
		t.Fatalf("Agent Mapping files = %#v, error=%v; want file from non-cancelled attempt", agentMappingFiles, err)
	}

	transactor := database.NewTransactor(pool)
	workflow := NewCatalogWorkflow(queries, transactor, NewOperationScheduler(transactor, &integrationJobInserter{}))
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
	saveGateTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = saveGateTx.Rollback(context.Background()) }()
	var lockedSaveAcquisitionID uuid.UUID
	if err := saveGateTx.QueryRow(ctx, `SELECT id FROM acquisitions WHERE id = $1 FOR UPDATE`, acquisitionID).Scan(&lockedSaveAcquisitionID); err != nil {
		t.Fatal(err)
	}
	type saveCallResult struct {
		saved domain.SavedEpisodeMapping
		err   error
	}
	saveCtx, saveCancel := context.WithCancel(ctx)
	defer saveCancel()
	saveResults := make(chan saveCallResult, 2)
	saveStarted := make(chan struct{}, 2)
	for range 2 {
		go func() {
			saveStarted <- struct{}{}
			saved, saveErr := workflow.SaveEpisodeMapping(saveCtx, input)
			saveResults <- saveCallResult{saved: saved, err: saveErr}
		}()
	}
	<-saveStarted
	<-saveStarted
	waitForBlockedIntegrationQuery(t, ctx, pool, "LockAcquisitionForMapping")
	if err := saveGateTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	concurrentResults := []domain.SavedEpisodeMapping{{}, {}}
	for index := range concurrentResults {
		callResult := <-saveResults
		if callResult.err != nil {
			t.Fatalf("concurrent SaveEpisodeMapping() error = %v", callResult.err)
		}
		concurrentResults[index] = callResult.saved
	}
	first := concurrentResults[0]
	if first.ProfileID != concurrentResults[1].ProfileID || first.Version != 1 || concurrentResults[1].Version != 1 {
		t.Fatalf("concurrent saved mappings = %#v", concurrentResults)
	}
	var saveRecordCount, savedEventCount, profileCount, materializeOperationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mapping_saves WHERE idempotency_key = $1`, "mapping.save:"+actorID.String()+":fixture-save").Scan(&saveRecordCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = 'mapping.profile_saved' AND resource_type = 'acquisition' AND resource_id = $1`, acquisitionID).Scan(&savedEventCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mapping_profiles WHERE name = $1`, "acquisition:"+acquisitionID.String()).Scan(&profileCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'download.materialize' AND resource_id = $1`, downloadID).Scan(&materializeOperationCount); err != nil {
		t.Fatal(err)
	}
	var sourcePayloadSeason, sourcePayloadEpisode int
	if err := pool.QueryRow(ctx, `
SELECT (source_payload->>'sourceSeason')::integer, (source_payload->>'sourceEpisode')::integer
FROM acquisitions
WHERE id = $1`, acquisitionID).Scan(&sourcePayloadSeason, &sourcePayloadEpisode); err != nil {
		t.Fatal(err)
	}
	if saveRecordCount != 1 || savedEventCount != 1 || profileCount != 1 || materializeOperationCount != 1 {
		t.Fatalf("concurrent save side effects = records %d events %d profiles %d materializations %d, want 1/1/1/1", saveRecordCount, savedEventCount, profileCount, materializeOperationCount)
	}
	if sourcePayloadSeason != 2 || sourcePayloadEpisode != 1 {
		t.Fatalf("canonical download source payload = S%02dE%02d, want S02E01", sourcePayloadSeason, sourcePayloadEpisode)
	}
	legacyPayload := fmt.Sprintf(
		`{"acquisitionId":"%s","anchor":{"SourceFileID":"%s","Target":{"Season":2,"Episode":1}}}`,
		acquisitionID,
		fileID,
	)
	legacyFingerprint := sha256.Sum256([]byte(legacyPayload))
	if _, err := pool.Exec(ctx, `UPDATE episode_mapping_saves SET request_fingerprint = $1 WHERE idempotency_key = $2`, legacyFingerprint[:], "mapping.save:"+actorID.String()+":fixture-save"); err != nil {
		t.Fatal(err)
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
	assertMaterializationConflict := func(status string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE downloads SET status = $2 WHERE id = $1`, downloadID, status); err != nil {
			t.Fatal(err)
		}
		blocked := input
		blocked.IdempotencyKey = "blocked-" + status
		_, saveErr := workflow.SaveEpisodeMapping(ctx, blocked)
		var serviceErr *Error
		if !errors.As(saveErr, &serviceErr) || serviceErr.Code != "mapping_materialization_conflict" || !errors.Is(saveErr, ErrStateConflict) {
			t.Fatalf("SaveEpisodeMapping(%s) error = %v", status, saveErr)
		}
	}
	assertMaterializationConflict(string(domain.DownloadSelectingFiles))
	if _, err := pool.Exec(ctx, `UPDATE downloads SET status = 'downloading' WHERE id = $1`, downloadID); err != nil {
		t.Fatal(err)
	}
	materializeTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = materializeTx.Rollback(ctx) }()
	var lockedAcquisitionID, lockedDownloadID uuid.UUID
	if err := materializeTx.QueryRow(ctx, `SELECT id FROM acquisitions WHERE id = $1 FOR UPDATE`, acquisitionID).Scan(&lockedAcquisitionID); err != nil {
		t.Fatal(err)
	}
	if err := materializeTx.QueryRow(ctx, `SELECT id FROM downloads WHERE id = $1 FOR UPDATE`, downloadID).Scan(&lockedDownloadID); err != nil {
		t.Fatal(err)
	}
	concurrentSave := input
	concurrentSave.IdempotencyKey = "blocked-concurrent-materialize"
	saveResult := make(chan error, 1)
	go func() {
		_, saveErr := workflow.SaveEpisodeMapping(ctx, concurrentSave)
		saveResult <- saveErr
	}()
	waitForBlockedIntegrationQuery(t, ctx, pool, "LockAcquisitionForMapping")
	if _, err := materializeTx.Exec(ctx, `UPDATE downloads SET status = 'materialized' WHERE id = $1`, downloadID); err != nil {
		t.Fatal(err)
	}
	if err := materializeTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	saveErr := <-saveResult
	var serviceErr *Error
	if !errors.As(saveErr, &serviceErr) || serviceErr.Code != "mapping_materialization_conflict" || !errors.Is(saveErr, ErrStateConflict) {
		t.Fatalf("concurrent materialized SaveEpisodeMapping() error = %v", saveErr)
	}
}

func TestExplicitEpisodeMappingSerializesWithRSSTargetReservationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	operations := NewOperationScheduler(transactor, &integrationJobInserter{})
	catalogWorkflow := NewCatalogWorkflow(queries, transactor, operations)
	rssWorkflow := NewRSSWorkflow(queries, transactor, operations)

	actorID, seriesID, seasonID, targetEpisodeID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	acquisitionID, downloadID, sourceFileID := uuid.New(), uuid.New(), uuid.New()
	profileID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "target-reservation-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Target Reservation Fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 1)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, 1, 'Reserved Episode')`, targetEpisodeID, seasonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (
    id, series_id, name, version, source_season_lengths, active,
    anchor_source_season, anchor_source_episode, anchor_target_episode_id, target_episode_offset,
    created_by, decision_source
) VALUES ($1, $2, 'rss-target-reservation', 1, ARRAY[1], true, 1, 1, $3, 0, $4, 'user')`, profileID, seriesID, targetEpisodeID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mappings (
    id, profile_id, source_season, source_episode, absolute_episode,
    target_episode_id, mapping_status, match_source
) VALUES ($1, $2, 1, 1, 1, $3, 'mapped', 'absolute')`, uuid.New(), profileID, targetEpisodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
    id, series_id, mapping_profile_id, name, feed_url, enabled,
    poll_interval_seconds, source_season
) VALUES ($1, $2, $3, 'Target Reservation RSS', 'https://example.test/target-reservation.xml', true, 900, 1)`, subscriptionID, seriesID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, 'guid:target-reservation', 'Target Reservation E01', 'https://example.test/target-reservation.torrent', true, ARRAY[]::text[], 1, 1, 'discovered')`, entryID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by)
VALUES ($1, $2, 'manual', 'magnet:?xt=urn:btih:3123456789abcdef0123456789abcdef01234567', $3)`, acquisitionID, seriesID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status, progress) VALUES ($1, $2, 'completed', 1)`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind,
    selected, source_season, source_episode
) VALUES ($1, $2, 0, 'Target.Reservation.S01E01.mkv', 1000, 'video', true, 1, 1)`, sourceFileID, downloadID); err != nil {
		t.Fatal(err)
	}

	seriesGate, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seriesGate.Rollback(context.Background()) }()
	var lockedSeriesID uuid.UUID
	if err := seriesGate.QueryRow(ctx, `SELECT id FROM media_series WHERE id = $1 FOR UPDATE`, seriesID).Scan(&lockedSeriesID); err != nil {
		t.Fatal(err)
	}

	saveResult := make(chan error, 1)
	go func() {
		_, saveErr := catalogWorkflow.SaveEpisodeMapping(ctx, domain.EpisodeMappingPlanInput{
			AcquisitionID: acquisitionID,
			Mode:          domain.EpisodeMappingModeExplicit,
			Assignments: []domain.EpisodeMappingExplicitInput{{
				SourceFileID: sourceFileID,
				Action:       domain.EpisodeMappingExplicitMap,
				Target:       domain.EpisodeCoordinate{Season: 1, Episode: 1},
			}},
			IdempotencyKey: "target-reservation-explicit",
			ActorUserID:    actorID,
		})
		saveResult <- saveErr
	}()
	waitForBlockedIntegrationQuery(t, ctx, pool, "LockMediaSeries")

	rssResult := make(chan error, 1)
	go func() {
		rssResult <- rssWorkflow.ScheduleRSSDownload(ctx, domain.RSSEnqueueCandidate{EntryID: entryID})
	}()
	waitForBlockedIntegrationQuery(t, ctx, pool, "rss-target:")

	if err := seriesGate.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if saveErr := <-saveResult; saveErr != nil {
		t.Fatalf("SaveEpisodeMapping() error = %v", saveErr)
	}
	if rssErr := <-rssResult; rssErr != nil {
		t.Fatalf("ScheduleRSSDownload() error = %v", rssErr)
	}

	var entryStatus string
	var downloadable bool
	var rejectionReasons []string
	if err := pool.QueryRow(ctx, `SELECT status, downloadable, rejection_reasons FROM rss_entries WHERE id = $1`, entryID).Scan(&entryStatus, &downloadable, &rejectionReasons); err != nil {
		t.Fatal(err)
	}
	var rssAcquisitionCount, materializeOperationCount, enqueueOperationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE rss_entry_id = $1`, entryID).Scan(&rssAcquisitionCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'download.materialize' AND resource_id = $1`, downloadID).Scan(&materializeOperationCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'download.enqueue'`).Scan(&enqueueOperationCount); err != nil {
		t.Fatal(err)
	}
	if entryStatus != string(domain.RSSDiscovered) || downloadable || len(rejectionReasons) != 1 || rejectionReasons[0] != rssTargetProcessingReason {
		t.Fatalf("RSS target reservation state = %s/%t/%v", entryStatus, downloadable, rejectionReasons)
	}
	if rssAcquisitionCount != 0 || materializeOperationCount != 1 || enqueueOperationCount != 0 {
		t.Fatalf("target reservation side effects = RSS acquisitions %d materializations %d enqueues %d, want 0/1/0", rssAcquisitionCount, materializeOperationCount, enqueueOperationCount)
	}
}

func TestExplicitEpisodeMappingMaterializesRegularSpecialAndExcludedVideosIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	operations := NewOperationScheduler(transactor, &integrationJobInserter{})
	catalogWorkflow := NewCatalogWorkflow(queries, transactor, operations)
	mediaWorkflow := NewMediaWorkflow(queries, transactor, operations)

	actorID, seriesID := uuid.New(), uuid.New()
	seasonIDs := map[int]uuid.UUID{0: uuid.New(), 1: uuid.New(), 2: uuid.New()}
	acquisitionID, downloadID := uuid.New(), uuid.New()
	excludedAcquisitionID, excludedDownloadID := uuid.New(), uuid.New()
	acquisitionIDs := []uuid.UUID{acquisitionID, excludedAcquisitionID}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "explicit-mapping-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM acquisitions WHERE id = ANY($1::uuid[])`, acquisitionIDs)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_series WHERE id = $1`, seriesID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM admin_users WHERE id = $1`, actorID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Season Pack Fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES
($1, $4, 0, 4), ($2, $4, 1, 12), ($3, $4, 2, 13)`, seasonIDs[0], seasonIDs[1], seasonIDs[2], seriesID); err != nil {
		t.Fatal(err)
	}
	for season, count := range map[int]int{0: 4, 1: 12, 2: 13} {
		for episode := 1; episode <= count; episode++ {
			targetID := uuid.New()
			title := fmt.Sprintf("Season %d Episode %d", season, episode)
			if season == 0 {
				title = fmt.Sprintf("Special %d", episode)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, $3, $4)`, targetID, seasonIDs[season], episode, title); err != nil {
				t.Fatal(err)
			}
		}
	}
	transcodeProfileID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, true, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)`, transcodeProfileID, "explicit-mapping-"+transcodeProfileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by)
VALUES ($1, $2, 'manual', 'magnet:?xt=urn:btih:1123456789abcdef0123456789abcdef01234567', $3)`, acquisitionID, seriesID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, progress, save_path, failure_stage, error_code, error_message)
VALUES ($1, $2, 'failed', 1, '/downloads/season-pack', 'materialize', 'mapping_profile_required', 'mapping is required')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	sourceFileIDs := make([]uuid.UUID, 16)
	assignments := make([]domain.EpisodeMappingExplicitInput, 0, 16)
	for index := range sourceFileIDs {
		sourceFileIDs[index] = uuid.New()
		sourceEpisode := index + 1
		if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, $3, $4, 1000, 'video', true, 1, 1)`, sourceFileIDs[index], downloadID, index, fmt.Sprintf("Season Pack - S01E%02d.mkv", sourceEpisode)); err != nil {
			t.Fatal(err)
		}
		target := domain.EpisodeCoordinate{Season: 1, Episode: sourceEpisode}
		if sourceEpisode > 12 {
			target = domain.EpisodeCoordinate{Season: 0, Episode: sourceEpisode - 12}
		}
		assignments = append(assignments, domain.EpisodeMappingExplicitInput{
			SourceFileID: sourceFileIDs[index], Action: domain.EpisodeMappingExplicitMap, Target: target,
		})
	}
	firstSubtitleID, secondSubtitleID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode) VALUES
($1, $3, 16, 'Season Pack - S01E01.ass', 100, 'subtitle', true, 1, 1),
($2, $3, 17, 'Season Pack - S01E02.ass', 100, 'subtitle', true, 1, 1)`, firstSubtitleID, secondSubtitleID, downloadID); err != nil {
		t.Fatal(err)
	}

	input := domain.EpisodeMappingPlanInput{
		AcquisitionID: acquisitionID, Mode: domain.EpisodeMappingModeExplicit, Assignments: assignments,
		IdempotencyKey: "explicit-season-pack", ActorUserID: actorID,
	}
	preview, err := catalogWorkflow.PreviewEpisodeMapping(ctx, input)
	if err != nil {
		t.Fatalf("PreviewEpisodeMapping(explicit) error = %v", err)
	}
	if preview.Mode != domain.EpisodeMappingModeExplicit || len(preview.Rows) != 16 {
		t.Fatalf("explicit preview mode/rows = %q/%d", preview.Mode, len(preview.Rows))
	}
	for index, row := range preview.Rows {
		wantSeason, wantEpisode := 1, index+1
		if index >= 12 {
			wantSeason, wantEpisode = 0, index-11
		}
		if row.Status != domain.MappingMapped || row.MatchSource != domain.MappingMatchExplicit || row.TargetSeason != wantSeason || row.TargetEpisode != wantEpisode {
			t.Fatalf("explicit preview row %d = %#v, want S%02dE%02d", index, row, wantSeason, wantEpisode)
		}
	}

	invalidCases := []struct {
		name        string
		assignments []domain.EpisodeMappingExplicitInput
		wantCode    string
	}{
		{name: "incomplete scope", assignments: assignments[:15], wantCode: "mapping_scope_incomplete"},
		{name: "duplicate target", assignments: append(append([]domain.EpisodeMappingExplicitInput{}, assignments[:15]...), domain.EpisodeMappingExplicitInput{SourceFileID: sourceFileIDs[15], Action: domain.EpisodeMappingExplicitMap, Target: assignments[0].Target}), wantCode: "mapping_target_duplicate"},
		{name: "missing target", assignments: append(append([]domain.EpisodeMappingExplicitInput{}, assignments[:15]...), domain.EpisodeMappingExplicitInput{SourceFileID: sourceFileIDs[15], Action: domain.EpisodeMappingExplicitMap, Target: domain.EpisodeCoordinate{Season: 99, Episode: 1}}), wantCode: "mapping_target_invalid"},
		{name: "all excluded", assignments: func() []domain.EpisodeMappingExplicitInput {
			values := make([]domain.EpisodeMappingExplicitInput, 0, len(sourceFileIDs))
			for _, sourceFileID := range sourceFileIDs {
				values = append(values, domain.EpisodeMappingExplicitInput{SourceFileID: sourceFileID, Action: domain.EpisodeMappingExplicitExclude})
			}
			return values
		}(), wantCode: "mapping_explicit_empty"},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			_, previewErr := catalogWorkflow.PreviewEpisodeMapping(ctx, domain.EpisodeMappingPlanInput{
				AcquisitionID: acquisitionID, Mode: domain.EpisodeMappingModeExplicit, Assignments: test.assignments,
			})
			var serviceErr *Error
			if !errors.As(previewErr, &serviceErr) || serviceErr.Code != test.wantCode {
				t.Fatalf("PreviewEpisodeMapping() error = %v, want %s", previewErr, test.wantCode)
			}
		})
	}

	saved, err := catalogWorkflow.SaveEpisodeMapping(ctx, input)
	if err != nil {
		t.Fatalf("SaveEpisodeMapping(explicit) error = %v", err)
	}
	var mappingCount, specialMappingCount int
	var anchorSourceSeason *int
	if err := pool.QueryRow(ctx, `
SELECT count(mapping.id), count(mapping.id) FILTER (WHERE season.season_number = 0), profile.anchor_source_season
FROM episode_mapping_profiles AS profile
LEFT JOIN episode_mappings AS mapping ON mapping.profile_id = profile.id
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE profile.id = $1
GROUP BY profile.anchor_source_season`, saved.ProfileID).Scan(&mappingCount, &specialMappingCount, &anchorSourceSeason); err != nil {
		t.Fatal(err)
	}
	if mappingCount != 16 || specialMappingCount != 4 || anchorSourceSeason != nil {
		t.Fatalf("explicit profile mappings/specials/anchor = %d/%d/%v, want 16/4/nil", mappingCount, specialMappingCount, anchorSourceSeason)
	}
	var repairedCoordinateCount int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT (source_season, source_episode)) FROM download_files WHERE download_id = $1 AND selected AND media_kind = 'video'`, downloadID).Scan(&repairedCoordinateCount); err != nil {
		t.Fatal(err)
	}
	if repairedCoordinateCount != 16 {
		t.Fatalf("repaired explicit source coordinates = %d, want 16", repairedCoordinateCount)
	}
	var repairedSubtitles int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM download_files
WHERE (id = $1 AND source_season = 1 AND source_episode = 1)
   OR (id = $2 AND source_season = 1 AND source_episode = 2)`, firstSubtitleID, secondSubtitleID).Scan(&repairedSubtitles); err != nil {
		t.Fatal(err)
	}
	if repairedSubtitles != 2 {
		t.Fatalf("repaired explicit subtitles = %d, want 2", repairedSubtitles)
	}
	var failedNoTaskTargetID uuid.UUID
	if err := pool.QueryRow(ctx, `
SELECT target_episode_id
FROM episode_mappings
WHERE profile_id = $1 AND source_season = 1 AND source_episode = 1`, saved.ProfileID).Scan(&failedNoTaskTargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE downloads
SET status = 'failed', failure_stage = 'materialize',
    error_code = 'mapping_profile_required', error_message = 'fixture mapping recovery'
WHERE id = $1`, downloadID); err != nil {
		t.Fatal(err)
	}
	failedNoTaskOccupancy, err := queries.GetEpisodeMappingTargetOccupancy(ctx, db.GetEpisodeMappingTargetOccupancyParams{
		ExcludedAcquisitionID: repository.UUIDToPG(excludedAcquisitionID),
		TargetEpisodeID:       repository.UUIDToPG(failedNoTaskTargetID),
		SeriesID:              repository.UUIDToPG(seriesID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !failedNoTaskOccupancy.ProcessingPresent || failedNoTaskOccupancy.ManagedImportPresent {
		t.Fatalf("failed no-task occupancy = %#v, want processing only", failedNoTaskOccupancy)
	}
	if _, err := pool.Exec(ctx, `
UPDATE downloads
SET status = 'completed', failure_stage = NULL, error_code = NULL, error_message = NULL
WHERE id = $1`, downloadID); err != nil {
		t.Fatal(err)
	}
	replacementInput := input
	replacementInput.IdempotencyKey = "explicit-season-pack-replacement"
	replacement, err := catalogWorkflow.SaveEpisodeMapping(ctx, replacementInput)
	if err != nil {
		t.Fatalf("SaveEpisodeMapping(pre-materialize replacement) error = %v", err)
	}
	if replacement.ProfileID == saved.ProfileID || replacement.Version != 2 {
		t.Fatalf("replacement Mapping = %#v, previous = %#v", replacement, saved)
	}
	var firstReplacementMappingID uuid.UUID
	if err := pool.QueryRow(ctx, `
SELECT id
FROM episode_mappings
WHERE profile_id = $1 AND source_season = 1 AND source_episode = 1`, replacement.ProfileID).Scan(&firstReplacementMappingID); err != nil {
		t.Fatal(err)
	}
	concurrentTaskTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = concurrentTaskTx.Rollback(ctx) }()
	var lockedTaskAcquisitionID, lockedDownloadID uuid.UUID
	if err := concurrentTaskTx.QueryRow(ctx, `SELECT id FROM acquisitions WHERE id = $1 FOR UPDATE`, acquisitionID).Scan(&lockedTaskAcquisitionID); err != nil {
		t.Fatal(err)
	}
	if err := concurrentTaskTx.QueryRow(ctx, `SELECT id FROM downloads WHERE id = $1 AND status = 'completed' FOR UPDATE`, downloadID).Scan(&lockedDownloadID); err != nil {
		t.Fatal(err)
	}
	concurrentTaskID := deterministicResourceID(string(domain.TaskMediaEpisode) + "-task:" + acquisitionID.String() + ":" + sourceFileIDs[0].String())
	if _, err := concurrentTaskTx.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id, media_type)
VALUES ($1, $2, $3, $4, $5, 'episode')`, concurrentTaskID, acquisitionID, sourceFileIDs[0], firstReplacementMappingID, transcodeProfileID); err != nil {
		t.Fatal(err)
	}
	concurrentTaskSave := replacementInput
	concurrentTaskSave.IdempotencyKey = "explicit-concurrent-task"
	concurrentTaskSaveResult := make(chan error, 1)
	go func() {
		_, saveErr := catalogWorkflow.SaveEpisodeMapping(ctx, concurrentTaskSave)
		concurrentTaskSaveResult <- saveErr
	}()
	waitForBlockedIntegrationQuery(t, ctx, pool, "LockAcquisitionForMapping")
	if err := concurrentTaskTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	concurrentTaskSaveErr := <-concurrentTaskSaveResult
	var concurrentTaskServiceErr *Error
	if !errors.As(concurrentTaskSaveErr, &concurrentTaskServiceErr) || concurrentTaskServiceErr.Code != "mapping_materialization_conflict" || !errors.Is(concurrentTaskSaveErr, ErrStateConflict) {
		t.Fatalf("concurrent task SaveEpisodeMapping() error = %v", concurrentTaskSaveErr)
	}
	var materializeOperationID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM operations WHERE resource_id = $1 AND kind = 'download.materialize' ORDER BY created_at DESC LIMIT 1`, downloadID).Scan(&materializeOperationID); err != nil {
		t.Fatal(err)
	}
	if err := mediaWorkflow.MaterializeDownload(ctx, downloadID, materializeOperationID); err != nil {
		t.Fatalf("MaterializeDownload(explicit season pack) error = %v", err)
	}
	var regularTasks, specialTasks int
	if err := pool.QueryRow(ctx, `
SELECT
    count(*) FILTER (WHERE season.season_number = 1),
    count(*) FILTER (WHERE season.season_number = 0)
FROM episode_tasks AS task
JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE task.acquisition_id = $1`, acquisitionID).Scan(&regularTasks, &specialTasks); err != nil {
		t.Fatal(err)
	}
	if regularTasks != 12 || specialTasks != 4 {
		t.Fatalf("materialized regular/special tasks = %d/%d, want 12/4", regularTasks, specialTasks)
	}
	var replacementTaskCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM episode_tasks AS task
JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
WHERE task.acquisition_id = $1 AND mapping.profile_id = $2`, acquisitionID, replacement.ProfileID).Scan(&replacementTaskCount); err != nil {
		t.Fatal(err)
	}
	if replacementTaskCount != 16 {
		t.Fatalf("tasks using replacement Mapping = %d, want 16", replacementTaskCount)
	}
	blockedAfterMaterialize := replacementInput
	blockedAfterMaterialize.IdempotencyKey = "explicit-after-materialize"
	if _, saveErr := catalogWorkflow.SaveEpisodeMapping(ctx, blockedAfterMaterialize); saveErr == nil {
		t.Fatal("SaveEpisodeMapping() replaced a materialized Mapping")
	} else {
		var serviceErr *Error
		if !errors.As(saveErr, &serviceErr) || serviceErr.Code != "mapping_materialization_conflict" || !errors.Is(saveErr, ErrStateConflict) {
			t.Fatalf("SaveEpisodeMapping(after materialize) error = %v", saveErr)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE downloads SET status = 'completed' WHERE id = $1`, downloadID); err != nil {
		t.Fatal(err)
	}
	blockedByTask := replacementInput
	blockedByTask.IdempotencyKey = "explicit-existing-task"
	if _, saveErr := catalogWorkflow.SaveEpisodeMapping(ctx, blockedByTask); saveErr == nil {
		t.Fatal("SaveEpisodeMapping() replaced a Mapping with existing tasks")
	} else {
		var serviceErr *Error
		if !errors.As(saveErr, &serviceErr) || serviceErr.Code != "mapping_materialization_conflict" || !errors.Is(saveErr, ErrStateConflict) {
			t.Fatalf("SaveEpisodeMapping(existing task) error = %v", saveErr)
		}
	}
	var downloadCount, enqueueOperationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM downloads WHERE acquisition_id = $1`, acquisitionID).Scan(&downloadCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_id = $1 AND kind = 'download.enqueue'`, downloadID).Scan(&enqueueOperationCount); err != nil {
		t.Fatal(err)
	}
	if downloadCount != 1 || enqueueOperationCount != 0 {
		t.Fatalf("mapping recovery downloads/enqueue operations = %d/%d, want 1/0", downloadCount, enqueueOperationCount)
	}
	var specialTaskID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM episode_tasks WHERE acquisition_id = $1 AND source_video_file_id = $2`, acquisitionID, sourceFileIDs[12]).Scan(&specialTaskID); err != nil {
		t.Fatal(err)
	}
	command, err := mediaWorkflow.BeginTranscode(ctx, specialTaskID)
	if err != nil {
		t.Fatalf("BeginTranscode(Season 0) error = %v", err)
	}
	if command.Names.BaseName != "Season Pack Fixture - S00E01 - Special 1" || command.OutputRelativeDirectory != filepath.Join("Season Pack Fixture", "Season0") {
		t.Fatalf("Season 0 command names/directory = %q/%q", command.Names.BaseName, command.OutputRelativeDirectory)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by)
VALUES ($1, $2, 'manual', 'magnet:?xt=urn:btih:2123456789abcdef0123456789abcdef01234567', $3)`, excludedAcquisitionID, seriesID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, progress, failure_stage, error_code, error_message)
VALUES ($1, $2, 'failed', 1, 'materialize', 'mapping_profile_required', 'mapping is required')`, excludedDownloadID, excludedAcquisitionID); err != nil {
		t.Fatal(err)
	}
	keptFileID, excludedFileID := uuid.New(), uuid.New()
	keptSubtitleID, excludedSubtitleID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode) VALUES
($1, $5, 0, 'Second Pack - S02E01.mkv', 1000, 'video', true, 2, 1),
($2, $5, 1, 'Second Pack - S02E02.mkv', 1000, 'video', true, 2, 1),
($3, $5, 2, 'Second Pack - S02E01.ass', 100, 'subtitle', true, 2, 1),
($4, $5, 3, 'Second Pack - S02E02.ass', 100, 'subtitle', true, 2, 1)`, keptFileID, excludedFileID, keptSubtitleID, excludedSubtitleID, excludedDownloadID); err != nil {
		t.Fatal(err)
	}
	occupiedInput := domain.EpisodeMappingPlanInput{
		AcquisitionID: excludedAcquisitionID, Mode: domain.EpisodeMappingModeExplicit,
		Assignments: []domain.EpisodeMappingExplicitInput{
			{SourceFileID: keptFileID, Action: domain.EpisodeMappingExplicitMap, Target: domain.EpisodeCoordinate{Season: 1, Episode: 1}},
			{SourceFileID: excludedFileID, Action: domain.EpisodeMappingExplicitExclude},
		},
	}
	assertTargetOccupied := func(label string) {
		t.Helper()
		_, occupiedErr := catalogWorkflow.PreviewEpisodeMapping(ctx, occupiedInput)
		var serviceErr *Error
		if !errors.As(occupiedErr, &serviceErr) || serviceErr.Code != "mapping_target_occupied" {
			t.Fatalf("%s occupied explicit target error = %v", label, occupiedErr)
		}
	}
	if _, err := pool.Exec(ctx, `
UPDATE episode_tasks
SET state = 'cancelled', video_state = 'failed', subtitle_state = 'ass_ready',
    failure_stage = NULL, error_code = NULL, error_message = NULL
WHERE acquisition_id = $1 AND source_video_file_id = $2`, acquisitionID, sourceFileIDs[0]); err != nil {
		t.Fatal(err)
	}
	assertTargetOccupied("recoverable cancelled task")
	if _, err := pool.Exec(ctx, `
UPDATE episode_tasks
SET state = 'cancelled', video_state = 'failed', subtitle_state = 'cancelled'
WHERE acquisition_id = $1 AND source_video_file_id = $2`, acquisitionID, sourceFileIDs[0]); err != nil {
		t.Fatal(err)
	}
	if releasedPreview, err := catalogWorkflow.PreviewEpisodeMapping(ctx, occupiedInput); err != nil {
		t.Fatalf("unrecoverable cancelled task retained target occupancy: %v", err)
	} else if len(releasedPreview.Rows) != 2 {
		t.Fatalf("unrecoverable cancelled task preview rows = %d, want 2", len(releasedPreview.Rows))
	}
	if _, err := pool.Exec(ctx, `
UPDATE episode_tasks
SET state = 'failed', video_state = 'failed', subtitle_state = 'ass_ready',
    failure_stage = 'video', error_code = 'fixture_transcode_failed', error_message = 'fixture transcode failed'
WHERE acquisition_id = $1 AND source_video_file_id = $2`, acquisitionID, sourceFileIDs[0]); err != nil {
		t.Fatal(err)
	}
	assertTargetOccupied("failed task")
	excludedInput := domain.EpisodeMappingPlanInput{
		AcquisitionID: excludedAcquisitionID, Mode: domain.EpisodeMappingModeExplicit,
		Assignments: []domain.EpisodeMappingExplicitInput{
			{SourceFileID: keptFileID, Action: domain.EpisodeMappingExplicitMap, Target: domain.EpisodeCoordinate{Season: 2, Episode: 13}},
			{SourceFileID: excludedFileID, Action: domain.EpisodeMappingExplicitExclude},
		},
		IdempotencyKey: "explicit-exclusion", ActorUserID: actorID,
	}
	if _, err := catalogWorkflow.SaveEpisodeMapping(ctx, excludedInput); err != nil {
		t.Fatalf("SaveEpisodeMapping(exclusion) error = %v", err)
	}
	var selectedKeptFamily, selectedExcludedFamily, repairedExcludedFamily int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE id = ANY($1::uuid[]) AND selected`, []uuid.UUID{keptFileID, keptSubtitleID}).Scan(&selectedKeptFamily); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE id = ANY($1::uuid[]) AND selected`, []uuid.UUID{excludedFileID, excludedSubtitleID}).Scan(&selectedExcludedFamily); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE id = ANY($1::uuid[]) AND source_season = 2 AND source_episode = 2`, []uuid.UUID{excludedFileID, excludedSubtitleID}).Scan(&repairedExcludedFamily); err != nil {
		t.Fatal(err)
	}
	if selectedKeptFamily != 2 || selectedExcludedFamily != 0 || repairedExcludedFamily != 2 {
		t.Fatalf("explicit file families kept/excluded/repaired = %d/%d/%d, want 2/0/2", selectedKeptFamily, selectedExcludedFamily, repairedExcludedFamily)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM operations WHERE resource_id = $1 AND kind = 'download.materialize' ORDER BY created_at DESC LIMIT 1`, excludedDownloadID).Scan(&materializeOperationID); err != nil {
		t.Fatal(err)
	}
	if err := mediaWorkflow.MaterializeDownload(ctx, excludedDownloadID, materializeOperationID); err != nil {
		t.Fatalf("MaterializeDownload(explicit exclusion) error = %v", err)
	}
	var keptTaskCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_tasks WHERE acquisition_id = $1`, excludedAcquisitionID).Scan(&keptTaskCount); err != nil {
		t.Fatal(err)
	}
	if keptTaskCount != 1 {
		t.Fatalf("materialized tasks after exclusion = %d, want 1", keptTaskCount)
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
