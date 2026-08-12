//go:build integration

package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestRestoreRSSHistoryIsDisabledIncompleteAtomicAndDoesNotScheduleIntegration(t *testing.T) {
	ctx := context.Background()
	_, pool := testutil.NewMigratedPostgres(t)
	actorID, seriesID, seasonID, profileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	episodeIDs := []uuid.UUID{uuid.New(), uuid.New()}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-history-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Restored History')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 2)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	for index, episodeID := range episodeIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, $3, $4)`, episodeID, seasonID, index+1, "Episode"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active, created_by, decision_source)
VALUES ($1, $2, 'restore', 1, ARRAY[2]::integer[], true, $3, 'user')
`, profileID, seriesID, actorID); err != nil {
		t.Fatal(err)
	}
	for index, episodeID := range episodeIDs {
		if _, err := pool.Exec(ctx, `
INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, absolute_episode, target_episode_id, mapping_status, match_source)
VALUES ($1, $2, 1, $3, $3, $4, 'mapped', 'explicit')
`, uuid.New(), profileID, index+1, episodeID); err != nil {
			t.Fatal(err)
		}
	}

	request := validRestoreRequest(t)
	request.Snapshot.Subscription.SeriesID = seriesID
	request.MappingProfileID = profileID
	restorer := NewRSSHistoryRestorer(database.NewTransactor(pool))
	result, err := restorer.Restore(ctx, request)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if result.SubscriptionID != request.Snapshot.Subscription.ID || result.EntryCount != 2 {
		t.Fatalf("Restore() result = %#v", result)
	}

	var enabled, archived, completed bool
	var restoredProfile uuid.UUID
	var entryCount, normalizedCount, acquisitionCount, operationCount, riverJobCount int
	if err := pool.QueryRow(ctx, `
SELECT
    subscription.enabled,
    subscription.deleted_at IS NOT NULL,
    subscription.completed_at IS NOT NULL,
    subscription.mapping_profile_id,
    (SELECT count(*) FROM rss_entries WHERE subscription_id = subscription.id),
    (SELECT count(*) FROM rss_entries WHERE subscription_id = subscription.id AND status = 'enqueued' AND enqueued_at IS NOT NULL AND last_error_code IS NULL AND NOT last_error_retryable),
    (SELECT count(*) FROM acquisitions AS acquisition JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id WHERE entry.subscription_id = subscription.id),
    (SELECT count(*) FROM operations WHERE resource_type = 'rss_subscription' AND resource_id = subscription.id),
    (SELECT count(*) FROM river_job)
FROM rss_subscriptions AS subscription
WHERE subscription.id = $1
`, request.Snapshot.Subscription.ID).Scan(
		&enabled, &archived, &completed, &restoredProfile, &entryCount, &normalizedCount,
		&acquisitionCount, &operationCount, &riverJobCount,
	); err != nil {
		t.Fatal(err)
	}
	if enabled || archived || completed || restoredProfile != profileID {
		t.Fatalf("subscription = enabled %t archived %t completed %t profile %s", enabled, archived, completed, restoredProfile)
	}
	if entryCount != 2 || normalizedCount != 2 || acquisitionCount != 0 || operationCount != 0 || riverJobCount != 0 {
		t.Fatalf("side effects = entries %d normalized %d acquisitions %d operations %d River jobs %d", entryCount, normalizedCount, acquisitionCount, operationCount, riverJobCount)
	}
	var eventTopic string
	if err := pool.QueryRow(ctx, `SELECT topic FROM events WHERE resource_type = 'rss_subscription' AND resource_id = $1 ORDER BY event_sequence DESC LIMIT 1`, request.Snapshot.Subscription.ID).Scan(&eventTopic); err != nil {
		t.Fatal(err)
	}
	if eventTopic != "rss.subscription.history_restored" {
		t.Fatalf("restoration event = %q", eventTopic)
	}

	if _, err := restorer.Restore(ctx, request); err == nil {
		t.Fatal("Restore(replay) error = nil")
	}
	var replayEntryCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_entries WHERE subscription_id = $1`, request.Snapshot.Subscription.ID).Scan(&replayEntryCount); err != nil {
		t.Fatal(err)
	}
	if replayEntryCount != 2 {
		t.Fatalf("entry count after rejected replay = %d", replayEntryCount)
	}
}
