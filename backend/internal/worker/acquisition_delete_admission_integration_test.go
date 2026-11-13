//go:build integration

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

// 删除消费者协议回归：真实 TaskWorkflow/EmbyImportHandler/服务事务/PostgreSQL/文件系统。
// 队列插入使用既有 probeJobInserter 桩，River 作业不执行；删除调度、操作记录、锁、
// 占用判定与文件操作仍是真实生产路径。

type deletionCommandGateStore struct {
	*service.AcquisitionDeletionWorkflow
	loaded  chan domain.AcquisitionDeletionCommand
	release chan struct{}
	once    sync.Once
}

func (store *deletionCommandGateStore) LoadDeletionCommand(ctx context.Context, acquisitionID uuid.UUID) (domain.AcquisitionDeletionCommand, error) {
	command, err := store.AcquisitionDeletionWorkflow.LoadDeletionCommand(ctx, acquisitionID)
	if err != nil {
		return command, err
	}
	store.once.Do(func() { store.loaded <- command })
	select {
	case <-store.release:
		return command, nil
	case <-ctx.Done():
		return domain.AcquisitionDeletionCommand{}, context.Cause(ctx)
	}
}

type lockedDeletionGateStore struct {
	*service.AcquisitionDeletionWorkflow
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (store *lockedDeletionGateStore) DeleteImportedLibraryFiles(
	ctx context.Context,
	acquisitionID, operationID uuid.UUID,
	candidates []domain.AcquisitionDeletionLibraryFile,
	remove func(context.Context, []domain.AcquisitionDeletionLibraryFile) error,
) (bool, error) {
	return store.AcquisitionDeletionWorkflow.DeleteImportedLibraryFiles(
		ctx, acquisitionID, operationID, candidates,
		func(callbackCtx context.Context, files []domain.AcquisitionDeletionLibraryFile) error {
			store.once.Do(func() { close(store.reached) })
			select {
			case <-store.release:
			case <-callbackCtx.Done():
				return context.Cause(callbackCtx)
			}
			return remove(callbackCtx, files)
		},
	)
}

// rebuildBarrierStore 记录真实准入结果并在真实提交前暂停，用于验证删除锁与
// 采用者重建时序。missingAfterPrepare 在准入返回瞬间记录最终文件状态。
type rebuildBarrierStore struct {
	*service.TaskWorkflow
	enteredPrepare  chan struct{}
	prepareDone     chan struct{}
	completeReached chan struct{}
	completeRelease chan struct{}
	prepareEnter    sync.Once
	prepareDoneOnce sync.Once
	completeOnce    sync.Once
	missingMu       sync.Mutex
	videoMissing    bool
	subtitleMissing bool
}

func (store *rebuildBarrierStore) PrepareImportDestination(ctx context.Context, completion domain.ImportCompletion) (bool, error) {
	store.prepareEnter.Do(func() { close(store.enteredPrepare) })
	admitted, err := store.TaskWorkflow.PrepareImportDestination(ctx, completion)
	if err == nil && admitted {
		store.missingMu.Lock()
		store.videoMissing = deletionFileAbsent(completion.DestinationVideoPath)
		store.subtitleMissing = deletionFileAbsent(completion.DestinationSubtitlePath)
		store.missingMu.Unlock()
		store.prepareDoneOnce.Do(func() { close(store.prepareDone) })
	}
	return admitted, err
}

func (store *rebuildBarrierStore) CompleteImport(ctx context.Context, completion domain.ImportCompletion) error {
	store.completeOnce.Do(func() { close(store.completeReached) })
	select {
	case <-store.completeRelease:
		return store.TaskWorkflow.CompleteImport(ctx, completion)
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (store *rebuildBarrierStore) missingAfterPrepare() (bool, bool) {
	store.missingMu.Lock()
	defer store.missingMu.Unlock()
	return store.videoMissing, store.subtitleMissing
}

func deletionFileAbsent(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

func deletionDestinations(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) (string, string) {
	t.Helper()
	var video, subtitle string
	if err := pool.QueryRow(ctx, `
SELECT destination_video_path, destination_subtitle_path
FROM imports WHERE task_id=$1 AND status='succeeded'`, taskID).Scan(&video, &subtitle); err != nil {
		t.Fatalf("读取成功目标：%v", err)
	}
	return video, subtitle
}

func importTaskForDeletion(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	workflow *service.TaskWorkflow,
	configuration MediaConfiguration,
	taskID, actorID uuid.UUID,
) domain.Operation {
	t.Helper()
	operation := reviewHandleImport(t, ctx, pool, workflow, taskID, actorID)
	assertStagingMatchesImportCommand(t, ctx, pool, taskID)
	lifecycle := repository.NewOperationLifecycle(db.New(pool), database.NewTransactor(pool))
	if _, err := lifecycle.StartAttempt(ctx, operation.ID, 1, "deletion-probe"); err != nil {
		t.Fatalf("A 开始尝试：%v", err)
	}
	if err := NewEmbyImportHandler(configuration, workflow, newTestImportedLibraryAccess()).Handle(ctx, operation); err != nil {
		t.Fatalf("A 导入失败：%v", err)
	}
	if err := lifecycle.SucceedAttempt(ctx, operation.ID, 1); err != nil {
		t.Fatalf("A 完成尝试：%v", err)
	}
	// 本组回归只验证已入库文件协议；清空下载保存路径，避免清理逻辑触碰
	// 测试主机上不存在的固定下载目录。
	if _, err := pool.Exec(ctx, `
UPDATE downloads SET save_path=NULL
WHERE acquisition_id=(SELECT acquisition_id FROM episode_tasks WHERE id=$1)`, taskID,
	); err != nil {
		t.Fatalf("清空测试下载路径：%v", err)
	}
	return operation
}

// assertStagingMatchesImportCommand 校验数据库导入命令与实际暂存文件的路径、
// 大小和 SHA-256 一致，确认回归使用的是合法暂存元数据而非手造状态。
func assertStagingMatchesImportCommand(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) {
	t.Helper()
	importID := latestImportID(t, ctx, pool, taskID)
	row, err := db.New(pool).GetTaskImportCommand(ctx, db.GetTaskImportCommandParams{
		TaskID: repository.UUIDToPG(taskID), ImportID: repository.UUIDToPG(importID),
	})
	if err != nil {
		t.Fatalf("读取导入命令：%v", err)
	}
	for _, artifact := range []struct {
		label string
		path  string
		size  int64
		sum   []byte
	}{
		{"video", row.VideoFilePath, row.VideoSizeBytes, row.VideoChecksumSha256},
		{"subtitle", row.SubtitleFilePath, row.SubtitleSizeBytes, row.SubtitleChecksumSha256},
	} {
		if artifact.path == "" || artifact.size <= 0 || len(artifact.sum) != sha256.Size {
			t.Fatalf("%s 导入命令元数据不完整：%#v", artifact.label, artifact)
		}
		info, statErr := os.Lstat(artifact.path)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != artifact.size {
			t.Fatalf("%s 暂存文件状态 = %v，命令大小 %d，err=%v", artifact.label, info, artifact.size, statErr)
		}
		content, readErr := os.ReadFile(artifact.path)
		if readErr != nil {
			t.Fatalf("读取 %s 暂存文件：%v", artifact.label, readErr)
		}
		sum := sha256.Sum256(content)
		if !bytes.Equal(sum[:], artifact.sum) {
			t.Fatalf("%s 暂存文件 SHA-256 与导入命令不一致", artifact.label)
		}
	}
	if row.Basename == "" || filepath.Base(row.VideoFilePath) == "" {
		t.Fatalf("导入命令缺少基名：%#v", row)
	}
}

func acquisitionIDForTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) uuid.UUID {
	t.Helper()
	var acquisitionID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT acquisition_id FROM episode_tasks WHERE id=$1`, taskID).Scan(&acquisitionID); err != nil {
		t.Fatalf("读取 acquisition：%v", err)
	}
	return acquisitionID
}

func stagingRootForTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) string {
	t.Helper()
	var artifactPath string
	if err := pool.QueryRow(ctx, `SELECT file_path FROM media_artifacts WHERE task_id=$1 AND kind='video'`, taskID).Scan(&artifactPath); err != nil {
		t.Fatalf("读取暂存路径：%v", err)
	}
	return filepath.Dir(artifactPath)
}

func newAcquisitionDeletionConfiguration(libraryRoot, stagingRoot string) *downloadConfigurationStub {
	return &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{
		AnimeLibraryRoot: libraryRoot,
		StagingRoot:      stagingRoot,
		DownloadRoot:     "/downloads",
	}}}}
}

func associateAcquisitionWithRSS(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, seriesID, acquisitionID uuid.UUID,
) (subscriptionID, entryID uuid.UUID) {
	t.Helper()
	subscriptionID, entryID = uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, $3, $4, false, 900, 1)`,
		subscriptionID, seriesID, "Deletion Probe "+subscriptionID.String(), "https://example.test/"+subscriptionID.String()+".xml",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, status)
VALUES ($1, $2, $3, 'Deletion Probe Episode', 'enqueued')`,
		entryID, subscriptionID, "deletion-probe-"+entryID.String(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE acquisitions SET source_kind='rss', rss_entry_id=$2, source_uri=NULL WHERE id=$1`, acquisitionID, entryID,
	); err != nil {
		t.Fatal(err)
	}
	return subscriptionID, entryID
}

func assertDeletionFilePresent(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%s 不存在：%v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s 不是普通文件：%v", path, info.Mode())
	}
}

func assertDeletionFileAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s 应不存在，实际 %v", path, err)
	}
}

func assertDeletionPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%s 应存在：%v", path, err)
	}
}

func newDeletionTorrentFactory() AcquisitionDeleteClientFactory {
	return func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error) {
		return &acquisitionDeleteTorrentStub{}, nil
	}
}

// TestAcquisitionDeletionHoldsAdmissionLockThenAdopterRebuildsIntegration 使用真实
// 并发验证：删除在持锁回调中暂停时，B 的真实 Prepare 等待同一 advisory 锁且未触
// 最终文件；删除提交后 B 在准入返回时看到文件缺失，并实际重建、成功提交。
func TestAcquisitionDeletionHoldsAdmissionLockThenAdopterRebuildsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	videoContent := []byte("deletion-lock-rebuild-video")
	taskA := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskA, videoContent)
	taskB := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskB, videoContent)
	libraryRoot := t.TempDir()
	transactor := database.NewTransactor(pool)
	workflow := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: libraryRoot}}}}
	importTaskForDeletion(t, ctx, pool, workflow, configuration, taskA, actorID)
	videoA, subtitleA := deletionDestinations(t, ctx, pool, taskA)
	assertFileContent(t, videoA, videoContent)
	assertFileContent(t, subtitleA, validASSFixture)
	acquisitionA := acquisitionIDForTask(t, ctx, pool, taskA)
	deletionWorkflow := service.NewAcquisitionDeletionWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	request, err := deletionWorkflow.RequestDeletion(ctx, acquisitionA, "deletion-lock:"+acquisitionA.String(), actorID)
	if err != nil {
		t.Fatalf("请求删除：%v", err)
	}
	request.Payload = []byte(`{"deleteImported":true}`)
	lockedStore := &lockedDeletionGateStore{AcquisitionDeletionWorkflow: deletionWorkflow, reached: make(chan struct{}), release: make(chan struct{})}
	deletionHandler := NewAcquisitionDeleteHandler(
		newAcquisitionDeletionConfiguration(libraryRoot, stagingRootForTask(t, ctx, pool, taskA)),
		lockedStore, newDeletionTorrentFactory(),
	)
	deletionResult := make(chan error, 1)
	go func() { deletionResult <- deletionHandler.Handle(ctx, request) }()
	select {
	case <-lockedStore.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("删除未进入持锁回调")
	}
	// 持锁回调暂停时 A 的文件仍未 unlink，B 尚未参与。
	assertDeletionFilePresent(t, videoA)
	assertDeletionFilePresent(t, subtitleA)

	operationB := reviewHandleImport(t, ctx, pool, workflow, taskB, actorID)
	assertStagingMatchesImportCommand(t, ctx, pool, taskB)
	barrierB := &rebuildBarrierStore{
		TaskWorkflow:   workflow,
		enteredPrepare: make(chan struct{}), prepareDone: make(chan struct{}),
		completeReached: make(chan struct{}), completeRelease: make(chan struct{}),
	}
	resultB := make(chan error, 1)
	go func() {
		resultB <- NewEmbyImportHandler(configuration, barrierB, newTestImportedLibraryAccess()).Handle(ctx, operationB)
	}()
	select {
	case <-barrierB.enteredPrepare:
	case <-time.After(10 * time.Second):
		t.Fatal("B 未进入准入")
	}
	select {
	case <-barrierB.prepareDone:
		t.Fatal("B 准入未被删除锁阻塞")
	case <-time.After(400 * time.Millisecond):
	}
	var waiting int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if waiting == 0 {
		t.Fatal("未观察到等待目标路径 advisory 锁的准入会话")
	}
	assertDeletionFilePresent(t, videoA)
	assertDeletionFilePresent(t, subtitleA)

	close(lockedStore.release)
	select {
	case err := <-deletionResult:
		if err != nil {
			t.Fatalf("删除失败：%v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var acquisitionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE id=$1`, acquisitionA).Scan(&acquisitionCount); err != nil || acquisitionCount != 0 {
		t.Fatalf("删除后 acquisition 行数 = %d，%v", acquisitionCount, err)
	}
	select {
	case <-barrierB.prepareDone:
	case <-time.After(10 * time.Second):
		t.Fatal("B 准入未在删除提交后继续")
	}
	videoMissing, subtitleMissing := barrierB.missingAfterPrepare()
	if !videoMissing || !subtitleMissing {
		t.Fatalf("B 准入返回时未看到删除结果：videoMissing=%t subtitleMissing=%t", videoMissing, subtitleMissing)
	}
	select {
	case <-barrierB.completeReached:
	case <-time.After(10 * time.Second):
		t.Fatal("B 未重建到提交前")
	}
	assertFileContent(t, videoA, videoContent)
	assertFileContent(t, subtitleA, validASSFixture)
	close(barrierB.completeRelease)
	select {
	case err := <-resultB:
		if err != nil {
			t.Fatalf("B 重建导入失败：%v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	view, err := db.New(pool).GetTaskView(ctx, repository.UUIDToPG(taskB))
	if err != nil || view.ImportStatus != "succeeded" || view.DestinationVideoPath == nil || *view.DestinationVideoPath != videoA {
		t.Fatalf("B 成功视图 = %#v，%v", view, err)
	}
	assertFileContent(t, videoA, videoContent)
	assertFileContent(t, subtitleA, validASSFixture)
}

// TestRSSSubscriptionDeletionPreservesRunningAdopterAcrossFreshClaimIntegration 走真实
// RSS 调度入口：RequestSubscriptionDeletion(deleteImported=true) 产生操作，随后
// RSSSubscriptionDeleteHandler.Handle 执行。删除命令快照在 B 登记前加载且
// Preserve=false；B 随后完成真实准入并在提交前暂停；删除必须由锁后新鲜判定保留
// 共享文件，B 成功提交且文件保持。队列插入为桩，River 作业不执行。
func TestRSSSubscriptionDeletionPreservesRunningAdopterAcrossFreshClaimIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	videoContent := []byte("rss-deletion-preserve-video")
	taskA := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskA, videoContent)
	taskB := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskB, videoContent)
	libraryRoot := t.TempDir()
	transactor := database.NewTransactor(pool)
	workflow := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: libraryRoot}}}}
	acquisitionA := acquisitionIDForTask(t, ctx, pool, taskA)
	subscriptionID, _ := associateAcquisitionWithRSS(t, ctx, pool, seriesID, acquisitionA)
	importTaskForDeletion(t, ctx, pool, workflow, configuration, taskA, actorID)
	videoA, subtitleA := deletionDestinations(t, ctx, pool, taskA)
	deletionWorkflow := service.NewAcquisitionDeletionWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	rssWorkflow := service.NewRSSWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	operation, err := rssWorkflow.RequestSubscriptionDeletion(ctx, subscriptionID, 1, "rss-deletion-preserve:"+subscriptionID.String(), true, actorID)
	if err != nil {
		t.Fatalf("请求 RSS 删除：%v", err)
	}
	if !operationPayloadDeleteImported(t, operation) {
		t.Fatalf("RSS 删除操作未记录 deleteImported=true：%s", operation.Payload)
	}
	commandGate := &deletionCommandGateStore{
		AcquisitionDeletionWorkflow: deletionWorkflow,
		loaded:                      make(chan domain.AcquisitionDeletionCommand, 1),
		release:                     make(chan struct{}),
	}
	acquisitionHandler := NewAcquisitionDeleteHandler(
		newAcquisitionDeletionConfiguration(libraryRoot, stagingRootForTask(t, ctx, pool, taskA)),
		commandGate, newDeletionTorrentFactory(),
	)
	rssHandler := NewRSSSubscriptionDeleteHandler(deletionWorkflow, acquisitionHandler)
	rssResult := make(chan error, 1)
	go func() { rssResult <- rssHandler.Handle(ctx, operation) }()
	var snapshot domain.AcquisitionDeletionCommand
	select {
	case snapshot = <-commandGate.loaded:
	case <-time.After(10 * time.Second):
		t.Fatal("RSS 删除未加载删除命令")
	}
	foundVideo, foundSubtitle := false, false
	for _, file := range snapshot.LibraryFiles {
		if file.FilePath == videoA {
			foundVideo = true
			if file.Preserve {
				t.Fatalf("命令快照在 B 登记前已标记视频保留：%#v", file)
			}
		}
		if file.FilePath == subtitleA {
			foundSubtitle = true
			if file.Preserve {
				t.Fatalf("命令快照在 B 登记前已标记字幕保留：%#v", file)
			}
		}
	}
	if !foundVideo || !foundSubtitle {
		t.Fatalf("删除命令快照缺少 A 的入库文件：%#v", snapshot.LibraryFiles)
	}
	// 命令快照已加载（Preserve=false），B 此时才真实准入并暂停在提交前。
	operationB := reviewHandleImport(t, ctx, pool, workflow, taskB, actorID)
	assertStagingMatchesImportCommand(t, ctx, pool, taskB)
	barrierB := &rebuildBarrierStore{
		TaskWorkflow:   workflow,
		enteredPrepare: make(chan struct{}), prepareDone: make(chan struct{}),
		completeReached: make(chan struct{}), completeRelease: make(chan struct{}),
	}
	resultB := make(chan error, 1)
	go func() {
		resultB <- NewEmbyImportHandler(configuration, barrierB, newTestImportedLibraryAccess()).Handle(ctx, operationB)
	}()
	select {
	case <-barrierB.completeReached:
	case <-time.After(10 * time.Second):
		t.Fatal("B 未在删除命令加载后到达提交前")
	}
	assertDeletionFilePresent(t, videoA)
	assertDeletionFilePresent(t, subtitleA)

	close(commandGate.release)
	select {
	case err := <-rssResult:
		if err != nil {
			t.Fatalf("RSS 删除失败：%v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var acquisitionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE id=$1`, acquisitionA).Scan(&acquisitionCount); err != nil || acquisitionCount != 0 {
		t.Fatalf("A 未被删除：count=%d err=%v", acquisitionCount, err)
	}
	var subscriptionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_subscriptions WHERE id=$1`, subscriptionID).Scan(&subscriptionCount); err != nil || subscriptionCount != 0 {
		t.Fatalf("订阅未被删除：count=%d err=%v", subscriptionCount, err)
	}
	assertDeletionFilePresent(t, videoA)
	assertDeletionFilePresent(t, subtitleA)
	close(barrierB.completeRelease)
	select {
	case err := <-resultB:
		if err != nil {
			t.Fatalf("B 在共享文件保留后提交失败：%v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	view, err := db.New(pool).GetTaskView(ctx, repository.UUIDToPG(taskB))
	if err != nil || view.ImportStatus != "succeeded" || view.DestinationVideoPath == nil || *view.DestinationVideoPath != videoA {
		t.Fatalf("B 成功视图 = %#v，%v", view, err)
	}
	assertFileContent(t, videoA, videoContent)
	assertFileContent(t, subtitleA, validASSFixture)
}

func operationPayloadDeleteImported(t *testing.T, operation domain.Operation) bool {
	t.Helper()
	var payload struct {
		DeleteImported bool `json:"deleteImported"`
	}
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		t.Fatalf("解析 RSS 删除操作负载：%v", err)
	}
	return payload.DeleteImported
}

// TestRSSSubscriptionDeletionKeepsOtherSucceededReferenceIntegration 验证其它真实成功
// 引用保留文件：C 在同一目标真实导入成功后，A 的 RSS 显式删除仍不 unlink 共享文件。
func TestRSSSubscriptionDeletionKeepsOtherSucceededReferenceIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	videoContent := []byte("rss-deletion-succeeded-share-video")
	taskA := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskA, videoContent)
	taskC := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskC, videoContent)
	libraryRoot := t.TempDir()
	transactor := database.NewTransactor(pool)
	workflow := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: libraryRoot}}}}
	acquisitionA := acquisitionIDForTask(t, ctx, pool, taskA)
	subscriptionID, _ := associateAcquisitionWithRSS(t, ctx, pool, seriesID, acquisitionA)
	importTaskForDeletion(t, ctx, pool, workflow, configuration, taskA, actorID)
	importTaskForDeletion(t, ctx, pool, workflow, configuration, taskC, actorID)
	video, subtitle := deletionDestinations(t, ctx, pool, taskA)
	videoC, subtitleC := deletionDestinations(t, ctx, pool, taskC)
	if videoC != video || subtitleC != subtitle {
		t.Fatalf("C 未采用同一目标：%q/%q vs %q/%q", videoC, subtitleC, video, subtitle)
	}
	deletionWorkflow := service.NewAcquisitionDeletionWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	rssWorkflow := service.NewRSSWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	operation, err := rssWorkflow.RequestSubscriptionDeletion(ctx, subscriptionID, 1, "rss-deletion-succeeded:"+subscriptionID.String(), true, actorID)
	if err != nil {
		t.Fatalf("请求 RSS 删除：%v", err)
	}
	acquisitionHandler := NewAcquisitionDeleteHandler(
		newAcquisitionDeletionConfiguration(libraryRoot, stagingRootForTask(t, ctx, pool, taskA)),
		deletionWorkflow, newDeletionTorrentFactory(),
	)
	rssHandler := NewRSSSubscriptionDeleteHandler(deletionWorkflow, acquisitionHandler)
	if err := rssHandler.Handle(ctx, operation); err != nil {
		t.Fatalf("RSS 删除失败：%v", err)
	}
	var acquisitionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE id=$1`, acquisitionA).Scan(&acquisitionCount); err != nil || acquisitionCount != 0 {
		t.Fatalf("A 未被删除：count=%d err=%v", acquisitionCount, err)
	}
	assertFileContent(t, video, videoContent)
	assertFileContent(t, subtitle, validASSFixture)
	view, err := db.New(pool).GetTaskView(ctx, repository.UUIDToPG(taskC))
	if err != nil || view.ImportStatus != "succeeded" || view.DestinationVideoPath == nil || *view.DestinationVideoPath != video {
		t.Fatalf("C 成功引用视图 = %#v，%v", view, err)
	}
}

