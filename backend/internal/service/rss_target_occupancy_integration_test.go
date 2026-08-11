//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type rssTargetFixture struct {
	ctx              context.Context
	queries          *db.Queries
	workflow         *RSSWorkflow
	pool             *pgxpool.Pool
	actorID          uuid.UUID
	seriesID         uuid.UUID
	profileID        uuid.UUID
	subscriptionID   uuid.UUID
	pollOperationID  uuid.UUID
	targetEpisodeIDs []uuid.UUID
	targetTMDbIDs    []int64
}

type rssRealtimeVerifierRecordingStub struct {
	subscriptionID uuid.UUID
	coordinates    []domain.EpisodeCoordinate
	checkID        uuid.UUID
}

func (stub *rssRealtimeVerifierRecordingStub) VerifySubscription(context.Context, uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}

func (stub *rssRealtimeVerifierRecordingStub) VerifyEntry(context.Context, uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}

func (stub *rssRealtimeVerifierRecordingStub) VerifyCoordinates(
	_ context.Context,
	subscriptionID uuid.UUID,
	coordinates []domain.EpisodeCoordinate,
) (uuid.UUID, error) {
	stub.subscriptionID = subscriptionID
	stub.coordinates = append([]domain.EpisodeCoordinate(nil), coordinates...)
	return stub.checkID, nil
}

func newRSSTargetFixture(t *testing.T) rssTargetFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	fixture := rssTargetFixture{
		ctx: ctx, queries: db.New(pool), pool: pool,
		actorID: uuid.New(), seriesID: uuid.New(), profileID: uuid.New(), subscriptionID: uuid.New(), pollOperationID: uuid.New(),
		targetEpisodeIDs: []uuid.UUID{uuid.New(), uuid.New()}, targetTMDbIDs: []int64{910001, 910002},
	}
	fixture.workflow = NewRSSWorkflow(fixture.queries, transactor, NewOperationScheduler(transactor, &integrationJobInserter{}))
	seasonID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, fixture.actorID, "rss-target-"+fixture.actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, 9100, 'Target Occupancy Series')`, fixture.seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 2, 2)`, seasonID, fixture.seriesID); err != nil {
		t.Fatal(err)
	}
	for index, episodeID := range fixture.targetEpisodeIDs {
		if _, err := pool.Exec(ctx, `
INSERT INTO media_episodes (id, season_id, tmdb_episode_id, episode_number, title)
VALUES ($1, $2, $3, $4, $5)`, episodeID, seasonID, fixture.targetTMDbIDs[index], index+1, fmt.Sprintf("Target %d", index+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (
    id, series_id, name, version, source_season_lengths, active,
    anchor_source_season, anchor_source_episode, anchor_target_episode_id, target_episode_offset,
    decision_source
) VALUES ($1, $2, 'target-occupancy', 1, ARRAY[2], true, 1, 1, $3, 0, 'deterministic')`, fixture.profileID, fixture.seriesID, fixture.targetEpisodeIDs[0]); err != nil {
		t.Fatal(err)
	}
	for index, episodeID := range fixture.targetEpisodeIDs {
		if _, err := pool.Exec(ctx, `
INSERT INTO episode_mappings (
    id, profile_id, source_season, source_episode, absolute_episode,
    target_episode_id, mapping_status, match_source
) VALUES ($1, $2, 1, $3, $3, $4, 'mapped', $5)`, uuid.New(), fixture.profileID, index+1, episodeID, map[bool]string{true: "anchor", false: "absolute"}[index == 0]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
    id, series_id, mapping_profile_id, name, feed_url, enabled,
    poll_interval_seconds, source_season
) VALUES ($1, $2, $3, 'Target Occupancy Feed', 'https://example.test/target-occupancy.xml', true, 900, 1)`, fixture.subscriptionID, fixture.seriesID, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'running', 3, 60)`, fixture.pollOperationID, fixture.subscriptionID, "rss-target-poll-"+fixture.pollOperationID.String()); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture rssTargetFixture) addCatalogEpisode(t *testing.T, index int) {
	t.Helper()
	scanOperationID, scanID, libraryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, attempt_count, timeout_seconds, started_at, finished_at
) VALUES ($1, 'emby.scan', 'emby_scan', $2, $3, 'succeeded', 3, 1, 300, now(), now());
INSERT INTO emby_scan_runs (
    id, operation_id, status, library_count, item_count, completed_at, created_by
) VALUES ($2, $1, 'succeeded', 1, 1, now(), $4);
INSERT INTO emby_libraries (
    id, emby_id, name, collection_type, locations, present, last_scan_run_id, last_seen_at
) VALUES ($5, $6, 'Anime', 'tvshows', ARRAY['/library'], true, $2, now());
INSERT INTO emby_library_items (
    id, emby_id, library_id, item_type, name, file_path, provider_ids,
    season_number, episode_number, present, last_scan_run_id, last_seen_at
) VALUES ($7, $8, $5, 'Episode', $9, $10, jsonb_build_object('Tmdb', $11::text), 2, $12, true, $2, now())`,
		scanOperationID, scanID, "emby-scan-"+scanID.String(), fixture.actorID,
		libraryID, "library-"+libraryID.String(), uuid.New(), "episode-"+fixture.targetEpisodeIDs[index].String(),
		fmt.Sprintf("Target %d", index+1), fmt.Sprintf("/library/Target/Season2/Target-S02E%02d.mkv", index+1),
		fixture.targetTMDbIDs[index], index+1,
	); err != nil {
		t.Fatal(err)
	}
}

