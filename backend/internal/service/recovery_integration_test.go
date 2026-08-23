//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
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

func TestDownloadRetryNoMainVideoClearsStaleManifestAndReenqueuesIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	commands := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 准备一个 file_resolution/download_no_main_video 失败的下载，含 hash 与陈旧 extra 清单
	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":2,"sourceEpisode":5,"singleEpisode":true}`)
	downloadID := uuid.New()
	torrentHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	savePath := "/downloads/stale-manifest"
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status, progress, failure_stage, error_code, error_message, version, save_path, client_state, last_synced_at, file_resolution_source, agent_resolution_id)
VALUES ($1, $2, $3, 'failed', 0.42, 'file_resolution', 'download_no_main_video', 'the torrent contains no selectable main video', 1, $4, 'metadata_ready', now(), 'deterministic', NULL)
`, downloadID, acquisitionID, torrentHash, savePath); err != nil {
		t.Fatal(err)
	}
	// 陈旧清单：顶层复合标签被旧逻辑误判为 extra，实际应为视频/字幕
	top := "SyntheticPack S02 SP Limited Edition 1080p"
	staleFiles := []struct {
		id       uuid.UUID
		index    int
		relative string
	}{
		{id: uuid.New(), index: 0, relative: top + "/Synthetic - 01.mkv"},
		{id: uuid.New(), index: 1, relative: top + "/Synthetic - 02.mkv"},
		{id: uuid.New(), index: 2, relative: top + "/Synthetic - 01.ass"},
	}
	for _, file := range staleFiles {
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, $3, $4, 2048, 'extra', false, NULL, NULL)
`, file.id, downloadID, file.index, file.relative); err != nil {
			t.Fatal(err)
		}
	}

	// 版本冲突先行：错误版本不应泄漏删除
	if _, _, err := commands.Retry(ctx, downloadID, 99, "no-main-video-conflict-"+downloadID.String(), fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("Retry with stale version error = %v, want state conflict", err)
	}
	var conflictFiles int
	var conflictHash *string
	var conflictStatus string
	var conflictVersion int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE download_id = $1`, downloadID).Scan(&conflictFiles); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT torrent_hash, status, version FROM downloads WHERE id = $1`, downloadID).Scan(&conflictHash, &conflictStatus, &conflictVersion); err != nil {
		t.Fatal(err)
	}
	if conflictFiles != 3 || conflictHash == nil || *conflictHash != torrentHash || conflictStatus != "failed" || conflictVersion != 1 {
		t.Fatalf("version conflict leaked mutation: files=%d hash=%v status=%q version=%d", conflictFiles, conflictHash, conflictStatus, conflictVersion)
	}
	var conflictOps int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1`, "no-main-video-conflict-"+downloadID.String()).Scan(&conflictOps); err != nil {
		t.Fatal(err)
	}
	if conflictOps != 0 {
		t.Fatalf("version conflict created operation, want 0")
	}

	// 正确重试：应清空陈旧清单并回到 enqueue_pending
	idempotencyKey := "no-main-video-" + downloadID.String()
	view, operation, err := commands.Retry(ctx, downloadID, 1, idempotencyKey, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if view.Status != string(domain.DownloadEnqueuePending) || view.Version != 2 || view.TorrentHash != "" || len(view.Files) != 0 {
		t.Fatalf("retry view = status %q version %d hash %q files %d, want enqueue_pending/2/empty/0", view.Status, view.Version, view.TorrentHash, len(view.Files))
	}
	if view.FailureStage != "" || view.ErrorCode != "" || view.ErrorMessage != "" {
		t.Fatalf("retry did not clear failure fields: stage %q code %q msg %q", view.FailureStage, view.ErrorCode, view.ErrorMessage)
	}
	// DB 层校验：hash/savePath/client_state/last_synced/resolution/agent/failure/error 已清空
	var dbHash *string
	var dbSavePath *string
	var dbClientState *string
	var dbLastSynced *time.Time
	var dbResolution *string
	var dbAgentID *uuid.UUID
	var dbFailureStage *string
	var dbErrorCode *string
	var dbErrorMessage *string
	var dbStatus string
	var dbVersion int
	var dbProgress float64
	if err := fixture.pool.QueryRow(ctx, `