// TestRSSSubscriptionDeletionRemovesUnclaimedImportedFilesIntegration 验证无其它占用
// 时显式 deleteImported 仍实际 unlink，并推进 acquisition/订阅删除完成。
func TestRSSSubscriptionDeletionRemovesUnclaimedImportedFilesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	videoContent := []byte("rss-deletion-unclaimed-video")
	taskA := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskA, videoContent)
	libraryRoot := t.TempDir()
	transactor := database.NewTransactor(pool)
	workflow := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: libraryRoot}}}}
	acquisitionA := acquisitionIDForTask(t, ctx, pool, taskA)
	subscriptionID, _ := associateAcquisitionWithRSS(t, ctx, pool, seriesID, acquisitionA)
	importTaskForDeletion(t, ctx, pool, workflow, configuration, taskA, actorID)
	video, subtitle := deletionDestinations(t, ctx, pool, taskA)
	assertDeletionFilePresent(t, video)
	assertDeletionFilePresent(t, subtitle)
	deletionWorkflow := service.NewAcquisitionDeletionWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	rssWorkflow := service.NewRSSWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	operation, err := rssWorkflow.RequestSubscriptionDeletion(ctx, subscriptionID, 1, "rss-deletion-unclaimed:"+subscriptionID.String(), true, actorID)
	if err != nil {
		t.Fatalf("请求 RSS 删除：%v", err)
	}
	acquisitionHandler := NewAcquisitionDeleteHandler(
		newAcquisitionDeletionConfiguration(libraryRoot, stagingRootForTask(t, ctx, pool, taskA)),
		deletionWorkflow, newDeletionTorrentFactory(),
	)
	rssHandler := NewRSSSubscriptionDeleteHandler(deletionWorkflow, acquisitionHandler)
	if err := rssHandler.Handle(ctx, operation); err != nil {
		t.Fatalf("RSS 删除失败：%v", err)
	}
	assertDeletionFileAbsent(t, video)
	assertDeletionFileAbsent(t, subtitle)
	var acquisitionCount, subscriptionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE id=$1`, acquisitionA).Scan(&acquisitionCount); err != nil || acquisitionCount != 0 {
		t.Fatalf("A 未被删除：count=%d err=%v", acquisitionCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_subscriptions WHERE id=$1`, subscriptionID).Scan(&subscriptionCount); err != nil || subscriptionCount != 0 {
		t.Fatalf("订阅未被删除：count=%d err=%v", subscriptionCount, err)
	}
}

