//go:build integration

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

const (
	taskViewTMDBEpisodeIndex  = "emby_library_items_tmdb_episode_idx"
	taskViewPlanExecutionCeil = 5 * time.Second
)

type taskViewPlanFixture struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	actorID   uuid.UUID
	scanID    uuid.UUID
	libraryID uuid.UUID
}

func newTaskViewPlanFixture(t *testing.T) *taskViewPlanFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	fixture := &taskViewPlanFixture{
		ctx: ctx, pool: pool,
		actorID: uuid.New(), scanID: uuid.New(), libraryID: uuid.New(),
	}
	operationID := uuid.New()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, []any{fixture.actorID, "task-view-plan-" + fixture.actorID.String()}},
		{`INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, started_at) VALUES ($1, 'emby.scan', 'emby_scan', $2, $3, 'running', 3, 60, now())`, []any{operationID, fixture.scanID, "task-view-plan-scan-" + operationID.String()}},
		{`INSERT INTO emby_scan_runs (id, operation_id, status, created_by) VALUES ($1, $2, 'running', $3)`, []any{fixture.scanID, operationID, fixture.actorID}},
		{`INSERT INTO emby_libraries (id, emby_id, name, last_scan_run_id, last_seen_at) VALUES ($1, $2, 'Task View Plan', $3, now())`, []any{fixture.libraryID, "task-view-plan-library-" + fixture.libraryID.String(), fixture.scanID}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("夹具 %q 失败: %v", statement.sql, err)
		}
	}
	return fixture
}

// insertTask 建立 TMDb 目标的剧集任务；返回任务 ID 与仅能由 TMDb 分支命中的条目 ID。
// 条目路径与导入目标刻意不同，季集坐标也与背景条目无关，避免路径或坐标掩盖 TMDb 匹配。
func (fixture *taskViewPlanFixture) insertTask(t *testing.T, tmdbEpisodeID int64) (uuid.UUID, uuid.UUID) {
	t.Helper()
	seriesID, seasonID, episodeID, profileID, mappingID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	acquisitionID, downloadID, fileID, transcodeID, taskID, itemID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.pool.Exec(fixture.ctx, query, args...); err != nil {
			t.Fatalf("任务视图夹具失败: %v\nSQL: %s", err, query)
		}
	}
	exec(`INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Task View Plan')`, seriesID, tmdbEpisodeID+500000)
	exec(`INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 1)`, seasonID, seriesID)
	exec(`INSERT INTO media_episodes (id, season_id, tmdb_episode_id, episode_number, title) VALUES ($1, $2, $3, 1, 'Task View Plan Episode')`, episodeID, seasonID, tmdbEpisodeID)
	exec(`INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, decision_source, created_by) VALUES ($1, $2, $3, 1, ARRAY[1]::integer[], 'user', $4)`, profileID, seriesID, "task-view-plan-"+profileID.String(), fixture.actorID)
	exec(`INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, target_episode_id, mapping_status, match_source) VALUES ($1, $2, 1, 1, $3, 'mapped', 'explicit')`, mappingID, profileID, episodeID)
	exec(`INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by) VALUES ($1, $2, 'manual', $3, $4)`, acquisitionID, seriesID, "https://example.test/task-view-plan-"+acquisitionID.String()+".torrent", fixture.actorID)
	exec(`INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'completed')`, downloadID, acquisitionID)
	exec(`INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected) VALUES ($1, $2, 0, 'Task.View.Plan.mkv', 1024, 'video', true)`, fileID, downloadID)
	exec(`INSERT INTO transcode_profiles (id, name, version, video_codec, encoder, container, file_extension, quality_mode, quality_value, audio_policy, preset, pixel_format) VALUES ($1, $2, 1, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 23, 'copy', 'medium', 'yuv420p')`, transcodeID, "task-view-plan-"+transcodeID.String())
	exec(`INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, mapping_id, state) VALUES ($1, $2, $3, $4, $5, 'imported')`, taskID, acquisitionID, fileID, transcodeID, mappingID)
	exec(`INSERT INTO imports (task_id, attempt, status, destination_video_path, destination_subtitle_path, completed_at) VALUES ($1, 1, 'succeeded', $2, $3, now())`, taskID, "/library/TaskView/destination.mkv", "/library/TaskView/destination.ass")
	exec(`
INSERT INTO emby_library_items (
    id, emby_id, library_id, item_type, name, file_path, provider_ids,
    season_number, episode_number, last_scan_run_id, last_seen_at
) VALUES (
    $1, $2, $3, 'Episode', 'Task View TMDb-only item', '/library/TaskView/other-item.mkv',
    jsonb_build_object('Tmdb', $4::text), 9, 9, $5, now()
)`, itemID, "task-view-plan-"+taskID.String(), fixture.libraryID, fmt.Sprint(tmdbEpisodeID), fixture.scanID)
	return taskID, itemID
}

