//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

// 此边界测试执行真实 Handle、服务事务和文件操作；作业队列与路径配置使用桩。
type importCompletionBarrierStore struct {
	*service.TaskWorkflow
	reached chan domain.ImportCompletion
	release chan struct{}
}

func (store *importCompletionBarrierStore) CompleteImport(ctx context.Context, completion domain.ImportCompletion) error {
	select {
	case store.reached <- completion:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-store.release:
		return store.TaskWorkflow.CompleteImport(ctx, completion)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestEmbyImportHandleProtectsRegisteredAdopterAndHidesPlannedPathsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	videoContent := []byte("handle-admission-video")
	taskA := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskA, videoContent)
	taskB := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskB, videoContent)
	transactor := database.NewTransactor(pool)
	workflow := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: t.TempDir()}}}}
	storeA := &importCompletionBarrierStore{TaskWorkflow: workflow, reached: make(chan domain.ImportCompletion, 1), release: make(chan struct{})}
	storeB := &importCompletionBarrierStore{TaskWorkflow: workflow, reached: make(chan domain.ImportCompletion, 1), release: make(chan struct{})}

	operationA := reviewHandleImport(t, ctx, pool, workflow, taskA, actorID)
	resultA := make(chan error, 1)
	go func() {
		resultA <- NewEmbyImportHandler(configuration, storeA, newTestImportedLibraryAccess()).Handle(ctx, operationA)
	}()
	completionA := awaitImportCompletion(t, ctx, storeA.reached, resultA)
	if _, err := pool.Exec(ctx, `UPDATE episode_tasks SET state='cancelled', version=version+1 WHERE id=$1 AND state NOT IN ('imported','cancelled','rejected')`, taskA); err != nil {
		t.Fatal(err)
	}

	// A 取消后 B 合法审批并通过同一 Handle 准入、采用；B 尚未提交成功。
	operationB := reviewHandleImport(t, ctx, pool, workflow, taskB, actorID)
	resultB := make(chan error, 1)
	go func() {
		resultB <- NewEmbyImportHandler(configuration, storeB, newTestImportedLibraryAccess()).Handle(ctx, operationB)
	}()
	completionB := awaitImportCompletion(t, ctx, storeB.reached, resultB)
	if completionA.DestinationVideoPath != completionB.DestinationVideoPath || completionA.DestinationSubtitlePath != completionB.DestinationSubtitlePath {
		t.Fatalf("两任务没有共享目标：%#v / %#v", completionA, completionB)
	}
	assertPlannedPathsHidden(t, ctx, pool, taskB)

	close(storeA.release)
	select {
	case err := <-resultA:
		var failure *Failure
		if !errors.As(err, &failure) || failure.Code != "task_import_state_conflict" {
			t.Fatalf("A 取消结果 = %#v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertFileContent(t, completionB.DestinationVideoPath, videoContent)
	assertFileContent(t, completionB.DestinationSubtitlePath, validASSFixture)

	close(storeB.release)
	select {
	case err := <-resultB:
		if err != nil {
			t.Fatalf("B 采用失败 = %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	view, err := db.New(pool).GetTaskView(ctx, repository.UUIDToPG(taskB))
	if err != nil || view.DestinationVideoPath == nil || *view.DestinationVideoPath != completionB.DestinationVideoPath {
		t.Fatalf("成功目标未正确显示：%#v，%v", view.DestinationVideoPath, err)
	}
	assertFileContent(t, completionB.DestinationVideoPath, videoContent)
}

func TestImportTaskViewsHideRegisteredPathsBeforeSuccessIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	taskID := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskID, []byte("planned-path-video"))
	transactor := database.NewTransactor(pool)
	workflow := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: t.TempDir()}}}}
	store := &importCompletionBarrierStore{TaskWorkflow: workflow, reached: make(chan domain.ImportCompletion, 1), release: make(chan struct{})}
	operation := reviewHandleImport(t, ctx, pool, workflow, taskID, actorID)
	result := make(chan error, 1)
	go func() {
		result <- NewEmbyImportHandler(configuration, store, newTestImportedLibraryAccess()).Handle(ctx, operation)
	}()
	completion := awaitImportCompletion(t, ctx, store.reached, result)
	assertPlannedPathsHidden(t, ctx, pool, taskID)
	close(store.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	view, err := db.New(pool).GetTaskView(ctx, repository.UUIDToPG(taskID))
	if err != nil || view.DestinationVideoPath == nil || *view.DestinationVideoPath != completion.DestinationVideoPath {
		t.Fatalf("成功后路径投影 = %#v，%v", view.DestinationVideoPath, err)
	}
}

