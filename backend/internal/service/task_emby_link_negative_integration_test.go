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

type embyLinkMatrixFixture struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	actorID   uuid.UUID
	scanID    uuid.UUID
	libraryID uuid.UUID
}

func newEmbyLinkMatrixFixture(t *testing.T) *embyLinkMatrixFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	fixture := &embyLinkMatrixFixture{
		ctx: ctx, pool: pool,
		actorID: uuid.New(), scanID: uuid.New(), libraryID: uuid.New(),
	}
	operationID := uuid.New()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, []any{fixture.actorID, "link-matrix-" + fixture.actorID.String()}},
		{`INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, started_at) VALUES ($1, 'emby.scan', 'emby_scan', $2, $3, 'running', 3, 60, now())`, []any{operationID, fixture.scanID, "link-matrix-scan-" + operationID.String()}},
		{`INSERT INTO emby_scan_runs (id, operation_id, status, created_by) VALUES ($1, $2, 'running', $3)`, []any{fixture.scanID, operationID, fixture.actorID}},
		{`INSERT INTO emby_libraries (id, emby_id, name, last_scan_run_id, last_seen_at) VALUES ($1, $2, 'Matrix', $3, now())`, []any{fixture.libraryID, "link-matrix-library-" + fixture.libraryID.String(), fixture.scanID}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("夹具 %q 失败: %v", statement.sql, err)
		}
	}
	return fixture
}

// insertEpisodeTask 建立没有可复用条目的剧集任务，返回任务 ID。条目需由调用方单独插入。
func (fixture *embyLinkMatrixFixture) insertEpisodeTask(t *testing.T, tmdbEpisodeID int64, destination string) uuid.UUID {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.pool.Exec(fixture.ctx, query, args...); err != nil {
			t.Fatalf("剧集任务夹具失败: %v\nSQL: %s", err, query)
		}
	}
	seriesID, seasonID, episodeID, profileID, mappingID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	acquisitionID, downloadID, fileID, transcodeID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Link Matrix')`, seriesID, tmdbEpisodeID+500000)
	exec(`INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 1)`, seasonID, seriesID)
	exec(`INSERT INTO media_episodes (id, season_id, tmdb_episode_id, episode_number, title) VALUES ($1, $2, $3, 1, 'Episode 1')`, episodeID, seasonID, tmdbEpisodeID)
	exec(`INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, decision_source, created_by) VALUES ($1, $2, $3, 1, ARRAY[1]::integer[], 'user', $4)`, profileID, seriesID, "link-matrix-"+profileID.String(), fixture.actorID)
	exec(`INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, target_episode_id, mapping_status, match_source) VALUES ($1, $2, 1, 1, $3, 'mapped', 'explicit')`, mappingID, profileID, episodeID)
	exec(`INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by) VALUES ($1, $2, 'manual', $3, $4)`, acquisitionID, seriesID, "https://example.test/"+taskID.String()+".torrent", fixture.actorID)
	exec(`INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'completed')`, downloadID, acquisitionID)
	exec(`INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected) VALUES ($1, $2, 0, 'Link.Matrix.S01E01.mkv', 1024, 'video', true)`, fileID, downloadID)
	exec(`INSERT INTO transcode_profiles (id, name, version, video_codec, encoder, container, file_extension, quality_mode, quality_value, audio_policy, preset, pixel_format) VALUES ($1, $2, 1, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 23, 'copy', 'medium', 'yuv420p')`, transcodeID, "link-matrix-"+transcodeID.String())
	exec(`INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, mapping_id, state) VALUES ($1, $2, $3, $4, $5, 'imported')`, taskID, acquisitionID, fileID, transcodeID, mappingID)
	exec(`INSERT INTO imports (task_id, attempt, status, destination_video_path, destination_subtitle_path, completed_at) VALUES ($1, 1, 'succeeded', $2, $3, now())`, taskID, destination, destination+".ass")
	return taskID
}

type embyLinkMatrixItem struct {
	itemType      string
	filePath      string
	tmdbProvider  string
	present       bool
	seasonNumber  int
	episodeNumber int
	parentEmbyID  string
}

