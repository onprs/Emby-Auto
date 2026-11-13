//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

const (
	occupancyTmdbEpisodeIndex  = "emby_library_items_tmdb_episode_idx"
	occupancyTmdbSeriesIndex   = "emby_library_items_tmdb_series_idx"
	occupancyCoordinateIndex   = "emby_library_items_episode_coordinate_idx"
	occupancyPlanExecutionCeil = 5 * time.Second
)

type occupancyPlanTarget struct {
	seriesID       uuid.UUID
	episodeID      uuid.UUID
	subscriptionID uuid.UUID
	season         int
	episode        int
}

type occupancyPlanFixture struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	actorID   uuid.UUID
	scanID    uuid.UUID
	libraryID uuid.UUID
	tmdbHit   occupancyPlanTarget
	coordHit  occupancyPlanTarget
	miss      occupancyPlanTarget
}

func newOccupancyPlanFixture(t *testing.T) *occupancyPlanFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	fixture := &occupancyPlanFixture{
		ctx: ctx, pool: pool,
		actorID: uuid.New(), scanID: uuid.New(), libraryID: uuid.New(),
	}
	operationID := uuid.New()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, []any{fixture.actorID, "occupancy-plan-" + fixture.actorID.String()}},
		{`INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, started_at) VALUES ($1, 'emby.scan', 'emby_scan', $2, $3, 'running', 3, 60, now())`, []any{operationID, fixture.scanID, "occupancy-plan-scan-" + operationID.String()}},
		{`INSERT INTO emby_scan_runs (id, operation_id, status, created_by) VALUES ($1, $2, 'running', $3)`, []any{fixture.scanID, operationID, fixture.actorID}},
		{`INSERT INTO emby_libraries (id, emby_id, name, last_scan_run_id, last_seen_at) VALUES ($1, $2, 'Plan', $3, now())`, []any{fixture.libraryID, "occupancy-plan-library-" + fixture.libraryID.String(), fixture.scanID}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("夹具 %q 失败: %v", statement.sql, err)
		}
	}
	fixture.tmdbHit = fixture.insertTarget(t, 900001, 910001, 3, 4)
	fixture.coordHit = fixture.insertTarget(t, 900002, 910002, 2, 5)
	fixture.miss = fixture.insertTarget(t, 900003, 910003, 5, 23)
	fixture.insertCatalogItems(t)
	return fixture
}

// insertTarget 建立剧集目标、映射、源坐标与 RSS 订阅；每个目标对应一个独立订阅。
func (fixture *occupancyPlanFixture) insertTarget(t *testing.T, tmdbEpisodeID, tmdbSeriesID int64, seasonNumber, episodeNumber int) occupancyPlanTarget {
	t.Helper()
	target := occupancyPlanTarget{
		seriesID: uuid.New(), episodeID: uuid.New(), subscriptionID: uuid.New(),
		season: seasonNumber, episode: episodeNumber,
	}
	seasonID, profileID, mappingID := uuid.New(), uuid.New(), uuid.New()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.pool.Exec(fixture.ctx, query, args...); err != nil {
			t.Fatalf("目标夹具失败: %v\nSQL: %s", err, query)
		}
	}
	exec(`INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, $3)`, target.seriesID, tmdbSeriesID, "Plan "+target.seriesID.String())
	exec(`INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, $3, 1)`, seasonID, target.seriesID, seasonNumber)
	exec(`INSERT INTO media_episodes (id, season_id, tmdb_episode_id, episode_number, title) VALUES ($1, $2, $3, $4, 'Plan Episode')`, target.episodeID, seasonID, tmdbEpisodeID, episodeNumber)
	exec(`INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, decision_source, created_by) VALUES ($1, $2, $3, 1, ARRAY[1]::integer[], 'user', $4)`, profileID, target.seriesID, "occupancy-plan-"+profileID.String(), fixture.actorID)
	exec(`INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, target_episode_id, mapping_status, match_source) VALUES ($1, $2, 1, 1, $3, 'mapped', 'explicit')`, mappingID, profileID, target.episodeID)
	exec(`INSERT INTO rss_subscriptions (id, series_id, mapping_profile_id, name, feed_url) VALUES ($1, $2, $3, $4, $5)`, target.subscriptionID, target.seriesID, profileID, "plan-feed-"+target.subscriptionID.String(), "https://example.test/"+target.subscriptionID.String()+".xml")
	return target
}

