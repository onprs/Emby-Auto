//go:build integration

package database_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	appmigrations "github.com/onprs/emby-auto/backend/db/migrations"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func enqueueProvenanceData(acquisitionID, downloadID uuid.UUID, attempt int) string {
	return fmt.Sprintf(`{"acquisitionId":"%s","downloadId":"%s","downloadAttempt":%d}`, acquisitionID, downloadID, attempt)
}

func TestRSSSubscriptionProgressMigrationBackfillsThroughUnifiedReconciliationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	downgradeApplication(t, ctx, pool, 38)

	seriesID, subscriptionID := uuid.New(), uuid.New()
	entryID, acquisitionID, downloadID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Progress migration fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Progress migration fixture', $3, true, 900, 1)
`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, $3, 'Fixture S01E01', 'https://example.test/episode.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued')
`, entryID, subscriptionID, "guid:"+entryID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, acquisitionID, seriesID, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status, progress) VALUES ($1, $2, 'downloading', 0.5)`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}

	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("Migrate() from v38 error = %v", err)
	}
	var triggerCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM pg_trigger
WHERE NOT tgisinternal
  AND tgname LIKE 'rss_subscription_progress_%_changes'
`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 13 {
		t.Fatalf("progress dependency trigger count = %d, want 13", triggerCount)
	}
	var sortIndex, dirtyIndex string
	if err := pool.QueryRow(ctx, `
SELECT COALESCE(to_regclass('rss_subscription_progress_sort_idx')::text, ''),
       COALESCE(to_regclass('rss_subscription_progress_dirty_idx')::text, '')
`).Scan(&sortIndex, &dirtyIndex); err != nil {
		t.Fatal(err)
	}
	if sortIndex == "" || dirtyIndex == "" {
		t.Fatalf("progress indexes = sort %v dirty %v", sortIndex, dirtyIndex)
	}
	var dirty bool
	var sourceRevision, calculatedRevision int64
	var modelVersion int32
	if err := pool.QueryRow(ctx, `
SELECT dirty, source_revision, calculated_revision, model_version
FROM rss_subscription_progress
WHERE subscription_id = $1
`, subscriptionID).Scan(&dirty, &sourceRevision, &calculatedRevision, &modelVersion); err != nil {
		t.Fatal(err)
	}
	if !dirty || sourceRevision != 1 || calculatedRevision != 0 || modelVersion != 0 {
		t.Fatalf("backfill row = dirty %t revisions %d/%d model %d", dirty, sourceRevision, calculatedRevision, modelVersion)
	}

	workflow := service.NewRSSWorkflow(db.New(pool), database.NewTransactor(pool), nil)
	reconciled, err := workflow.ReconcileSubscriptionProgress(ctx)
	if err != nil || reconciled != 1 {
		t.Fatalf("ReconcileSubscriptionProgress() = %d, %v, want 1, nil", reconciled, err)
	}
	detail, err := workflow.GetSubscription(ctx, subscriptionID)
	if err != nil {
		t.Fatalf("GetSubscription() error = %v", err)
	}
	if detail.OverallProgress != 0.16 || detail.TaskCount != 1 || detail.CompletedTaskCount != 0 || detail.AttentionTaskCount != 0 {
		t.Fatalf("reconciled detail = %#v, want progress 0.16 and counts 1/0/0", detail)
	}
	replayed, err := workflow.ReconcileSubscriptionProgress(ctx)
	if err != nil || replayed != 0 {
		t.Fatalf("replayed ReconcileSubscriptionProgress() = %d, %v, want 0, nil", replayed, err)
	}
}

func TestRSSAcquisitionProvenanceMigrationBackfillsAndRestoresRollbackBoundaryIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	downgradeApplication(t, ctx, pool, 39)

	seriesID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	acquisitionID, downloadID, taskID := uuid.New(), uuid.New(), uuid.New()
	profileID, fileID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Provenance migration fixture')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Provenance migration fixture', $3, true, 900, 1)
`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status, imported_at
) VALUES ($1, $2, $3, 'Fixture S01E01', 'https://example.test/episode.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued', $4)
`, entryID, subscriptionID, "guid:"+entryID.String(), time.Date(2026, 7, 1, 12, 6, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, acquisitionID, seriesID, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container,
    file_extension, quality_mode, quality_value, audio_policy, preset,
    pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)