func targetFeedEntry(index int, suffix string) domain.RSSFeedEntry {
	return domain.RSSFeedEntry{
		GUID:        fmt.Sprintf("target-%d-%s", index+1, suffix),
		Title:       fmt.Sprintf("Target Occupancy Series - S01E%02d [%s]", index+1, suffix),
		DownloadURI: fmt.Sprintf("magnet:?xt=urn:btih:%040x", 1000+index*100+len(suffix)),
	}
}

func (fixture rssTargetFixture) addRealtimeCheck(t *testing.T, index int, present bool, checkedAt time.Time) uuid.UUID {
	t.Helper()
	checkID := uuid.New()
	matchSource := "absent"
	if present {
		matchSource = "tmdb_episode"
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO rss_target_realtime_checks (target_episode_id, check_id, present, match_source, checked_at)
VALUES ($1, $2, $3, $4, $5)`, fixture.targetEpisodeIDs[index], checkID, present, matchSource, checkedAt); err != nil {
		t.Fatal(err)
	}
	return checkID
}

func TestRSSRealtimeChecksRemainIndependentForSameTargetIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	absentCheckID := fixture.addRealtimeCheck(t, 0, false, time.Now())
	presentCheckID := fixture.addRealtimeCheck(t, 0, true, time.Now())

	absent, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(
		fixture.ctx, fixture.queries, fixture.subscriptionID, 1, 1, uuid.Nil, absentCheckID,
	)
	if err != nil {
		t.Fatalf("load absent check: %v", err)
	}
	present, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(
		fixture.ctx, fixture.queries, fixture.subscriptionID, 1, 1, uuid.Nil, presentCheckID,
	)
	if err != nil {
		t.Fatalf("load present check: %v", err)
	}
	if absent.Reason != "" || present.Reason != rssTargetInLibraryReason {
		t.Fatalf("occupancy = absent %#v present %#v", absent, present)
	}
}

func TestRSSPollRealtimeCheckOverridesStaleCatalogSnapshotIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	fixture.addCatalogEpisode(t, 0)
	checkID := fixture.addRealtimeCheck(t, 0, false, time.Now())

	result, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "live-absent")},
	}, domain.RSSPollPersistOptions{RealtimeCheckID: checkID})
	if err != nil {
		t.Fatalf("PersistPoll() error = %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want one because live Emby is authoritative", result.Candidates)
	}
	var fulfilled bool
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT imported_at IS NOT NULL FROM rss_entries WHERE id = $1`, result.Candidates[0].EntryID).Scan(&fulfilled); err != nil {
		t.Fatal(err)
	}
	if fulfilled {
		t.Fatal("stale catalog snapshot created fulfillment despite live absence")
	}
}

