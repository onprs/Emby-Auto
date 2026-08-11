//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestRSSReadModelKeepsRejectionAndEnqueueErrorsSeparateIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	seriesID, subscriptionID := uuid.New(), uuid.New()
	failedID, rejectedID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'RSS Error Presentation')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'RSS Error Presentation', 'https://example.test/errors.xml', true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status,
    last_error_code, last_error_message, last_error_retryable
) VALUES
    ($1, $3, 'guid:enqueue-failed', 'Show S01E01', 'https://example.test/1.torrent', true,
     ARRAY[]::text[], 1, 1, 'enqueue_failed', 'download_no_main_video', 'the torrent contains no selectable main video', false),
    ($2, $3, 'guid:rejected', 'Show recap', NULL, false,
     ARRAY['non_episode_extra']::text[], NULL, NULL, 'discovered', NULL, NULL, false)`, failedID, rejectedID, subscriptionID); err != nil {
		t.Fatal(err)
	}

	page, err := NewReadService(db.New(pool)).ListRSSEntries(ctx, subscriptionID, nil, 10, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListRSSEntries() error = %v", err)
	}
	entries := make(map[uuid.UUID]struct {
		classification string
		rejectReason   string
		errorCode      string
		errorMessage   string
	}, len(page.Items))
	for _, item := range page.Items {
		entries[item.ID] = struct {
			classification string
			rejectReason   string
			errorCode      string
			errorMessage   string
		}{item.Classification, item.RejectReason, item.ErrorCode, item.ErrorMessage}
	}

	failed := entries[failedID]
	if failed.classification != "enqueue_failed" || failed.rejectReason != "" || failed.errorCode != "download_no_main_video" || failed.errorMessage == "" {
		t.Fatalf("enqueue failure view = %#v", failed)
	}
	rejected := entries[rejectedID]
	if rejected.classification != "rejected" || rejected.rejectReason != "non_episode_extra" || rejected.errorCode != "" || rejected.errorMessage != "" {
		t.Fatalf("rejected view = %#v", rejected)
	}

	readService := NewReadService(db.New(pool))
	confirmedGroup, skippedGroup := "confirmed", "skipped"
	confirmed, err := readService.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, &confirmedGroup, nil, nil)
	if err != nil || len(confirmed.Items) != 1 || confirmed.Items[0].ID != failedID {
		t.Fatalf("confirmed RSS group = %#v, error = %v", confirmed.Items, err)
	}
	skipped, err := readService.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, &skippedGroup, nil, nil)
	if err != nil || len(skipped.Items) != 1 || skipped.Items[0].ID != rejectedID {
		t.Fatalf("skipped RSS group = %#v, error = %v", skipped.Items, err)
	}
}

func TestRSSReadModelPlacesHistoricalEnqueueWithCatalogFulfillmentInSkippedGroupIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	seriesID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO media_series (id, title) VALUES ($1, 'RSS Catalog Fulfillment Presentation');
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($2, $1, 'RSS Catalog Fulfillment Presentation', 'https://example.test/catalog-fulfillment.xml', true, 900, 1);
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status, imported_at, fulfillment_source
) VALUES (
    $3, $2, 'guid:catalog-fulfilled-enqueued', 'Show S01E01', 'https://example.test/catalog-fulfilled.torrent', false,
    ARRAY['target_episode_in_library']::text[], 1, 1, 'enqueued', now(), 'emby_catalog'
)`, seriesID, subscriptionID, entryID); err != nil {
		t.Fatal(err)
	}

	readService := NewReadService(db.New(pool))
	confirmedGroup, skippedGroup := "confirmed", "skipped"
	confirmed, err := readService.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, &confirmedGroup, nil, nil)
	if err != nil || len(confirmed.Items) != 0 {
		t.Fatalf("confirmed RSS catalog fulfillment group = %#v, error = %v", confirmed.Items, err)
	}
	skipped, err := readService.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, &skippedGroup, nil, nil)
	if err != nil || len(skipped.Items) != 1 {
		t.Fatalf("skipped RSS catalog fulfillment group = %#v, error = %v", skipped.Items, err)
	}
	entry := skipped.Items[0]
	if entry.ID != entryID || entry.Status != "enqueued" || entry.Classification != "rejected" || entry.RejectReason != rssTargetInLibraryReason {
		t.Fatalf("catalog fulfillment entry = %#v", entry)
	}
}