SELECT torrent_hash, save_path, client_state, last_synced_at, file_resolution_source, agent_resolution_id, failure_stage, error_code, error_message, status, version, progress
FROM downloads WHERE id = $1`, downloadID).Scan(&dbHash, &dbSavePath, &dbClientState, &dbLastSynced, &dbResolution, &dbAgentID, &dbFailureStage, &dbErrorCode, &dbErrorMessage, &dbStatus, &dbVersion, &dbProgress); err != nil {
		t.Fatal(err)
	}
	if dbHash != nil || dbSavePath != nil || dbClientState != nil || dbLastSynced != nil || dbResolution != nil || dbAgentID != nil || dbFailureStage != nil || dbErrorCode != nil || dbErrorMessage != nil {
		t.Fatalf("db not cleared: hash=%v save=%v client=%v synced=%v resolution=%v agent=%v failure=%v code=%v msg=%v", dbHash, dbSavePath, dbClientState, dbLastSynced, dbResolution, dbAgentID, dbFailureStage, dbErrorCode, dbErrorMessage)
	}
	if dbStatus != string(domain.DownloadEnqueuePending) || dbVersion != 2 || dbProgress != 0 {
		t.Fatalf("db status/version/progress = %q/%d/%v, want enqueue_pending/2/0", dbStatus, dbVersion, dbProgress)
	}
	var remainingFiles int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE download_id = $1`, downloadID).Scan(&remainingFiles); err != nil {
		t.Fatal(err)
	}
	if remainingFiles != 0 {
		t.Fatalf("remaining files = %d, want 0", remainingFiles)
	}
	// 操作校验：KindDownloadEnqueue、5 次、3m、payload 恢复 acquisition 坐标
	if operation.Kind != appqueue.KindDownloadEnqueue || operation.MaxAttempts != 5 || operation.Timeout != DownloadEnqueueTimeout {
		t.Fatalf("operation = kind %q attempts %d timeout %v, want %q/5/%v", operation.Kind, operation.MaxAttempts, operation.Timeout, appqueue.KindDownloadEnqueue, DownloadEnqueueTimeout)
	}
	var payload map[string]any
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		t.Fatalf("unmarshal operation payload: %v", err)
	}
	if payload["command"] != "retry" || int(payload["defaultSeason"].(float64)) != 2 || int(payload["defaultEpisode"].(float64)) != 5 || payload["singleEpisode"] != true {
		t.Fatalf("operation payload = %#v, want command retry season 2 episode 5 single true", payload)
	}
	// 事件存在
	var retryEventCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE resource_type = 'download' AND resource_id = $1 AND topic = 'download.retry_requested'`, downloadID).Scan(&retryEventCount); err != nil {
		t.Fatal(err)
	}
	if retryEventCount != 1 {
		t.Fatalf("retry event count = %d, want 1", retryEventCount)
	}
	var opEventCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE operation_id = $1 AND topic = 'operation.queued'`, operation.ID).Scan(&opEventCount); err != nil {
		t.Fatal(err)
	}
	if opEventCount != 1 {
		t.Fatalf("operation queued event count = %d, want 1", opEventCount)
	}

	// 同幂等键重放：返回同一 operation，不重复修改
	replayedView, replayedOp, err := commands.Retry(ctx, downloadID, 1, idempotencyKey, fixture.actorID)
	if err != nil {
		t.Fatalf("replayed Retry error = %v", err)
	}
	if replayedOp.ID != operation.ID || replayedView.Version != 2 || replayedView.Status != string(domain.DownloadEnqueuePending) || len(replayedView.Files) != 0 {
		t.Fatalf("replayed = op %s version %d status %q files %d, want %s/2/enqueue_pending/0", replayedOp.ID, replayedView.Version, replayedView.Status, len(replayedView.Files), operation.ID)
	}
	var opCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1`, idempotencyKey).Scan(&opCount); err != nil {
		t.Fatal(err)
	}
	if opCount != 1 {
		t.Fatalf("idempotent operation count = %d, want 1", opCount)
	}

	// 同一 fixture 随后用修复后分类结果重新 CompleteEnqueue，相同 file_index 可重建且无唯一冲突
	queries := db.New(fixture.pool)
	downloadWorkflow := NewDownloadWorkflow(queries, fixture.transactor, fixture.scheduler)
	// 修复后分类：顶层复合不再污染，应为 video/subtitle，且可正常选择
	filesInput := []domain.DownloadFile{
		{Index: 0, RelativePath: top + "/Synthetic - 01.mkv", SizeBytes: 2048},
		{Index: 1, RelativePath: top + "/Synthetic - 02.mkv", SizeBytes: 2048},
		{Index: 2, RelativePath: top + "/Synthetic - 01.ass", SizeBytes: 80},
	}
	classified, err := domain.ClassifyDownloadFiles(filesInput, domain.FileSelectionOptions{DefaultSeason: 2})
	if err != nil {
		t.Fatalf("reclassify error = %v", err)
	}
	for _, file := range classified {
		if file.Kind == domain.MediaExtra {
			t.Fatalf("reclassified %q should not be extra", file.RelativePath)
		}
	}
	selectResult, err := domain.SelectDownloadFiles(filesInput, domain.FileSelectionOptions{DefaultSeason: 2})
	if err != nil {
		t.Fatalf("reselect error = %v", err)
	}
	if len(selectResult.Episodes) != 2 {
		t.Fatalf("reselected episodes = %d, want 2", len(selectResult.Episodes))
	}
	newHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	newSavePath := "/downloads/recovered-" + downloadID.String()
	completion := domain.DownloadEnqueueCompletion{
		OperationID: operation.ID,
		DownloadID:  downloadID,
		TorrentHash: newHash,
		SavePath:    newSavePath,
		Files:       selectResult.Files,
		Outcome:     domain.DownloadManifestResolved,
	}
	if err := downloadWorkflow.CompleteEnqueue(ctx, completion); err != nil {
		t.Fatalf("CompleteEnqueue after retry error = %v", err)
	}
	var afterStatus string
	var afterHash *string
	var afterSavePath *string
	var afterFileCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT status, torrent_hash, save_path FROM downloads WHERE id = $1`, downloadID).Scan(&afterStatus, &afterHash, &afterSavePath); err != nil {
		t.Fatal(err)
	}
	if afterStatus != string(domain.DownloadFileResolutionPending) || afterHash == nil || *afterHash != newHash || afterSavePath == nil || *afterSavePath != newSavePath {
		t.Fatalf("after CompleteEnqueue status/hash/save = %q/%v/%v, want file_resolution_pending/%s/%s", afterStatus, afterHash, afterSavePath, newHash, newSavePath)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE download_id = $1`, downloadID).Scan(&afterFileCount); err != nil {
		t.Fatal(err)
	}
	if afterFileCount != 3 {
		t.Fatalf("after files = %d, want 3", afterFileCount)
	}
	// 校验重新持久化的行与选择状态一致且无索引冲突
	var persistedKinds []string
	rows, err := fixture.pool.Query(ctx, `SELECT file_index, media_kind, selected FROM download_files WHERE download_id = $1 ORDER BY file_index`, downloadID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var idx int
		var kind string
		var selected bool
		if err := rows.Scan(&idx, &kind, &selected); err != nil {
			t.Fatal(err)
		}
		persistedKinds = append(persistedKinds, fmt.Sprintf("%d:%s:%t", idx, kind, selected))
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	// 至少验证视频被选中且索引保持原样（0,1,2 可重用）
	if len(persistedKinds) != 3 {
		t.Fatalf("persisted kinds = %v, want 3", persistedKinds)
	}
	// 选择态应由 SelectDownloadFiles 决定：两个视频选中，字幕选中
	var selectedCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE download_id = $1 AND selected`, downloadID).Scan(&selectedCount); err != nil {
		t.Fatal(err)
	}
	if selectedCount != 3 {
		t.Fatalf("selected count after re-persist = %d, want 3", selectedCount)
	}
	// 新产生的 selection.apply 操作应已调度
	var selectionOps int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_id = $1 AND kind = $2`, downloadID, appqueue.KindDownloadSelectionApply).Scan(&selectionOps); err != nil {
		t.Fatal(err)
	}
	if selectionOps != 1 {
		t.Fatalf("selection apply ops = %d, want 1", selectionOps)
	}
}

func TestDownloadRetryOtherFileResolutionKeepsSelectionApplyIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	commands := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1,"sourceEpisode":1,"singleEpisode":true}`)
	downloadID := uuid.New()
	torrentHash := "cccccccccccccccccccccccccccccccccccccccc"
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status, failure_stage, error_code, error_message, version, save_path, client_state, last_synced_at, file_resolution_source)
VALUES ($1, $2, $3, 'failed', 'file_resolution', 'download_file_resolution_invalid', 'the manifest is invalid', 1, '/downloads/other-error', 'metadata_ready', now(), 'deterministic')
`, downloadID, acquisitionID, torrentHash); err != nil {
		t.Fatal(err)
	}
	fileID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Show.S01E01.mkv', 2048, 'video', true, 1, 1)
`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}

	view, operation, err := commands.Retry(ctx, downloadID, 1, "other-file-resolution-"+downloadID.String(), fixture.actorID)
	if err != nil {
		t.Fatalf("Retry other error = %v", err)
	}
	if view.Status != string(domain.DownloadFileResolutionPending) || view.Version != 2 || view.TorrentHash != torrentHash || len(view.Files) != 1 || view.Files[0].ID != fileID {
		t.Fatalf("other retry view = status %q version %d hash %q files %v, want file_resolution_pending/2/%s", view.Status, view.Version, view.TorrentHash, view.Files, torrentHash)
	}
	if operation.Kind != appqueue.KindDownloadSelectionApply || operation.MaxAttempts != 5 || operation.Timeout != time.Minute {
		t.Fatalf("other operation = kind %q attempts %d timeout %v, want %q/5/1m", operation.Kind, operation.MaxAttempts, operation.Timeout, appqueue.KindDownloadSelectionApply)
	}
	var payload map[string]any
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["command"] != "retry" || len(payload) != 1 {
		t.Fatalf("other payload = %#v, want only command retry", payload)
	}
	// hash 与 files 应保留
	var dbHash *string
	var dbSavePath *string
	if err := fixture.pool.QueryRow(ctx, `SELECT torrent_hash, save_path FROM downloads WHERE id = $1`, downloadID).Scan(&dbHash, &dbSavePath); err != nil {
		t.Fatal(err)
	}
	if dbHash == nil || *dbHash != torrentHash || dbSavePath == nil || *dbSavePath != "/downloads/other-error" {
		t.Fatalf("other db hash/save = %v/%v, want %s/other", dbHash, dbSavePath, torrentHash)
	}
	var fileCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE download_id = $1`, downloadID).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if fileCount != 1 {
		t.Fatalf("other file count = %d, want 1", fileCount)
	}
}

func TestDownloadRetryNoMainVideoExtraOnlyKeepsSelectionApplyIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	commands := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1,"sourceEpisode":1,"singleEpisode":true}`)
	downloadID := uuid.New()
	torrentHash := "dddddddddddddddddddddddddddddddddddddddd"
	savePath := "/downloads/extra-only"
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status, failure_stage, error_code, error_message, version, save_path, client_state, last_synced_at, file_resolution_source)
VALUES ($1, $2, $3, 'failed', 'file_resolution', 'download_no_main_video', 'the torrent contains no selectable main video', 1, $4, 'metadata_ready', now(), 'deterministic')
`, downloadID, acquisitionID, torrentHash, savePath); err != nil {
		t.Fatal(err)
	}
	// 真实 extra-only 清单：当前分类器仍为 extra/other，不应重新 enqueue
	extraFiles := []struct {
		id       uuid.UUID
		idx      int
		relative string
		kind     string
	}{
		{id: uuid.New(), idx: 0, relative: "Show S01 Trailer.mkv", kind: "extra"},
		{id: uuid.New(), idx: 1, relative: "Show S01 NCOP.mkv", kind: "extra"},
		{id: uuid.New(), idx: 2, relative: "notes.txt", kind: "other"},
	}
	for _, file := range extraFiles {
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, $3, $4, 2048, $5, false, NULL, NULL)
`, file.id, downloadID, file.idx, file.relative, file.kind); err != nil {
			t.Fatal(err)
		}
	}
	// 校验当前分类器确实无 video
	checkFiles := []domain.DownloadFile{
		{Index: 0, RelativePath: "Show S01 Trailer.mkv", SizeBytes: 2048},
		{Index: 1, RelativePath: "Show S01 NCOP.mkv", SizeBytes: 2048},
		{Index: 2, RelativePath: "notes.txt", SizeBytes: 100},
	}
	classified, err := domain.ClassifyDownloadFiles(checkFiles, domain.FileSelectionOptions{DefaultSeason: 1, DefaultEpisode: 1, SingleEpisode: true})
	if err != nil {
		t.Fatalf("classify check error = %v", err)
	}
	for _, c := range classified {
		if c.Kind == domain.MediaVideo {
			t.Fatalf("extra-only check classified %q as video, want extra/other", c.RelativePath)
		}
	}

	view, operation, err := commands.Retry(ctx, downloadID, 1, "extra-only-"+downloadID.String(), fixture.actorID)
	if err != nil {
		t.Fatalf("Retry extra-only error = %v", err)
	}
	if view.Status != string(domain.DownloadFileResolutionPending) || view.Version != 2 || view.TorrentHash != torrentHash || len(view.Files) != 3 {
		t.Fatalf("extra-only retry view = status %q version %d hash %q files %d, want file_resolution_pending/2/%s/3", view.Status, view.Version, view.TorrentHash, len(view.Files), torrentHash)
	}
	if operation.Kind != appqueue.KindDownloadSelectionApply || operation.MaxAttempts != 5 || operation.Timeout != time.Minute {
		t.Fatalf("extra-only operation = kind %q attempts %d timeout %v, want %q/5/1m", operation.Kind, operation.MaxAttempts, operation.Timeout, appqueue.KindDownloadSelectionApply)
	}
	var payload map[string]any
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["command"] != "retry" || len(payload) != 1 {
		t.Fatalf("extra-only payload = %#v, want only command retry", payload)
	}
	var dbHash *string
	var dbSavePath *string
	if err := fixture.pool.QueryRow(ctx, `SELECT torrent_hash, save_path FROM downloads WHERE id = $1`, downloadID).Scan(&dbHash, &dbSavePath); err != nil {
		t.Fatal(err)
	}
	if dbHash == nil || *dbHash != torrentHash || dbSavePath == nil || *dbSavePath != savePath {
		t.Fatalf("extra-only db hash/save = %v/%v, want %s/%s", dbHash, dbSavePath, torrentHash, savePath)
	}
	var remaining int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE download_id = $1`, downloadID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 3 {
		t.Fatalf("extra-only remaining files = %d, want 3", remaining)
	}
	var enqueuedOps int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_id = $1 AND kind = $2`, downloadID, appqueue.KindDownloadEnqueue).Scan(&enqueuedOps); err != nil {
		t.Fatal(err)
	}
	if enqueuedOps != 0 {
		t.Fatalf("extra-only should not create enqueue operation, got %d", enqueuedOps)
	}
}