func TestRSSPollRealtimeCheckFindsTargetMissingFromCatalogSnapshotIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	checkID := fixture.addRealtimeCheck(t, 0, true, time.Now())

	result, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "live-present")},
	}, domain.RSSPollPersistOptions{AdjudicateReleases: true, RealtimeCheckID: checkID})
	if err != nil {
		t.Fatalf("PersistPoll() error = %v", err)
	}
	if len(result.Candidates) != 0 || len(result.AgentAdjudicationBatchIDs) != 0 {
		t.Fatalf("result = %#v, want no download or Agent candidate", result)
	}
	var reason, source string
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT rejection_reasons[1], fulfillment_source FROM rss_entries WHERE subscription_id = $1`, fixture.subscriptionID).Scan(&reason, &source); err != nil {
		t.Fatal(err)
	}
	if reason != rssTargetInLibraryReason || source != rssFulfillmentEmbyCatalog {
		t.Fatalf("occupancy = reason %q source %q", reason, source)
	}
}

func TestRSSPollRejectsExpiredRealtimeCheckIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	checkID := fixture.addRealtimeCheck(t, 0, false, time.Now().Add(-31*time.Second))

	_, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "expired")},
	}, domain.RSSPollPersistOptions{RealtimeCheckID: checkID})
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "rss_realtime_check_expired" {
		t.Fatalf("PersistPoll() error = %#v", err)
	}
}

func TestRSSEnqueueRealtimeCheckBlocksBeforeAcquisitionIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	initial, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "enqueue-live")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(initial.Candidates) != 1 {
		t.Fatalf("initial PersistPoll() = %#v, %v", initial, err)
	}
	checkID := fixture.addRealtimeCheck(t, 0, true, time.Now())
	if err := fixture.workflow.ScheduleRSSDownloadWithRealtimeCheck(fixture.ctx, initial.Candidates[0], checkID); err != nil {
		t.Fatalf("ScheduleRSSDownloadWithRealtimeCheck() error = %v", err)
	}
	var acquisitions, operations int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT
  (SELECT count(*) FROM acquisitions WHERE rss_entry_id = $1),
  (SELECT count(*) FROM operations WHERE kind = 'download.enqueue')`, initial.Candidates[0].EntryID).Scan(&acquisitions, &operations); err != nil {
		t.Fatal(err)
	}
	if acquisitions != 0 || operations != 0 {
		t.Fatalf("created acquisitions=%d operations=%d", acquisitions, operations)
	}
}

func TestRSSPollFiltersMappedTargetAlreadyInEmbyCatalogIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	fixture.addCatalogEpisode(t, 0)

	result, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "catalog")},
	}, domain.RSSPollPersistOptions{AdjudicateReleases: true})
	if err != nil {
		t.Fatalf("PersistPoll() error = %v", err)
	}
	if len(result.Candidates) != 0 || len(result.AgentAdjudicationBatchIDs) != 0 {
		t.Fatalf("poll result = %#v, want no candidate or Agent batch", result)
	}
	var downloadable, fulfilled bool
	var reason, source string
	var acquisitions int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT entry.downloadable, entry.imported_at IS NOT NULL, entry.rejection_reasons[1], entry.fulfillment_source,
       (SELECT count(*) FROM acquisitions AS acquisition WHERE acquisition.rss_entry_id = entry.id)
