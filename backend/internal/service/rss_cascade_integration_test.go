//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Sets up a subscription with two acquisitions: one with an active task plus a
// materialized download (must be cancelled and removed), and one already
// imported (must be preserved).
func TestRSSCascadeStopsUnfinishedAndPreservesImportedIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)
	queries := db.New(pool)
	downloadCommands := NewDownloadCommandWorkflow(transactor, scheduler)
	taskCommands := NewTaskCommandWorkflow(queries, transactor, scheduler, NewTaskWorkflow(queries, transactor, scheduler))
	runner := NewRSSCascadeRunner(queries, transactor, downloadCommands, taskCommands)

	actorID, seriesID, subscriptionID := uuid.New(), uuid.New(), uuid.New()
	activeEntryID, importedEntryID := uuid.New(), uuid.New()
	activeAcquisitionID, importedAcquisitionID := uuid.New(), uuid.New()
	activeDownloadID, importedDownloadID := uuid.New(), uuid.New()
	activeFileID, importedFileID := uuid.New(), uuid.New()
	activeTaskID, importedTaskID := uuid.New(), uuid.New()
	mappingProfileID, transcodeProfileID := uuid.New(), uuid.New()

	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-cascade-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO transcode_profiles (id, name, version, active, is_default, video_codec, encoder, container, file_extension, quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency) VALUES ($1, 'cascade', 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)`, transcodeProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Cascade Integration')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active, created_by) VALUES ($1, $2, 'p', 1, ARRAY[13], true, $3)`, mappingProfileID, seriesID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_subscriptions (id, series_id, mapping_profile_id, name, feed_url, enabled, poll_interval_seconds, source_season) VALUES ($1, $2, $3, 'Cascade', 'https://example.test/cascade.xml', false, 900, 1)`, subscriptionID, seriesID, mappingProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status)
VALUES ($1, $3, 'guid:active', 'Active E01', 'https://example.test/a.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued'),
       ($2, $3, 'guid:imported', 'Imported E02', 'https://example.test/b.torrent', true, ARRAY[]::text[], 1, 2, 'enqueued')`, activeEntryID, importedEntryID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, mapping_profile_id, source_kind, rss_entry_id)
VALUES ($1, $3, $4, 'rss', $5), ($2, $3, $4, 'rss', $6)`, activeAcquisitionID, importedAcquisitionID, seriesID, mappingProfileID, activeEntryID, importedEntryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, attempt, torrent_hash, status, save_path)
VALUES ($1, $3, 1, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'materialized', '/data/downloads/a'),
       ($2, $4, 1, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'materialized', '/data/downloads/b')`, activeDownloadID, importedDownloadID, activeAcquisitionID, importedAcquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $3, 0, 'a.mkv', 1024, 'video', true, 1, 1),
       ($2, $4, 0, 'b.mkv', 1024, 'video', true, 1, 2)`, activeFileID, importedFileID, activeDownloadID, importedDownloadID); err != nil {
		t.Fatal(err)
	}
	// Active task (processing) for the first, imported task for the second.
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, media_type, state, video_state, subtitle_state)
VALUES ($1, $3, $5, $7, 'episode', 'processing', 'transcoding', 'ass_ready'),
       ($2, $4, $6, $7, 'episode', 'imported', 'video_ready', 'ass_ready')`, activeTaskID, importedTaskID, activeAcquisitionID, importedAcquisitionID, activeFileID, importedFileID, transcodeProfileID); err != nil {
		t.Fatal(err)
	}

	var operationID uuid.UUID
	err = transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		scheduled, err := scheduler.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindRSSSubscriptionDelete,
			ResourceType:   "rss_subscription",
			ResourceID:     subscriptionID,
			IdempotencyKey: "test-cascade:" + subscriptionID.String(),
			MaxAttempts:    3,
			Timeout:        30 * time.Minute,
			Payload:        map[string]any{"subscriptionId": subscriptionID},
			ActorUserID:    actorID,
		})
		if err != nil {
			return err
		}
		operationID = scheduled.Operation.ID
		return nil
	})
	if err != nil {
		t.Fatalf("schedule cascade operation: %v", err)
	}

	result, err := runner.Run(ctx, operationID, subscriptionID, actorID)
	if err != nil {
		t.Fatalf("run cascade: %v", err)
	}
	if result.Acquisitions != 2 {
		t.Fatalf("acquisitions = %d, want 2", result.Acquisitions)
	}
	if result.ImportedKept != 1 {
		t.Fatalf("importedKept = %d, want 1", result.ImportedKept)
	}
	if len(result.FailedItems) != 0 {
		t.Fatalf("failedItems = %#v, want none", result.FailedItems)
	}

	// The active task must be cancelled.
	var activeState string
	if err := pool.QueryRow(ctx, `SELECT state FROM episode_tasks WHERE id = $1`, activeTaskID).Scan(&activeState); err != nil {
		t.Fatal(err)
	}
	if activeState != "cancelled" {
		t.Fatalf("active task state = %q, want cancelled", activeState)
	}
	// The active download must have a removal requested (the worker performs
	// the actual torrent/file removal and soft-delete asynchronously).
	var removalRequested bool
	if err := pool.QueryRow(ctx, `SELECT deletion_requested_at IS NOT NULL FROM downloads WHERE id = $1`, activeDownloadID).Scan(&removalRequested); err != nil {
		t.Fatal(err)
	}
	if !removalRequested {
		t.Fatalf("active download removal was not requested")
	}
	// A download.cancel operation must have been scheduled to finish removal.
	var cancelOps int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'download.cancel' AND resource_id = $1`, activeDownloadID).Scan(&cancelOps); err != nil {
		t.Fatal(err)
	}
	if cancelOps == 0 {
		t.Fatalf("no download.cancel operation scheduled for active download")
	}
	// The imported task and its download must be preserved.
	var importedState string
	if err := pool.QueryRow(ctx, `SELECT state FROM episode_tasks WHERE id = $1`, importedTaskID).Scan(&importedState); err != nil {
		t.Fatal(err)
	}
	if importedState != "imported" {
		t.Fatalf("imported task state = %q, want imported", importedState)
	}
	var importedRemoval bool
	if err := pool.QueryRow(ctx, `SELECT deletion_requested_at IS NOT NULL OR deleted_at IS NOT NULL FROM downloads WHERE id = $1`, importedDownloadID).Scan(&importedRemoval); err != nil {
		t.Fatal(err)
	}
	if importedRemoval {
		t.Fatalf("imported download must be preserved")
	}
	// A completion event must be recorded for traceability.
	var eventTopic string
	if err := pool.QueryRow(ctx, `SELECT topic FROM events WHERE resource_type = 'rss_subscription' AND resource_id = $1 AND topic LIKE 'rss.subscription.delete%' ORDER BY event_sequence DESC LIMIT 1`, subscriptionID).Scan(&eventTopic); err != nil {
		t.Fatal(err)
	}
	if eventTopic != "rss.subscription.delete_completed" {
		t.Fatalf("cascade event topic = %q, want delete_completed", eventTopic)
	}
}
