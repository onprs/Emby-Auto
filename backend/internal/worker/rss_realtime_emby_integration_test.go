//go:build integration

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type realtimeConfigurationStub struct{}

func (realtimeConfigurationStub) Load(context.Context) (domain.Configuration, error) {
	return domain.Configuration{Settings: domain.RuntimeSettings{Emby: domain.EmbySettings{URL: "http://emby.test"}}}, nil
}

func (realtimeConfigurationStub) ResolveSecret(context.Context, string) (string, error) {
	return "configured-key", nil
}

type realtimeEmbyClientStub struct {
	seriesID int64
	episodes []domain.EmbyLibraryItemCatalog
}

func (stub *realtimeEmbyClientStub) SeriesEpisodesByTMDb(_ context.Context, seriesID int64) ([]domain.EmbyLibraryItemCatalog, error) {
	stub.seriesID = seriesID
	return stub.episodes, nil
}

func TestRSSRealtimeEmbyVerifierRecordsMappedTargetCheckIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	seriesID, seasonID, episodeID := uuid.New(), uuid.New(), uuid.New()
	profileID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, 500, 'Realtime Target');
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($2, $1, 2, 1);
INSERT INTO media_episodes (id, season_id, tmdb_episode_id, episode_number, title) VALUES ($3, $2, 9001, 1, 'Mapped Target');
INSERT INTO episode_mapping_profiles (
  id, series_id, name, version, source_season_lengths, active,
  anchor_source_season, anchor_source_episode, anchor_target_episode_id,
  target_episode_offset, decision_source
) VALUES ($4, $1, 'realtime', 1, ARRAY[1], true, 1, 1, $3, 0, 'deterministic');
INSERT INTO episode_mappings (
  id, profile_id, source_season, source_episode, absolute_episode,
  target_episode_id, mapping_status, match_source
) VALUES ($5, $4, 1, 1, 1, $3, 'mapped', 'anchor');
INSERT INTO rss_subscriptions (
  id, series_id, mapping_profile_id, name, feed_url, enabled,
  poll_interval_seconds, source_season
) VALUES ($6, $1, $4, 'Realtime Feed', 'https://example.test/feed.xml', true, 900, 1);
INSERT INTO rss_entries (
  id, subscription_id, identity_key, title, downloadable, rejection_reasons,
  source_season, source_episode, status, imported_at, fulfillment_source
) VALUES ($7, $6, $8, 'Catalog target', false, ARRAY['target_episode_in_library']::text[],
          1, 1, 'discovered', now() - interval '2 minutes', 'emby_catalog');
INSERT INTO rss_target_fulfillments (
  rss_entry_id, target_episode_id, source, verified_at, invalidated_at
) VALUES ($7, $3, 'emby_catalog', now() - interval '2 minutes', now() - interval '1 minute')`,
		seriesID, seasonID, episodeID, profileID, uuid.New(), subscriptionID, entryID, "guid:"+entryID.String(),
	); err != nil {
		t.Fatal(err)
	}
	targetSeason, targetEpisode := 2, 1
	client := &realtimeEmbyClientStub{episodes: []domain.EmbyLibraryItemCatalog{{
		ItemType: "Episode", Path: "/library/target.mkv", ProviderIDs: map[string]string{"Tmdb": "9001"},
		SeasonNumber: &targetSeason, EpisodeNumber: &targetEpisode,
	}}}
	verifier := NewRSSRealtimeEmbyVerifier(
		realtimeConfigurationStub{}, queries, database.NewTransactor(pool),
		func(options emby.ClientOptions) (RSSRealtimeEmbyClient, error) {
			if options.BaseURL != "http://emby.test" || options.APIKey != "configured-key" {
				t.Fatalf("client options = %#v", options)
			}
			return client, nil
		},
	)
	checkID, err := verifier.VerifyCoordinates(ctx, subscriptionID, []domain.EpisodeCoordinate{{Season: 1, Episode: 1}})
	if err != nil {
		t.Fatalf("VerifyCoordinates() error = %v", err)
	}
	if checkID == uuid.Nil || client.seriesID != 500 {
		t.Fatalf("checkID = %s, seriesID = %d", checkID, client.seriesID)
	}
	var gotTargetID uuid.UUID
	var present bool
	var source string
	var invalidatedAt *time.Time
	if err := pool.QueryRow(ctx, `
SELECT realtime.target_episode_id, realtime.present, realtime.match_source, fulfillment.invalidated_at
FROM rss_target_realtime_checks AS realtime
JOIN rss_target_fulfillments AS fulfillment
  ON fulfillment.target_episode_id = realtime.target_episode_id
WHERE realtime.check_id = $1`, checkID).Scan(&gotTargetID, &present, &source, &invalidatedAt); err != nil {
		t.Fatal(err)
	}
	if gotTargetID != episodeID || !present || source != "tmdb_episode" || invalidatedAt != nil {
		t.Fatalf("stored present check = target %s present %t source %q invalidated %v", gotTargetID, present, source, invalidatedAt)
	}

	client.episodes = nil
	absentCheckID, err := verifier.VerifyCoordinates(ctx, subscriptionID, []domain.EpisodeCoordinate{{Season: 1, Episode: 1}})
	if err != nil {
		t.Fatalf("VerifyCoordinates(absent) error = %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT realtime.present, realtime.match_source, fulfillment.invalidated_at
FROM rss_target_realtime_checks AS realtime
JOIN rss_target_fulfillments AS fulfillment
  ON fulfillment.target_episode_id = realtime.target_episode_id
WHERE realtime.check_id = $1`, absentCheckID).Scan(&present, &source, &invalidatedAt); err != nil {
		t.Fatal(err)
	}
	if present || source != "absent" || invalidatedAt == nil {
		t.Fatalf("stored absent check = present %t source %q invalidated %v", present, source, invalidatedAt)
	}
}
