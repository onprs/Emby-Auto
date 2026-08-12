//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func TestAcquisitionDeletionIsIdempotentAndPhysicallyPurgesWorkflowIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)
	workflow := NewAcquisitionDeletionWorkflow(queries, transactor, scheduler)

	actorID, seriesID, profileID := uuid.New(), uuid.New(), uuid.New()
	acquisitionID, downloadID, fileID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	videoID, subtitleID, setID := uuid.New(), uuid.New(), uuid.New()
	activeOperationID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "delete-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Deletion Fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (id, name, version, active, is_default, video_codec, encoder, container, file_extension, quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency)
VALUES ($1, $2, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)`, profileID, "delete-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', 'magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`, acquisitionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, attempt, torrent_hash, status, save_path) VALUES ($1, $2, 1, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'materialized', '/downloads/delete-fixture')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected) VALUES ($1, $2, 0, 'episode.mkv', 1024, 'video', true)`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, media_type, state, video_state, subtitle_state) VALUES ($1, $2, $3, $4, 'episode', 'imported', 'video_ready', 'ass_ready')`, taskID, acquisitionID, fileID, profileID); err != nil {
		t.Fatal(err)
	}
	checksum := make([]byte, 32)
	if _, err := pool.Exec(ctx, `
INSERT INTO media_artifacts (id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $3, $4, $5, 'video', 'episode', '/staging/task/episode.mkv', 'mkv', 1024, $6),
       ($2, $3, $4, NULL, 'subtitle', 'episode', '/staging/task/episode.ass', 'ass', 128, $6)`, videoID, subtitleID, taskID, fileID, profileID, checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO artifact_sets (id, task_id, transcode_profile_id, basename, video_artifact_id, subtitle_artifact_id) VALUES ($1, $2, $3, 'episode', $4, $5)`, setID, taskID, profileID, videoID, subtitleID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO reviews (task_id, decision, reviewed_by, idempotency_key, expected_task_version) VALUES ($1, 'approved', $2, $3, 1)`, taskID, actorID, "delete-review-"+taskID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO imports (task_id, status, destination_video_path, destination_subtitle_path) VALUES ($1, 'succeeded', '/library/episode.mkv', '/library/episode.ass')`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO cleanup_runs (task_id, download_id, status, error_code, error_message) VALUES ($1, $2, 'failed', 'fixture', 'fixture')`, taskID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'emby.refresh', 'episode_task', $2, $3, 'running', 1, 60)`, activeOperationID, taskID, "active-"+activeOperationID.String()); err != nil {
		t.Fatal(err)
	}

	key := "delete-acquisition-" + acquisitionID.String()
	operation, err := workflow.RequestDeletion(ctx, acquisitionID, key, actorID)
	if err != nil {
		t.Fatalf("RequestDeletion() error = %v", err)
	}
	replayed, err := workflow.RequestDeletion(ctx, acquisitionID, key, actorID)
	if err != nil || replayed.ID != operation.ID {
		t.Fatalf("replayed RequestDeletion() = %#v, %v", replayed, err)
	}
	var requested bool
	if err := pool.QueryRow(ctx, `SELECT deletion_requested_at IS NOT NULL FROM acquisitions WHERE id = $1`, acquisitionID).Scan(&requested); err != nil || !requested {
		t.Fatalf("acquisition deletion request = %t, %v", requested, err)
	}
	var activeCancelled bool
	if err := pool.QueryRow(ctx, `SELECT cancel_requested_at IS NOT NULL FROM operations WHERE id = $1`, activeOperationID).Scan(&activeCancelled); err != nil || !activeCancelled {
		t.Fatalf("related operation cancellation = %t, %v", activeCancelled, err)
	}
	ready, err := workflow.DeletionReady(ctx, acquisitionID, operation.ID)
	if err != nil || ready {
		t.Fatalf("DeletionReady() = %t, %v; want waiting", ready, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE operations SET status = 'cancelled', finished_at = now() WHERE id = $1`, activeOperationID); err != nil {
		t.Fatal(err)
	}
	ready, err = workflow.DeletionReady(ctx, acquisitionID, operation.ID)
	if err != nil || !ready {
		t.Fatalf("DeletionReady() = %t, %v; want ready", ready, err)
	}
	command, err := workflow.LoadDeletionCommand(ctx, acquisitionID)
	if err != nil || len(command.TaskIDs) != 1 || len(command.Downloads) != 1 || len(command.ArtifactPaths) != 2 || len(command.LibraryFiles) != 2 {
		t.Fatalf("LoadDeletionCommand() = %#v, %v", command, err)
	}
	for _, libraryFile := range command.LibraryFiles {
		if libraryFile.Preserve || (libraryFile.FilePath != "/library/episode.mkv" && libraryFile.FilePath != "/library/episode.ass") {
			t.Fatalf("library deletion target = %#v", libraryFile)
		}
	}
	result, err := workflow.CompleteDeletion(ctx, acquisitionID, operation.ID)
	if err != nil {
		t.Fatalf("CompleteDeletion() error = %v", err)
	}
	if result.TasksRemoved != 1 || result.Downloads != 1 || result.Artifacts != 2 {
		t.Fatalf("CompleteDeletion() result = %#v", result)
	}
	for table, id := range map[string]uuid.UUID{
		"acquisitions": acquisitionID, "downloads": downloadID, "download_files": fileID,
		"episode_tasks": taskID, "artifact_sets": setID, "media_artifacts": videoID,
	} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s row count = %d, %v", table, count, err)
		}
	}
	var operationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id IN ($1, $2)`, operation.ID, activeOperationID).Scan(&operationCount); err != nil || operationCount != 2 {
		t.Fatalf("audit operations count = %d, %v", operationCount, err)
	}
	if _, err := workflow.CompleteDeletion(ctx, acquisitionID, operation.ID); err != nil {
		t.Fatalf("idempotent CompleteDeletion() error = %v", err)
	}

}

func TestAcquisitionDeletionIgnoresArchivedRSSOwnersButPreservesLiveOwnersIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	workflow := NewAcquisitionDeletionWorkflow(db.New(pool), nil, nil)

	seriesID := uuid.New()
	targetAcquisitionID := uuid.New()
	archivedSubscriptionID, liveSubscriptionID := uuid.New(), uuid.New()
	archivedEntryID, liveEntryID := uuid.New(), uuid.New()
	archivedAcquisitionID, liveAcquisitionID, manualAcquisitionID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Deletion Ownership Fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, deleted_at)
VALUES ($1, $3, 'Archived Owner', $4, false, 900, 1, now()),
       ($2, $3, 'Live Owner', $5, false, 900, 1, NULL)`, archivedSubscriptionID, liveSubscriptionID, seriesID,
		"https://example.test/"+archivedSubscriptionID.String()+".xml", "https://example.test/"+liveSubscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, status)