FROM rss_entries AS entry
WHERE entry.subscription_id = $1`, fixture.subscriptionID).Scan(&downloadable, &fulfilled, &reason, &source, &acquisitions); err != nil {
		t.Fatal(err)
	}
	if downloadable || !fulfilled || reason != rssTargetInLibraryReason || source != rssFulfillmentEmbyCatalog || acquisitions != 0 {
		t.Fatalf("entry = downloadable %t fulfilled %t reason %q source %q acquisitions %d", downloadable, fulfilled, reason, source, acquisitions)
	}

	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE emby_library_items SET present = false WHERE provider_ids @> jsonb_build_object('Tmdb', $1::bigint::text)`, fixture.targetTMDbIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{Title: "Target Occupancy"}, domain.RSSPollPersistOptions{}); err != nil {
		t.Fatalf("PersistPoll() after catalog removal error = %v", err)
	}
	var stillFulfilled bool
	var fulfillmentSource *string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT imported_at IS NOT NULL, fulfillment_source FROM rss_entries WHERE subscription_id = $1`, fixture.subscriptionID).Scan(&stillFulfilled, &fulfillmentSource); err != nil {
		t.Fatal(err)
	}
	if stillFulfilled || fulfillmentSource != nil {
		t.Fatalf("stale catalog fulfillment remained: fulfilled %t source %v", stillFulfilled, fulfillmentSource)
	}
}

func TestRSSEnqueueRechecksCatalogAndProcessingOccupancyIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	first, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "primary")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(first.Candidates) != 1 {
		t.Fatalf("initial PersistPoll() = %#v, %v", first, err)
	}
	fixture.addCatalogEpisode(t, 0)
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, first.Candidates[0]); err != nil {
		t.Fatalf("ScheduleRSSDownload() error = %v", err)
	}
	var acquisitions, enqueueOperations int
	var reason string
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT
    (SELECT count(*) FROM acquisitions AS acquisition WHERE acquisition.rss_entry_id = $1),
    (SELECT count(*) FROM operations WHERE kind = 'download.enqueue'),
    (SELECT rejection_reasons[1] FROM rss_entries WHERE id = $1)`, first.Candidates[0].EntryID).Scan(&acquisitions, &enqueueOperations, &reason); err != nil {
		t.Fatal(err)
	}
	if acquisitions != 0 || enqueueOperations != 0 || reason != rssTargetInLibraryReason {
		t.Fatalf("enqueue guard = acquisitions %d operations %d reason %q", acquisitions, enqueueOperations, reason)
	}

	second, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(1, "owner")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(second.Candidates) != 1 {
		t.Fatalf("second PersistPoll() = %#v, %v", second, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, second.Candidates[0]); err != nil {
		t.Fatalf("schedule processing owner: %v", err)
	}
	blocked, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(1, "alternate")},
	}, domain.RSSPollPersistOptions{})
	if err != nil {
		t.Fatalf("processing occupancy PersistPoll() error = %v", err)
	}
	if len(blocked.Candidates) != 0 {
		t.Fatalf("processing occupancy candidates = %v, want none", blocked.Candidates)
	}
	var processingReason string
	var processingFulfilled bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT rejection_reasons[1], imported_at IS NOT NULL