func (fixture *occupancyPlanFixture) insertCatalogItems(t *testing.T) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.pool.Exec(fixture.ctx, query, args...); err != nil {
			t.Fatalf("命中条目夹具失败: %v\nSQL: %s", err, query)
		}
	}
	exec(`
INSERT INTO emby_library_items (
    id, emby_id, library_id, parent_emby_id, item_type, name, file_path,
    provider_ids, season_number, episode_number, last_scan_run_id, last_seen_at
) VALUES (
    $1, $2, $3, NULL, 'Episode', 'Plan TMDb hit', '/library/plan/tmdb-hit.mkv',
    jsonb_build_object('Tmdb', '900001'), 9, 9, $4, now()
)`, uuid.New(), "plan-tmdb-hit", fixture.libraryID, fixture.scanID)
	exec(`
INSERT INTO emby_library_items (
    id, emby_id, library_id, parent_emby_id, item_type, name, file_path,
    provider_ids, season_number, episode_number, last_scan_run_id, last_seen_at
) VALUES (
    $1, 'plan-series-hit', $2, NULL, 'Series', 'Plan coordinate series', NULL,
    jsonb_build_object('Tmdb', '910002'), NULL, NULL, $3, now()
)`, uuid.New(), fixture.libraryID, fixture.scanID)
	exec(`
INSERT INTO emby_library_items (
    id, emby_id, library_id, parent_emby_id, item_type, name, file_path,
    provider_ids, season_number, episode_number, last_scan_run_id, last_seen_at
) VALUES (
    $1, 'plan-season-hit', $2, 'plan-series-hit', 'Season', 'Plan coordinate season', NULL,
    '{}'::jsonb, 2, NULL, $3, now()
)`, uuid.New(), fixture.libraryID, fixture.scanID)
	exec(`
INSERT INTO emby_library_items (
    id, emby_id, library_id, parent_emby_id, item_type, name, file_path,
    provider_ids, season_number, episode_number, last_scan_run_id, last_seen_at
) VALUES (
    $1, 'plan-coordinate-hit', $2, 'plan-season-hit', 'Episode', 'Plan coordinate episode',
    '/library/plan/coordinate-hit.mkv', jsonb_build_object('Tmdb', '999000001'), 2, 5, $3, now()
)`, uuid.New(), fixture.libraryID, fixture.scanID)
}