`, profileID, "migration-provenance-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, attempt, status) VALUES ($1, $2, 1, 'enqueue_pending')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($1, $2, 0, $3, 1024, 'video', true)
`, fileID, downloadID, "migration-"+fileID.String()+".mkv"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id)
VALUES ($1, $2, $3, $4)
`, taskID, acquisitionID, fileID, profileID); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO events (topic, resource_type, resource_id, data, occurred_at)
VALUES
    ('rss.entry.enqueueing', 'rss_entry', $1::uuid, jsonb_build_object('acquisitionId', $2::uuid, 'downloadId', $3::uuid, 'downloadAttempt', 1, 'enqueueAttempt', 1), $4::timestamptz),
    ('task.created', 'episode_task', $5::uuid, jsonb_build_object('downloadId', $3::uuid), $6::timestamptz),
    ('task.video_ready', 'episode_task', $5::uuid, '{}'::jsonb, $7::timestamptz),
    ('task.imported', 'episode_task', $5::uuid, '{}'::jsonb, $8::timestamptz),
    ('acquisition.delete_completed', 'acquisition', $2::uuid, '{}'::jsonb, $9::timestamptz)
`, entryID, acquisitionID, downloadID, base, taskID, base.Add(time.Minute), base.Add(2*time.Minute), base.Add(6*time.Minute), base.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("Migrate() from v39 error = %v", err)
	}
	assertProvenanceMigrationRow(t, ctx, pool, acquisitionID, downloadID, taskID)
	var discardable bool
	if err := pool.QueryRow(ctx, `SELECT event_is_discardable('task.created')`).Scan(&discardable); err != nil {
		t.Fatal(err)
	}
	if !discardable {
		t.Fatal("task.created remained protected after provenance migration")
	}

	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("repeated Migrate() at v40 error = %v", err)
	}
	assertProvenanceMigrationRow(t, ctx, pool, acquisitionID, downloadID, taskID)

	downgradeApplication(t, ctx, pool, 39)
	var tableExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('rss_acquisition_provenance') IS NOT NULL`).Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if tableExists {
		t.Fatal("provenance table still exists after migration 40 down")
	}
	var droppedIndexes, droppedHelpers int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM pg_indexes
WHERE indexname IN (
    'rss_acquisition_provenance_entry_idx'
)
`).Scan(&droppedIndexes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM pg_proc
WHERE proname IN ('rss_provenance_uuid', 'rss_provenance_positive_int', 'sync_rss_acquisition_provenance_from_event')
`).Scan(&droppedHelpers); err != nil {
		t.Fatal(err)
	}
	if droppedIndexes != 0 || droppedHelpers != 0 {
		t.Fatalf("migration 40 down dependencies = indexes %d helpers %d, want 0/0", droppedIndexes, droppedHelpers)
	}
	if err := pool.QueryRow(ctx, `SELECT event_is_discardable('task.created')`).Scan(&discardable); err != nil {
		t.Fatal(err)
	}
	if discardable {
		t.Fatal("migration 40 down did not restore protected task.created topic")
	}

	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("Migrate() after rollback boundary error = %v", err)
	}
	assertProvenanceMigrationRow(t, ctx, pool, acquisitionID, downloadID, taskID)
}

func assertProvenanceMigrationRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, acquisitionID, downloadID, taskID uuid.UUID) {
	t.Helper()
	var gotDownloadID, gotTaskID uuid.UUID
	var gotAttempt int
	var gotVideoReady time.Time
	if err := pool.QueryRow(ctx, `
SELECT download_id, download_attempt, task_id, video_ready_at
FROM rss_acquisition_provenance
WHERE acquisition_id = $1
`, acquisitionID).Scan(&gotDownloadID, &gotAttempt, &gotTaskID, &gotVideoReady); err != nil {
		t.Fatal(err)
	}
	if gotDownloadID != downloadID || gotAttempt != 1 || gotTaskID != taskID || gotVideoReady.IsZero() {
		t.Fatalf("backfilled provenance = %s/attempt%d/%s/%v, want %s/attempt1/%s/nonzero", gotDownloadID, gotAttempt, gotTaskID, gotVideoReady, downloadID, taskID)
	}
}

func TestRSSAcquisitionProvenanceMigrationRejectsUnverifiedAssociationHistoryIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	downgradeApplication(t, ctx, pool, 39)

	seriesID, subscriptionID := uuid.New(), uuid.New()
	entries := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	acquisitionIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	downloads := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	profileID := uuid.New()
	files := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	tasks := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Provenance association migration fixture')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Provenance association migration fixture', $3, true, 900, 1)
`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	for index, entryID := range entries {
		if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, $3, $4, $5, true, ARRAY[]::text[], 1, $6, 'enqueued')
`, entryID, subscriptionID, "guid:"+entryID.String(), fmt.Sprintf("Fixture S01E%02d", index+1), "https://example.test/"+entryID.String()+".torrent", index+1); err != nil {
			t.Fatal(err)
		}
	}
	for index, acquisitionID := range acquisitionIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, acquisitionID, seriesID, entries[index]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container,
    file_extension, quality_mode, quality_value, audio_policy, preset,
    pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)
`, profileID, "migration-association-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	for index, downloadID := range downloads {
		if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, attempt, status) VALUES ($1, $2, 1, 'enqueue_pending')`, downloadID, acquisitionIDs[index]); err != nil {
			t.Fatal(err)
		}
	}
	for index, fileID := range []uuid.UUID{files[0], files[1], files[2]} {
		if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($1, $2, 0, $3, 1024, 'video', true)
`, fileID, downloads[[]int{0, 2, 3}[index]], "migration-association-"+fileID.String()+".mkv"); err != nil {
			t.Fatal(err)
		}
	}
	for index, taskID := range []uuid.UUID{tasks[0], tasks[1], tasks[2]} {
		if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id)
