//go:build integration

package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

// 电影任务（media_type='movie'）没有 TMDb 剧集目标；已入库结果通过
// destination_video_path 匹配 item_type='Movie' 的媒体库条目。
// 相关条目的类型约束只允许加在 TMDb 剧集分支上，不能放在整个侧连接，
// 否则任务视图会丢失 embyItemId/embyLibraryId。
func TestMovieTaskKeepsRelatedEmbyLibraryItemIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	actorID, operationID, scanID, libraryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seriesID, acquisitionID, downloadID, fileID, profileID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	itemID := uuid.New()
	destination := "/library/Movies/Movie.2026.mkv"
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "movie-link-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_series (id, title, media_type, tmdb_movie_id, release_year)
VALUES ($1, 'Movie Link', 'movie', $2, 2026)
`, seriesID, time.Now().UnixNano()%1000000+1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri)
VALUES ($1, $2, 'manual', $3)
`, acquisitionID, seriesID, "https://example.test/movie-link.torrent"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'completed')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($1, $2, 0, 'Movie.2026.mkv', 1024, 'video', true)
`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (id, name, version, video_codec, encoder, container, file_extension, quality_mode, quality_value, audio_policy, preset, pixel_format)
VALUES ($1, $2, 1, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 23, 'copy', 'medium', 'yuv420p')
`, profileID, "movie-link-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, media_type, state)
VALUES ($1, $2, $3, $4, 'movie', 'imported')
`, taskID, acquisitionID, fileID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO imports (task_id, attempt, status, destination_video_path, destination_subtitle_path, completed_at)
VALUES ($1, 1, 'succeeded', $2, $3, now())
`, taskID, destination, "/library/Movies/Movie.2026.ass"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, started_at)
VALUES ($1, 'emby.scan', 'emby_scan', $2, $3, 'running', 3, 60, now())
`, operationID, scanID, "movie-scan-"+operationID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO emby_scan_runs (id, operation_id, status, created_by) VALUES ($1, $2, 'running', $3)`, scanID, operationID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO emby_libraries (id, emby_id, name, last_scan_run_id, last_seen_at)
VALUES ($1, $2, 'Movies', $3, now())
`, libraryID, "movie-library-"+libraryID.String(), scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO emby_library_items (id, emby_id, library_id, item_type, name, file_path, provider_ids, last_scan_run_id, last_seen_at)
VALUES ($1, $2, $3, 'Movie', 'Movie Link', $4, '{}'::jsonb, $5, now())
`, itemID, "movie-item-"+itemID.String(), libraryID, destination, scanID); err != nil {
		t.Fatal(err)
	}

	row, err := db.New(pool).GetTaskView(ctx, repository.UUIDToPG(taskID))
	if err != nil {
		t.Fatal(err)
	}
	if !row.EmbyItemID.Valid || row.EmbyItemID.Bytes != itemID {
		t.Fatalf("GetTaskView movie related Emby item = %v, want %s", row.EmbyItemID, itemID)
	}
	if !row.EmbyLibraryID.Valid || row.EmbyLibraryID.Bytes != libraryID {
		t.Fatalf("GetTaskView movie related Emby library = %v, want %s", row.EmbyLibraryID, libraryID)
	}
	pages, err := db.New(pool).ListTaskViews(ctx, db.ListTaskViewsParams{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	var listed *db.ListTaskViewsRow
	for index := range pages {
		if repository.UUIDFromPG(pages[index].ID) == taskID {
			listed = &pages[index]
			break
		}
	}
	if listed == nil {
		t.Fatal("ListTaskViews did not return the movie task")
	}
	if !listed.EmbyItemID.Valid || listed.EmbyItemID.Bytes != itemID {
		t.Fatalf("ListTaskViews movie related Emby item = %v, want %s", listed.EmbyItemID, itemID)
	}
	if !listed.EmbyLibraryID.Valid || listed.EmbyLibraryID.Bytes != libraryID {
		t.Fatalf("ListTaskViews movie related Emby library = %v, want %s", listed.EmbyLibraryID, libraryID)
	}
}

// TMDb 剧集任务的关联仍通过生成列匹配 Episode 条目，不受类型约束收窄影响。
func TestEpisodeTaskStillMatchesTMDbProviderIDIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	actorID, operationID, scanID, libraryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seriesID, acquisitionID, downloadID, fileID, profileID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seasonID, episodeID, mappingID := uuid.New(), uuid.New(), uuid.New()
	itemID := uuid.New()
	destination := "/library/Shows/Show - S01E01.mkv"
	// 条目路径与导入目标刻意不同，确保关联只能由 TMDb 分支命中。
	libraryPath := "/library/Shows/Show/Season 1/Show - S01E01.mkv"
	tmdbEpisodeID := time.Now().UnixNano()%900000 + 100000
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "tmdb-link-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'TMDB Link')`, seriesID, time.Now().UnixNano()%1000000+1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 1)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_episodes (id, season_id, tmdb_episode_id, episode_number, title)
VALUES ($1, $2, $3, 1, 'Episode 1')
`, episodeID, seasonID, tmdbEpisodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, decision_source, created_by)
VALUES ($1, $2, 'tmdb-link', 1, ARRAY[1]::integer[], 'user', $3)
`, profileID, seriesID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, target_episode_id, mapping_status, match_source)
VALUES ($1, $2, 1, 1, $3, 'mapped', 'explicit')
`, mappingID, profileID, episodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by)
VALUES ($1, $2, 'manual', $3, $4)
`, acquisitionID, seriesID, "https://example.test/tmdb-link.torrent", actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'completed')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($1, $2, 0, 'Show.S01E01.mkv', 1024, 'video', true)
`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (id, name, version, video_codec, encoder, container, file_extension, quality_mode, quality_value, audio_policy, preset, pixel_format)
VALUES ($1, $2, 1, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 23, 'copy', 'medium', 'yuv420p')
`, profileID, "tmdb-link-task-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, mapping_id, state)
VALUES ($1, $2, $3, $4, $5, 'imported')
`, taskID, acquisitionID, fileID, profileID, mappingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO imports (task_id, attempt, status, destination_video_path, destination_subtitle_path, completed_at)
VALUES ($1, 1, 'succeeded', $2, $3, now())
`, taskID, destination, "/library/Shows/Show - S01E01.ass"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, started_at)
VALUES ($1, 'emby.scan', 'emby_scan', $2, $3, 'running', 3, 60, now())
`, operationID, scanID, "tmdb-scan-"+operationID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO emby_scan_runs (id, operation_id, status, created_by) VALUES ($1, $2, 'running', $3)`, scanID, operationID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO emby_libraries (id, emby_id, name, last_scan_run_id, last_seen_at)
VALUES ($1, $2, 'Shows', $3, now())
`, libraryID, "tmdb-library-"+libraryID.String(), scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO emby_library_items (id, emby_id, library_id, item_type, name, file_path, provider_ids, last_scan_run_id, last_seen_at)
VALUES ($1, $2, $3, 'Episode', 'Episode 1', $4, jsonb_build_object('Tmdb', $5::text), $6, now())
`, itemID, "tmdb-item-"+itemID.String(), libraryID, libraryPath, strconv.FormatInt(tmdbEpisodeID, 10), scanID); err != nil {
		t.Fatal(err)
	}

	row, err := db.New(pool).GetTaskView(ctx, repository.UUIDToPG(taskID))
	if err != nil {
		t.Fatal(err)
	}
	if !row.EmbyItemID.Valid || row.EmbyItemID.Bytes != itemID {
		t.Fatalf("episode task related Emby item = %v, want %s", row.EmbyItemID, itemID)
	}
	if !row.EmbyLibraryID.Valid || row.EmbyLibraryID.Bytes != libraryID {
		t.Fatalf("episode task related Emby library = %v, want %s", row.EmbyLibraryID, libraryID)
	}
}
