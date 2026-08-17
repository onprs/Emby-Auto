//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	appmigrations "github.com/onprs/emby-auto/backend/db/migrations"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type provenanceExec interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func insertProvenanceEvent(
	t *testing.T,
	ctx context.Context,
	exec provenanceExec,
	topic string,
	resourceType string,
	resourceID uuid.UUID,
	data string,
	occurredAt time.Time,
) {
	t.Helper()
	if _, err := exec.Exec(ctx, `
INSERT INTO events (topic, resource_type, resource_id, data, occurred_at)
VALUES ($1, $2, $3, $4::jsonb, $5)
`, topic, resourceType, resourceID, data, occurredAt); err != nil {
		t.Fatalf("insert %s event: %v", topic, err)
	}
}

func seedProvenanceEntities(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	seriesID, subscriptionID, entryID, acquisitionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Provenance fixture')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Provenance fixture', $3, true, 900, 1)
`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, $3, 'Provenance S01E01', 'https://example.test/episode.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued')
`, entryID, subscriptionID, "guid:"+entryID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, acquisitionID, seriesID, entryID); err != nil {
		t.Fatal(err)
	}
	return seriesID, subscriptionID, entryID, acquisitionID
}

func seedProvenanceProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	profileID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container,
    file_extension, quality_mode, quality_value, audio_policy, preset,
    pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)
`, profileID, "provenance-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	return profileID
}

func seedProvenanceAttempt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	acquisitionID, profileID, downloadID, taskID uuid.UUID,
	attempt int,
) {
	t.Helper()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, attempt, status)
VALUES ($1, $2, $3, 'enqueue_pending')
`, downloadID, acquisitionID, attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($1, $2, 0, $3, 1024, 'video', true)
`, fileID, downloadID, "Provenance-"+downloadID.String()+".mkv"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id)
VALUES ($1, $2, $3, $4)
`, taskID, acquisitionID, fileID, profileID); err != nil {
		t.Fatal(err)
	}
}

func enqueueProvenanceData(acquisitionID, downloadID uuid.UUID, attempt int) string {
	return `{"acquisitionId":"` + acquisitionID.String() + `","downloadId":"` + downloadID.String() + `","downloadAttempt":` + fmt.Sprint(attempt) + `}`
}