// TestRSSSubscriptionDeletionKeepsUnsafeImportedFileIntegration 验证锁内安全回调：
// 库根内指向根外的符号链接被拒绝，文件系统改动不发生，也不推进 CompleteDeletion。
func TestRSSSubscriptionDeletionKeepsUnsafeImportedFileIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	videoContent := []byte("rss-deletion-unsafe-video")
	taskA := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskA, videoContent)
	libraryRoot := t.TempDir()
	transactor := database.NewTransactor(pool)
	workflow := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: libraryRoot}}}}
	acquisitionA := acquisitionIDForTask(t, ctx, pool, taskA)
	subscriptionID, _ := associateAcquisitionWithRSS(t, ctx, pool, seriesID, acquisitionA)
	importTaskForDeletion(t, ctx, pool, workflow, configuration, taskA, actorID)
	video, subtitle := deletionDestinations(t, ctx, pool, taskA)
	outsideRoot := t.TempDir()
	outsidePath := filepath.Join(outsideRoot, "outside.mkv")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(video); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, video); err != nil {
		t.Fatal(err)
	}
	deletionWorkflow := service.NewAcquisitionDeletionWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	rssWorkflow := service.NewRSSWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	operation, err := rssWorkflow.RequestSubscriptionDeletion(ctx, subscriptionID, 1, "rss-deletion-unsafe:"+subscriptionID.String(), true, actorID)
	if err != nil {
		t.Fatalf("请求 RSS 删除：%v", err)
	}
	acquisitionHandler := NewAcquisitionDeleteHandler(
		newAcquisitionDeletionConfiguration(libraryRoot, stagingRootForTask(t, ctx, pool, taskA)),
		deletionWorkflow, newDeletionTorrentFactory(),
	)
	rssHandler := NewRSSSubscriptionDeleteHandler(deletionWorkflow, acquisitionHandler)
	err = rssHandler.Handle(ctx, operation)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "acquisition_delete_library_path_unsafe" || failure.Retryable {
		t.Fatalf("RSS 删除错误 = %#v，期望永久路径安全失败", err)
	}
	assertDeletionPathExists(t, video)
	assertDeletionPathExists(t, subtitle)
	if content, readErr := os.ReadFile(outsidePath); readErr != nil || string(content) != "outside" {
		t.Fatalf("库外文件被改动：%q，%v", content, readErr)
	}
	var acquisitionCount, subscriptionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE id=$1`, acquisitionA).Scan(&acquisitionCount); err != nil || acquisitionCount != 1 {
		t.Fatalf("失败后 A 应保留：count=%d err=%v", acquisitionCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_subscriptions WHERE id=$1`, subscriptionID).Scan(&subscriptionCount); err != nil || subscriptionCount != 1 {
		t.Fatalf("失败后订阅应保留：count=%d err=%v", subscriptionCount, err)
	}
}

