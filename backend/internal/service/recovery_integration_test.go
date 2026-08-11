//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type integrationJobInserter struct {
	nextID atomic.Int64
}

func (inserter *integrationJobInserter) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	id := inserter.nextID.Add(1)
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: id}}, nil
}

type recoveryFixture struct {
	pool       *pgxpool.Pool
	actorID    uuid.UUID
	seriesID   uuid.UUID
	profileID  uuid.UUID
	transactor *database.Transactor
	scheduler  *OperationScheduler
}

func newRecoveryFixture(t *testing.T) recoveryFixture {
	t.Helper()
	_, pool := testutil.NewMigratedPostgres(t)
	fixture := recoveryFixture{
		pool: pool, actorID: uuid.New(), seriesID: uuid.New(), profileID: uuid.New(),
		transactor: database.NewTransactor(pool),
	}
	fixture.scheduler = NewOperationScheduler(fixture.transactor, &integrationJobInserter{})
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, fixture.actorID, "recovery-"+fixture.actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Recovery Series')`, fixture.seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)
`, fixture.profileID, "recovery-"+fixture.profileID.String()); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture recoveryFixture) createAcquisition(t *testing.T, sourcePayload string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := fixture.pool.Exec(context.Background(), `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, source_payload, created_by)
VALUES ($1, $2, 'manual', $3, $4::jsonb, $5)
`, id, fixture.seriesID, "magnet:?xt=urn:btih:"+fmt.Sprintf("%040x", time.Now().UnixNano()), sourcePayload, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDownloadRetryResumesTheRecordedFailureStageIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	workflow := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tests := []struct {
		stage      string
		wantKind   string
		wantStatus string
		withHash   bool
	}{
		{stage: "enqueue", wantKind: appqueue.KindDownloadEnqueue, wantStatus: "enqueue_pending"},
		{stage: "sync", wantKind: appqueue.KindDownloadSync, wantStatus: "downloading", withHash: true},
		{stage: "materialize", wantKind: appqueue.KindDownloadMaterialize, wantStatus: "completed", withHash: true},
	}
	for index, test := range tests {
		t.Run(test.stage, func(t *testing.T) {
			acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1,"sourceEpisode":2,"singleEpisode":true}`)
			downloadID := uuid.New()
			var torrentHash any
			if test.withHash {
				torrentHash = fmt.Sprintf("%040x", index+1)
			}
			if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status, progress, failure_stage, error_code, error_message)
VALUES ($1, $2, $3, 'failed', 0.42, $4, 'fixture_failure', 'fixture failure')
`, downloadID, acquisitionID, torrentHash, test.stage); err != nil {
				t.Fatal(err)
			}
			fileID := uuid.New()
			if test.withHash {
				if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Series.S01E02.mkv', 1024, 'video', true, 1, 2)
`, fileID, downloadID); err != nil {
					t.Fatal(err)
				}
			}
			key := "download-retry-" + downloadID.String()
			view, operation, err := workflow.Retry(ctx, downloadID, 1, key, fixture.actorID)
			if err != nil {
				t.Fatalf("Retry() error = %v", err)
			}
			if operation.Kind != test.wantKind || view.Status != test.wantStatus || view.Version != 2 || view.FailureStage != "" {
				t.Fatalf("retry result = operation %q, status %q, version %d, stage %q", operation.Kind, view.Status, view.Version, view.FailureStage)
			}
			if test.withHash && (view.TorrentHash == "" || len(view.Files) != 1 || view.Files[0].ID != fileID) {
				t.Fatalf("retry did not preserve confirmed torrent/files: %#v", view)
			}
			replayed, replayOperation, err := workflow.Retry(ctx, downloadID, 1, key, fixture.actorID)
			if err != nil {
				t.Fatalf("replayed Retry() error = %v", err)
			}
			if replayOperation.ID != operation.ID || replayed.Version != 2 {
				t.Fatalf("replayed retry = operation %s version %d, want %s/2", replayOperation.ID, replayed.Version, operation.ID)
			}
		})
	}
}