func TestRSSAcquisitionProvenanceIsAtomicBoundedAndCascadesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	events := NewEvents(queries)
	_, subscriptionID, entryID, acquisitionID := seedProvenanceEntities(t, ctx, pool)
	profileID := seedProvenanceProfile(t, ctx, pool)
	downloadID, taskID := uuid.New(), uuid.New()
	seedProvenanceAttempt(t, ctx, pool, acquisitionID, profileID, downloadID, taskID, 1)
	base := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	enqueueData := enqueueProvenanceData(acquisitionID, downloadID, 1)
	taskData := `{"downloadId":"` + downloadID.String() + `"}`

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertProvenanceEvent(t, ctx, tx, "rss.entry.enqueueing", "rss_entry", entryID, enqueueData, base)
	var withinTransactionCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rss_acquisition_provenance WHERE acquisition_id = $1`, acquisitionID).Scan(&withinTransactionCount); err != nil {
		t.Fatal(err)
	}
	if withinTransactionCount != 1 {
		t.Fatalf("provenance rows inside transaction = %d, want 1", withinTransactionCount)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var rolledBackCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_acquisition_provenance WHERE acquisition_id = $1`, acquisitionID).Scan(&rolledBackCount); err != nil {
		t.Fatal(err)
	}
	if rolledBackCount != 0 {
		t.Fatalf("rolled-back provenance rows = %d, want 0", rolledBackCount)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertProvenanceEvent(t, ctx, tx, "rss.entry.enqueueing", "rss_entry", entryID, enqueueData, base)
	insertProvenanceEvent(t, ctx, tx, "task.created", "episode_task", taskID, taskData, base.Add(time.Minute))
	insertProvenanceEvent(t, ctx, tx, "task.video_ready", "episode_task", taskID, `{}`, base.Add(2*time.Minute))
	insertProvenanceEvent(t, ctx, tx, "task.subtitle_ready", "episode_task", taskID, `{}`, base.Add(3*time.Minute))
	insertProvenanceEvent(t, ctx, tx, "task.awaiting_review", "episode_task", taskID, `{}`, base.Add(4*time.Minute))
	insertProvenanceEvent(t, ctx, tx, "task.reviewed", "episode_task", taskID, `{}`, base.Add(5*time.Minute))
	insertProvenanceEvent(t, ctx, tx, "task.imported", "episode_task", taskID, `{}`, base.Add(6*time.Minute))
	insertProvenanceEvent(t, ctx, tx, "acquisition.delete_completed", "acquisition", acquisitionID, `{}`, base.Add(7*time.Minute))
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE rss_entries SET imported_at = $1 WHERE id = $2`, base.Add(6*time.Minute), entryID); err != nil {
		t.Fatal(err)
	}

	var gotDownloadID, gotTaskID uuid.UUID
	var gotCreated, gotTaskCreated, gotVideoReady, gotSubtitleReady, gotArtifactReady, gotReviewed, gotImported, gotArchived time.Time
	if err := pool.QueryRow(ctx, `
SELECT download_id, task_id, acquisition_created_at, task_created_at,
       video_ready_at, subtitle_ready_at, artifact_ready_at, reviewed_at,
       imported_at, archived_at
FROM rss_acquisition_provenance
WHERE acquisition_id = $1
`, acquisitionID).Scan(
		&gotDownloadID, &gotTaskID, &gotCreated, &gotTaskCreated,
		&gotVideoReady, &gotSubtitleReady, &gotArtifactReady, &gotReviewed,
		&gotImported, &gotArchived,
	); err != nil {
		t.Fatal(err)
	}
	if gotDownloadID != downloadID || gotTaskID != taskID || !gotCreated.Equal(base) ||
		!gotTaskCreated.Equal(base.Add(time.Minute)) || !gotVideoReady.Equal(base.Add(2*time.Minute)) ||
		!gotSubtitleReady.Equal(base.Add(3*time.Minute)) || !gotArtifactReady.Equal(base.Add(4*time.Minute)) ||
		!gotReviewed.Equal(base.Add(5*time.Minute)) || !gotImported.Equal(base.Add(6*time.Minute)) ||
		!gotArchived.Equal(base.Add(7*time.Minute)) {
		t.Fatalf("provenance row = %s/%s %v/%v/%v/%v/%v/%v/%v/%v, want event milestones", gotDownloadID, gotTaskID, gotCreated, gotTaskCreated, gotVideoReady, gotSubtitleReady, gotArtifactReady, gotReviewed, gotImported, gotArchived)
	}

	// Replaying the same enqueue/task events updates one row and preserves the
	// original pending milestone timestamps instead of adding another history row.
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertProvenanceEvent(t, ctx, tx, "rss.entry.enqueueing", "rss_entry", entryID, enqueueData, base.Add(8*time.Minute))
	insertProvenanceEvent(t, ctx, tx, "task.created", "episode_task", taskID, taskData, base.Add(9*time.Minute))
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var provenanceRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_acquisition_provenance WHERE rss_entry_id = $1`, entryID).Scan(&provenanceRows); err != nil {
		t.Fatal(err)
	}
	if provenanceRows != 1 {
		t.Fatalf("replayed provenance rows = %d, want 1", provenanceRows)
	}

	deleted, err := events.DeleteExpired(ctx, base.Add(24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 10 {
		t.Fatalf("retention deleted %d provenance events, want 10 replayed/initial events", deleted)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_acquisition_provenance WHERE acquisition_id = $1`, acquisitionID).Scan(&provenanceRows); err != nil {
		t.Fatal(err)
	}
	if provenanceRows != 1 {
		t.Fatalf("provenance after event retention = %d, want 1", provenanceRows)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM acquisitions WHERE id = $1`, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM rss_subscriptions WHERE id = $1`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_acquisition_provenance WHERE acquisition_id = $1`, acquisitionID).Scan(&provenanceRows); err != nil {
		t.Fatal(err)
	}
	if provenanceRows != 0 {
		t.Fatalf("provenance after RSS entity deletion = %d, want 0", provenanceRows)
	}
}

func TestRSSAcquisitionProvenanceIgnoresMalformedAndStaleEventsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, subscriptionID, entryID, acquisitionID := seedProvenanceEntities(t, ctx, pool)
	profileID := seedProvenanceProfile(t, ctx, pool)
	downloadID, taskID := uuid.New(), uuid.New()
	seedProvenanceAttempt(t, ctx, pool, acquisitionID, profileID, downloadID, taskID, 1)

	secondEntryID, secondAcquisitionID, secondDownloadID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, $3, 'Provenance S01E02', 'https://example.test/episode-2.torrent', true, ARRAY[]::text[], 1, 2, 'enqueued')
