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
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func TestMovieSearchAcquisitionMaterializesWithoutMappingIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operations := NewOperationScheduler(transactor, riverClient)
	searchWorkflow := NewSearchWorkflow(queries, transactor, operations)
	mediaWorkflow := NewMediaWorkflow(queries, transactor, operations)
	actorID, searchID := uuid.New(), uuid.New()

	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "movie-workflow-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateSearchRun(ctx, db.CreateSearchRunParams{ID: repository.UUIDToPG(searchID), Query: "Fixture Movie", RequestedBy: repository.UUIDToPG(actorID)}); err != nil {
		t.Fatal(err)
	}
	if err := searchWorkflow.CompleteSearch(ctx, searchID, uuid.Nil, domain.SearchProviderResult{Candidates: []domain.ReleaseCandidate{{
		Provider: "fixture", IdentityKey: "fixture:movie", Title: "Fixture Movie 2024",
		DownloadURI: "magnet:?xt=urn:btih:1123456789abcdef0123456789abcdef01234567",
	}}}); err != nil {
		t.Fatal(err)
	}
	var candidateID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM release_candidates WHERE search_run_id = $1`, searchID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}

	created, err := searchWorkflow.CreateAcquisition(ctx, domain.CreateSearchAcquisition{
		CandidateID: candidateID, MediaType: domain.TaskMediaMovie, TMDbMovieID: 12345,
		MovieTitle: "Fixture Movie", ReleaseYear: 2024,
		IdempotencyKey: "fixture-movie-acquisition", ActorUserID: actorID,
	})
	if err != nil {
		t.Fatalf("CreateAcquisition(movie) error = %v", err)
	}
	var mediaType, title string
	var tmdbSeriesID *int64
	var mappingProfileID *uuid.UUID
	var tmdbMovieID int64
	var releaseYear int
	if err := pool.QueryRow(ctx, `
		SELECT media.media_type, media.title, media.tmdb_series_id, media.tmdb_movie_id, media.release_year,
		       acquisition.mapping_profile_id
		FROM acquisitions AS acquisition
		JOIN media_series AS media ON media.id = acquisition.series_id
		WHERE acquisition.id = $1`, created.AcquisitionID).Scan(
		&mediaType, &title, &tmdbSeriesID, &tmdbMovieID, &releaseYear, &mappingProfileID,
	); err != nil {
		t.Fatal(err)
	}
	if mediaType != "movie" || title != "Fixture Movie" || tmdbSeriesID != nil || tmdbMovieID != 12345 || releaseYear != 2024 || mappingProfileID != nil {
		t.Fatalf("movie acquisition metadata = %q/%q/%v/%d/%d/%v", mediaType, title, tmdbSeriesID, tmdbMovieID, releaseYear, mappingProfileID)
	}
	var tmdbSyncCount, queuedJobCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'tmdb.sync'`).Scan(&tmdbSyncCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job`).Scan(&queuedJobCount); err != nil {
		t.Fatal(err)
	}
	if tmdbSyncCount != 0 || queuedJobCount != 1 {
		t.Fatalf("movie acquisition TMDb sync/jobs = %d/%d, want 0/1", tmdbSyncCount, queuedJobCount)
	}

	profileID, sourceFileID, materializeOperationID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO transcode_profiles (
			id, name, version, is_default, video_codec, encoder, container, file_extension,
			quality_mode, quality_value, audio_policy, audio_codec, preset, pixel_format, max_concurrency
		) VALUES ($1, 'movie-webm', 1, true, 'av1', 'libaom-av1', 'webm', 'webm', 'crf', 31, 'transcode', 'opus', '4', 'yuv420p', 1)`, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE downloads
		SET status = 'completed', torrent_hash = '1123456789abcdef0123456789abcdef01234567', save_path = '/downloads/movie', progress = 1, completed_at = now()
		WHERE id = $1`, created.DownloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
		VALUES ($1, $2, 0, 'Fixture Movie/source.mkv', 2000000, 'video', true, 1, 1)`, sourceFileID, created.DownloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
		VALUES ($1, 'download.materialize', 'download', $2, $3, 'running', 3, 1200)`,
		materializeOperationID, created.DownloadID, "movie-materialize:"+created.DownloadID.String(),
	); err != nil {
		t.Fatal(err)
	}

	if err := mediaWorkflow.MaterializeDownload(ctx, created.DownloadID, materializeOperationID); err != nil {
		t.Fatalf("MaterializeDownload(movie) error = %v", err)
	}
	var taskID uuid.UUID
	var taskMediaType, downloadStatus string
	var mappingID *uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT task.id, task.media_type, task.mapping_id, download.status
		FROM episode_tasks AS task
		JOIN downloads AS download ON download.id = $1
		WHERE task.acquisition_id = $2`, created.DownloadID, created.AcquisitionID).Scan(&taskID, &taskMediaType, &mappingID, &downloadStatus); err != nil {
		t.Fatal(err)
	}
	if taskMediaType != "movie" || mappingID != nil || downloadStatus != "materialized" {
		t.Fatalf("materialized movie task = %q/%v/%q", taskMediaType, mappingID, downloadStatus)
	}
	var mediaOperationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_id = $1 AND kind IN ('subtitle.prepare', 'transcode.run')`, taskID).Scan(&mediaOperationCount); err != nil {
		t.Fatal(err)
	}
	if mediaOperationCount != 2 {
		t.Fatalf("movie media operation count = %d, want 2", mediaOperationCount)
	}

	transcodeCommand, err := mediaWorkflow.BeginTranscode(ctx, taskID)
	if err != nil {
		t.Fatalf("BeginTranscode(movie) error = %v", err)
	}
	subtitleCommand, err := mediaWorkflow.BeginSubtitle(ctx, taskID)
	if err != nil {
		t.Fatalf("BeginSubtitle(movie) error = %v", err)
	}
	for name, command := range map[string]domain.TaskMediaCommand{"transcode": transcodeCommand, "subtitle": subtitleCommand} {
		if command.MediaType != domain.TaskMediaMovie || command.Names.BaseName != "Fixture Movie(2024)" || command.Names.VideoName != "Fixture Movie(2024).webm" || command.Names.SubtitleName != "Fixture Movie(2024).ass" || command.OutputRelativeDirectory != "Fixture Movie(2024)" {
			t.Fatalf("%s movie command = %#v", name, command)
		}
	}
}