VALUES ($1, $2, $3, $4)
`, taskID, acquisitionIDs[[]int{0, 2, 3}[index]], files[index], profileID); err != nil {
			t.Fatal(err)
		}
	}

	insertEvent := func(topic, resourceType string, resourceID uuid.UUID, data string, occurredAt time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
INSERT INTO events (topic, resource_type, resource_id, data, occurred_at)
VALUES ($1, $2, $3, $4::jsonb, $5)
`, topic, resourceType, resourceID, data, occurredAt); err != nil {
			t.Fatalf("insert %s event: %v", topic, err)
		}
	}
	base := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	// Live task T1 is sourced from D1, while task.created claims D2.
	insertEvent("rss.entry.enqueueing", "rss_entry", entries[0], enqueueProvenanceData(acquisitionIDs[0], downloads[0], 1), base)
	// The entry and acquisition are correct, but D2 belongs to A2 and must not be accepted for A1.
	insertEvent("rss.entry.enqueueing", "rss_entry", entries[0], enqueueProvenanceData(acquisitionIDs[0], downloads[1], 1), base.Add(30*time.Second))
	insertEvent("task.created", "episode_task", tasks[0], `{"downloadId":"`+downloads[1].String()+`"}`, base.Add(time.Minute))
	insertEvent("task.imported", "episode_task", tasks[0], `{}`, base.Add(2*time.Minute))
	// Live A2 belongs to E2, but its enqueue event claims E3.
	insertEvent("rss.entry.enqueueing", "rss_entry", entries[2], enqueueProvenanceData(acquisitionIDs[1], downloads[1], 1), base.Add(3*time.Minute))
	// Deleted A3 has a complete and ordered historical chain.
	insertEvent("rss.entry.enqueueing", "rss_entry", entries[2], enqueueProvenanceData(acquisitionIDs[2], downloads[2], 1), base.Add(4*time.Minute))
	insertEvent("task.created", "episode_task", tasks[1], `{"downloadId":"`+downloads[2].String()+`"}`, base.Add(5*time.Minute))
	insertEvent("task.video_ready", "episode_task", tasks[1], `{}`, base.Add(6*time.Minute))
	insertEvent("task.imported", "episode_task", tasks[1], `{}`, base.Add(7*time.Minute))
	insertEvent("acquisition.delete_completed", "acquisition", acquisitionIDs[2], `{}`, base.Add(8*time.Minute))
	// Deleted A4 has two competing entry identities, so its chain is not deterministic.
	insertEvent("rss.entry.enqueueing", "rss_entry", entries[3], enqueueProvenanceData(acquisitionIDs[3], downloads[3], 1), base.Add(9*time.Minute))
	insertEvent("rss.entry.enqueueing", "rss_entry", entries[4], enqueueProvenanceData(acquisitionIDs[3], downloads[3], 1), base.Add(10*time.Minute))
	insertEvent("task.created", "episode_task", tasks[2], `{"downloadId":"`+downloads[3].String()+`"}`, base.Add(11*time.Minute))
	insertEvent("task.imported", "episode_task", tasks[2], `{}`, base.Add(12*time.Minute))
	insertEvent("acquisition.delete_completed", "acquisition", acquisitionIDs[3], `{}`, base.Add(13*time.Minute))
	// Deleted A5 has only enqueue/delete evidence and must not become a success.
	insertEvent("rss.entry.enqueueing", "rss_entry", entries[4], enqueueProvenanceData(acquisitionIDs[4], downloads[4], 1), base.Add(14*time.Minute))
	insertEvent("acquisition.delete_completed", "acquisition", acquisitionIDs[4], `{}`, base.Add(15*time.Minute))
	if _, err := pool.Exec(ctx, `DELETE FROM episode_tasks WHERE id = ANY($1::uuid[])`, []uuid.UUID{tasks[1], tasks[2]}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM acquisitions WHERE id = ANY($1::uuid[])`, []uuid.UUID{acquisitionIDs[2], acquisitionIDs[3], acquisitionIDs[4]}); err != nil {
		t.Fatal(err)
	}

	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("Migrate() from v39 error = %v", err)
	}
	var liveDownload, liveTask, livePending *uuid.UUID
	var liveImported bool
	if err := pool.QueryRow(ctx, `
SELECT download_id, task_id, pending_download_id, imported_at IS NOT NULL
FROM rss_acquisition_provenance
WHERE acquisition_id = $1
`, acquisitionIDs[0]).Scan(&liveDownload, &liveTask, &livePending, &liveImported); err != nil {
		t.Fatal(err)
	}
	if liveDownload != nil || liveTask != nil || livePending == nil || *livePending != downloads[0] || liveImported {
		t.Fatalf("live source mismatch provenance = %v/%v/%v/imported=%t, want D1 pending and no success", liveDownload, liveTask, livePending, liveImported)
	}
	// A legal replay after migration must use the retained D1 pending fact and complete normally.
	insertEvent("task.created", "episode_task", tasks[0], `{"downloadId":"`+downloads[0].String()+`"}`, base.Add(16*time.Minute))
	insertEvent("task.imported", "episode_task", tasks[0], `{}`, base.Add(17*time.Minute))
	var replayDownload, replayTask, replayPending *uuid.UUID
	var replayAttempt int
	var replayImported bool
	if err := pool.QueryRow(ctx, `
SELECT download_id, task_id, pending_download_id, download_attempt, imported_at IS NOT NULL
FROM rss_acquisition_provenance
WHERE acquisition_id = $1
`, acquisitionIDs[0]).Scan(&replayDownload, &replayTask, &replayPending, &replayAttempt, &replayImported); err != nil {
		t.Fatal(err)
	}
	if replayDownload == nil || *replayDownload != downloads[0] || replayTask == nil || *replayTask != tasks[0] || replayPending != nil || replayAttempt != 1 || !replayImported {
		t.Fatalf("legal replay provenance = %v/%v/pending=%v/attempt=%d/imported=%t, want D1/T1 success", replayDownload, replayTask, replayPending, replayAttempt, replayImported)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_acquisition_provenance WHERE acquisition_id = $1`, acquisitionIDs[1]).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("live acquisition/entry mismatch rows = %d, want 0", count)
	}
	assertProvenanceMigrationRow(t, ctx, pool, acquisitionIDs[2], downloads[2], tasks[1])
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_acquisition_provenance WHERE acquisition_id = $1`, acquisitionIDs[3]).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("deleted conflicting history rows = %d, want 0", count)
	}
	var incompleteTask *uuid.UUID
	var incompleteImported, incompleteArchived bool
	if err := pool.QueryRow(ctx, `
