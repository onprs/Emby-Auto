//go:build integration

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func tmdbCatalogEpisode(number int, tmdbID int64, title string) domain.TMDbEpisodeCatalog {
	return domain.TMDbEpisodeCatalog{
		TMDbEpisodeID: tmdbID,
		EpisodeNumber: number,
		Name:          title,
		Payload:       []byte(fmt.Sprintf(`{"id":%d}`, tmdbID)),
	}
}

func tmdbCatalogSeason(number int, tmdbID int64, episodes ...domain.TMDbEpisodeCatalog) domain.TMDbSeasonCatalog {
	return domain.TMDbSeasonCatalog{
		TMDbSeasonID: tmdbID,
		SeasonNumber: number,
		Name:         fmt.Sprintf("Season %d", number),
		Payload:      []byte(fmt.Sprintf(`{"id":%d}`, tmdbID)),
		Episodes:     episodes,
	}
}

// TestSaveTMDbCatalogRetainsReferencedStaleRowsIntegration 覆盖 TMDb 目录刷新
// 删除已映射/已锚定的集与整季时必须保留引用行，而不是让 RESTRICT 外键回滚
// 整次同步；过期行以 present=false 保留，TMDb 恢复后再重新激活。
func TestSaveTMDbCatalogRetainsReferencedStaleRowsIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := db.New(fixture.pool)
	workflow := NewCatalogWorkflow(queries, fixture.transactor, fixture.scheduler)
	const tmdbSeriesID int64 = 900001
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_series SET tmdb_series_id = $2 WHERE id = $1`, fixture.seriesID, tmdbSeriesID); err != nil {
		t.Fatal(err)
	}
	operationID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, started_at)
VALUES ($1, 'tmdb.sync', 'media_series', $2, $3, 'running', 4, 600, now())
`, operationID, fixture.seriesID, "tmdb-sync-"+operationID.String()); err != nil {
		t.Fatal(err)
	}
	operation := domain.Operation{ID: operationID, ResourceType: "media_series", ResourceID: fixture.seriesID}
	initial := domain.TMDbSeriesCatalog{
		TMDbSeriesID: tmdbSeriesID,
		Name:         "Recovery Series",
		Payload:      []byte(`{"id":900001}`),
		Seasons: []domain.TMDbSeasonCatalog{tmdbCatalogSeason(
			1, 910001,
			tmdbCatalogEpisode(1, 920001, "Episode 1"),
			tmdbCatalogEpisode(2, 920002, "Episode 2"),
		)},
	}
	if err := workflow.SaveTMDbCatalog(ctx, operation, initial); err != nil {
		t.Fatalf("初始目录同步失败: %v", err)
	}
	var episode1ID, episode2ID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `SELECT id FROM media_episodes WHERE tmdb_episode_id = 920001`).Scan(&episode1ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT id FROM media_episodes WHERE tmdb_episode_id = 920002`).Scan(&episode2ID); err != nil {
		t.Fatal(err)
	}
	profileID, mappingID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (
    id, series_id, name, version, source_season_lengths, active, created_by,
    anchor_source_season, anchor_source_episode, anchor_target_episode_id, target_episode_offset,
    decision_source
)
VALUES ($1, $2, $3, 1, ARRAY[2]::integer[], true, $4, 1, 2, $5, 0, 'user')
`, profileID, fixture.seriesID, "tmdb-stale-"+profileID.String(), fixture.actorID, episode2ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_mappings (
    id, profile_id, source_season, source_episode, absolute_episode,
    target_episode_id, mapping_status, match_source
)
VALUES ($1, $2, 1, 2, 2, $3, 'mapped', 'anchor')
`, mappingID, profileID, episode2ID); err != nil {
		t.Fatal(err)
	}

	// 上游移除已被映射的第 2 集：同步必须成功并保留映射目标。
	withoutSecond := initial
	withoutSecond.Seasons = []domain.TMDbSeasonCatalog{tmdbCatalogSeason(
		1, 910001, tmdbCatalogEpisode(1, 920001, "Episode 1"),
	)}
	if err := workflow.SaveTMDbCatalog(ctx, operation, withoutSecond); err != nil {
		t.Fatalf("移除已映射集时同步失败: %v", err)
	}
	var stalePresent bool
	if err := fixture.pool.QueryRow(ctx, `SELECT present FROM media_episodes WHERE id = $1`, episode2ID).Scan(&stalePresent); err != nil {
		t.Fatal(err)
	}
	if stalePresent {
		t.Fatal("过期集没有标记为 present=false")
	}
	var mappingTarget uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `SELECT target_episode_id FROM episode_mappings WHERE id = $1`, mappingID).Scan(&mappingTarget); err != nil {
		t.Fatal(err)
	}
	var anchorTarget uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `SELECT anchor_target_episode_id FROM episode_mapping_profiles WHERE id = $1`, profileID).Scan(&anchorTarget); err != nil {
		t.Fatal(err)
	}
	if mappingTarget != episode2ID || anchorTarget != episode2ID {
		t.Fatalf("映射或锚点被改写: mapping=%s anchor=%s want=%s", mappingTarget, anchorTarget, episode2ID)
	}
	catalogRows, err := queries.ListSeriesMappingCatalog(ctx, repository.UUIDToPG(fixture.seriesID))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range catalogRows {
		if row.EpisodeID.Valid && repository.UUIDFromPG(row.EpisodeID) == episode2ID {
			t.Fatal("过期集仍出现在可映射目录中")
		}
	}

	// 上游恢复同一集：保留的行重新激活，映射仍指向原目标。
	if err := workflow.SaveTMDbCatalog(ctx, operation, initial); err != nil {
		t.Fatalf("恢复过期集时同步失败: %v", err)
	}
	var restoredPresent bool
	var restoredTMDbID *int64
	if err := fixture.pool.QueryRow(ctx, `SELECT present, tmdb_episode_id FROM media_episodes WHERE id = $1`, episode2ID).Scan(&restoredPresent, &restoredTMDbID); err != nil {
		t.Fatal(err)
	}
	if !restoredPresent || restoredTMDbID == nil || *restoredTMDbID != 920002 {
		t.Fatalf("恢复后的集状态错误: present=%t tmdb=%v", restoredPresent, restoredTMDbID)
	}

	// 整季移除仍被锚点引用：季与集都保留为历史行。
	emptyCatalog := initial
	emptyCatalog.Seasons = nil
	if err := workflow.SaveTMDbCatalog(ctx, operation, emptyCatalog); err != nil {
		t.Fatalf("移除已引用整季时同步失败: %v", err)
	}
	var seasonPresent, episodePresent bool
	if err := fixture.pool.QueryRow(ctx, `
SELECT season.present, episode.present
FROM tmdb_seasons AS season
JOIN media_episodes AS episode ON episode.season_id = season.id
WHERE episode.id = $1
`, episode2ID).Scan(&seasonPresent, &episodePresent); err != nil {
		t.Fatal(err)
	}
	if seasonPresent || episodePresent {
		t.Fatalf("过期季/集未标记: season=%t episode=%t", seasonPresent, episodePresent)
	}

	// 重新编号导致 TMDb 集标识移动到另一个集号：不能触发唯一键冲突。
	renumbered := initial
	renumbered.Seasons = []domain.TMDbSeasonCatalog{tmdbCatalogSeason(
		1, 910001, tmdbCatalogEpisode(1, 920002, "Episode 2 moved"),
	)}
	if err := workflow.SaveTMDbCatalog(ctx, operation, renumbered); err != nil {
		t.Fatalf("重新编号的目录同步失败: %v", err)
	}
	var firstTMDbID *int64
	if err := fixture.pool.QueryRow(ctx, `SELECT tmdb_episode_id FROM media_episodes WHERE id = $1`, episode1ID).Scan(&firstTMDbID); err != nil {
		t.Fatal(err)
	}
	if firstTMDbID == nil || *firstTMDbID != 920002 {
		t.Fatalf("重新编号没有落到第 1 集: %v", firstTMDbID)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT present FROM media_episodes WHERE id = $1`, episode2ID).Scan(&stalePresent); err != nil {
		t.Fatal(err)
	}
	if stalePresent {
		t.Fatal("重新编号后旧集号仍为 present")
	}
}