func TestDownloadRetryNoMainVideoClassificationErrorDoesNotLeakDeleteIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	commands := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 使用 SingleEpisode=true 但缺失 sourceEpisode 的非法 payload，使 Classify 因 DefaultEpisode 非正而失败，DB 仍可正常插入文件
	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1,"singleEpisode":true}`)
	downloadID := uuid.New()
	torrentHash := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	savePath := "/downloads/classify-error"
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status, failure_stage, error_code, error_message, version, save_path, client_state, last_synced_at, file_resolution_source)
VALUES ($1, $2, $3, 'failed', 'file_resolution', 'download_no_main_video', 'the torrent contains no selectable main video', 1, $4, 'metadata_ready', now(), 'deterministic')
`, downloadID, acquisitionID, torrentHash, savePath); err != nil {
		t.Fatal(err)
	}
	fileID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Show.S01E01.mkv', 1024, 'video', false, NULL, NULL)
`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	key := "classify-error-" + downloadID.String()
	_, _, err := commands.Retry(ctx, downloadID, 1, key, fixture.actorID)
	if err == nil {
		t.Fatal("Retry with classify error succeeded, want error")
	}
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "download_file_manifest_invalid" {
		t.Fatalf("classify error = %#v, want download_file_manifest_invalid", err)
	}
	var dbStatus string
	var dbVersion int
	var dbHash *string
	var dbSavePath *string
	var dbFailureStage *string
	var dbErrorCode *string
	if err := fixture.pool.QueryRow(ctx, `SELECT status, version, torrent_hash, save_path, failure_stage, error_code FROM downloads WHERE id = $1`, downloadID).Scan(&dbStatus, &dbVersion, &dbHash, &dbSavePath, &dbFailureStage, &dbErrorCode); err != nil {
		t.Fatal(err)
	}
	if dbStatus != "failed" || dbVersion != 1 || dbHash == nil || *dbHash != torrentHash || dbSavePath == nil || *dbSavePath != savePath || dbFailureStage == nil || *dbFailureStage != "file_resolution" || dbErrorCode == nil || *dbErrorCode != "download_no_main_video" {
		t.Fatalf("classify error leaked mutation: status %q version %d hash %v save %v failure %v code %v", dbStatus, dbVersion, dbHash, dbSavePath, dbFailureStage, dbErrorCode)
	}
	var fileCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM download_files WHERE download_id = $1`, downloadID).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if fileCount != 1 {
		t.Fatalf("classify error file count = %d, want 1", fileCount)
	}
	var opCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1`, key).Scan(&opCount); err != nil {
		t.Fatal(err)
	}
	if opCount != 0 {
		t.Fatalf("classify error created operation, want 0")
	}
}

func TestCompleteEnqueueHardRejectCancelPerEnqueueIdempotencyIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := db.New(fixture.pool)
	workflow := NewDownloadWorkflow(queries, fixture.transactor, fixture.scheduler)

	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1,"sourceEpisode":1,"singleEpisode":true}`)
	downloadID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status, version) VALUES ($1, $2, 'enqueue_pending', 1)`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	hash := strings.ToLower("EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE")
	savePath := "/downloads/hard-reject-" + downloadID.String()
	files := []domain.ClassifiedDownloadFile{
		{DownloadFile: domain.DownloadFile{Index: 0, RelativePath: "Trailer.mkv", SizeBytes: 1024}, Kind: domain.MediaExtra},
		{DownloadFile: domain.DownloadFile{Index: 1, RelativePath: "notes.txt", SizeBytes: 100}, Kind: domain.MediaOther},
	}
	op1 := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds) VALUES ($1, $2, $3, $4, $5, 'running', 5, 180)`, op1, appqueue.KindDownloadEnqueue, "download", downloadID, "test-enqueue-"+op1.String()); err != nil {
		t.Fatal(err)
	}
	completion1 := domain.DownloadEnqueueCompletion{
		OperationID: op1, DownloadID: downloadID, TorrentHash: hash, SavePath: savePath, Files: files, Outcome: domain.DownloadManifestHardRejected, ReasonCode: "download_no_main_video",
	}
	if err := workflow.CompleteEnqueue(ctx, completion1); err != nil {
		t.Fatalf("first CompleteEnqueue error = %v", err)
	}
	var cancelKey1 string
	var cancelID1 uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `SELECT id, idempotency_key FROM operations WHERE resource_id = $1 AND kind = $2 ORDER BY created_at LIMIT 1`, downloadID, appqueue.KindDownloadCancel).Scan(&cancelID1, &cancelKey1); err != nil {
		t.Fatal(err)
	}
	expectedKey1 := "download.cancel:hard-rejected:" + downloadID.String() + ":" + hash + ":" + op1.String()
	if cancelKey1 != expectedKey1 {
		t.Fatalf("first cancel key = %q, want %q", cancelKey1, expectedKey1)
	}
	var payload1 []byte
	if err := fixture.pool.QueryRow(ctx, `SELECT payload FROM operations WHERE id = $1`, cancelID1).Scan(&payload1); err != nil {
		t.Fatal(err)
	}
	var decoded1 map[string]any
	if err := json.Unmarshal(payload1, &decoded1); err != nil || decoded1["deleteFiles"] != false {
		t.Fatalf("first cancel payload = %s, want deleteFiles false", payload1)
	}
	// 同 OperationID 重放：不应重复创建
	if err := workflow.CompleteEnqueue(ctx, completion1); err != nil {
		t.Fatalf("replayed same operation CompleteEnqueue error = %v", err)
	}
	var cancelCountAfterReplay int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = $1 AND resource_id = $2`, appqueue.KindDownloadCancel, downloadID).Scan(&cancelCountAfterReplay); err != nil {
		t.Fatal(err)
	}
	if cancelCountAfterReplay != 1 {
		t.Fatalf("replay created duplicate cancel, count = %d, want 1", cancelCountAfterReplay)
	}
	// 重置为 enqueue_pending 以模拟 Retry 后重新 enqueue 同 hash 不同 OperationID
	if _, err := fixture.pool.Exec(ctx, `UPDATE downloads SET status='enqueue_pending', torrent_hash=NULL, save_path=NULL, progress=0, client_state=NULL, last_synced_at=NULL, file_resolution_source=NULL, agent_resolution_id=NULL, failure_stage=NULL, error_code=NULL, error_message=NULL, version=version+1 WHERE id=$1`, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM download_files WHERE download_id=$1`, downloadID); err != nil {
		t.Fatal(err)
	}
	op2 := uuid.New()
	if op2 == op1 {
		t.Fatal("op2 equals op1")
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds) VALUES ($1, $2, $3, $4, $5, 'running', 5, 180)`, op2, appqueue.KindDownloadEnqueue, "download", downloadID, "test-enqueue-"+op2.String()); err != nil {
		t.Fatal(err)
	}
	completion2 := domain.DownloadEnqueueCompletion{
		OperationID: op2, DownloadID: downloadID, TorrentHash: hash, SavePath: savePath + "-2", Files: files, Outcome: domain.DownloadManifestHardRejected, ReasonCode: "download_no_main_video",
	}
	if err := workflow.CompleteEnqueue(ctx, completion2); err != nil {
		t.Fatalf("second CompleteEnqueue with different operation error = %v", err)
	}
	rows, err := fixture.pool.Query(ctx, `SELECT id, idempotency_key FROM operations WHERE kind=$1 AND resource_id=$2 ORDER BY created_at`, appqueue.KindDownloadCancel, downloadID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		keys = append(keys, key)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if len(keys) != 2 {
		t.Fatalf("cancel keys count = %d, want 2", len(keys))
	}
	expectedKey2 := "download.cancel:hard-rejected:" + downloadID.String() + ":" + hash + ":" + op2.String()
	if keys[0] != expectedKey1 || keys[1] != expectedKey2 {
		t.Fatalf("cancel keys = %v, want [%q %q]", keys, expectedKey1, expectedKey2)
	}
	if ids[0] == ids[1] {
		t.Fatalf("second cancel reused same operation id")
	}
	var payload2 []byte
	if err := fixture.pool.QueryRow(ctx, `SELECT payload FROM operations WHERE id = $1`, ids[1]).Scan(&payload2); err != nil {
		t.Fatal(err)
	}
	var decoded2 map[string]any
	if err := json.Unmarshal(payload2, &decoded2); err != nil || decoded2["deleteFiles"] != false {
		t.Fatalf("second cancel payload = %s, want deleteFiles false", payload2)
	}
}