SELECT task_id, imported_at IS NOT NULL, archived_at IS NOT NULL
FROM rss_acquisition_provenance
WHERE acquisition_id = $1
`, acquisitionIDs[4]).Scan(&incompleteTask, &incompleteImported, &incompleteArchived); err != nil {
		t.Fatal(err)
	}
	if incompleteTask != nil || incompleteImported || !incompleteArchived {
		t.Fatalf("deleted incomplete history = %v/imported=%t/archived=%t, want archive-only", incompleteTask, incompleteImported, incompleteArchived)
	}
}

type migrationProvenanceFixture struct {
	entries      map[string]uuid.UUID
	acquisitions map[string]uuid.UUID
	downloads    map[string]uuid.UUID
	tasks        map[string]uuid.UUID
}

type migrationProvenanceEvent struct {
	topic        string
	resourceType string
	resourceID   uuid.UUID
	data         string
	occurredAt   time.Time
}

type migrationProvenanceExpectation struct {
	rows                   int
	download               string
	task                   string
	pendingDownload        string
	pendingTask            string
	downloadAttempt        int
	pendingDownloadAttempt int
	videoReady             bool
	subtitleReady          bool
	artifactReady          bool
	reviewed               bool
	pendingVideoReady      bool
	pendingSubtitleReady   bool
	pendingArtifactReady   bool
	pendingReviewed        bool
	imported               bool
	archived               bool
}

func seedMigrationProvenanceFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) migrationProvenanceFixture {
	t.Helper()
	fixture := migrationProvenanceFixture{
		entries:      map[string]uuid.UUID{"e1": uuid.New(), "e2": uuid.New()},
		acquisitions: map[string]uuid.UUID{"a1": uuid.New(), "a2": uuid.New()},
		downloads:    map[string]uuid.UUID{"d1": uuid.New(), "d2": uuid.New(), "d3": uuid.New()},
		tasks:        map[string]uuid.UUID{"t1": uuid.New(), "t2": uuid.New(), "t3": uuid.New()},
	}
	seriesID, subscriptionID, profileID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Provenance state fixture')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Provenance state fixture', $3, true, 900, 1)
`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container,
    file_extension, quality_mode, quality_value, audio_policy, preset,
    pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)
`, profileID, "provenance-state-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	for index, key := range []string{"e1", "e2"} {
		if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, $3, $4, $5, true, ARRAY[]::text[], 1, $6, 'enqueued')
`, fixture.entries[key], subscriptionID, "guid:"+fixture.entries[key].String(), "Fixture "+key, "https://example.test/"+key+".torrent", index+1); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id)
VALUES ($1, $2, 'rss', $3)
`, fixture.acquisitions["a"+fmt.Sprint(index+1)], seriesID, fixture.entries[key]); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		key         string
		acquisition string
		attempt     int
	}{
		{key: "d1", acquisition: "a1", attempt: 1},
		{key: "d2", acquisition: "a1", attempt: 2},
		{key: "d3", acquisition: "a2", attempt: 1},
	} {
		if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, attempt, status)
VALUES ($1, $2, $3, 'enqueue_pending')
`, fixture.downloads[item.key], fixture.acquisitions[item.acquisition], item.attempt); err != nil {
			t.Fatal(err)
		}
		fileID := uuid.New()
		if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($1, $2, 0, $3, 1024, 'video', true)
`, fileID, fixture.downloads[item.key], "state-"+item.key+".mkv"); err != nil {
			t.Fatal(err)
		}
		taskKey := map[string]string{"d1": "t1", "d2": "t2", "d3": "t3"}[item.key]
		if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id)
VALUES ($1, $2, $3, $4)
`, fixture.tasks[taskKey], fixture.acquisitions[item.acquisition], fileID, profileID); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func migrationEnqueueEvent(fixture migrationProvenanceFixture, acquisition, entry, download string, attempt int, occurredAt time.Time) migrationProvenanceEvent {
	return migrationProvenanceEvent{
		topic:        "rss.entry.enqueueing",
		resourceType: "rss_entry",
		resourceID:   fixture.entries[entry],
		data:         enqueueProvenanceData(fixture.acquisitions[acquisition], fixture.downloads[download], attempt),
		occurredAt:   occurredAt,
	}
}

func migrationTaskCreatedEvent(fixture migrationProvenanceFixture, task, download string, occurredAt time.Time) migrationProvenanceEvent {
	return migrationProvenanceEvent{
		topic:        "task.created",
		resourceType: "episode_task",
		resourceID:   fixture.tasks[task],
		data:         `{"downloadId":"` + fixture.downloads[download].String() + `"}`,
		occurredAt:   occurredAt,
	}
}

func migrationLegacyEnqueueEvent(fixture migrationProvenanceFixture, acquisition, entry, download string, enqueueAttempt int, occurredAt time.Time) migrationProvenanceEvent {
	return migrationProvenanceEvent{
		topic:        "rss.entry.enqueueing",
		resourceType: "rss_entry",
		resourceID:   fixture.entries[entry],
		data:         fmt.Sprintf(`{"acquisitionId":"%s","downloadId":"%s","enqueueAttempt":%d}`, fixture.acquisitions[acquisition], fixture.downloads[download], enqueueAttempt),
		occurredAt:   occurredAt,
	}
}

func migrationTaskMilestoneEvent(fixture migrationProvenanceFixture, task, topic string, occurredAt time.Time) migrationProvenanceEvent {
	return migrationProvenanceEvent{
		topic:        topic,
		resourceType: "episode_task",
		resourceID:   fixture.tasks[task],
		data:         `{}`,
		occurredAt:   occurredAt,
	}
}

func migrationTaskImportedEvent(fixture migrationProvenanceFixture, task string, occurredAt time.Time) migrationProvenanceEvent {
	return migrationProvenanceEvent{
		topic:        "task.imported",
		resourceType: "episode_task",
		resourceID:   fixture.tasks[task],
		data:         `{}`,
		occurredAt:   occurredAt,
	}
}

func migrationDeleteEvent(fixture migrationProvenanceFixture, acquisition string, occurredAt time.Time) migrationProvenanceEvent {
	return migrationProvenanceEvent{
		topic:        "acquisition.delete_completed",
		resourceType: "acquisition",
		resourceID:   fixture.acquisitions[acquisition],
		data:         `{}`,
		occurredAt:   occurredAt,
	}
}

func insertMigrationProvenanceEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, events []migrationProvenanceEvent) {
	t.Helper()
	for _, event := range events {
		if _, err := pool.Exec(ctx, `
INSERT INTO events (topic, resource_type, resource_id, data, occurred_at)
VALUES ($1, $2, $3, $4::jsonb, $5)
`, event.topic, event.resourceType, event.resourceID, event.data, event.occurredAt); err != nil {
			t.Fatalf("insert %s event: %v", event.topic, err)
		}
	}
}