func TestArchivedRSSImportRemainsLinkedWithCompletedStagesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	seriesID, seasonID, episodeID, profileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	subscriptionID, entryID, acquisitionID, downloadID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, 99001, 'Archived RSS Series');
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($2, $1, 1, 1);
INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($3, $2, 1, 'Archived Episode');
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active)
VALUES ($4, $1, 'archive-profile', 1, ARRAY[1]::integer[], true);
INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, absolute_episode, target_episode_id, mapping_status, match_source)
VALUES (gen_random_uuid(), $4, 1, 1, 1, $3, 'mapped', 'explicit');
INSERT INTO rss_subscriptions (
    id, series_id, mapping_profile_id, name, feed_url, enabled,
    poll_interval_seconds, source_season, completed_at
)
VALUES ($5, $1, $4, 'Archived RSS', 'https://example.test/archive.xml', false, 900, 1, now());
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status, imported_at, fulfillment_source
)
VALUES ($6, $5, 'guid:archived-entry', 'Archived RSS S01E01', 'https://example.test/archive.torrent', true,
        ARRAY[]::text[], 1, 1, 'enqueued', now(), 'managed_import');
`, seriesID, seasonID, episodeID, profileID, subscriptionID, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO events (id, topic, resource_type, resource_id, data, occurred_at) VALUES
    (gen_random_uuid(), 'rss.entry.enqueueing', 'rss_entry', $1,
     jsonb_build_object('acquisitionId', $2::uuid, 'downloadId', $3::uuid), now() - interval '10 minutes'),
    (gen_random_uuid(), 'task.created', 'episode_task', $4,
     jsonb_build_object('downloadId', $3::uuid, 'sourceSeason', 1, 'sourceEpisode', 1, 'targetSeason', 1, 'targetEpisode', 1), now() - interval '9 minutes'),
    (gen_random_uuid(), 'task.video_ready', 'episode_task', $4, '{}'::jsonb, now() - interval '8 minutes'),
    (gen_random_uuid(), 'task.subtitle_ready', 'episode_task', $4, '{}'::jsonb, now() - interval '7 minutes'),
    (gen_random_uuid(), 'task.awaiting_review', 'episode_task', $4, '{}'::jsonb, now() - interval '6 minutes'),
    (gen_random_uuid(), 'task.reviewed', 'episode_task', $4, jsonb_build_object('decision', 'approved'), now() - interval '5 minutes'),
    (gen_random_uuid(), 'task.imported', 'episode_task', $4, '{}'::jsonb, now() - interval '4 minutes'),
    (gen_random_uuid(), 'acquisition.delete_completed', 'acquisition', $2, '{}'::jsonb, now() - interval '3 minutes');
`, entryID, acquisitionID, downloadID, taskID); err != nil {
		t.Fatal(err)
	}

	readService := NewReadService(db.New(pool))
	page, err := readService.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListRSSEntries() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].AcquisitionID == nil || *page.Items[0].AcquisitionID != acquisitionID || page.Items[0].ImportedAt == nil {
		t.Fatalf("archived RSS entry = %#v", page.Items)
	}
	if page.Items[0].AcquisitionProgress == nil || page.Items[0].AcquisitionProgress.AggregateStatus != "completed" || page.Items[0].AcquisitionProgress.OverallProgress != 1 {
		t.Fatalf("archived RSS progress = %#v", page.Items[0].AcquisitionProgress)
	}

	acquisition, err := readService.GetAcquisition(ctx, acquisitionID)
	if err != nil {
		t.Fatalf("GetAcquisition(archived) error = %v", err)
	}
	if !acquisition.Archived || acquisition.ArchivedAt == nil || acquisition.DownloadID != nil || len(acquisition.Tasks) != 0 || len(acquisition.Stages) != 9 {
		t.Fatalf("archived acquisition = %#v", acquisition)
	}
	for _, stage := range acquisition.Stages {
		if stage.Status != "completed" || stage.Progress != 1 || stage.CompletedItems != 1 || stage.TotalItems != 1 || stage.UpdatedAt == nil {
			t.Fatalf("archived stage = %#v", stage)
		}
	}
}