func TestDownloadCancelIsAtomicAndIdempotentIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	workflow := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1}`)
	downloadID := uuid.New()
	hash := fmt.Sprintf("%040x", 101)
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, torrent_hash, status) VALUES ($1, $2, $3, 'downloading')`, downloadID, acquisitionID, hash); err != nil {
		t.Fatal(err)
	}
	resourceType := "download"
	oldOperationIDs := []uuid.UUID{uuid.New(), uuid.New()}
	for index, id := range oldOperationIDs {
		status := "queued"
		attemptCount := 0
		if index == 1 {
			status, attemptCount = "running", 1
		}
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count, timeout_seconds, started_at)
VALUES ($1, 'download.sync', $2, $3, $4, $5, 3, $6, 60, CASE WHEN $5 = 'running' THEN now() END)
`, id, resourceType, downloadID, "old-"+id.String(), status, attemptCount); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := workflow.Cancel(ctx, downloadID, 99, "wrong-version", fixture.actorID); err == nil {
		t.Fatal("Cancel() with stale version succeeded")
	}
	var prematurelyRequested int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id = ANY($1) AND cancel_requested_at IS NOT NULL`, oldOperationIDs).Scan(&prematurelyRequested); err != nil || prematurelyRequested != 0 {
		t.Fatalf("stale cancel leaked cancellation requests: count=%d error=%v", prematurelyRequested, err)
	}
	view, operation, err := workflow.Cancel(ctx, downloadID, 1, "cancel-"+downloadID.String(), fixture.actorID)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if view.Status != "cancelled" || view.Version != 2 || operation.Kind != appqueue.KindDownloadCancel {
		t.Fatalf("cancel result = %#v / %#v", view, operation)
	}
	var requested int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id = ANY($1) AND cancel_requested_at IS NOT NULL`, oldOperationIDs).Scan(&requested); err != nil || requested != 2 {
		t.Fatalf("old operation cancellation count = %d, error=%v", requested, err)
	}
	replayed, replayOperation, err := workflow.Cancel(ctx, downloadID, 1, "cancel-"+downloadID.String(), fixture.actorID)
	if err != nil || replayOperation.ID != operation.ID || replayed.Version != 2 {
		t.Fatalf("replayed Cancel() = %#v / %#v / %v", replayed, replayOperation, err)
	}
}

func TestDownloadRemovalRejectsActiveTasksAndHidesCompletedRemovalIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1,"sourceEpisode":1,"singleEpisode":true}`)
	downloadID, fileID, taskID := uuid.New(), uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status, progress)
VALUES ($1, $2, $3, 'materialized', 1)
`, downloadID, acquisitionID, fmt.Sprintf("%040x", 202)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Series.S01E01.mkv', 1024, 'video', true, 1, 1)
`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, 'processing', 'transcoding', 'extracting_or_converting')
`, taskID, acquisitionID, fileID, fixture.profileID); err != nil {
		t.Fatal(err)
	}

	commands := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler)
	key := "remove-" + downloadID.String()
	if _, _, err := commands.Remove(ctx, downloadID, 1, key, fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("Remove() with active task error = %v, want state conflict", err)
	}
	var deletionRequested bool
	if err := fixture.pool.QueryRow(ctx, `SELECT deletion_requested_at IS NOT NULL FROM downloads WHERE id = $1`, downloadID).Scan(&deletionRequested); err != nil || deletionRequested {
		t.Fatalf("blocked removal changed download: requested=%t error=%v", deletionRequested, err)
	}

	if _, err := fixture.pool.Exec(ctx, `
UPDATE episode_tasks
SET state = 'failed', video_state = 'failed', subtitle_state = 'failed',
    failure_stage = 'video', error_code = 'fixture_failure', error_message = 'fixture failure'
