//go:build integration

package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

type deletionCallRecorder struct {
	mu    sync.Mutex
	calls [][]string
}

func (recorder *deletionCallRecorder) remove(_ context.Context, files []domain.AcquisitionDeletionLibraryFile) error {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.FilePath)
	}
	sort.Strings(paths)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.calls = append(recorder.calls, paths)
	return nil
}

func (recorder *deletionCallRecorder) snapshot() [][]string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	copied := make([][]string, len(recorder.calls))
	copy(copied, recorder.calls)
	return copied
}

func (recorder *deletionCallRecorder) reset() {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.calls = nil
}

// createDeletionImportFixture 创建一条 acquisition→download→task→import 链路。
func createDeletionImportFixture(
	t *testing.T, fixture recoveryFixture, status, videoPath, subtitlePath string,
) (acquisitionID, taskID, importID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	taskState := map[string]string{
		"succeeded": "imported", "running": "importing", "failed": "failed", "cancelled": "cancelled",
	}[status]
	if taskState == "" {
		t.Fatalf("unsupported import status %q", status)
	}
	acquisitionID, taskID, importID = uuid.New(), uuid.New(), uuid.New()
	downloadID, fileID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by)
VALUES ($1, $2, 'manual', $3, $4)`,
		acquisitionID, fixture.seriesID, "manual://deletion-lock-"+acquisitionID.String(), fixture.actorID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, progress)
VALUES ($1, $2, 'materialized', 1)`, downloadID, acquisitionID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($1, $2, 0, 'fixture.mkv', 1024, 'video', true)`, fileID, downloadID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, $5, 'video_ready', 'ass_ready')`,
		taskID, acquisitionID, fileID, fixture.profileID, taskState,
	); err != nil {
		t.Fatal(err)
	}
	var video, subtitle any
	if status == "succeeded" || status == "running" {
		video, subtitle = videoPath, subtitlePath
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO imports (id, task_id, attempt, status, destination_video_path, destination_subtitle_path)
VALUES ($1, $2, 1, $3, $4, $5)`, importID, taskID, status, video, subtitle,
	); err != nil {
		t.Fatal(err)
	}
	return acquisitionID, taskID, importID
}

func createSameAcquisitionRunningImport(
	t *testing.T, fixture recoveryFixture, acquisitionID uuid.UUID, videoPath, subtitlePath string,
) (taskID, importID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	downloadID, fileID, taskID, importID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, attempt, status, progress)
VALUES ($1, $2, 2, 'materialized', 1)`, downloadID, acquisitionID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($1, $2, 1, 'fixture-2.mkv', 1024, 'video', true)`, fileID, downloadID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, 'importing', 'video_ready', 'ass_ready')`,
		taskID, acquisitionID, fileID, fixture.profileID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO imports (id, task_id, attempt, status, destination_video_path, destination_subtitle_path)
VALUES ($1, $2, 1, 'running', $3, $4)`, importID, taskID, videoPath, subtitlePath,
	); err != nil {
		t.Fatal(err)
	}
	return taskID, importID
}

func insertDeletionOperation(t *testing.T, fixture recoveryFixture, operationID, acquisitionID, taskID uuid.UUID, kind, resourceType string) {
	t.Helper()
	ctx := context.Background()
	var resourceID uuid.UUID
	switch resourceType {
	case "acquisition":
		resourceID = acquisitionID
	default:
		resourceID = taskID
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, $2, $3, $4, $5, 'running', 1, 30)`,
		operationID, kind, resourceType, resourceID, "deletion-lock-"+operationID.String(),
	); err != nil {
		t.Fatal(err)
	}
}