// TestAcquisitionDeletionWithRealConfigurationAndSmallPoolIntegration 使用真实
// repository.Configuration/ConfigurationService 与 pool=1 执行显式删除：配置在锁外
// 只加载一次，持锁事务只用同一连接完成查询和文件回调，不再 Acquire。
func TestAcquisitionDeletionWithRealConfigurationAndSmallPoolIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	targetURL, fixturePool := testutil.NewMigratedPostgres(t)
	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, fixturePool, seriesID, profileID, actorID)
	videoContent := []byte("deletion-small-pool-video")
	taskA := seedAdoptionTask(t, ctx, fixturePool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, fixturePool, taskA, videoContent)
	libraryRoot := t.TempDir()
	stagingRoot := stagingRootForTask(t, ctx, fixturePool, taskA)

	poolConfiguration, err := pgxpool.ParseConfig(targetURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfiguration.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	configurationStore := repository.NewConfiguration(queries, transactor)
	if _, err := configurationStore.Save(ctx, domain.SaveConfiguration{
		ExpectedVersion: 0, UpdatedBy: actorID, Settings: domain.RuntimeSettings{
			Paths:     domain.PathSettings{AnimeLibraryRoot: libraryRoot, StagingRoot: stagingRoot, DownloadRoot: "/downloads"},
			Transcode: domain.DefaultTranscodeProfile(),
			Agent:     domain.DefaultAgentSettings(),
			Events:    domain.DefaultEventsSettings(),
		},
	}); err != nil {
		t.Fatalf("保存真实配置：%v", err)
	}
	configuration := service.NewConfigurationService(configurationStore, nil)
	workflow := service.NewTaskWorkflow(queries, transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	importTaskForDeletion(t, ctx, fixturePool, workflow, configuration, taskA, actorID)
	video, subtitle := deletionDestinations(t, ctx, fixturePool, taskA)
	acquisitionA := acquisitionIDForTask(t, ctx, fixturePool, taskA)
	deletionWorkflow := service.NewAcquisitionDeletionWorkflow(queries, transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	request, err := deletionWorkflow.RequestDeletion(ctx, acquisitionA, "deletion-small-pool:"+acquisitionA.String(), actorID)
	if err != nil {
		t.Fatalf("请求删除：%v", err)
	}
	request.Payload = []byte(`{"deleteImported":true}`)
	handler := NewAcquisitionDeleteHandler(configuration, deletionWorkflow, newDeletionTorrentFactory())
	if err := handler.Handle(ctx, request); err != nil {
		t.Fatalf("pool=1 真实配置删除失败：%v", err)
	}
	assertDeletionFileAbsent(t, video)
	assertDeletionFileAbsent(t, subtitle)
	var acquisitionCount int
	if err := fixturePool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE id=$1`, acquisitionA).Scan(&acquisitionCount); err != nil || acquisitionCount != 0 {
		t.Fatalf("pool=1 删除后 A 未移除：count=%d err=%v", acquisitionCount, err)
	}
}