`, secondEntryID, subscriptionID, "guid:"+secondEntryID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, secondAcquisitionID, seriesID, secondEntryID); err != nil {
		t.Fatal(err)
	}
	seedProvenanceAttempt(t, ctx, pool, secondAcquisitionID, profileID, secondDownloadID, uuid.New(), 1)

	deletedEntryID, deletedAcquisitionID, deletedDownloadID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, $3, 'Provenance S01E03', 'https://example.test/episode-3.torrent', true, ARRAY[]::text[], 1, 3, 'enqueued')
`, deletedEntryID, subscriptionID, "guid:"+deletedEntryID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, deletedAcquisitionID, seriesID, deletedEntryID); err != nil {
		t.Fatal(err)
	}
	seedProvenanceAttempt(t, ctx, pool, deletedAcquisitionID, profileID, deletedDownloadID, uuid.New(), 1)
	if _, err := pool.Exec(ctx, `DELETE FROM acquisitions WHERE id = $1`, deletedAcquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM rss_entries WHERE id = $1`, deletedEntryID); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, time.July, 2, 12, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertProvenanceEvent(t, ctx, tx, "rss.entry.enqueueing", "rss_entry", entryID, `{"acquisitionId":"not-a-uuid","downloadId":"not-a-uuid"}`, base)
	insertProvenanceEvent(t, ctx, tx, "rss.entry.enqueueing", "rss_entry", entryID, `{"acquisitionId":null,"downloadId":null}`, base)
	insertProvenanceEvent(t, ctx, tx, "rss.entry.enqueueing", "rss_entry", entryID, `{"acquisitionId":{},"downloadId":1}`, base)
	insertProvenanceEvent(t, ctx, tx, "rss.entry.enqueueing", "rss_entry", entryID, `{}`, base)
	insertProvenanceEvent(t, ctx, tx, "rss.entry.enqueueing", "rss_entry", entryID, enqueueProvenanceData(acquisitionID, secondDownloadID, 1), base)
	insertProvenanceEvent(t, ctx, tx, "rss.entry.enqueueing", "rss_entry", deletedEntryID, enqueueProvenanceData(deletedAcquisitionID, deletedDownloadID, 1), base)
	insertProvenanceEvent(t, ctx, tx, "task.created", "episode_task", taskID, `{"downloadId":"not-a-uuid"}`, base)
	insertProvenanceEvent(t, ctx, tx, "task.created", "episode_task", taskID, `{"downloadId":null}`, base)
	insertProvenanceEvent(t, ctx, tx, "task.created", "episode_task", taskID, `{"downloadId":{}}`, base)
	insertProvenanceEvent(t, ctx, tx, "task.created", "episode_task", taskID, `{}`, base)
	insertProvenanceEvent(t, ctx, tx, "task.created", "episode_task", taskID, `{"downloadId":"`+secondDownloadID.String()+`"}`, base)
	insertProvenanceEvent(t, ctx, tx, "task.imported", "episode_task", uuid.New(), `{}`, base)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("malformed/stale event transaction committed: %v", err)
	}

	var provenanceRows, eventRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_acquisition_provenance`).Scan(&provenanceRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE occurred_at = $1`, base).Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	if provenanceRows != 0 || eventRows != 12 {
		t.Fatalf("malformed/stale transaction results = provenance %d events %d, want 0/12", provenanceRows, eventRows)
	}
}

func TestRSSAcquisitionProvenanceRejectsTaskSourceDownloadMismatchIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	_, _, entryID, acquisitionID := seedProvenanceEntities(t, ctx, pool)
	profileID := seedProvenanceProfile(t, ctx, pool)
	download1, task1 := uuid.New(), uuid.New()
	seedProvenanceAttempt(t, ctx, pool, acquisitionID, profileID, download1, task1, 1)
	download2 := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, attempt, status)
VALUES ($1, $2, 2, 'enqueue_pending')
`, download2, acquisitionID); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.July, 2, 13, 0, 0, 0, time.UTC)

	insertProvenanceEvent(t, ctx, pool, "rss.entry.enqueueing", "rss_entry", entryID, enqueueProvenanceData(acquisitionID, download1, 1), base)
	insertProvenanceEvent(t, ctx, pool, "task.created", "episode_task", task1, `{"downloadId":"`+download2.String()+`"}`, base.Add(time.Minute))
	var pendingTaskID *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT pending_task_id FROM rss_acquisition_provenance WHERE acquisition_id = $1`, acquisitionID).Scan(&pendingTaskID); err != nil {
		t.Fatal(err)
	}
	if pendingTaskID != nil {
		t.Fatalf("mismatched task source populated pending task %s", pendingTaskID)
	}

	insertProvenanceEvent(t, ctx, pool, "task.created", "episode_task", task1, `{"downloadId":"`+download1.String()+`"}`, base.Add(2*time.Minute))
	if err := pool.QueryRow(ctx, `SELECT pending_task_id FROM rss_acquisition_provenance WHERE acquisition_id = $1`, acquisitionID).Scan(&pendingTaskID); err != nil {
		t.Fatal(err)
	}
	if pendingTaskID == nil || *pendingTaskID != task1 {
		t.Fatalf("valid task source did not populate task %s: got %v", task1, pendingTaskID)
	}
	insertProvenanceEvent(t, ctx, pool, "task.imported", "episode_task", task1, `{}`, base.Add(3*time.Minute))
	var gotDownloadID, gotTaskID uuid.UUID
	if err := pool.QueryRow(ctx, `