func TestAcquisitionDeletionLibraryFreshClaimsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewAcquisitionDeletionWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	videoPath := "/library/Deletion Show/Season1/Deletion Show - S01E01.mkv"
	subtitlePath := "/library/Deletion Show/Season1/Deletion Show - S01E01.ass"
	acquisitionID, _, _ := createDeletionImportFixture(t, fixture, "succeeded", videoPath, subtitlePath)
	candidates := []domain.AcquisitionDeletionLibraryFile{
		{FilePath: videoPath},
		{FilePath: subtitlePath},
		{FilePath: ""},
		{FilePath: "/library/Deletion Show/Season1/stale.mkv"},
	}
	recorder := &deletionCallRecorder{}
	remove := func(callCtx context.Context, files []domain.AcquisitionDeletionLibraryFile) error {
		return recorder.remove(callCtx, files)
	}
	assertFiles := func(label string, expecting bool) {
		t.Helper()
		recorder.reset()
		prepared, err := workflow.DeleteImportedLibraryFiles(ctx, acquisitionID, uuid.New(), candidates, remove)
		if err != nil || !prepared {
			t.Fatalf("%s: prepared=%t err=%v", label, prepared, err)
		}
		calls := recorder.snapshot()
		if expecting {
			if len(calls) != 1 || len(calls[0]) != 2 || calls[0][0] != subtitlePath || calls[0][1] != videoPath {
				t.Fatalf("%s: removable files = %#v", label, calls)
			}
			return
		}
		if len(calls) != 0 {
			t.Fatalf("%s: files unexpectedly removable = %#v", label, calls)
		}
	}

	assertFiles("无其它引用", true)

	otherAcquisitionID, _, otherImportID := createDeletionImportFixture(t, fixture, "running", videoPath, subtitlePath)
	assertFiles("其它 running 采用登记", false)
	if _, err := fixture.pool.Exec(ctx, `UPDATE imports SET status='failed' WHERE id=$1`, otherImportID); err != nil {
		t.Fatal(err)
	}
	assertFiles("其它 failed 终态", true)
	if _, err := fixture.pool.Exec(ctx, `UPDATE imports SET status='succeeded' WHERE id=$1`, otherImportID); err != nil {
		t.Fatal(err)
	}
	assertFiles("其它 succeeded 引用", false)
	if _, err := fixture.pool.Exec(ctx, `UPDATE acquisitions SET deletion_requested_at=now() WHERE id=$1`, otherAcquisitionID); err != nil {
		t.Fatal(err)
	}
	assertFiles("其它已请求删除的 succeeded 引用", true)
	if _, err := fixture.pool.Exec(ctx, `UPDATE imports SET status='running' WHERE id=$1`, otherImportID); err != nil {
		t.Fatal(err)
	}
	assertFiles("其它已请求删除的 running 采用登记", false)
	if _, err := fixture.pool.Exec(ctx, `UPDATE imports SET status='cancelled' WHERE id=$1`, otherImportID); err != nil {
		t.Fatal(err)
	}
	assertFiles("其它 cancelled 终态", true)

	_, sameImportID := createSameAcquisitionRunningImport(t, fixture, acquisitionID, videoPath, subtitlePath)
	assertFiles("同 acquisition running 采用登记", false)
	if _, err := fixture.pool.Exec(ctx, `UPDATE imports SET status='cancelled' WHERE id=$1`, sameImportID); err != nil {
		t.Fatal(err)
	}
	assertFiles("同 acquisition 取消后无占用", true)

	// 命令快照加载后新增的成功文件不能在本次未加锁删除；必须要求重新加载。
	newVideoPath := "/library/Deletion Show/Season1/Deletion Show - S01E02.mkv"
	newSubtitlePath := "/library/Deletion Show/Season1/Deletion Show - S01E02.ass"
	if _, err := fixture.pool.Exec(ctx, `
UPDATE imports SET status='succeeded', destination_video_path=$2, destination_subtitle_path=$3 WHERE id=$1`,
		sameImportID, newVideoPath, newSubtitlePath,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE episode_tasks SET state='imported', version=version+1
WHERE id=(SELECT task_id FROM imports WHERE id=$1)`, sameImportID,
	); err != nil {
		t.Fatal(err)
	}
	recorder.reset()
	prepared, err := workflow.DeleteImportedLibraryFiles(ctx, acquisitionID, uuid.New(), candidates, remove)
	if err == nil || prepared {
		t.Fatalf("快照后新增成功文件未要求重新加载：prepared=%t err=%v", prepared, err)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatalf("快照后新增成功文件触发了删除：%#v", recorder.snapshot())
	}
	expanded := append(append([]domain.AcquisitionDeletionLibraryFile{}, candidates...),
		domain.AcquisitionDeletionLibraryFile{FilePath: newVideoPath},
		domain.AcquisitionDeletionLibraryFile{FilePath: newSubtitlePath},
	)
	recorder.reset()
	prepared, err = workflow.DeleteImportedLibraryFiles(ctx, acquisitionID, uuid.New(), expanded, remove)
	if err != nil || !prepared {
		t.Fatalf("重新加载命令后删除失败：prepared=%t err=%v", prepared, err)
	}
	calls := recorder.snapshot()
	if len(calls) != 1 || len(calls[0]) != 4 {
		t.Fatalf("重新加载命令后的可删除文件 = %#v", calls)
	}

	// 空候选与过期候选不触发文件删除。
	recorder.reset()
	emptyPrepared, err := workflow.DeleteImportedLibraryFiles(ctx, acquisitionID, uuid.New(), nil, remove)
	if err != nil || !emptyPrepared || len(recorder.snapshot()) != 0 {
		t.Fatalf("空候选 = prepared %t calls %#v err %v", emptyPrepared, recorder.snapshot(), err)
	}
}

func TestAcquisitionDeletionLibraryReadinessRecheckIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewAcquisitionDeletionWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	videoPath := "/library/Readiness Show/Season1/Readiness Show - S01E01.mkv"
	subtitlePath := "/library/Readiness Show/Season1/Readiness Show - S01E01.ass"
	acquisitionID, taskID, _ := createDeletionImportFixture(t, fixture, "succeeded", videoPath, subtitlePath)
	candidates := []domain.AcquisitionDeletionLibraryFile{{FilePath: videoPath}, {FilePath: subtitlePath}}
	recorder := &deletionCallRecorder{}
	remove := func(callCtx context.Context, files []domain.AcquisitionDeletionLibraryFile) error {
		return recorder.remove(callCtx, files)
	}

	activeOperationID := uuid.New()
	insertDeletionOperation(t, fixture, activeOperationID, acquisitionID, taskID, "emby.refresh", "episode_task")
	prepared, err := workflow.DeleteImportedLibraryFiles(ctx, acquisitionID, uuid.New(), candidates, remove)
	if err != nil || prepared {
		t.Fatalf("活跃操作未阻止删除：prepared=%t err=%v", prepared, err)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatalf("活跃操作期间执行了文件回调：%#v", recorder.snapshot())
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE operations SET status='succeeded', finished_at=now() WHERE id=$1`, activeOperationID); err != nil {
		t.Fatal(err)
	}
	prepared, err = workflow.DeleteImportedLibraryFiles(ctx, acquisitionID, uuid.New(), candidates, remove)
	if err != nil || !prepared || len(recorder.snapshot()) != 1 {
		t.Fatalf("活动操作退出后未删除：prepared=%t calls=%#v err=%v", prepared, recorder.snapshot(), err)
	}

	// 当前删除操作自身按 operationID 排除，不阻断删除。
	deletionOperationID := uuid.New()
	insertDeletionOperation(t, fixture, deletionOperationID, acquisitionID, taskID, "acquisition.delete", "acquisition")
	recorder.reset()
	prepared, err = workflow.DeleteImportedLibraryFiles(ctx, acquisitionID, deletionOperationID, candidates, remove)
	if err != nil || !prepared || len(recorder.snapshot()) != 1 {
		t.Fatalf("当前删除操作被误判为活跃：prepared=%t calls=%#v err=%v", prepared, recorder.snapshot(), err)
	}

	// 已请求取消的 queued 操作不再阻断删除。
	queuedOperationID := uuid.New()
	insertDeletionOperation(t, fixture, queuedOperationID, acquisitionID, taskID, "emby.refresh", "episode_task")
	if _, err := fixture.pool.Exec(ctx, `UPDATE operations SET status='queued', cancel_requested_at=now() WHERE id=$1`, queuedOperationID); err != nil {
		t.Fatal(err)
	}
	recorder.reset()
	prepared, err = workflow.DeleteImportedLibraryFiles(ctx, acquisitionID, deletionOperationID, candidates, remove)
	if err != nil || !prepared || len(recorder.snapshot()) != 1 {
		t.Fatalf("已取消 queued 操作阻止删除：prepared=%t calls=%#v err=%v", prepared, recorder.snapshot(), err)
	}

	// 取消的调用上下文不执行文件回调。
	canceledCtx, cancelCall := context.WithCancel(context.Background())
	cancelCall()
	recorder.reset()
	prepared, err = workflow.DeleteImportedLibraryFiles(canceledCtx, acquisitionID, deletionOperationID, candidates, remove)
	if err == nil || prepared {
		t.Fatalf("取消上下文 = prepared %t err %v", prepared, err)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatalf("取消上下文仍执行了文件回调：%#v", recorder.snapshot())
	}

	// 连接池不可用时查询失败，文件回调不得执行。
	brokenPool, poolErr := pgxpool.NewWithConfig(ctx, fixture.pool.Config())
	if poolErr != nil {
		t.Fatal(poolErr)
	}
	brokenPool.Close()
	brokenWorkflow := NewAcquisitionDeletionWorkflow(db.New(brokenPool), database.NewTransactor(brokenPool), fixture.scheduler)
	recorder.reset()
	prepared, err = brokenWorkflow.DeleteImportedLibraryFiles(ctx, acquisitionID, deletionOperationID, candidates, remove)
	if err == nil || prepared {
		t.Fatalf("查询失败 = prepared %t err %v", prepared, err)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatalf("查询失败仍执行了文件回调：%#v", recorder.snapshot())
	}
}