// insertBackground 插入与目标条目 TMDb 值不重复的混合类型背景条目；其中约四分之一为
// present Episode，旧谓词若无法蕴含 TMDb 部分索引条件，会退化为扫描这些条目。
func (fixture *taskViewPlanFixture) insertBackground(t *testing.T, first, last int) {
	t.Helper()
	if first > last {
		return
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO emby_library_items (
    id, emby_id, library_id, item_type, name, file_path, provider_ids,
    season_number, episode_number, last_scan_run_id, last_seen_at
)
SELECT gen_random_uuid(), 'task-view-plan-bg-' || g, $1::uuid,
       CASE WHEN g % 4 = 0 THEN 'Episode' WHEN g % 4 = 1 THEN 'Movie' WHEN g % 4 = 2 THEN 'Series' ELSE 'Season' END,
       'Task view plan background ' || g,
       '/library/TaskView/' || g || CASE WHEN g % 4 = 0 THEN '.mkv' ELSE '.strm' END,
       jsonb_build_object('Tmdb', (200000 + g)::text),
       CASE WHEN g % 4 = 0 THEN (g % 5) + 1 END,
       CASE WHEN g % 4 = 0 THEN (g % 24) + 1 END,
       $2::uuid, now()
FROM generate_series($3::integer, $4::integer) AS g`, fixture.libraryID, fixture.scanID, first, last); err != nil {
		t.Fatalf("背景条目夹具失败: %v", err)
	}
}

func taskViewIndexActual(plan occupancyExplain, index string) (loops, rows float64) {
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if name, ok := node["Index Name"].(string); ok && name == index {
			if actual, ok := node["Actual Loops"].(float64); ok && actual > loops {
				loops = actual
				rows, _ = node["Actual Rows"].(float64)
			}
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				if childNode, ok := child.(map[string]any); ok {
					walk(childNode)
				}
			}
		}
	}
	walk(plan.Plan)
	return loops, rows
}

// TMDb-only 任务关联必须能从查询自身蕴含生成列的非空条件，从而实际执行
// emby_library_items_tmdb_episode_idx；路径与季集坐标都已刻意错开，不能代替该分支。
// 2k 规模允许规划器按成本不选该索引，20k 必须实际执行；custom/generic 均验证。
func TestTaskViewsUseTMDbEpisodeIndexForTMDbOnlyLinkIntegration(t *testing.T) {
	fixture := newTaskViewPlanFixture(t)
	taskID, itemID := fixture.insertTask(t, 777001)
	acquired, err := fixture.pool.Acquire(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer acquired.Release()
	conn := acquired.Conn()
	for _, setting := range []string{
		`SET statement_timeout = '10s'`,
		`SET lock_timeout = '5s'`,
	} {
		if _, err := conn.Exec(fixture.ctx, setting); err != nil {
			t.Fatalf("会话设置 %q: %v", setting, err)
		}
	}
	queries := db.New(conn)
	above := jitAboveCost(t, fixture.ctx, conn)

	inserted := 0
	getSQL, listSQL := "", ""
	for _, scale := range []int{2000, 20000} {
		fixture.insertBackground(t, inserted+1, scale)
		inserted = scale
		if _, err := conn.Exec(fixture.ctx, "ANALYZE emby_library_items"); err != nil {
			t.Fatalf("ANALYZE: %v", err)
		}

		row, err := queries.GetTaskView(fixture.ctx, repository.UUIDToPG(taskID))
		if err != nil {
			t.Fatalf("规模 %d GetTaskView: %v", scale, err)
		}
		if !row.EmbyItemID.Valid || repository.UUIDFromPG(row.EmbyItemID) != itemID {
			t.Fatalf("规模 %d GetTaskView TMDb-only 条目关联 = %v, want %s", scale, row.EmbyItemID, itemID)
		}
		pages, err := queries.ListTaskViews(fixture.ctx, db.ListTaskViewsParams{PageSize: 200})
		if err != nil {
			t.Fatalf("规模 %d ListTaskViews: %v", scale, err)
		}
		listed := false
		for index := range pages {
			if repository.UUIDFromPG(pages[index].ID) == taskID {
				listed = pages[index].EmbyItemID.Valid && repository.UUIDFromPG(pages[index].EmbyItemID) == itemID
				break
			}
		}
		if !listed {
			t.Fatalf("规模 %d ListTaskViews TMDb-only 条目关联缺失", scale)
		}

		if getSQL == "" {
			getSQL = preparedSQL(t, fixture.ctx, conn, "-- name: GetTaskView :one")
			listSQL = preparedSQL(t, fixture.ctx, conn, "-- name: ListTaskViews :many")
		}
		getArguments := fmt.Sprintf("'%s'::uuid", taskID)
		listArguments := "NULL::text, NULL::text, NULL::uuid, 200"
		for _, mode := range []string{"custom", "generic"} {
			if _, err := conn.Exec(fixture.ctx, "SET plan_cache_mode = 'force_"+mode+"_plan'"); err != nil {
				t.Fatalf("设置 plan_cache_mode: %v", err)
			}
			for _, scenario := range []struct {
				name      string
				sql       string
				arguments string
			}{
				{name: "get_task_view_tmdb_only", sql: getSQL, arguments: getArguments},
				{name: "list_task_views_tmdb_only", sql: listSQL, arguments: listArguments},
			} {
				label := fmt.Sprintf("%s_scale_%d_%s", scenario.name, scale, mode)
				plan := explainPrepared(t, fixture.ctx, conn, label, scenario.sql, scenario.arguments)
				expected := []string{}
				if scale == 20000 {
					expected = []string{taskViewTMDBEpisodeIndex}
				}
				requirePlanBudget(t, label, plan, above, expected...)
				loops, rows := taskViewIndexActual(plan, taskViewTMDBEpisodeIndex)
				t.Logf("%s: tmdb_index_loops=%.0f tmdb_index_rows=%.0f execution_ms=%.3f",
					label, loops, rows, plan.ExecutionTime)
				if scale == 20000 {
					if loops <= 0 {
						t.Fatalf("%s: %s 未在 20k TMDb-only 关联中实际执行", label, taskViewTMDBEpisodeIndex)
					}
					if rows != 1 {
						t.Fatalf("%s: %s 实际返回 %.0f 行，want 1", label, taskViewTMDBEpisodeIndex, rows)
					}
				}
				if plan.ExecutionTime > taskViewPlanExecutionCeil.Seconds()*1000 {
					t.Fatalf("%s: 执行耗时 %.0f ms 超过 %.0f ms 预算", label, plan.ExecutionTime, taskViewPlanExecutionCeil.Seconds()*1000)
				}
			}
		}
	}
}
