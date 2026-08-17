//go:build integration

package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
)

type rssProgressTriggerFixture struct {
	subscriptionID       uuid.UUID
	entryID              uuid.UUID
	acquisitionID        uuid.UUID
	downloadID           uuid.UUID
	fileID               uuid.UUID
	taskID               uuid.UUID
	artifactSetID        uuid.UUID
	reviewID             uuid.UUID
	importID             uuid.UUID
	cleanupID            uuid.UUID
	olderRefreshID       uuid.UUID
	newerRefreshID       uuid.UUID
	mappingID            uuid.UUID
	mappingProfileID     uuid.UUID
	secondSubscriptionID uuid.UUID
	secondEntryID        uuid.UUID
	secondAcquisitionID  uuid.UUID
}

func seedRSSProgressTriggerFixture(
	t *testing.T,
	ctx context.Context,
	fixture recoveryFixture,
) rssProgressTriggerFixture {
	t.Helper()
	ids := rssProgressTriggerFixture{
		subscriptionID:       uuid.New(),
		entryID:              uuid.New(),
		acquisitionID:        uuid.New(),
		downloadID:           uuid.New(),
		fileID:               uuid.New(),
		taskID:               uuid.New(),
		artifactSetID:        uuid.New(),
		reviewID:             uuid.New(),
		importID:             uuid.New(),
		cleanupID:            uuid.New(),
		olderRefreshID:       uuid.New(),
		newerRefreshID:       uuid.New(),
		mappingID:            uuid.New(),
		mappingProfileID:     uuid.New(),
		secondSubscriptionID: uuid.New(),
		secondEntryID:        uuid.New(),
		secondAcquisitionID:  uuid.New(),
	}
	seasonID, episodeID := uuid.New(), uuid.New()
	videoArtifactID, subtitleArtifactID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count)
VALUES ($1, $2, 1, 1)
`, seasonID, fixture.seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO media_episodes (id, season_id, episode_number, title)
VALUES ($1, $2, 1, 'Trigger fixture episode')
`, episodeID, seasonID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (
    id, series_id, name, version, source_season_lengths, created_by, decision_source
) VALUES ($1, $2, $3, 1, ARRAY[1], $4, 'user')
`, ids.mappingProfileID, fixture.seriesID, "progress-trigger-"+ids.mappingProfileID.String(), fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_mappings (
    id, profile_id, source_season, source_episode, absolute_episode,
    target_episode_id, mapping_status, match_source
) VALUES ($1, $2, 1, 1, 1, $3, 'mapped', 'explicit')
`, ids.mappingID, ids.mappingProfileID, episodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
    id, series_id, mapping_profile_id, name, feed_url, enabled,
    poll_interval_seconds, source_season
) VALUES
    ($1, $3, $4, 'Trigger fixture primary', $5, true, 900, 1),
    ($2, $3, $4, 'Trigger fixture secondary', $6, true, 900, 1)
`, ids.subscriptionID, ids.secondSubscriptionID, fixture.seriesID, ids.mappingProfileID,
		"https://example.test/"+ids.subscriptionID.String()+".xml",
		"https://example.test/"+ids.secondSubscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES
    ($1, $3, $5, 'Trigger fixture S01E01', 'https://example.test/primary.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued'),
    ($2, $4, $6, 'Trigger fixture secondary S01E01', 'https://example.test/secondary.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued')
`, ids.entryID, ids.secondEntryID, ids.subscriptionID, ids.secondSubscriptionID,
		"guid:"+ids.entryID.String(), "guid:"+ids.secondEntryID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, mapping_profile_id, source_kind, rss_entry_id)
VALUES
    ($1, $3, $4, 'rss', $5),
    ($2, $3, $4, 'rss', $6)
`, ids.acquisitionID, ids.secondAcquisitionID, fixture.seriesID, ids.mappingProfileID, ids.entryID, ids.secondEntryID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, progress)
VALUES ($1, $2, 'materialized', 1)
`, ids.downloadID, ids.acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind,
    selected, source_season, source_episode
) VALUES ($1, $2, 0, 'Trigger.S01E01.mkv', 1024, 'video', true, 1, 1)
`, ids.fileID, ids.downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_tasks (
    id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id,
    state, video_state, subtitle_state
) VALUES ($1, $2, $3, $4, $5, 'processing', 'transcoding', 'extracting_or_converting')
`, ids.taskID, ids.acquisitionID, ids.fileID, ids.mappingID, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO media_artifacts (
    id, task_id, source_file_id, transcode_profile_id, kind, basename,
    file_path, format, size_bytes, checksum_sha256
) VALUES
    ($1, $3, $4, $5, 'video', 'Trigger - S01E01', $6, 'mkv', 1024, decode(repeat('01', 32), 'hex')),
    ($2, $3, $4, $5, 'subtitle', 'Trigger - S01E01', $7, 'ass', 128, decode(repeat('02', 32), 'hex'))