func deleteMigrationFixtureAcquisition(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture migrationProvenanceFixture, acquisition string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM episode_tasks WHERE acquisition_id = $1`, fixture.acquisitions[acquisition]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM acquisitions WHERE id = $1`, fixture.acquisitions[acquisition]); err != nil {
		t.Fatal(err)
	}
}

func assertMigrationProvenanceExpectation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture migrationProvenanceFixture, acquisition string, want migrationProvenanceExpectation) {
	t.Helper()
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_acquisition_provenance WHERE acquisition_id = $1`, fixture.acquisitions[acquisition]).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != want.rows {
		t.Fatalf("provenance rows = %d, want %d", rows, want.rows)
	}
	if want.rows == 0 {
		return
	}
	var gotDownload, gotTask, gotPendingDownload, gotPendingTask *uuid.UUID
	var gotDownloadAttempt, gotPendingAttempt *int
	var gotVideoReady, gotSubtitleReady, gotArtifactReady, gotReviewed bool
	var gotPendingVideoReady, gotPendingSubtitleReady, gotPendingArtifactReady, gotPendingReviewed bool
	var gotImported, gotArchived bool
	if err := pool.QueryRow(ctx, `
SELECT download_id, task_id, download_attempt,
       pending_download_id, pending_task_id, pending_download_attempt,
       video_ready_at IS NOT NULL, subtitle_ready_at IS NOT NULL,
       artifact_ready_at IS NOT NULL, reviewed_at IS NOT NULL,
       pending_video_ready_at IS NOT NULL, pending_subtitle_ready_at IS NOT NULL,
       pending_artifact_ready_at IS NOT NULL, pending_reviewed_at IS NOT NULL,
       imported_at IS NOT NULL, archived_at IS NOT NULL
FROM rss_acquisition_provenance
WHERE acquisition_id = $1
`, fixture.acquisitions[acquisition]).Scan(
		&gotDownload, &gotTask, &gotDownloadAttempt,
		&gotPendingDownload, &gotPendingTask, &gotPendingAttempt,
		&gotVideoReady, &gotSubtitleReady, &gotArtifactReady, &gotReviewed,
		&gotPendingVideoReady, &gotPendingSubtitleReady, &gotPendingArtifactReady, &gotPendingReviewed,
		&gotImported, &gotArchived,
	); err != nil {
		t.Fatal(err)
	}
	wantDownload := fixture.downloads[want.download]
	wantTask := fixture.tasks[want.task]
	wantPendingDownload := fixture.downloads[want.pendingDownload]
	wantPendingTask := fixture.tasks[want.pendingTask]
	if want.download == "" && gotDownload != nil || want.download != "" && (gotDownload == nil || *gotDownload != wantDownload) {
		t.Fatalf("download = %v, want %q", gotDownload, want.download)
	}
	if want.task == "" && gotTask != nil || want.task != "" && (gotTask == nil || *gotTask != wantTask) {
		t.Fatalf("task = %v, want %q", gotTask, want.task)
	}
	if want.pendingDownload == "" && gotPendingDownload != nil || want.pendingDownload != "" && (gotPendingDownload == nil || *gotPendingDownload != wantPendingDownload) {
		t.Fatalf("pending download = %v, want %q", gotPendingDownload, want.pendingDownload)
	}
	if want.pendingTask == "" && gotPendingTask != nil || want.pendingTask != "" && (gotPendingTask == nil || *gotPendingTask != wantPendingTask) {
		t.Fatalf("pending task = %v, want %q", gotPendingTask, want.pendingTask)
	}
	if want.downloadAttempt == 0 && gotDownloadAttempt != nil || want.downloadAttempt != 0 && (gotDownloadAttempt == nil || *gotDownloadAttempt != want.downloadAttempt) {
		t.Fatalf("download attempt = %v, want %d", gotDownloadAttempt, want.downloadAttempt)
	}
	if want.pendingDownloadAttempt == 0 && gotPendingAttempt != nil || want.pendingDownloadAttempt != 0 && (gotPendingAttempt == nil || *gotPendingAttempt != want.pendingDownloadAttempt) {
		t.Fatalf("pending attempt = %v, want %d", gotPendingAttempt, want.pendingDownloadAttempt)
	}
	if gotVideoReady != want.videoReady || gotSubtitleReady != want.subtitleReady || gotArtifactReady != want.artifactReady || gotReviewed != want.reviewed ||
		gotPendingVideoReady != want.pendingVideoReady || gotPendingSubtitleReady != want.pendingSubtitleReady || gotPendingArtifactReady != want.pendingArtifactReady || gotPendingReviewed != want.pendingReviewed {
		t.Fatalf("milestone flags = success video %t subtitle %t artifact %t reviewed %t; pending video %t subtitle %t artifact %t reviewed %t, want success video %t subtitle %t artifact %t reviewed %t; pending video %t subtitle %t artifact %t reviewed %t",
			gotVideoReady, gotSubtitleReady, gotArtifactReady, gotReviewed,
			gotPendingVideoReady, gotPendingSubtitleReady, gotPendingArtifactReady, gotPendingReviewed,
			want.videoReady, want.subtitleReady, want.artifactReady, want.reviewed,
			want.pendingVideoReady, want.pendingSubtitleReady, want.pendingArtifactReady, want.pendingReviewed)
	}
	if gotImported != want.imported || gotArchived != want.archived {
		t.Fatalf("terminal flags = imported %t archived %t, want imported %t archived %t", gotImported, gotArchived, want.imported, want.archived)
	}
}

func TestRSSAcquisitionProvenanceMigrationStateMachineCasesIntegration(t *testing.T) {
	type migrationCase struct {
		name    string
		before  func(migrationProvenanceFixture, time.Time) []migrationProvenanceEvent
		after   func(migrationProvenanceFixture, time.Time) []migrationProvenanceEvent
		prepare func(*testing.T, context.Context, *pgxpool.Pool, migrationProvenanceFixture)
		want    migrationProvenanceExpectation
		check   func(*testing.T, context.Context, *pgxpool.Pool, migrationProvenanceFixture)
	}
	cases := []migrationCase{
		{
			name: "live cross owner download is rejected",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{migrationEnqueueEvent(f, "a1", "e1", "d3", 2, base)}
			},
			want: migrationProvenanceExpectation{},
		},
		{
			name: "deleted cross owner soft deleted download is rejected",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d3", 2, base),
					migrationDeleteEvent(f, "a1", base.Add(time.Minute)),
				}
			},
			prepare: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				if _, err := pool.Exec(ctx, `UPDATE downloads SET deleted_at = now() WHERE id = $1`, f.downloads["d3"]); err != nil {
					t.Fatal(err)
				}
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a1")
			},
			want: migrationProvenanceExpectation{},
		},
		{
			name: "deleted missing download with complete chain succeeds",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(2*time.Minute)),
					migrationDeleteEvent(f, "a1", base.Add(3*time.Minute)),
				}
			},
			prepare: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a1")
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, imported: true, archived: true},
		},
		{
			name: "deleted missing download with mismatched task chain stays pending",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d2", base.Add(time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(2*time.Minute)),
					migrationDeleteEvent(f, "a1", base.Add(3*time.Minute)),
				}
			},
			prepare: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a1")
			},
			want: migrationProvenanceExpectation{rows: 1, pendingDownload: "d1", pendingDownloadAttempt: 1, archived: true},
		},
		{
			name: "higher attempt before old import leaves higher pending",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationEnqueueEvent(f, "a1", "e1", "d2", 2, base.Add(2*time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(3*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{rows: 1, pendingDownload: "d2", pendingDownloadAttempt: 2},
		},
		{
			name: "success before higher attempt remains success",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(2*time.Minute)),
					migrationEnqueueEvent(f, "a1", "e1", "d2", 2, base.Add(3*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, imported: true},
		},
		{
			name: "higher attempt succeeds after superseding old chain",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationEnqueueEvent(f, "a1", "e1", "d2", 2, base.Add(2*time.Minute)),
					migrationTaskCreatedEvent(f, "t2", "d2", base.Add(3*time.Minute)),
					migrationTaskImportedEvent(f, "t2", base.Add(4*time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(5*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d2", task: "t2", downloadAttempt: 2, imported: true},
		},
		{
			name: "duplicate enqueue keeps task for late import",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base.Add(2*time.Minute)),
				}
			},
			after: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{migrationTaskImportedEvent(f, "t1", base.Add(3*time.Minute))}
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, imported: true},
		},
		{
			name: "duplicate enqueue after import does not replace success",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(2*time.Minute)),
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base.Add(3*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, imported: true},
		},
		{
			name: "same timestamp duplicate task replay keeps late import owner",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base),
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base),
				}
			},
			after: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{migrationTaskImportedEvent(f, "t1", base)}
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, imported: true},
		},
		{
			name: "legacy duplicate hard deleted chain keeps one attempt",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationLegacyEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationLegacyEnqueueEvent(f, "a1", "e1", "d1", 2, base.Add(2*time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(3*time.Minute)),
					migrationDeleteEvent(f, "a1", base.Add(4*time.Minute)),
				}
			},
			prepare: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a1")
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, imported: true, archived: true},
		},
		{
			name: "legacy different downloads use stable first order",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationLegacyEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationLegacyEnqueueEvent(f, "a1", "e1", "d2", 1, base.Add(time.Minute)),
					migrationTaskCreatedEvent(f, "t2", "d2", base.Add(2*time.Minute)),
					migrationTaskImportedEvent(f, "t2", base.Add(3*time.Minute)),
					migrationDeleteEvent(f, "a1", base.Add(4*time.Minute)),
				}
			},
			prepare: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a1")
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d2", task: "t2", downloadAttempt: 2, imported: true, archived: true},
		},
		{
			name: "explicit and legacy attempts use non-colliding priority",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationLegacyEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationEnqueueEvent(f, "a1", "e1", "d2", 1, base.Add(time.Minute)),
					migrationTaskCreatedEvent(f, "t2", "d2", base.Add(2*time.Minute)),
					migrationTaskImportedEvent(f, "t2", base.Add(3*time.Minute)),
					migrationDeleteEvent(f, "a1", base.Add(4*time.Minute)),
				}
			},
			prepare: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a1")
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d2", task: "t2", downloadAttempt: 1, imported: true, archived: true},
		},
		{
			name: "first deletion blocks stale chain and duplicate deletion",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationLegacyEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationDeleteEvent(f, "a1", base.Add(time.Minute)),
					migrationLegacyEnqueueEvent(f, "a1", "e1", "d1", 2, base.Add(2*time.Minute)),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(3*time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(4*time.Minute)),
					migrationDeleteEvent(f, "a1", base.Add(5*time.Minute)),
				}
			},
			prepare: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a1")
			},
			want: migrationProvenanceExpectation{rows: 1, pendingDownload: "d1", pendingDownloadAttempt: 1, archived: true},
		},
		{
			name: "preterminal success survives duplicate deletion",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(2*time.Minute)),
					migrationDeleteEvent(f, "a1", base.Add(3*time.Minute)),
					migrationDeleteEvent(f, "a1", base.Add(3*time.Minute)),
				}
			},
			prepare: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a1")
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, imported: true, archived: true},
		},
		{
			name: "milestone before task created is ignored",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskMilestoneEvent(f, "t1", "task.video_ready", base.Add(time.Minute)),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(2*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{rows: 1, pendingDownload: "d1", pendingTask: "t1", pendingDownloadAttempt: 1},
		},
		{
			name: "milestone after task created is retained",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.video_ready", base.Add(2*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{rows: 1, pendingDownload: "d1", pendingTask: "t1", pendingDownloadAttempt: 1, pendingVideoReady: true},
		},
		{
			name: "milestone after import is ignored",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(2*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.video_ready", base.Add(3*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.subtitle_ready", base.Add(4*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.awaiting_review", base.Add(5*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.reviewed", base.Add(6*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, imported: true},
		},
		{
			name: "pre-import milestones survive post-import replay",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.video_ready", base.Add(2*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.subtitle_ready", base.Add(3*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.awaiting_review", base.Add(4*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.reviewed", base.Add(5*time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(6*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.video_ready", base.Add(7*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.subtitle_ready", base.Add(8*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.awaiting_review", base.Add(9*time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.reviewed", base.Add(10*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{
				rows: 1, download: "d1", task: "t1", downloadAttempt: 1,
				videoReady: true, subtitleReady: true, artifactReady: true, reviewed: true, imported: true,
			},
		},
		{
			name: "duplicate task created keeps generation milestone",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.video_ready", base.Add(2*time.Minute)),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(3*time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(4*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, videoReady: true, imported: true},
		},
		{
			name: "milestone timestamp tie follows event sequence",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationTaskMilestoneEvent(f, "t1", "task.video_ready", base.Add(time.Minute)),
					migrationTaskCreatedEvent(f, "t1", "d1", base.Add(time.Minute)),
					migrationTaskMilestoneEvent(f, "t1", "task.video_ready", base.Add(time.Minute)),
					migrationTaskImportedEvent(f, "t1", base.Add(2*time.Minute)),
				}
			},
			want: migrationProvenanceExpectation{rows: 1, download: "d1", task: "t1", downloadAttempt: 1, videoReady: true, imported: true},
		},
		{
			name: "hard deleted task identity rejects two owners",
			before: func(f migrationProvenanceFixture, base time.Time) []migrationProvenanceEvent {
				return []migrationProvenanceEvent{
					migrationEnqueueEvent(f, "a1", "e1", "d1", 1, base),
					migrationEnqueueEvent(f, "a2", "e2", "d3", 1, base.Add(time.Minute)),
					migrationTaskCreatedEvent(f, "t2", "d1", base.Add(2*time.Minute)),
					migrationTaskCreatedEvent(f, "t2", "d3", base.Add(3*time.Minute)),
					migrationTaskImportedEvent(f, "t2", base.Add(4*time.Minute)),
					migrationDeleteEvent(f, "a1", base.Add(5*time.Minute)),
					migrationDeleteEvent(f, "a2", base.Add(6*time.Minute)),
				}
			},
			prepare: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a1")
				deleteMigrationFixtureAcquisition(t, ctx, pool, f, "a2")
			},
			want: migrationProvenanceExpectation{rows: 1, pendingDownload: "d1", pendingDownloadAttempt: 1, archived: true},
			check: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f migrationProvenanceFixture) {
				assertMigrationProvenanceExpectation(t, ctx, pool, f, "a2", migrationProvenanceExpectation{rows: 1, pendingDownload: "d3", pendingDownloadAttempt: 1, archived: true})
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			databaseURL, pool := testutil.NewMigratedPostgres(t)
			downgradeApplication(t, ctx, pool, 39)
			fixture := seedMigrationProvenanceFixture(t, ctx, pool)
			base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
			insertMigrationProvenanceEvents(t, ctx, pool, testCase.before(fixture, base))
			if testCase.prepare != nil {
				testCase.prepare(t, ctx, pool, fixture)
			}
			if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
				t.Fatalf("Migrate() from v39 error = %v", err)
			}
			if testCase.after != nil {
				insertMigrationProvenanceEvents(t, ctx, pool, testCase.after(fixture, base))
			}
			assertMigrationProvenanceExpectation(t, ctx, pool, fixture, "a1", testCase.want)
			if testCase.check != nil {
				testCase.check(t, ctx, pool, fixture)
			}
		})
	}
}

func TestApplicationMigrationsUpgradeV8AndV9Integration(t *testing.T) {
	for _, startingVersion := range []int64{8, 9} {
		t.Run(fmt.Sprintf("v%d", startingVersion), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			databaseURL, pool := testutil.NewMigratedPostgres(t)
			downgradeApplication(t, ctx, pool, startingVersion)

			seriesID, acquisitionID, downloadID := uuid.New(), uuid.New(), uuid.New()
			if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Upgrade Fixture')`, seriesID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', 'fixture://upgrade')`, acquisitionID, seriesID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'enqueue_pending')`, downloadID, acquisitionID); err != nil {
				t.Fatal(err)
			}

			if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
				t.Fatalf("Migrate() from v%d error = %v", startingVersion, err)
			}
			if err := database.RequireCurrentMigrations(ctx, pool); err != nil {
				t.Fatalf("RequireCurrentMigrations() after v%d upgrade error = %v", startingVersion, err)
			}
			var version int32
			var failureStage, clientState *string
			if err := pool.QueryRow(ctx, `SELECT version, failure_stage, client_state FROM downloads WHERE id = $1`, downloadID).Scan(&version, &failureStage, &clientState); err != nil {
				t.Fatalf("read upgraded v%d download: %v", startingVersion, err)
			}
			if version != 1 || failureStage != nil || clientState != nil {
				t.Fatalf("upgraded v%d download = version %d failureStage %v clientState %v", startingVersion, version, failureStage, clientState)
			}
		})
	}
}