FROM rss_entries
WHERE subscription_id = $1 AND identity_key <> (SELECT identity_key FROM rss_entries WHERE id = $2)
ORDER BY discovered_at DESC LIMIT 1`, fixture.subscriptionID, second.Candidates[0].EntryID).Scan(&processingReason, &processingFulfilled); err != nil {
		t.Fatal(err)
	}
	if processingReason != rssTargetProcessingReason || processingFulfilled {
		t.Fatalf("processing block = reason %q fulfilled %t", processingReason, processingFulfilled)
	}
}

func TestRSSPollFiltersTargetImportedByAnotherManagedTaskIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	owner, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "managed-owner")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(owner.Candidates) != 1 {
		t.Fatalf("owner PersistPoll() = %#v, %v", owner, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, owner.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	var acquisitionID, downloadID, mappingID uuid.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT acquisition.id, download.id
FROM acquisitions AS acquisition
JOIN downloads AS download ON download.acquisition_id = acquisition.id
WHERE acquisition.rss_entry_id = $1`, owner.Candidates[0].EntryID).Scan(&acquisitionID, &downloadID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT id FROM episode_mappings WHERE profile_id = $1 AND source_season = 1 AND source_episode = 1`, fixture.profileID).Scan(&mappingID); err != nil {
		t.Fatal(err)
	}
	fileID, transcodeID, taskID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
UPDATE downloads SET status = 'materialized' WHERE id = $1;
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($2, $1, 0, 'Managed.S01E01.mkv', 1024, 'video', true, 1, 1);
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($3, $4, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1);
INSERT INTO episode_tasks (
    id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id,
    state, video_state, subtitle_state
) VALUES ($5, $6, $2, $7, $3, 'imported', 'video_ready', 'ass_ready');
INSERT INTO imports (
    id, task_id, status, destination_video_path, destination_subtitle_path, completed_at
) VALUES ($8, $5, 'succeeded', '/library/Managed/Managed-S02E01.mkv', '/library/Managed/Managed-S02E01.ass', now())`,
		downloadID, fileID, transcodeID, "managed-target-"+transcodeID.String(), taskID, acquisitionID, mappingID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	blocked, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "managed-alternate")},
	}, domain.RSSPollPersistOptions{})
	if err != nil {
		t.Fatalf("managed occupancy PersistPoll() error = %v", err)
	}
	if len(blocked.Candidates) != 0 {
		t.Fatalf("managed occupancy candidates = %v, want none", blocked.Candidates)
	}
	var reason, source string
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT rejection_reasons[1], fulfillment_source
FROM rss_entries
WHERE subscription_id = $1 AND id <> $2
ORDER BY discovered_at DESC LIMIT 1`, fixture.subscriptionID, owner.Candidates[0].EntryID).Scan(&reason, &source); err != nil {
		t.Fatal(err)
	}
	if reason != rssTargetImportedReason || source != rssFulfillmentManagedImport {
		t.Fatalf("managed occupancy = reason %q source %q", reason, source)
	}
}

func TestAgentCoordinateRefreshesMappedTargetBeforeApplyIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	persisted, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "agent-check")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(persisted.Candidates) != 1 {
		t.Fatalf("PersistPoll() = %#v, %v", persisted, err)
	}
	checkID := uuid.New()
	verifier := &rssRealtimeVerifierRecordingStub{checkID: checkID}
	agentService := NewAgentResolutionService(fixture.queries, database.NewTransactor(fixture.pool), fixture.workflow.operations, nil, nil, nil).
		WithRSSRealtimeTargetVerifier(verifier)
	got, err := agentService.verifyRSSProposalTargets(fixture.ctx, domain.AgentCapabilityRSSCoordinate, domain.AgentRSSCoordinateProposal{
		EntryID: persisted.Candidates[0].EntryID, SourceSeason: 1, SourceEpisode: 1,
	})
	if err != nil {
		t.Fatalf("verifyRSSProposalTargets() error = %v", err)
	}
	if got != checkID || verifier.subscriptionID != fixture.subscriptionID || len(verifier.coordinates) != 1 || verifier.coordinates[0] != (domain.EpisodeCoordinate{Season: 1, Episode: 1}) {
		t.Fatalf("verification = check %s subscription %s coordinates %#v", got, verifier.subscriptionID, verifier.coordinates)
	}
}

func TestAgentAdjudicationCannotOverrideTargetThatBecameOccupiedIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	feed := domain.RSSFeed{Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{
		targetFeedEntry(0, "agent-primary"), targetFeedEntry(0, "agent-alternate"),
	}}
	persisted, err := fixture.workflow.PersistPoll(
		fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, feed, domain.RSSPollPersistOptions{AdjudicateReleases: true},
	)
	if err != nil || len(persisted.AgentAdjudicationBatchIDs) != 1 {
		t.Fatalf("PersistPoll() = %#v, %v", persisted, err)
	}
	batchID := persisted.AgentAdjudicationBatchIDs[0]
	rows, err := fixture.pool.Query(fixture.ctx, `SELECT entry_id FROM rss_entry_adjudications WHERE batch_id = $1 ORDER BY entry_id`, batchID)
	if err != nil {
		t.Fatal(err)
	}
	entryIDs := make([]uuid.UUID, 0, 2)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		entryIDs = append(entryIDs, id)
	}
	rows.Close()
	if len(entryIDs) != 2 {
		t.Fatalf("staged entry count = %d, want 2", len(entryIDs))
	}
	fixture.addCatalogEpisode(t, 0)
	resolutionID, agentOperationID := uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'running', 3, 60);
INSERT INTO agent_resolutions (
    id, operation_id, capability, resource_type, resource_id, trigger, status,
    input_fingerprint, configuration_version, protocol, provider_origin, model, prompt_version, toolset_version
) VALUES (
    $2, $1, 'rss_release_adjudication', 'rss_adjudication_batch', $4, 'automatic', 'proposed',
    $5, 1, 'openai_chat_completions', 'https://provider.example', 'fixture-model', 'rss-release-adjudication-v2', 'agent-tools-v1'
)`, agentOperationID, resolutionID, "agent-target-"+resolutionID.String(), batchID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	season, episode := 1, 1
	proposal := domain.AgentRSSReleaseAdjudicationProposal{
		BatchID: batchID, ScopedEntryIDs: entryIDs, Decision: "resolved",
		Entries: []domain.AgentRSSReleaseDisposition{
			{EntryID: entryIDs[0], Disposition: "ignore", RelatedEntryID: &entryIDs[1], EvidenceCodes: []string{"alternate_release"}},
			{EntryID: entryIDs[1], Disposition: "select", SourceSeason: &season, SourceEpisode: &episode, EvidenceCodes: []string{"preferred_release"}},
		},
	}
	service := NewAgentResolutionService(fixture.queries, database.NewTransactor(fixture.pool), fixture.workflow.operations, nil, nil, nil)
	resolution := domain.AgentResolution{
		ID: resolutionID, OperationID: agentOperationID, Version: 1,
		Capability:   domain.AgentCapabilityRSSReleaseAdjudication,
		ResourceType: "rss_adjudication_batch", ResourceID: batchID, Status: domain.AgentResolutionProposed,
	}
	checkID := fixture.addRealtimeCheck(t, 0, true, time.Now())
	if err := service.applyRSSReleaseAdjudication(
		fixture.ctx, resolution, proposal,
		domain.AgentProposalValidation{Verdict: domain.AgentValidationAutoApplicable}, checkID,
	); err != nil {
		t.Fatalf("applyRSSReleaseAdjudication() error = %v", err)
	}
	var selectedState, selectedSource, reason, fulfillmentSource string
	var acquisitions int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT adjudication.state, adjudication.source, entry.rejection_reasons[1], entry.fulfillment_source,
       (SELECT count(*) FROM acquisitions AS acquisition WHERE acquisition.rss_entry_id = entry.id)