func TestAcquisitionDeletionLibraryWaitsAndSeesCommittedClaimIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewAcquisitionDeletionWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	videoPath := "/library/Isolation Show/Season1/Isolation Show - S01E01.mkv"
	subtitlePath := "/library/Isolation Show/Season1/Isolation Show - S01E01.ass"
	acquisitionID, _, _ := createDeletionImportFixture(t, fixture, "succeeded", videoPath, subtitlePath)
	candidates := []domain.AcquisitionDeletionLibraryFile{{FilePath: videoPath}, {FilePath: subtitlePath}}
	recorder := &deletionCallRecorder{}
	remove := func(callCtx context.Context, files []domain.AcquisitionDeletionLibraryFile) error {
		return recorder.remove(callCtx, files)
	}

	// 先占住目标路径锁，让删除事务在取得锁后等待。
	blockingTx, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockingTx.Rollback(context.Background()) }()
	if _, err := db.New(blockingTx).LockImportRecoveryDestinations(ctx, db.LockImportRecoveryDestinationsParams{
		DestinationVideoPath: videoPath, DestinationSubtitlePath: subtitlePath,
	}); err != nil {
		t.Fatalf("占用目标路径锁：%v", err)
	}

	type deletionResult struct {
		prepared bool
		err      error
	}
	result := make(chan deletionResult, 1)
	go func() {
		prepared, callErr := workflow.DeleteImportedLibraryFiles(ctx, acquisitionID, uuid.New(), candidates, remove)
		result <- deletionResult{prepared: prepared, err: callErr}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("删除事务未等待目标路径 advisory 锁")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 删除事务已开始并等待，期间提交一个新的 running 采用登记；锁后独立语句
	// 在 READ COMMITTED 下必须看到该登记并保留文件。
	createDeletionImportFixture(t, fixture, "running", videoPath, subtitlePath)
	if err := blockingTx.Rollback(ctx); err != nil {
		t.Fatalf("释放目标路径锁：%v", err)
	}
	select {
	case outcome := <-result:
		if outcome.err != nil || !outcome.prepared {
			t.Fatalf("删除结果 = prepared %t err %v", outcome.prepared, outcome.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("锁后未看到等待期间提交的 running 登记：%#v", calls)
	}
}

func TestAcquisitionDeletionLibraryCancellationWhileWaitingPreservesFilesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewAcquisitionDeletionWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	videoPath := "/library/Cancel Show/Season1/Cancel Show - S01E01.mkv"
	subtitlePath := "/library/Cancel Show/Season1/Cancel Show - S01E01.ass"
	acquisitionID, _, _ := createDeletionImportFixture(t, fixture, "succeeded", videoPath, subtitlePath)
	candidates := []domain.AcquisitionDeletionLibraryFile{{FilePath: videoPath}, {FilePath: subtitlePath}}
	recorder := &deletionCallRecorder{}
	remove := func(callCtx context.Context, files []domain.AcquisitionDeletionLibraryFile) error {
		return recorder.remove(callCtx, files)
	}

	blockingTx, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockingTx.Rollback(context.Background()) }()
	if _, err := db.New(blockingTx).LockImportRecoveryDestinations(ctx, db.LockImportRecoveryDestinationsParams{
		DestinationVideoPath: videoPath, DestinationSubtitlePath: subtitlePath,
	}); err != nil {
		t.Fatalf("占用目标路径锁：%v", err)
	}

	callCtx, callCancel := context.WithCancel(ctx)
	defer callCancel()
	type deletionResult struct {
		prepared bool
		err      error
	}
	result := make(chan deletionResult, 1)
	go func() {
		prepared, callErr := workflow.DeleteImportedLibraryFiles(callCtx, acquisitionID, uuid.New(), candidates, remove)
		result <- deletionResult{prepared: prepared, err: callErr}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("删除事务未等待目标路径 advisory 锁")
		}
		time.Sleep(20 * time.Millisecond)
	}
	callCancel()
	select {
	case outcome := <-result:
		if outcome.prepared || !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("等待中取消 = prepared %t err %v", outcome.prepared, outcome.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("等待中取消仍执行了文件回调：%#v", calls)
	}
	if err := blockingTx.Rollback(ctx); err != nil {
		t.Fatalf("释放目标路径锁：%v", err)
	}
}
