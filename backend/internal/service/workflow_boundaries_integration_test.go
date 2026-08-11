//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestRSSPollPersistsEmptyRejectionArrayAndSchedulesAcquisitionIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	workflow := NewRSSWorkflow(db.New(pool), transactor, scheduler)
	seriesID, subscriptionID, operationID := uuid.New(), uuid.New(), uuid.New()

	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'RSS Integration')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'RSS Integration', 'https://example.test/feed.xml', true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'running', 3, 60)`, operationID, subscriptionID, "rss-poll-"+operationID.String()); err != nil {
		t.Fatal(err)
	}

	result, err := workflow.PersistPoll(ctx, operationID, subscriptionID, domain.RSSFeed{Title: "RSS Integration", Entries: []domain.RSSFeedEntry{{
		GUID: "rss-integration-1", Title: "RSS Integration - S01E01", URL: "https://example.test/1",
		DownloadURI: "magnet:?xt=urn:btih:2123456789abcdef0123456789abcdef01234567",
	}}}, domain.RSSPollPersistOptions{})
	if err != nil {
		t.Fatalf("PersistPoll() error = %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(result.Candidates))
	}
	var reasons []string
	if err := pool.QueryRow(ctx, `SELECT rejection_reasons FROM rss_entries WHERE subscription_id = $1`, subscriptionID).Scan(&reasons); err != nil {
		t.Fatal(err)
	}
	if reasons == nil || len(reasons) != 0 {
		t.Fatalf("rejection reasons = %#v, want non-nil empty array", reasons)
	}
	if err := workflow.ScheduleRSSDownload(ctx, result.Candidates[0]); err != nil {
		t.Fatalf("ScheduleRSSCandidate() error = %v", err)
	}
	var acquisitionCount, downloadCount, jobCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE rss_entry_id = $1`, result.Candidates[0].EntryID).Scan(&acquisitionCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM downloads WHERE acquisition_id IN (SELECT id FROM acquisitions WHERE rss_entry_id = $1)`, result.Candidates[0].EntryID).Scan(&downloadCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'download.enqueue' AND resource_type = 'download'`).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if acquisitionCount != 1 || downloadCount != 1 || jobCount != 1 {
		t.Fatalf("acquisition/download/enqueue operation counts = %d/%d/%d, want 1/1/1", acquisitionCount, downloadCount, jobCount)
	}

	var downloadID, enqueueOperationID uuid.UUID
	if err := pool.QueryRow(ctx, `
SELECT download.id, operation.id
FROM downloads AS download
JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
JOIN operations AS operation ON operation.resource_id = download.id AND operation.kind = 'download.enqueue'
WHERE acquisition.rss_entry_id = $1`, result.Candidates[0].EntryID).Scan(&downloadID, &enqueueOperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE rss_entries SET status = 'enqueue_failed' WHERE id = $1`, result.Candidates[0].EntryID); err != nil {
		t.Fatal(err)
	}
	downloadWorkflow := NewDownloadWorkflow(db.New(pool), transactor, scheduler)
	if err := downloadWorkflow.CompleteEnqueue(ctx, domain.DownloadEnqueueCompletion{
		OperationID: enqueueOperationID,
		DownloadID:  downloadID,
		TorrentHash: "2123456789abcdef0123456789abcdef01234567",
		SavePath:    "/downloads/recovered",
		Files: []domain.ClassifiedDownloadFile{{
			DownloadFile:  domain.DownloadFile{Index: 0, RelativePath: "RSS Integration - S01E01.mkv", SizeBytes: 1000},
			Kind:          domain.MediaVideo,
			Selected:      true,
			SourceSeason:  1,
			SourceEpisode: 1,
		}},
	}); err != nil {
		t.Fatalf("CompleteEnqueue() after RSS enqueue failure error = %v", err)
	}
	var entryStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM rss_entries WHERE id = $1`, result.Candidates[0].EntryID).Scan(&entryStatus); err != nil {
		t.Fatal(err)
	}
	if entryStatus != string(domain.RSSEnqueued) {
		t.Fatalf("RSS entry status after recovered enqueue = %q, want %q", entryStatus, domain.RSSEnqueued)
	}
}

func TestRSSPollRetriesOnlyFailuresMarkedRetryableIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, subscriptionID, operationID := uuid.New(), uuid.New(), uuid.New()
	discoveredID, retryableID, permanentID := uuid.New(), uuid.New(), uuid.New()

	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'RSS Retry Boundary')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'RSS Retry Boundary', 'https://example.test/retry.xml', true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status,
    last_error_code, last_error_message, last_error_retryable
) VALUES
    ($1, $4, 'guid:discovered', 'Discovered E01', 'https://example.test/1.torrent', true, ARRAY[]::text[], 1, 1, 'discovered', NULL, NULL, false),
    ($2, $4, 'guid:retryable', 'Retryable E02', 'https://example.test/2.torrent', true, ARRAY[]::text[], 1, 2, 'enqueue_failed', 'qbittorrent_unavailable', 'temporary outage', true),
    ($3, $4, 'guid:permanent', 'Permanent E03', 'https://example.test/3.torrent', true, ARRAY[]::text[], 1, 3, 'enqueue_failed', 'duplicate_torrent', 'already owned', false)`, discoveredID, retryableID, permanentID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'running', 3, 60)`, operationID, subscriptionID, "rss-retry-boundary-"+operationID.String()); err != nil {
		t.Fatal(err)
	}

	workflow := NewRSSWorkflow(db.New(pool), database.NewTransactor(pool), nil)
	result, err := workflow.PersistPoll(ctx, operationID, subscriptionID, domain.RSSFeed{Title: "RSS Retry Boundary"}, domain.RSSPollPersistOptions{})
	if err != nil {
		t.Fatalf("PersistPoll() error = %v", err)
	}
	got := make(map[uuid.UUID]bool, len(result.Candidates))
	for _, candidate := range result.Candidates {
		got[candidate.EntryID] = true
	}
	if len(got) != 2 || !got[discoveredID] || !got[retryableID] || got[permanentID] {
		t.Fatalf("automatic retry candidates = %v, want discovered %s and retryable %s only", got, discoveredID, retryableID)
	}
}