WHERE id = $1
`, taskID); err != nil {
		t.Fatal(err)
	}
	view, operation, err := commands.Remove(ctx, downloadID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if view.Version != 2 || operation.Kind != appqueue.KindDownloadCancel {
		t.Fatalf("removal result = %#v / %#v", view, operation)
	}
	var payload []byte
	if err := fixture.pool.QueryRow(ctx, `SELECT payload FROM operations WHERE id = $1`, operation.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var removalPayload struct {
		Command         string `json:"command"`
		DeleteFiles     bool   `json:"deleteFiles"`
		PreserveTorrent bool   `json:"preserveTorrent"`
	}
	if err := json.Unmarshal(payload, &removalPayload); err != nil || removalPayload.Command != "remove" || !removalPayload.DeleteFiles || removalPayload.PreserveTorrent {
		t.Fatalf("removal payload = %s, error=%v", payload, err)
	}

	queries := db.New(fixture.pool)
	workflow := NewDownloadWorkflow(queries, fixture.transactor, fixture.scheduler)
	if err := workflow.CompleteRemoval(ctx, downloadID, operation.ID); err != nil {
		t.Fatalf("CompleteRemoval() error = %v", err)
	}
	if _, err := queries.GetDownloadByID(ctx, repository.UUIDToPG(downloadID)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("default download read error = %v, want no rows", err)
	}
	removed, err := queries.GetDownloadByIDIncludingDeleted(ctx, repository.UUIDToPG(downloadID))
	if err != nil || !removed.DeletedAt.Valid {
		t.Fatalf("deleted audit row = %#v, error=%v", removed, err)
	}
	if err := workflow.CompleteRemoval(ctx, downloadID, operation.ID); err != nil {
		t.Fatalf("replayed CompleteRemoval() error = %v", err)
	}
	replayed, replayOperation, err := commands.Remove(ctx, downloadID, 1, key, fixture.actorID)
	if err != nil || replayOperation.ID != operation.ID || replayed.Version != 2 {
		t.Fatalf("replayed Remove() = %#v / %#v / %v", replayed, replayOperation, err)
	}
}

func TestDownloadRemovalPreservesTorrentReusedByActiveDownloadIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	targetAcquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1,"sourceEpisode":1,"singleEpisode":true}`)
	activeAcquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1,"sourceEpisode":2,"singleEpisode":true}`)
	targetID, activeID := uuid.New(), uuid.New()
	hash := fmt.Sprintf("%040x", 303)
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status) VALUES
    ($1, $2, $5, 'cancelled'),
    ($3, $4, $5, 'downloading')
`, targetID, targetAcquisitionID, activeID, activeAcquisitionID, hash); err != nil {
		t.Fatal(err)
	}

	commands := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler)
	_, operation, err := commands.Remove(ctx, targetID, 1, "remove-shared-"+targetID.String(), fixture.actorID)
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	var payload []byte
	if err := fixture.pool.QueryRow(ctx, `SELECT payload FROM operations WHERE id = $1`, operation.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var removalPayload struct {
		DeleteFiles     bool `json:"deleteFiles"`
		PreserveTorrent bool `json:"preserveTorrent"`
	}
	if err := json.Unmarshal(payload, &removalPayload); err != nil || removalPayload.DeleteFiles || !removalPayload.PreserveTorrent {
		t.Fatalf("shared removal payload = %s, error=%v", payload, err)
	}

	queries := db.New(fixture.pool)
	workflow := NewDownloadWorkflow(queries, fixture.transactor, fixture.scheduler)
	if err := workflow.CompleteRemoval(ctx, targetID, operation.ID); err != nil {
		t.Fatalf("CompleteRemoval() error = %v", err)
	}
	active, err := queries.GetDownloadByID(ctx, repository.UUIDToPG(activeID))
	if err != nil || active.Status != "downloading" || active.TorrentHash == nil || *active.TorrentHash != hash {
		t.Fatalf("active reused torrent = %#v, error=%v", active, err)
	}
}

func TestTaskCancelStopsBothMediaBranchesAndRequestsOperationsIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1}`)
	downloadID, fileID, taskID := uuid.New(), uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'downloading')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Series.S01E01.mkv', 1024, 'video', true, 1, 1)
`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, 'processing', 'transcoding', 'extracting_or_converting')
`, taskID, acquisitionID, fileID, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	oldOperationIDs := []uuid.UUID{uuid.New(), uuid.New()}
	for index, id := range oldOperationIDs {
		kind := appqueue.KindTranscodeRun
		if index == 1 {
			kind = appqueue.KindSubtitlePrepare
		}
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, $2, 'episode_task', $3, $4, 'queued', 3, 60)
`, id, kind, taskID, "task-old-"+id.String()); err != nil {
			t.Fatal(err)
		}
	}
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	task, operation, err := workflow.Cancel(ctx, taskID, 1, "task-cancel-"+taskID.String(), fixture.actorID)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if task.State != "cancelled" || task.VideoState != "cancelled" || task.SubtitleState != "cancelled" || task.Version != 2 || operation.Kind != appqueue.KindTaskCancel {
		t.Fatalf("cancelled task = %#v, operation = %#v", task, operation)
	}
	var requested int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id = ANY($1) AND cancel_requested_at IS NOT NULL`, oldOperationIDs).Scan(&requested); err != nil || requested != 2 {
		t.Fatalf("task operation cancellation count = %d, error=%v", requested, err)
	}
	if _, err := queries.MarkTaskVideoReady(ctx, repository.UUIDToPG(taskID)); err == nil {
		t.Fatal("a cancelled task accepted a late video success")
	}
	replayed, replayOperation, err := workflow.Cancel(ctx, taskID, 1, "task-cancel-"+taskID.String(), fixture.actorID)
	if err != nil || replayOperation.ID != operation.ID || replayed.Version != 2 {
		t.Fatalf("replayed task Cancel() = %#v / %#v / %v", replayed, replayOperation, err)
	}
}