func downgradeApplication(t *testing.T, ctx context.Context, pool *pgxpool.Pool, target int64) {
	t.Helper()
	latest, err := database.LatestApplicationMigrationVersion()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := appmigrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for version := latest; version > target; version-- {
		prefix := fmt.Sprintf("%06d_", version)
		name := ""
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".down.sql") {
				name = entry.Name()
				break
			}
		}
		if name == "" {
			t.Fatalf("down migration %d not found", version)
		}
		contents, err := appmigrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE schema_migrations SET version = $1, dirty = false`, version-1); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRSSSourceCleanupPolicyMigrationPreservesManualDeletionIntentIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	downgradeApplication(t, ctx, pool, 25)

	seriesID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'RSS policy upgrade fixture')`, seriesID); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name                       string
		cancelAutomatic            bool
		manualSubscriptionDeletion bool
		manualAcquisitionDeletion  bool
		wantDeletionRequested      bool
	}{
		{name: "active automatic completion", wantDeletionRequested: false},
		{name: "cancelled automatic completion", cancelAutomatic: true, wantDeletionRequested: true},
		{name: "manual subscription deletion", manualSubscriptionDeletion: true, wantDeletionRequested: true},
		{name: "manual acquisition deletion", manualAcquisitionDeletion: true, wantDeletionRequested: true},
	}
	type fixture struct {
		name, subscriptionID, acquisitionID string
		wantDeletionRequested               bool
	}
	fixtures := make([]fixture, 0, len(cases))
	for _, test := range cases {
		subscriptionID, entryID, acquisitionID, automaticOperationID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
    id, series_id, name, feed_url, enabled, delete_imported_on_completion,
    poll_interval_seconds, source_season, completed_at
)
VALUES ($1, $2, $3, $4, false, true, 900, 1, now())
`, subscriptionID, seriesID, test.name, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode
)
VALUES ($1, $2, $3, 'Episode 1', 'https://example.test/episode.torrent', true, ARRAY[]::text[], 1, 1)
`, entryID, subscriptionID, "guid:"+entryID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id, deletion_requested_at)
VALUES ($1, $2, 'rss', $3, now())
`, acquisitionID, seriesID, entryID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, timeout_seconds, payload, cancel_requested_at
)
VALUES ($1, 'rss.subscription.complete', 'rss_subscription', $2, $3, 'queued',
        3, 1800, '{"trigger":"final_import"}'::jsonb, CASE WHEN $4 THEN now() ELSE NULL END)
`, automaticOperationID, subscriptionID, "automatic-completion:"+automaticOperationID.String(), test.cancelAutomatic); err != nil {
			t.Fatal(err)
		}
		if test.manualSubscriptionDeletion {
			operationID := uuid.New()
			if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, timeout_seconds, payload
)
VALUES ($1, 'rss.subscription.delete', 'rss_subscription', $2, $3, 'queued',
        3, 1800, '{"trigger":"manual"}'::jsonb)