SELECT download_id, task_id
FROM rss_acquisition_provenance
WHERE acquisition_id = $1
`, acquisitionID).Scan(&gotDownloadID, &gotTaskID); err != nil {
		t.Fatal(err)
	}
	if gotDownloadID != download1 || gotTaskID != task1 {
		t.Fatalf("valid task source result = %s/%s, want %s/%s", gotDownloadID, gotTaskID, download1, task1)
	}
}

func TestRSSAcquisitionProvenanceRejectsLateAttemptReplayAndPreservesSuccessIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	_, _, entryID, acquisitionID := seedProvenanceEntities(t, ctx, pool)
	profileID := seedProvenanceProfile(t, ctx, pool)
	download1, task1 := uuid.New(), uuid.New()
	download2, task2 := uuid.New(), uuid.New()
	download3, task3 := uuid.New(), uuid.New()
	seedProvenanceAttempt(t, ctx, pool, acquisitionID, profileID, download1, task1, 1)
	seedProvenanceAttempt(t, ctx, pool, acquisitionID, profileID, download2, task2, 2)
	seedProvenanceAttempt(t, ctx, pool, acquisitionID, profileID, download3, task3, 3)
	base := time.Date(2026, time.July, 3, 12, 0, 0, 0, time.UTC)

	insertProvenanceEvent(t, ctx, pool, "rss.entry.enqueueing", "rss_entry", entryID, enqueueProvenanceData(acquisitionID, download1, 1), base)
	insertProvenanceEvent(t, ctx, pool, "task.created", "episode_task", task1, `{"downloadId":"`+download1.String()+`"}`, base)
	insertProvenanceEvent(t, ctx, pool, "task.video_ready", "episode_task", task1, `{}`, base.Add(time.Minute))

	// D2 is a legitimate retry and has the same occurred_at as D1. The
	// persisted download attempt, then event_sequence, selects D2 deterministically.
	insertProvenanceEvent(t, ctx, pool, "rss.entry.enqueueing", "rss_entry", entryID, enqueueProvenanceData(acquisitionID, download2, 2), base)
	var pendingTaskNull, pendingVideoNull bool
	if err := pool.QueryRow(ctx, `
SELECT pending_task_id IS NULL, pending_video_ready_at IS NULL
FROM rss_acquisition_provenance WHERE acquisition_id = $1
`, acquisitionID).Scan(&pendingTaskNull, &pendingVideoNull); err != nil {
		t.Fatal(err)
	}
	if !pendingTaskNull || !pendingVideoNull {
		t.Fatalf("new attempt did not clear old pending state = task null %t/video null %t", pendingTaskNull, pendingVideoNull)
	}
	insertProvenanceEvent(t, ctx, pool, "task.created", "episode_task", task2, `{"downloadId":"`+download2.String()+`"}`, base)
	insertProvenanceEvent(t, ctx, pool, "task.video_ready", "episode_task", task2, `{}`, base.Add(2*time.Minute))
	insertProvenanceEvent(t, ctx, pool, "task.imported", "episode_task", task2, `{}`, base.Add(3*time.Minute))

	// A later enqueue for D3 must not replace already confirmed success.
	insertProvenanceEvent(t, ctx, pool, "rss.entry.enqueueing", "rss_entry", entryID, enqueueProvenanceData(acquisitionID, download3, 3), base.Add(4*time.Minute))
	insertProvenanceEvent(t, ctx, pool, "task.created", "episode_task", task3, `{"downloadId":"`+download3.String()+`"}`, base.Add(4*time.Minute))
	insertProvenanceEvent(t, ctx, pool, "task.imported", "episode_task", task3, `{}`, base.Add(5*time.Minute))

	// D1 replay arrives after D2 has succeeded. It must be a no-op even though
	// its occurred_at is later than the original event and its task still exists.
	insertProvenanceEvent(t, ctx, pool, "rss.entry.enqueueing", "rss_entry", entryID, enqueueProvenanceData(acquisitionID, download1, 1), base.Add(6*time.Minute))
	insertProvenanceEvent(t, ctx, pool, "task.created", "episode_task", task1, `{"downloadId":"`+download1.String()+`"}`, base.Add(6*time.Minute))
	insertProvenanceEvent(t, ctx, pool, "task.imported", "episode_task", task1, `{}`, base.Add(7*time.Minute))

	var gotDownloadID, gotTaskID uuid.UUID
	var gotAttempt int
	var gotImported time.Time
	if err := pool.QueryRow(ctx, `
