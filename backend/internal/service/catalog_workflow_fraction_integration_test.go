//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestExplicitRSSMappingSynchronizesFractionalSourceFactsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	actorID, seriesID, seasonID, targetEpisodeID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	subscriptionID, entryID, acquisitionID := uuid.New(), uuid.New(), uuid.New()
	downloadID, sourceFileID := uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO admin_users (id, username, password_hash)
VALUES ($1, $2, 'fixture-hash');
INSERT INTO media_series (id, tmdb_series_id, title)
VALUES ($3, $4, 'Fractional explicit RSS');
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count)
VALUES ($5, $3, 0, 1);
INSERT INTO media_episodes (id, season_id, episode_number, title)
VALUES ($6, $5, 1, 'Special 1');
INSERT INTO rss_subscriptions (
    id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season
) VALUES ($7, $3, 'Fractional explicit RSS', $8, false, 900, 1);
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, source_episode_fraction_hundredths, status
) VALUES ($9, $7, $10, 'Fractional explicit RSS 12.5', $11, true, ARRAY[]::text[], 1, 12, 0, 'enqueued');
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id, created_by)
VALUES ($12, $3, 'rss', $9, $1);
INSERT INTO downloads (id, acquisition_id, status, progress)
VALUES ($13, $12, 'completed', 1);
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind, selected,
    source_season, source_episode, source_episode_fraction_hundredths
) VALUES ($14, $13, 0, 'Show.S01E12.5.mkv', 1000, 'video', true, 1, 12, 0)
`, actorID, "fractional-explicit-"+actorID.String(), seriesID, time.Now().UnixNano(), seasonID,
		targetEpisodeID, subscriptionID, "https://example.test/"+subscriptionID.String()+".xml",
		entryID, "guid:"+entryID.String(), "https://example.test/12.5.torrent",
		acquisitionID, downloadID, sourceFileID); err != nil {
		t.Fatal(err)
	}

	transactor := database.NewTransactor(pool)
	workflow := NewCatalogWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, &integrationJobInserter{}))
	saved, err := workflow.SaveEpisodeMapping(ctx, domain.EpisodeMappingPlanInput{
		AcquisitionID: acquisitionID,
		Mode:          domain.EpisodeMappingModeExplicit,
		Assignments: []domain.EpisodeMappingExplicitInput{{
			SourceFileID: sourceFileID,
			Action:       domain.EpisodeMappingExplicitMap,
			Target:       domain.EpisodeCoordinate{Season: 0, Episode: 1},
		}},
		IdempotencyKey: "fractional-explicit-rss",
		ActorUserID:    actorID,
	})
	if err != nil {
		t.Fatalf("SaveEpisodeMapping() error = %v", err)
	}
	if len(saved.Preview.Rows) != 1 || saved.Preview.Rows[0].SourceEpisode != 12 || saved.Preview.Rows[0].SourceEpisodeFractionHundredths != 50 {
		t.Fatalf("saved preview = %#v", saved.Preview)
	}

	var fileEpisode, fileFraction, payloadEpisode, payloadFraction, entryEpisode, entryFraction int
	if err := pool.QueryRow(ctx, `
SELECT
    file.source_episode,
    file.source_episode_fraction_hundredths,
    (acquisition.source_payload->>'sourceEpisode')::integer,
    (acquisition.source_payload->>'sourceEpisodeFractionHundredths')::integer,
    entry.source_episode,
    entry.source_episode_fraction_hundredths
FROM download_files AS file
JOIN downloads AS download ON download.id = file.download_id
JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE file.id = $1
`, sourceFileID).Scan(&fileEpisode, &fileFraction, &payloadEpisode, &payloadFraction, &entryEpisode, &entryFraction); err != nil {
		t.Fatal(err)
	}
	if fileEpisode != 12 || payloadEpisode != 12 || entryEpisode != 12 || fileFraction != 50 || payloadFraction != 50 || entryFraction != 50 {
		t.Fatalf("fractional source facts = file %d.%02d payload %d.%02d entry %d.%02d", fileEpisode, fileFraction, payloadEpisode, payloadFraction, entryEpisode, entryFraction)
	}
}