`, operationID, subscriptionID, "manual-subscription-deletion:"+operationID.String()); err != nil {
				t.Fatal(err)
			}
		}
		if test.manualAcquisitionDeletion {
			operationID := uuid.New()
			if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, timeout_seconds, payload
)
VALUES ($1, 'acquisition.delete', 'acquisition', $2, $3, 'queued', 5, 1800, '{}'::jsonb)
`, operationID, acquisitionID, "manual-acquisition-deletion:"+operationID.String()); err != nil {
				t.Fatal(err)
			}
		}
		fixtures = append(fixtures, fixture{
			name: test.name, subscriptionID: subscriptionID.String(), acquisitionID: acquisitionID.String(),
			wantDeletionRequested: test.wantDeletionRequested,
		})
	}

	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("Migrate() from v25 error = %v", err)
	}
	for _, item := range fixtures {
		t.Run(item.name, func(t *testing.T) {
			var cleanupSource, deletionRequested bool
			if err := pool.QueryRow(ctx, `
SELECT subscription.cleanup_source_on_completion, acquisition.deletion_requested_at IS NOT NULL
FROM rss_subscriptions AS subscription
JOIN rss_entries AS entry ON entry.subscription_id = subscription.id
JOIN acquisitions AS acquisition ON acquisition.rss_entry_id = entry.id
WHERE subscription.id = $1 AND acquisition.id = $2
`, item.subscriptionID, item.acquisitionID).Scan(&cleanupSource, &deletionRequested); err != nil {
				t.Fatal(err)
			}
			if !cleanupSource || deletionRequested != item.wantDeletionRequested {
				t.Fatalf("upgraded policy = cleanup source %t deletion requested %t", cleanupSource, deletionRequested)
			}
		})
	}
}