func TestSubtitleArtifactPersistsWithoutTranscodeProfileIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, acquisitionID, downloadID := uuid.New(), uuid.New(), uuid.New()
	videoFileID, subtitleFileID, profileID, taskID, operationID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Artifact Integration')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', 'magnet:?xt=urn:btih:3123456789abcdef0123456789abcdef01234567')`, acquisitionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'materialized')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $3, 0, 'Show.S01E01.mkv', 1000, 'video', true, 1, 1),
       ($2, $3, 1, 'Show.S01E01.ass', 100, 'subtitle', true, 1, 1)`, videoFileID, subtitleFileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
  id, name, version, active, is_default, video_codec, encoder, container, file_extension,
  quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)`, profileID, "artifact-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, 'processing', 'transcoding', 'extracting_or_converting')`, taskID, acquisitionID, videoFileID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'subtitle.prepare', 'episode_task', $2, $3, 'running', 3, 60)`, operationID, taskID, "subtitle-"+operationID.String()); err != nil {
		t.Fatal(err)
	}

	workflow := NewMediaWorkflow(db.New(pool), database.NewTransactor(pool), nil)
	checksum := make([]byte, 32)
	checksum[0] = 1
	if err := workflow.CompleteArtifact(ctx, domain.MediaArtifactCompletion{
		TaskID: taskID, OperationID: operationID, SourceFileID: subtitleFileID, Kind: domain.MediaSubtitle,
		BaseName: "Artifact Integration - S01E01 - Pilot", FilePath: "/staging/Artifact Integration - S01E01 - Pilot.ass",
		Format: "ass", SizeBytes: 100, ChecksumSHA256: checksum,
	}); err != nil {
		t.Fatalf("CompleteArtifact() error = %v", err)
	}
	var transcodeProfileID *uuid.UUID
	var subtitleState string
	if err := pool.QueryRow(ctx, `SELECT transcode_profile_id FROM media_artifacts WHERE task_id = $1 AND kind = 'subtitle'`, taskID).Scan(&transcodeProfileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT subtitle_state FROM episode_tasks WHERE id = $1`, taskID).Scan(&subtitleState); err != nil {
		t.Fatal(err)
	}
	if transcodeProfileID != nil || subtitleState != "ass_ready" {
		t.Fatalf("subtitle profile/state = %v/%q, want nil/ass_ready", transcodeProfileID, subtitleState)
	}
}

func TestDownloadCompletionAllowsHashReuseOnlyAfterCancellationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	workflow := NewDownloadWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, &integrationJobInserter{}))
	seriesID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Duplicate Integration')`, seriesID); err != nil {
		t.Fatal(err)
	}
	hash := "4123456789abcdef0123456789abcdef01234567"
	complete := func(index int) (uuid.UUID, error) {
		acquisitionID, downloadID, operationID := uuid.New(), uuid.New(), uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', $3)`, acquisitionID, seriesID, "magnet:?xt=urn:btih:"+hash); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'enqueue_pending')`, downloadID, acquisitionID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'download.enqueue', 'download', $2, $3, 'running', 3, 60)`, operationID, downloadID, "enqueue-"+operationID.String()); err != nil {
			t.Fatal(err)
		}
		err := workflow.CompleteEnqueue(ctx, domain.DownloadEnqueueCompletion{
			OperationID: operationID, DownloadID: downloadID, TorrentHash: hash, SavePath: "/downloads/fixture",
			Files: []domain.ClassifiedDownloadFile{{DownloadFile: domain.DownloadFile{Index: index, RelativePath: "Show.S01E01.mkv", SizeBytes: 1000}, Kind: domain.MediaVideo, Selected: true, SourceSeason: 1, SourceEpisode: 1}},
		})
		return downloadID, err
	}
	firstID, err := complete(0)
	if err != nil {
		t.Fatalf("first CompleteEnqueue() error = %v", err)
	}
	if _, err := complete(1); !errors.Is(err, domain.ErrDuplicateTorrent) {
		t.Fatalf("second active CompleteEnqueue() error = %v, want ErrDuplicateTorrent", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE downloads SET status = 'cancelled' WHERE id = $1`, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := complete(2); err != nil {
		t.Fatalf("CompleteEnqueue() after cancellation error = %v", err)
	}

	var totalCount, activeCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE status <> 'cancelled')
		FROM downloads WHERE lower(torrent_hash) = $1`, hash).Scan(&totalCount, &activeCount); err != nil {
		t.Fatal(err)
	}
	if totalCount != 2 || activeCount != 1 {
		t.Fatalf("persisted hash counts = total %d active %d, want 2/1", totalCount, activeCount)
	}
}