func TestImportRecoveryKeepsReservationWhenCreatedFileWasChangedIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	taskID := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "")
	prepareHandleArtifacts(t, ctx, pool, taskID, []byte("changed-target-video"))
	transactor := database.NewTransactor(pool)
	workflow := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: t.TempDir()}}}}
	store := &importCompletionBarrierStore{TaskWorkflow: workflow, reached: make(chan domain.ImportCompletion, 1), release: make(chan struct{})}
	operation := reviewHandleImport(t, ctx, pool, workflow, taskID, actorID)
	result := make(chan error, 1)
	go func() {
		result <- NewEmbyImportHandler(configuration, store, newTestImportedLibraryAccess()).Handle(ctx, operation)
	}()
	completion := awaitImportCompletion(t, ctx, store.reached, result)
	changed := []byte("其它进程修改后的文件")
	if err := os.WriteFile(completion.DestinationVideoPath, changed, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE episode_tasks SET state='cancelled', version=version+1 WHERE id=$1 AND state NOT IN ('imported','cancelled','rejected')`, taskID); err != nil {
		t.Fatal(err)
	}
	close(store.release)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("取消后未返回失败")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertFileContent(t, completion.DestinationVideoPath, changed)
	var reserved *string
	if err := pool.QueryRow(ctx, `SELECT destination_video_path FROM imports WHERE id=$1`, completion.ImportID).Scan(&reserved); err != nil || reserved == nil || *reserved != completion.DestinationVideoPath {
		t.Fatalf("未清理文件的恢复登记丢失：%v，%v", reserved, err)
	}
}

func TestEmbyImportWithRealConfigurationAndSmallPoolIntegration(t *testing.T) {
	for _, maxConnections := range []int32{1, 2} {
		t.Run(fmt.Sprintf("pool-%d", maxConnections), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, fixturePool := testutil.NewMigratedPostgres(t)
			seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
			seedAdoptionBase(t, ctx, fixturePool, seriesID, profileID, actorID)
			otherSeriesID := uuid.New()
			if _, err := fixturePool.Exec(ctx, `INSERT INTO media_series (id,title,metadata) VALUES ($1,'Concurrent Configuration Show','{}'::jsonb)`, otherSeriesID); err != nil {
				t.Fatal(err)
			}
			var tasks []uuid.UUID
			for _, taskSeries := range []uuid.UUID{seriesID, otherSeriesID} {
				taskID := seedAdoptionTask(t, ctx, fixturePool, taskSeries, profileID, actorID, "")
				prepareHandleArtifacts(t, ctx, fixturePool, taskID, []byte("small-pool-video"))
				tasks = append(tasks, taskID)
			}
			poolConfiguration := fixturePool.Config()
			poolConfiguration.MaxConns = maxConnections
			pool, err := pgxpool.NewWithConfig(ctx, poolConfiguration)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			queries := db.New(pool)
			transactor := database.NewTransactor(pool)
			configurationStore := repository.NewConfiguration(queries, transactor)
			if _, err := configurationStore.Save(ctx, domain.SaveConfiguration{ExpectedVersion: 0, UpdatedBy: actorID, Settings: domain.RuntimeSettings{
				Paths:     domain.PathSettings{AnimeLibraryRoot: t.TempDir()},
				Transcode: domain.DefaultTranscodeProfile(),
				Agent:     domain.DefaultAgentSettings(),
				Events:    domain.DefaultEventsSettings(),
			}}); err != nil {
				t.Fatal(err)
			}
			// Load 不解密密钥；路径配置实际由同一小连接池的仓库读取。
			configuration := service.NewConfigurationService(configurationStore, nil)
			workflow := service.NewTaskWorkflow(queries, transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
			var operations []domain.Operation
			for _, taskID := range tasks {
				operations = append(operations, reviewHandleImport(t, ctx, pool, workflow, taskID, actorID))
			}
			results := make(chan error, len(operations))
			for _, operation := range operations {
				go func() {
					results <- NewEmbyImportHandler(configuration, workflow, newTestImportedLibraryAccess()).Handle(ctx, operation)
				}()
			}
			for range operations {
				select {
				case err := <-results:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			for _, taskID := range tasks {
				view, err := queries.GetTaskView(ctx, repository.UUIDToPG(taskID))
				if err != nil || view.ImportStatus != "succeeded" || view.DestinationVideoPath == nil {
					t.Fatalf("小池导入未成功：status=%q error=%v", view.ImportStatus, err)
				}
				assertFileContent(t, *view.DestinationVideoPath, []byte("small-pool-video"))
			}
		})
	}
}

func prepareHandleArtifacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID, video []byte) {
	t.Helper()
	baseName := "Adoption Show - S01E01 - Pilot"
	root := t.TempDir()
	for _, artifact := range []struct {
		kind, extension string
		content         []byte
	}{{"video", ".mkv", video}, {"subtitle", ".ass", validASSFixture}} {
		path := filepath.Join(root, baseName+artifact.extension)
		if err := os.WriteFile(path, artifact.content, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE media_artifacts SET basename=$2, file_path=$3, size_bytes=$4, checksum_sha256=$5 WHERE task_id=$1 AND kind=$6`,
			taskID, baseName, path, len(artifact.content), checksumFor(artifact.content), artifact.kind); err != nil {
			t.Fatal(err)
		}
	}
}