`, videoArtifactID, subtitleArtifactID, ids.taskID, ids.fileID, fixture.profileID,
		"/fixture/"+videoArtifactID.String()+".mkv", "/fixture/"+subtitleArtifactID.String()+".ass"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO artifact_sets (
    id, task_id, transcode_profile_id, basename, video_artifact_id, subtitle_artifact_id
) VALUES ($1, $2, $3, 'Trigger - S01E01', $4, $5)
`, ids.artifactSetID, ids.taskID, fixture.profileID, videoArtifactID, subtitleArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO reviews (id, task_id, decision, idempotency_key, expected_task_version)
VALUES ($1, $2, 'approved', $3, 1)
`, ids.reviewID, ids.taskID, "progress-trigger-review:"+ids.reviewID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO imports (id, task_id, status) VALUES ($1, $2, 'queued')`, ids.importID, ids.taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO cleanup_runs (id, task_id, download_id, status) VALUES ($1, $2, $3, 'queued')`, ids.cleanupID, ids.taskID, ids.downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, timeout_seconds, error_code, error_message,
    created_at, finished_at
) VALUES
    ($1, 'emby.refresh', 'episode_task', $3, $4, 'failed', 3, 60,
        'fixture_failure', 'fixture failure', now() - interval '2 minutes', now() - interval '2 minutes'),
    ($2, 'emby.refresh', 'episode_task', $3, $5, 'succeeded', 3, 60,
        NULL, NULL, now() - interval '1 minute', now() - interval '1 minute')
`, ids.olderRefreshID, ids.newerRefreshID, ids.taskID,
		"progress-trigger-refresh:"+ids.olderRefreshID.String(),
		"progress-trigger-refresh:"+ids.newerRefreshID.String()); err != nil {
		t.Fatal(err)
	}
	return ids
}

func assertRSSProgressDirtyAfterMutation(
	t *testing.T,
	ctx context.Context,
	fixture recoveryFixture,
	subscriptionIDs []uuid.UUID,
	mutate func(pgx.Tx) error,
) {
	t.Helper()
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	baseline := make(map[uuid.UUID]int64, len(subscriptionIDs))
	for _, subscriptionID := range subscriptionIDs {
		var revision int64
		var dirty bool
		if err := tx.QueryRow(ctx, `
SELECT source_revision, dirty
FROM rss_subscription_progress
WHERE subscription_id = $1
`, subscriptionID).Scan(&revision, &dirty); err != nil {
			t.Fatal(err)
		}
		if dirty {
			t.Fatalf("subscription %s projection is dirty before mutation", subscriptionID)
		}
		baseline[subscriptionID] = revision
	}
	if err := mutate(tx); err != nil {
		t.Fatal(err)
	}
	for _, subscriptionID := range subscriptionIDs {
		var revision int64
		var dirty bool
		if err := tx.QueryRow(ctx, `
SELECT source_revision, dirty
FROM rss_subscription_progress
WHERE subscription_id = $1
`, subscriptionID).Scan(&revision, &dirty); err != nil {
			t.Fatal(err)
		}
		if !dirty || revision <= baseline[subscriptionID] {
			t.Fatalf("subscription %s projection = dirty %t revision %d, want dirty revision above %d", subscriptionID, dirty, revision, baseline[subscriptionID])
		}
	}
}