VALUES ($1, $2, 'archived', 'Archived Episode', 'enqueued'),
       ($3, $4, 'live', 'Live Episode', 'enqueued')`, archivedEntryID, archivedSubscriptionID, liveEntryID, liveSubscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, rss_entry_id)
VALUES ($1, $2, 'manual', 'manual://target', NULL),
       ($3, $2, 'rss', NULL, $4),
       ($5, $2, 'rss', NULL, $6),
       ($7, $2, 'manual', 'manual://live-owner', NULL)`, targetAcquisitionID, seriesID,
		archivedAcquisitionID, archivedEntryID, liveAcquisitionID, liveEntryID, manualAcquisitionID); err != nil {
		t.Fatal(err)
	}
	const archivedHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const liveHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const manualHash = "cccccccccccccccccccccccccccccccccccccccc"
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, attempt, torrent_hash, status, save_path)
VALUES ($1, $2, 1, $3, 'cancelled', '/downloads/archived-shared'),
       ($4, $2, 2, $5, 'cancelled', '/downloads/live-shared'),
       ($6, $2, 3, $7, 'cancelled', '/downloads/manual-shared'),
       ($8, $9, 1, $3, 'cancelled', '/downloads/archived-shared'),
       ($10, $11, 1, $5, 'cancelled', '/downloads/live-shared'),
       ($12, $13, 1, $7, 'cancelled', '/downloads/manual-shared')`,
		uuid.New(), targetAcquisitionID, archivedHash,
		uuid.New(), liveHash, uuid.New(), manualHash,
		uuid.New(), archivedAcquisitionID, uuid.New(), liveAcquisitionID, uuid.New(), manualAcquisitionID); err != nil {
		t.Fatal(err)
	}

	command, err := workflow.LoadDeletionCommand(ctx, targetAcquisitionID)
	if err != nil {
		t.Fatalf("LoadDeletionCommand() error = %v", err)
	}
	byHash := make(map[string]struct{ torrent, path bool }, len(command.Downloads))
	for _, download := range command.Downloads {
		byHash[download.TorrentHash] = struct{ torrent, path bool }{download.PreserveTorrent, download.PreservePath}
	}
	if flags := byHash[archivedHash]; flags.torrent || flags.path {
		t.Fatalf("archived RSS owner preserved resources: %#v", flags)
	}
	for name, hash := range map[string]string{"live RSS": liveHash, "manual": manualHash} {
		if flags := byHash[hash]; !flags.torrent || !flags.path {
			t.Fatalf("%s owner did not preserve resources: %#v", name, flags)
		}
	}
}

func TestDownloadDeletionUsesAcquisitionBoundaryAndReplaysAfterRemovalIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewAcquisitionDeletionWorkflow(queries, transactor, NewOperationScheduler(transactor, riverClient))

	actorID, seriesID, acquisitionID, downloadID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "download-delete-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Download Deletion Fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', 'manual://download-delete')`, acquisitionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'cancelled')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}

	_, err = workflow.RequestDownloadDeletion(ctx, downloadID, 2, "wrong-version-"+downloadID.String(), actorID)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("wrong-version RequestDownloadDeletion() error = %v", err)
	}
	var requested bool
	if err := pool.QueryRow(ctx, `SELECT deletion_requested_at IS NOT NULL FROM acquisitions WHERE id = $1`, acquisitionID).Scan(&requested); err != nil || requested {
		t.Fatalf("wrong version changed acquisition = %t, %v", requested, err)
	}

	key := "delete-download-" + downloadID.String()
	operation, err := workflow.RequestDownloadDeletion(ctx, downloadID, 1, key, actorID)
	if err != nil {
		t.Fatalf("RequestDownloadDeletion() error = %v", err)
	}
	if operation.ResourceType != "acquisition" || operation.ResourceID != acquisitionID {
		t.Fatalf("deletion operation resource = %#v", operation)
	}
	if _, err := workflow.CompleteDeletion(ctx, acquisitionID, operation.ID); err != nil {
		t.Fatalf("CompleteDeletion() error = %v", err)
	}
	replayed, err := workflow.RequestDownloadDeletion(ctx, downloadID, 1, key, actorID)
	if err != nil || replayed.ID != operation.ID {
		t.Fatalf("replayed RequestDownloadDeletion() = %#v, %v", replayed, err)
	}
	for table, id := range map[string]uuid.UUID{"acquisitions": acquisitionID, "downloads": downloadID} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s row count = %d, %v", table, count, err)
		}
	}
}