SELECT download_id, download_attempt, task_id, imported_at, pending_download_id IS NULL
FROM rss_acquisition_provenance WHERE acquisition_id = $1
`, acquisitionID).Scan(&gotDownloadID, &gotAttempt, &gotTaskID, &gotImported, &pendingTaskNull); err != nil {
		t.Fatal(err)
	}
	if gotDownloadID != download2 || gotAttempt != 2 || gotTaskID != task2 || !gotImported.Equal(base.Add(3*time.Minute)) || !pendingTaskNull {
		t.Fatalf("attempt replay result = download %s attempt %d task %s imported %v pending null %t, want D2/T2/attempt2/success", gotDownloadID, gotAttempt, gotTaskID, gotImported, pendingTaskNull)
	}
}

func TestRSSAcquisitionProvenanceMigrationBackfillUsesMigrationSQLPlanIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	_, _, entryID, _ := seedProvenanceEntities(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
INSERT INTO events (topic, data, occurred_at)
SELECT 'plan.unrelated', '{}'::jsonb, now()
FROM generate_series(1, 5000)
`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE events`); err != nil {
		t.Fatal(err)
	}
	backfill := migration40BackfillSQL(t)
	plan := explainJSONPlan(t, ctx, tx, backfill)
	if strings.Count(strings.ToLower(backfill), "from events") != 1 {
		t.Fatalf("migration backfill source has multiple events reads: %s", backfill)
	}
	relationScans, candidateScans := countBackfillPlanNodes(plan)
	if relationScans != 1 || candidateScans == 0 {
		t.Fatalf("migration backfill plan relation scans = %d, candidate CTE scans = %d, want one events relation and at least one candidate CTE scan", relationScans, candidateScans)
	}
	if entryID == uuid.Nil {
		t.Fatal("fixture entry ID is unexpectedly nil")
	}
}

func migration40BackfillSQL(t *testing.T) string {
	t.Helper()
	source, err := appmigrations.Files.ReadFile("000040_rss_acquisition_provenance.up.sql")
	if err != nil {
		t.Fatalf("read migration 40 source: %v", err)
	}
	const startMarker = "-- BEGIN MIGRATION_40_RSS_PROVENANCE_BACKFILL"
	const endMarker = "-- END MIGRATION_40_RSS_PROVENANCE_BACKFILL"
	start := strings.Index(string(source), startMarker)
	end := strings.Index(string(source), endMarker)
	if start < 0 || end <= start {
		t.Fatalf("migration 40 backfill markers are missing or out of order")
	}
	backfill := strings.TrimSpace(string(source)[start+len(startMarker) : end])
	if !strings.HasPrefix(backfill, "-- Backfill current and historical successful facts") || !strings.Contains(backfill, "INSERT INTO rss_acquisition_provenance") {
		t.Fatalf("migration 40 marker does not enclose the backfill statement")
	}
	return backfill
}

func explainJSONPlan(t *testing.T, ctx context.Context, tx pgx.Tx, query string) map[string]any {
	t.Helper()
	var raw []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+query).Scan(&raw); err != nil {
		t.Fatalf("explain migration backfill: %v", err)
	}
	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatalf("decode JSON plan: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("JSON plan roots = %d, want 1", len(plans))
	}
	return plans[0]
}

func countBackfillPlanNodes(plan map[string]any) (relationScans, candidateScans int) {
	var visit func(any)
	visit = func(value any) {
		object, ok := value.(map[string]any)
		if !ok {
			if list, ok := value.([]any); ok {
				for _, item := range list {
					visit(item)
				}
			}
			return
		}
		if object["Relation Name"] == "events" {
			relationScans++
		}
		if object["Node Type"] == "CTE Scan" && object["CTE Name"] == "candidate_events" {
			candidateScans++
		}
		for _, child := range object {
			visit(child)
		}
	}
	visit(plan)
	return relationScans, candidateScans
}
