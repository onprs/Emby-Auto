//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestSaveFileSelectionValidatesCoordinatesAndMaterializesFromSelectingFilesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	workflow := NewDownloadCommandWorkflow(transactor, scheduler)
	mediaWorkflow := NewMediaWorkflow(queries, transactor, scheduler)

	actorID, seriesID, profileID, acquisitionID, downloadID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	videoID, duplicateVideoID, subtitleID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "selection-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_series (id, title, media_type, release_year, tmdb_movie_id)
VALUES ($1, 'Selection Movie', 'movie', 2024, $2)`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, true, 'h264', 'libx264', 'mp4', 'mp4', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)`, profileID, "selection-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', 'magnet:?xt=urn:btih:cccccccccccccccccccccccccccccccccccccccc')`, acquisitionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, attempt, status, version) VALUES ($1, $2, 1, 'completed', 2)`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	for index, file := range []struct {
		id       uuid.UUID
		path     string
		kind     string
		selected bool
	}{
		{id: videoID, path: "Selection.Movie.mkv", kind: "video", selected: true},
		{id: duplicateVideoID, path: "Selection.Movie.duplicate.mkv", kind: "video", selected: false},
		{id: subtitleID, path: "Selection.Movie.zh-Hans.ass", kind: "subtitle", selected: true},
	} {
		if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode, source_episode_fraction_hundredths)
VALUES ($1, $2, $3, $4, 1024, $5, $6, 1, 1, 0)`, file.id, downloadID, index, file.path, file.kind, file.selected); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := workflow.SaveFileSelection(ctx, downloadID, 2, map[uuid.UUID]bool{duplicateVideoID: true}, "duplicate-selection", actorID)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "download_coordinate_duplicate" {
		t.Fatalf("SaveFileSelection() duplicate error = %#v, want download_coordinate_duplicate", err)
	}
	var status string
	var version int
	if err := pool.QueryRow(ctx, `SELECT status, version FROM downloads WHERE id = $1`, downloadID).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || version != 2 {
		t.Fatalf("download after rejected selection = %s/v%d, want completed/v2", status, version)
	}

	view, operation, err := workflow.SaveFileSelection(ctx, downloadID, 2, map[uuid.UUID]bool{videoID: true, subtitleID: true}, "confirm-selection", actorID)
	if err != nil {
		t.Fatalf("SaveFileSelection() confirm error = %v", err)
	}
	if view.Status != "selecting_files" || operation.Kind != appqueue.KindDownloadMaterialize {
		t.Fatalf("confirmed selection = status %s operation %s, want selecting_files/%s", view.Status, operation.Kind, appqueue.KindDownloadMaterialize)
	}
	if err := mediaWorkflow.MaterializeDownload(ctx, downloadID, operation.ID); err != nil {
		t.Fatalf("MaterializeDownload() error = %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM downloads WHERE id = $1`, downloadID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "materialized" {
		t.Fatalf("download status after materialize = %s, want materialized", status)
	}
	var taskCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_tasks WHERE acquisition_id = $1 AND media_type = 'movie'`, acquisitionID).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("materialized movie task count = %d, want 1", taskCount)
	}
}