func reviewHandleImport(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workflow *service.TaskWorkflow, taskID, actorID uuid.UUID) domain.Operation {
	t.Helper()
	if _, err := workflow.ReviewTask(ctx, domain.ReviewTask{TaskID: taskID, ExpectedVersion: 1, Decision: domain.TaskApproved, IdempotencyKey: "handle-import:" + taskID.String(), ActorUserID: actorID}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(embyImportPayload{ImportID: latestImportID(t, ctx, pool, taskID)})
	if err != nil {
		t.Fatal(err)
	}
	return domain.Operation{ID: latestImportOperationID(t, ctx, pool, taskID), ResourceType: "episode_task", ResourceID: taskID, Payload: payload}
}

func awaitImportCompletion(t *testing.T, ctx context.Context, reached <-chan domain.ImportCompletion, result <-chan error) domain.ImportCompletion {
	t.Helper()
	select {
	case completion := <-reached:
		return completion
	case err := <-result:
		t.Fatalf("到达提交前提前结束：%v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return domain.ImportCompletion{}
}

func assertPlannedPathsHidden(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) {
	t.Helper()
	queries := db.New(pool)
	view, err := queries.GetTaskView(ctx, repository.UUIDToPG(taskID))
	if err != nil || view.DestinationVideoPath != nil || view.DestinationSubtitlePath != nil || view.ImportStatus != "running" {
		t.Fatalf("详情规划路径提前显示：status=%q video=%t subtitle=%t error=%v", view.ImportStatus, view.DestinationVideoPath != nil, view.DestinationSubtitlePath != nil, err)
	}
	rows, err := queries.ListTaskViews(ctx, db.ListTaskViewsParams{PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if repository.UUIDFromPG(row.ID) == taskID {
			if row.DestinationVideoPath != nil || row.DestinationSubtitlePath != nil {
				t.Fatal("列表提前展示计划目标")
			}
			return
		}
	}
	t.Fatal("列表缺少本任务")
}