FROM rss_entry_adjudications AS adjudication
JOIN rss_entries AS entry ON entry.id = adjudication.entry_id
WHERE entry.id = $1`, entryIDs[1]).Scan(&selectedState, &selectedSource, &reason, &fulfillmentSource, &acquisitions); err != nil {
		t.Fatal(err)
	}
	if selectedState != "ignored" || selectedSource != "deterministic" || reason != rssTargetInLibraryReason || fulfillmentSource != rssFulfillmentEmbyCatalog || acquisitions != 0 {
		t.Fatalf("occupied Agent selection = state %q source %q reason %q fulfillment %q acquisitions %d", selectedState, selectedSource, reason, fulfillmentSource, acquisitions)
	}
}

func TestRSSPollReconcilesImportConflictThroughAcquisitionDeletionIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	candidateResult, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "failed-import")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(candidateResult.Candidates) != 1 {
		t.Fatalf("PersistPoll() = %#v, %v", candidateResult, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, candidateResult.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	acquisitionID, downloadID, mappingID, fileID, taskID, transcodeID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT acquisition.id, download.id FROM acquisitions AS acquisition JOIN downloads AS download ON download.acquisition_id = acquisition.id WHERE acquisition.rss_entry_id = $1`, candidateResult.Candidates[0].EntryID).Scan(&acquisitionID, &downloadID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT id FROM episode_mappings WHERE profile_id = $1 AND source_season = 1 AND source_episode = 1`, fixture.profileID).Scan(&mappingID); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
UPDATE downloads SET status = 'materialized' WHERE id = $1;
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($2, $1, 0, 'Target.S01E01.mkv', 1024, 'video', true, 1, 1);
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($3, $4, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1);
INSERT INTO episode_tasks (
    id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id,
    state, video_state, subtitle_state, failure_stage, error_code, error_message
) VALUES ($5, $6, $2, $7, $3, 'failed', 'video_ready', 'ass_ready', 'import', 'library_destination_conflict', 'fixture conflict');
INSERT INTO imports (id, task_id, status, error_code, error_message, completed_at)
VALUES ($8, $5, 'failed', 'library_destination_conflict', 'fixture conflict', now())`,
		downloadID, fileID, transcodeID, "rss-target-"+transcodeID.String(), taskID, acquisitionID, mappingID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	fixture.addCatalogEpisode(t, 0)
	if _, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{Title: "Target Occupancy"}, domain.RSSPollPersistOptions{}); err != nil {
		t.Fatalf("reconciliation PersistPoll() error = %v", err)
	}
	var deletionRequested, fulfilled, failureStageCleared, errorCodeCleared, errorMessageCleared bool
	var deleteOperations int
	var taskState string
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT acquisition.deletion_requested_at IS NOT NULL,
       entry.imported_at IS NOT NULL,
       (SELECT count(*) FROM operations WHERE kind = 'acquisition.delete' AND resource_id = acquisition.id),
       task.state,
       task.failure_stage IS NULL,
       task.error_code IS NULL,
       task.error_message IS NULL
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN episode_tasks AS task ON task.acquisition_id = acquisition.id
WHERE acquisition.id = $1`, acquisitionID).Scan(
		&deletionRequested,
		&fulfilled,
		&deleteOperations,
		&taskState,
		&failureStageCleared,
		&errorCodeCleared,
		&errorMessageCleared,
	); err != nil {
		t.Fatal(err)
	}
	if !deletionRequested || !fulfilled || deleteOperations != 1 || taskState != "cancelled" ||
		!failureStageCleared || !errorCodeCleared || !errorMessageCleared {
		t.Fatalf(
			"reconciliation = deletion %t fulfilled %t operations %d task %q failure cleared %t/%t/%t",
			deletionRequested, fulfilled, deleteOperations, taskState,
			failureStageCleared, errorCodeCleared, errorMessageCleared,
		)
	}
}

func TestRSSSubscriptionCompletesWhenEveryMappedTargetIsInCatalogIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	fixture.addCatalogEpisode(t, 0)
	fixture.addCatalogEpisode(t, 1)
	result, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "fulfilled"), targetFeedEntry(1, "fulfilled")},
	}, domain.RSSPollPersistOptions{AdjudicateReleases: true})
	if err != nil {
		t.Fatalf("PersistPoll() error = %v", err)
	}
	var enabled, completed bool
	var fulfilled int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT subscription.enabled, subscription.completed_at IS NOT NULL,
       (SELECT count(DISTINCT source_episode) FROM rss_entries WHERE subscription_id = subscription.id AND imported_at IS NOT NULL)
FROM rss_subscriptions AS subscription WHERE subscription.id = $1`, fixture.subscriptionID).Scan(&enabled, &completed, &fulfilled); err != nil {
		t.Fatal(err)
	}
	if enabled || !completed || fulfilled != 2 || len(result.Candidates) != 0 || len(result.AgentCoordinateCandidates) != 0 || len(result.AgentAdjudicationBatchIDs) != 0 {
		t.Fatalf("completion = enabled %t completed %t fulfilled %d result %#v", enabled, completed, fulfilled, result)
	}
}
