//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestAgentMappingContextKeepsFractionalAndIntegerSourcesDistinctIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	seriesID, seasonID, targetEpisodeID := uuid.New(), uuid.New(), uuid.New()
	acquisitionID, downloadID := uuid.New(), uuid.New()
	fractionalFileID, integerFileID := uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, tmdb_series_id, title)
VALUES ($1, $2, 'Agent fractional mapping context');
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count)
VALUES ($3, $1, 1, 1);
INSERT INTO media_episodes (id, season_id, episode_number, title)
VALUES ($4, $3, 1, 'Episode 1');
INSERT INTO acquisitions (id, series_id, source_kind, source_uri)
VALUES ($5, $1, 'manual', 'https://example.test/agent-fractional.torrent');
INSERT INTO downloads (id, acquisition_id, status)
VALUES ($6, $5, 'completed');
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind, selected,
    source_season, source_episode, source_episode_fraction_hundredths
) VALUES
    ($7, $6, 0, 'Show.S01E12.5.mkv', 1000, 'video', true, 1, 12, 50),
    ($8, $6, 1, 'Show.S01E125.mkv', 1000, 'video', true, 1, 125, 0)
`, seriesID, time.Now().UnixNano(), seasonID, targetEpisodeID, acquisitionID, downloadID, fractionalFileID, integerFileID); err != nil {
		t.Fatal(err)
	}

	service := &AgentResolutionService{queries: db.New(pool), transactor: database.NewTransactor(pool)}
	snapshot, err := service.buildMappingAgentContext(ctx, acquisitionID)
	if err != nil {
		t.Fatalf("buildMappingAgentContext() error = %v", err)
	}
	if len(snapshot.Files) != 2 {
		t.Fatalf("mapping context file count = %d, want 2", len(snapshot.Files))
	}
	fractional := snapshot.Files[fractionalFileID]
	integer := snapshot.Files[integerFileID]
	if fractional.SourceEpisode == nil || *fractional.SourceEpisode != 12 || fractional.SourceEpisodeFractionHundredths != 50 {
		t.Fatalf("fractional context file = %#v", fractional)
	}
	if integer.SourceEpisode == nil || *integer.SourceEpisode != 125 || integer.SourceEpisodeFractionHundredths != 0 {
		t.Fatalf("integer context file = %#v", integer)
	}
}