func (fixture *occupancyPlanFixture) insertBackground(t *testing.T, first, last int) {
	t.Helper()
	if first > last {
		return
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO emby_library_items (
    id, emby_id, library_id, item_type, name, file_path, provider_ids,
    season_number, episode_number, last_scan_run_id, last_seen_at
)
SELECT gen_random_uuid(), 'plan-bg-' || g, $1::uuid,
       CASE WHEN g % 4 = 0 THEN 'Episode' WHEN g % 4 = 1 THEN 'Movie' WHEN g % 4 = 2 THEN 'Series' ELSE 'Season' END,
       'Plan background ' || g,
       '/library/plan/' || g || CASE WHEN g % 4 = 0 THEN '.mkv' ELSE '.strm' END,
       jsonb_build_object('Tmdb', (200000 + g)::text),
       CASE WHEN g % 4 = 0 THEN (g % 5) + 1 END,
       CASE WHEN g % 4 = 0 THEN (g % 24) + 1 END,
       $2::uuid, now()
FROM generate_series($3::integer, $4::integer) AS g`, fixture.libraryID, fixture.scanID, first, last); err != nil {
		t.Fatalf("背景条目夹具失败: %v", err)
	}
}

type occupancyExplain struct {
	Plan          map[string]any  `json:"Plan"`
	JIT           json.RawMessage `json:"JIT"`
	ExecutionTime float64         `json:"Execution Time"`
}

func preparedSQL(t *testing.T, ctx context.Context, conn *pgx.Conn, marker string) string {
	t.Helper()
	var statement string
	if err := conn.QueryRow(ctx, `SELECT statement FROM pg_prepared_statements WHERE position($1 in statement) > 0 ORDER BY prepare_time DESC LIMIT 1`, marker).Scan(&statement); err != nil {
		t.Fatalf("未找到 pgx 预编译语句 %q: %v", marker, err)
	}
	return statement
}

func explainPrepared(t *testing.T, ctx context.Context, conn *pgx.Conn, name, statement, arguments string) occupancyExplain {
	t.Helper()
	if _, err := conn.Exec(ctx, "PREPARE "+pgx.Identifier{name}.Sanitize()+" AS "+statement); err != nil {
		t.Fatalf("PREPARE %s: %v", name, err)
	}
	var raw []byte
	query := "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE " + pgx.Identifier{name}.Sanitize() + "(" + arguments + ")"
	if err := conn.QueryRow(ctx, query).Scan(&raw); err != nil {
		t.Fatalf("EXPLAIN %s: %v", name, err)
	}
	var documents []occupancyExplain
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("解析 EXPLAIN JSON 失败: %v\n%s", err, raw)
	}
	return documents[0]
}

func planIndexNames(node map[string]any) map[string]bool {
	names := map[string]bool{}
	if name, ok := node["Index Name"].(string); ok {
		names[name] = true
	}
	if children, ok := node["Plans"].([]any); ok {
		for _, child := range children {
			if childNode, ok := child.(map[string]any); ok {
				for name := range planIndexNames(childNode) {
					names[name] = true
				}
			}
		}
	}
	return names
}

func planIndexLoops(node map[string]any) map[string]float64 {
	loops := map[string]float64{}
	if name, ok := node["Index Name"].(string); ok {
		if actual, ok := node["Actual Loops"].(float64); ok && actual > loops[name] {
			loops[name] = actual
		}
	}
	if children, ok := node["Plans"].([]any); ok {
		for _, child := range children {
			if childNode, ok := child.(map[string]any); ok {
				for name, count := range planIndexLoops(childNode) {
					if count > loops[name] {
						loops[name] = count
					}
				}
			}
		}
	}
	return loops
}

func jitAboveCost(t *testing.T, ctx context.Context, conn *pgx.Conn) float64 {
	t.Helper()
	var value string
	if err := conn.QueryRow(ctx, "SHOW jit_above_cost").Scan(&value); err != nil {
		t.Fatalf("读取 jit_above_cost: %v", err)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("解析 jit_above_cost=%q: %v", value, err)
	}
	return parsed
}

func requirePlanBudget(t *testing.T, label string, plan occupancyExplain, above float64, expectedIndexes ...string) {
	t.Helper()
	total, ok := plan.Plan["Total Cost"].(float64)
	if !ok {
		t.Fatalf("%s: 计划缺少 Total Cost", label)
	}
	if plan.JIT != nil {
		t.Errorf("%s: 默认阈值为 %g 时触发了 JIT（计划成本 %g）", label, above, total)
	}
	if total >= above {
		t.Errorf("%s: 计划成本 %g 达到 JIT 阈值 %g，存在重复 LLVM 编译风险", label, total, above)
	}
	if plan.ExecutionTime > occupancyPlanExecutionCeil.Seconds()*1000 {
		t.Errorf("%s: 执行耗时 %.0f ms 超过 %.0f ms 预算", label, plan.ExecutionTime, occupancyPlanExecutionCeil.Seconds()*1000)
	}
	indexes := planIndexNames(plan.Plan)
	loops := planIndexLoops(plan.Plan)
	t.Logf("%s: total_cost=%.2f jit_threshold=%.0f execution_ms=%.3f jit_used=%t indexes=[%s]",
		label, total, above, plan.ExecutionTime, plan.JIT != nil, sortedIndexNames(indexes))
	for _, expected := range expectedIndexes {
		if !indexes[expected] {
			t.Errorf("%s: 计划未使用索引 %s（使用: %s）", label, expected, sortedIndexNames(indexes))
			continue
		}
		if loops[expected] <= 0 {
			t.Errorf("%s: 索引 %s 出现在计划中但实际未执行", label, expected)
		}
	}
}

func sortedIndexNames(indexes map[string]bool) string {
	names := make([]string, 0, len(indexes))
	for name := range indexes {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func TestOccupancyQueriesKeepJITBelowDefaultThresholdIntegration(t *testing.T) {
	fixture := newOccupancyPlanFixture(t)
	conn, err := fixture.pool.Acquire(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	queries := db.New(conn)
	for _, setting := range []string{
		`SET statement_timeout = '10s'`,
		`SET lock_timeout = '5s'`,
	} {
		if _, err := conn.Exec(fixture.ctx, setting); err != nil {
			t.Fatalf("会话设置 %q: %v", setting, err)
		}
	}
	above := jitAboveCost(t, fixture.ctx, conn.Conn())

	inserted := 0
	catalogSQL, rssSQL := "", ""
	for _, scale := range []int{2000, 20000} {
		fixture.insertBackground(t, inserted+1, scale)
		inserted = scale
		if _, err := conn.Exec(fixture.ctx, "ANALYZE emby_library_items"); err != nil {
			t.Fatalf("ANALYZE: %v", err)
		}

		catalogTMDB, err := queries.GetEpisodeMappingTargetOccupancy(fixture.ctx, db.GetEpisodeMappingTargetOccupancyParams{
			ExcludedAcquisitionID: repository.UUIDToPG(uuid.Nil),
			TargetEpisodeID:       repository.UUIDToPG(fixture.tmdbHit.episodeID),
			SeriesID:              repository.UUIDToPG(fixture.tmdbHit.seriesID),
		})
		if err != nil {
			t.Fatalf("规模 %d GetEpisodeMappingTargetOccupancy(TMDb): %v", scale, err)
		}
		if !catalogTMDB.CatalogPresent {
			t.Fatalf("规模 %d GetEpisodeMappingTargetOccupancy(TMDb) 未命中", scale)
		}
		catalogCoordinate, err := queries.GetEpisodeMappingTargetOccupancy(fixture.ctx, db.GetEpisodeMappingTargetOccupancyParams{
			ExcludedAcquisitionID: repository.UUIDToPG(uuid.Nil),
			TargetEpisodeID:       repository.UUIDToPG(fixture.coordHit.episodeID),
			SeriesID:              repository.UUIDToPG(fixture.coordHit.seriesID),
		})
		if err != nil {
			t.Fatalf("规模 %d GetEpisodeMappingTargetOccupancy(坐标): %v", scale, err)
		}
		if !catalogCoordinate.CatalogPresent {
			t.Fatalf("规模 %d GetEpisodeMappingTargetOccupancy(坐标) 未命中", scale)
		}
		catalogMiss, err := queries.GetEpisodeMappingTargetOccupancy(fixture.ctx, db.GetEpisodeMappingTargetOccupancyParams{
			ExcludedAcquisitionID: repository.UUIDToPG(uuid.Nil),
			TargetEpisodeID:       repository.UUIDToPG(fixture.miss.episodeID),
			SeriesID:              repository.UUIDToPG(fixture.miss.seriesID),
		})
		if err != nil {
			t.Fatalf("规模 %d GetEpisodeMappingTargetOccupancy(未命中): %v", scale, err)
		}
		if catalogMiss.CatalogPresent || catalogMiss.ManagedImportPresent || catalogMiss.ProcessingPresent {
			t.Fatalf("规模 %d GetEpisodeMappingTargetOccupancy(未命中) = %#v", scale, catalogMiss)
		}
		rssTMDB, err := queries.GetRSSMappedTargetOccupancy(fixture.ctx, db.GetRSSMappedTargetOccupancyParams{
			SubscriptionID: repository.UUIDToPG(fixture.tmdbHit.subscriptionID),
			SourceSeason:   1, SourceEpisode: 1,
		})
		if err != nil {
			t.Fatalf("规模 %d GetRSSMappedTargetOccupancy(TMDb): %v", scale, err)
		}
		if !rssTMDB.CatalogPresent {
			t.Fatalf("规模 %d GetRSSMappedTargetOccupancy(TMDb) 未命中", scale)
		}
		rssCoordinate, err := queries.GetRSSMappedTargetOccupancy(fixture.ctx, db.GetRSSMappedTargetOccupancyParams{
			SubscriptionID: repository.UUIDToPG(fixture.coordHit.subscriptionID),
			SourceSeason:   1, SourceEpisode: 1,
		})
		if err != nil {
			t.Fatalf("规模 %d GetRSSMappedTargetOccupancy(坐标): %v", scale, err)
		}
		if !rssCoordinate.CatalogPresent {
			t.Fatalf("规模 %d GetRSSMappedTargetOccupancy(坐标) 未命中", scale)
		}
		rssMiss, err := queries.GetRSSMappedTargetOccupancy(fixture.ctx, db.GetRSSMappedTargetOccupancyParams{
			SubscriptionID: repository.UUIDToPG(fixture.miss.subscriptionID),
			SourceSeason:   1, SourceEpisode: 1,
		})
		if err != nil {
			t.Fatalf("规模 %d GetRSSMappedTargetOccupancy(未命中): %v", scale, err)
		}
		if rssMiss.CatalogPresent || rssMiss.ManagedImportPresent || rssMiss.ProcessingPresent {
			t.Fatalf("规模 %d GetRSSMappedTargetOccupancy(未命中) = %#v", scale, rssMiss)
		}

		// 只在首次真正通过 pgx 执行后取一次语句原文；后续 PREPARE 也会出现在
		// pg_prepared_statements，不能再用 prepare_time 反查。
		if catalogSQL == "" {
			catalogSQL = preparedSQL(t, fixture.ctx, conn.Conn(), "-- name: GetEpisodeMappingTargetOccupancy :one")
			rssSQL = preparedSQL(t, fixture.ctx, conn.Conn(), "-- name: GetRSSMappedTargetOccupancy :one")
		}
		catalogArguments := func(target occupancyPlanTarget) string {
			return fmt.Sprintf("'%s'::uuid, '%s'::uuid, '%s'::uuid", uuid.Nil, target.episodeID, target.seriesID)
		}
		rssArguments := func(target occupancyPlanTarget) string {
			return fmt.Sprintf("NULL::uuid, NULL::uuid, 1, 1, '%s'::uuid", target.subscriptionID)
		}

		for _, mode := range []string{"custom", "generic"} {
			if _, err := conn.Exec(fixture.ctx, "SET plan_cache_mode = 'force_"+mode+"_plan'"); err != nil {
				t.Fatalf("设置 plan_cache_mode: %v", err)
			}
			for _, scenario := range []struct {
				name            string
				sql             string
				arguments       string
				expectedIndexes []string
			}{
				{name: "catalog_tmdb_hit", sql: catalogSQL, arguments: catalogArguments(fixture.tmdbHit), expectedIndexes: []string{occupancyTmdbEpisodeIndex}},
				{name: "catalog_coordinate_hit", sql: catalogSQL, arguments: catalogArguments(fixture.coordHit), expectedIndexes: []string{occupancyCoordinateIndex, occupancyTmdbSeriesIndex}},
				{name: "catalog_miss", sql: catalogSQL, arguments: catalogArguments(fixture.miss)},
				{name: "rss_tmdb_hit", sql: rssSQL, arguments: rssArguments(fixture.tmdbHit), expectedIndexes: []string{occupancyTmdbEpisodeIndex}},
				{name: "rss_coordinate_hit", sql: rssSQL, arguments: rssArguments(fixture.coordHit), expectedIndexes: []string{occupancyCoordinateIndex, occupancyTmdbSeriesIndex}},
				{name: "rss_miss", sql: rssSQL, arguments: rssArguments(fixture.miss)},
			} {
				name := fmt.Sprintf("%s_scale_%d_%s", scenario.name, scale, mode)
				plan := explainPrepared(t, fixture.ctx, conn.Conn(), name, scenario.sql, scenario.arguments)
				expected := scenario.expectedIndexes
				if scale != 20000 {
					expected = nil
				}
				requirePlanBudget(t, name, plan, above, expected...)
			}
		}
	}
}
