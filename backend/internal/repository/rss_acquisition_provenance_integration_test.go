//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	return seriesID, subscriptionID, entryID, acquisitionID
}

func TestRSSAcquisitionProvenanceIsAtomicBoundedAndCascadesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	events := NewEvents(queries)
	_, subscriptionID, entryID, acquisitionID := seedProvenanceEntities(t, ctx, pool)
	downloadID, taskID := uuid.New(), uuid.New()
	base := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	enqueueData := `{"acquisitionId":"` + acquisitionID.String() + `","downloadId":"` + downloadID.String() + `"}`
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