func (fixture *embyLinkMatrixFixture) insertItem(t *testing.T, item embyLinkMatrixItem) uuid.UUID {
	t.Helper()
	itemID := uuid.New()
	embyID := "link-matrix-item-" + itemID.String()
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO emby_library_items (
    id, emby_id, library_id, parent_emby_id, item_type, name, file_path,
    provider_ids, season_number, episode_number, present, last_scan_run_id, last_seen_at
) VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, NULLIF($7, ''), jsonb_build_object('Tmdb', $8::text), NULLIF($9, 0), NULLIF($10, 0), $11, $12, now())
`, itemID, embyID, fixture.libraryID, item.parentEmbyID, item.itemType, "Matrix "+item.itemType, item.filePath,
		item.tmdbProvider, item.seasonNumber, item.episodeNumber, item.present, fixture.scanID); err != nil {
		t.Fatalf("条目夹具失败: %v", err)
	}
	return itemID
}

func (fixture *embyLinkMatrixFixture) linkedItem(t *testing.T, taskID uuid.UUID) (uuid.UUID, bool) {
	t.Helper()
	queries := db.New(fixture.pool)
	row, err := queries.GetTaskView(fixture.ctx, repository.UUIDToPG(taskID))
	if err != nil {
		t.Fatalf("GetTaskView: %v", err)
	}
	listedValid := false
	listedID := uuid.Nil
	pages, err := queries.ListTaskViews(fixture.ctx, db.ListTaskViewsParams{PageSize: 200})
	if err != nil {
		t.Fatalf("ListTaskViews: %v", err)
	}
	for index := range pages {
		if repository.UUIDFromPG(pages[index].ID) != taskID {
			continue
		}
		listedValid = pages[index].EmbyItemID.Valid
		if listedValid {
			listedID = repository.UUIDFromPG(pages[index].EmbyItemID)
		}
		break
	}
	if !listedValid && row.EmbyItemID.Valid {
		t.Fatalf("GetTaskView 关联了条目 %s，但 ListTaskViews 未关联", repository.UUIDFromPG(row.EmbyItemID))
	}
	if listedValid && (!row.EmbyItemID.Valid || listedID != repository.UUIDFromPG(row.EmbyItemID)) {
		t.Fatalf("任务视图关联不一致: Get=%v List=%s", row.EmbyItemID, listedID)
	}
	if !row.EmbyItemID.Valid {
		return uuid.Nil, false
	}
	return repository.UUIDFromPG(row.EmbyItemID), true
}

// 剧集任务的 TMDb 分支只接受 Episode 条目；Movie/Series 数值碰撞、present=false、
// TMDb 不命中都不得关联。文件路径分支仍需独立成立，因此正例条目的路径与 TMDb 均独立。
func TestEpisodeTaskEmbyLinkTypePresenceMatrixIntegration(t *testing.T) {
	fixture := newEmbyLinkMatrixFixture(t)
	baseTMDB := time.Now().UnixNano()%700000 + 200000

	cases := []struct {
		name     string
		item     embyLinkMatrixItem
		wantLink bool
	}{
		{
			name: "episode_file_path_match_without_tmdb_match",
			item: embyLinkMatrixItem{
				itemType: "Episode", filePath: "MATCH_DESTINATION", tmdbProvider: fmt.Sprint(baseTMDB + 900),
				present: true, seasonNumber: 9, episodeNumber: 9,
			},
			wantLink: true,
		},
		{
			name: "episode_tmdb_match_without_file_path",
			item: embyLinkMatrixItem{
				itemType: "Episode", filePath: "/library/Matrix/Other/Episode.mkv", tmdbProvider: "MATCH_TMDB",
				present: true, seasonNumber: 9, episodeNumber: 9,
			},
			wantLink: true,
		},
		{
			name: "movie_tmdb_collision",
			item: embyLinkMatrixItem{
				itemType: "Movie", filePath: "/library/Matrix/Movies/Collision.mkv", tmdbProvider: "MATCH_TMDB",
				present: true,
			},
			wantLink: false,
		},
		{
			name: "series_tmdb_collision",
			item: embyLinkMatrixItem{
				itemType: "Series", filePath: "/library/Matrix/Series/Collision.strm", tmdbProvider: "MATCH_TMDB",
				present: true,
			},
			wantLink: false,
		},
		{
			name: "episode_tmdb_match_but_absent",
			item: embyLinkMatrixItem{
				itemType: "Episode", filePath: "/library/Matrix/Other/Absent.mkv", tmdbProvider: "MATCH_TMDB",
				present: false, seasonNumber: 9, episodeNumber: 9,
			},
			wantLink: false,
		},
		{
			name: "episode_tmdb_miss",
			item: embyLinkMatrixItem{
				itemType: "Episode", filePath: "/library/Matrix/Other/Miss.mkv", tmdbProvider: fmt.Sprint(baseTMDB + 901),
				present: true, seasonNumber: 9, episodeNumber: 9,
			},
			wantLink: false,
		},
	}

	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tmdbEpisodeID := baseTMDB + int64(index)
			destination := fmt.Sprintf("/library/Matrix/Shows/Show - S01E%02d.mkv", index+1)
			taskID := fixture.insertEpisodeTask(t, tmdbEpisodeID, destination)
			item := testCase.item
			if item.filePath == "MATCH_DESTINATION" {
				item.filePath = destination
			}
			if item.tmdbProvider == "MATCH_TMDB" {
				item.tmdbProvider = fmt.Sprint(tmdbEpisodeID)
			}
			itemID := fixture.insertItem(t, item)
			linkedID, linked := fixture.linkedItem(t, taskID)
			if testCase.wantLink {
				if !linked || linkedID != itemID {
					t.Fatalf("任务应关联条目 %s，实际 linked=%t item=%s", itemID, linked, linkedID)
				}
				return
			}
			if linked {
				t.Fatalf("任务不得关联条目 %s，实际 item=%s", itemID, linkedID)
			}
		})
	}
}