func TestRSSSubscriptionProgressDependencyTriggersMarkEverySourceIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	ids := seedRSSProgressTriggerFixture(t, ctx, fixture)
	if reconciled, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil || reconciled != 2 {
		t.Fatalf("initial reconciliation = %d, %v, want 2, nil", reconciled, err)
	}

	primary := []uuid.UUID{ids.subscriptionID}
	tests := []struct {
		name   string
		mutate func(pgx.Tx) error
	}{
		{name: "rss subscriptions", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE rss_subscriptions SET source_season = 2 WHERE id = $1`, ids.subscriptionID)
			return err
		}},
		{name: "rss entries", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE rss_entries SET source_episode = 2 WHERE id = $1`, ids.entryID)
			return err
		}},
		{name: "acquisitions", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE acquisitions SET deletion_requested_at = now() WHERE id = $1`, ids.acquisitionID)
			return err
		}},
		{name: "downloads", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE downloads SET progress = 0.5 WHERE id = $1`, ids.downloadID)
			return err
		}},
		{name: "download files", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE download_files SET selected = false WHERE id = $1`, ids.fileID)
			return err
		}},
		{name: "episode tasks", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE episode_tasks SET video_state = 'video_ready' WHERE id = $1`, ids.taskID)
			return err
		}},
		{name: "artifact sets delete old relation", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM artifact_sets WHERE id = $1`, ids.artifactSetID)
			return err
		}},
		{name: "reviews delete old relation", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM reviews WHERE id = $1`, ids.reviewID)
			return err
		}},
		{name: "imports", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE imports SET status = 'running' WHERE id = $1`, ids.importID)
			return err
		}},
		{name: "cleanup runs", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE cleanup_runs SET status = 'running' WHERE id = $1`, ids.cleanupID)
			return err
		}},
		{name: "operations latest ordering", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE operations SET created_at = now() + interval '1 minute' WHERE id = $1`, ids.olderRefreshID)
			return err
		}},
		{name: "episode mappings", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE episode_mappings SET source_episode = 2 WHERE id = $1`, ids.mappingID)
			return err
		}},
		{name: "media series", mutate: func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
UPDATE media_series
SET media_type = 'movie', tmdb_movie_id = $2, release_year = 2026
WHERE id = $1
`, fixture.seriesID, time.Now().UnixNano())
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertRSSProgressDirtyAfterMutation(t, ctx, fixture, primary, test.mutate)
		})
	}

	t.Run("download association update marks old and new subscriptions", func(t *testing.T) {
		assertRSSProgressDirtyAfterMutation(
			t,
			ctx,
			fixture,
			[]uuid.UUID{ids.subscriptionID, ids.secondSubscriptionID},
			func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE downloads SET acquisition_id = $2 WHERE id = $1`, ids.downloadID, ids.secondAcquisitionID)
				return err
			},
		)
	})
	t.Run("acquisition delete retains old entry association", func(t *testing.T) {
		assertRSSProgressDirtyAfterMutation(t, ctx, fixture, []uuid.UUID{ids.secondSubscriptionID}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM acquisitions WHERE id = $1`, ids.secondAcquisitionID)
			return err
		})
	})
}

func TestRSSSubscriptionProgressOperationOrderingAndAcquisitionSeriesReconcileIntegration(t *testing.T) {
	t.Run("operation created time changes latest refresh", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fixture := newRecoveryFixture(t)
		workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
		ids := seedRSSProgressTriggerFixture(t, ctx, fixture)
		if _, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil {
			t.Fatal(err)
		}
		before, err := workflow.GetSubscription(ctx, ids.subscriptionID)
		if err != nil {
			t.Fatal(err)
		}
		if before.AttentionTaskCount != 0 {
			t.Fatalf("baseline attention count = %d, want 0", before.AttentionTaskCount)
		}
		if _, err := fixture.pool.Exec(ctx, `UPDATE operations SET created_at = now() + interval '1 minute' WHERE id = $1`, ids.olderRefreshID); err != nil {
			t.Fatal(err)
		}
		after, err := workflow.GetSubscription(ctx, ids.subscriptionID)
		if err != nil {
			t.Fatal(err)
		}
		if after.AttentionTaskCount != 1 {
			t.Fatalf("reconciled attention count = %d, want 1", after.AttentionTaskCount)
		}
	})

	t.Run("acquisition media series marks linked RSS subscription", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fixture := newRecoveryFixture(t)
		workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
		ids := seedRSSProgressTriggerFixture(t, ctx, fixture)
		alternateSeriesID := uuid.New()
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Alternate acquisition series')`, alternateSeriesID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(ctx, `UPDATE acquisitions SET series_id = $2 WHERE id = $1`, ids.acquisitionID, alternateSeriesID); err != nil {
			t.Fatal(err)
		}
		if _, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil {
			t.Fatal(err)
		}
		before, err := workflow.GetSubscription(ctx, ids.subscriptionID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(ctx, `
UPDATE media_series
SET media_type = 'movie', tmdb_movie_id = $2, release_year = 2026
WHERE id = $1
`, alternateSeriesID, time.Now().UnixNano()); err != nil {
			t.Fatal(err)
		}
		var dirty bool
		if err := fixture.pool.QueryRow(ctx, `SELECT dirty FROM rss_subscription_progress WHERE subscription_id = $1`, ids.subscriptionID).Scan(&dirty); err != nil {
			t.Fatal(err)
		}
		if !dirty {
			t.Fatal("acquisition-linked media series change did not mark RSS progress dirty")
		}
		after, err := workflow.GetSubscription(ctx, ids.subscriptionID)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(after.OverallProgress-before.OverallProgress) < 1e-12 {
			t.Fatalf("media type reconciliation kept stale progress %.12f", after.OverallProgress)
		}
	})
}