func TestMigrationStatusRejectsBehindAndDirtyDatabasesIntegration(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		if err := database.RequireCurrentMigrations(ctx, pool); err != nil {
			t.Fatalf("RequireCurrentMigrations() error = %v", err)
		}
	})

	t.Run("application behind", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		latest, err := database.LatestApplicationMigrationVersion()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET version = $1`, latest-1); err != nil {
			t.Fatal(err)
		}
		if err := database.RequireCurrentMigrations(ctx, pool); err == nil || !strings.Contains(err.Error(), "application migrations are behind") {
			t.Fatalf("RequireCurrentMigrations() error = %v", err)
		}
	})

	t.Run("application dirty", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET dirty = true`); err != nil {
			t.Fatal(err)
		}
		if err := database.RequireCurrentMigrations(ctx, pool); err == nil || !strings.Contains(err.Error(), "application schema is dirty") {
			t.Fatalf("RequireCurrentMigrations() error = %v", err)
		}
	})

	t.Run("River behind", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		if _, err := pool.Exec(ctx, `DELETE FROM river_migration WHERE line = 'main' AND version = (SELECT max(version) FROM river_migration WHERE line = 'main')`); err != nil {
			t.Fatal(err)
		}
		if err := database.RequireCurrentMigrations(ctx, pool); err == nil || !strings.Contains(err.Error(), "river migrations are behind") {
			t.Fatalf("RequireCurrentMigrations() error = %v", err)
		}
	})
}
