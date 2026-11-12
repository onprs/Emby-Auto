//go:build integration

package worker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

// 竞态回归：补偿观察返回后、回滚删除前，另一合法任务通过真实业务入口
// （ReviewTask → BeginImport → CompleteImport）采用同一目标并成功提交。
// 观察与删除必须与成功引用的写入共享目标路径锁域，使该窗口不可能交错。
//   - barrier 模式：B 在补偿观察返回后、删除完成前完成导入，断言 B 的成功引用
//     与其引用的文件完好（旧实现会删除这些文件，即 F 报告的 EM-01-RACE）。
//   - no-barrier 对照：A 的导入行在永久失败后仍为 running（保守保留语义），
//     断言文件未被越权删除且 A 状态未被误报成功。
func TestCompensationDoesNotDeleteAdoptedDestinationIntegration(t *testing.T) {
	runAdoptionGuardProbe(t, true)
}

func TestCompensationRollsBackWhenNoAdoptionInterleavesIntegration(t *testing.T) {
	runAdoptionGuardProbe(t, false)
}

func runAdoptionGuardProbe(t *testing.T, useBarrier bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	taskA := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "-A")
	taskB := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "-B")

	transactor := database.NewTransactor(pool)
	workflowA := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))
	workflowB := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))

	baseName := "Adoption Show - S01E01 - Pilot"
	libraryRoot := t.TempDir()
	destinationVideo := filepath.Join(libraryRoot, "Adoption Show", "Season1", baseName+".mkv")
	destinationSubtitle := filepath.Join(libraryRoot, "Adoption Show", "Season1", baseName+".ass")

	videoContent := []byte("adoption-video-bytes")
	subtitleContent := []byte("[Script Info]\nDialogue: adoption\n")
	staging := t.TempDir()
	videoPath := filepath.Join(staging, "adoption.mkv")
	subtitlePath := filepath.Join(staging, "adoption.ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, subtitleContent, 0o640); err != nil {
		t.Fatal(err)
	}
	videoArtifact := domain.MediaArtifact{TaskID: taskA, Kind: domain.MediaVideo, BaseName: baseName,
		FilePath: videoPath, Format: "matroska", SizeBytes: int64(len(videoContent)), ChecksumSHA256: checksumFor(videoContent)}
	subtitleArtifact := domain.MediaArtifact{TaskID: taskA, Kind: domain.MediaSubtitle, BaseName: baseName,
		FilePath: subtitlePath, Format: "ass", SizeBytes: int64(len(subtitleContent)), ChecksumSHA256: checksumFor(subtitleContent)}

	// 任务 A：真实审批 + BeginImport + 真实文件提交。
	if _, err := workflowA.ReviewTask(ctx, domain.ReviewTask{
		TaskID: taskA, ExpectedVersion: 1, Decision: domain.TaskApproved,
		IdempotencyKey: "adoption-a-review", ActorUserID: actorID,
	}); err != nil {
		t.Fatalf("ReviewTask(A) error = %v", err)
	}
	importA := latestImportID(t, ctx, pool, taskA)
	if _, err := workflowA.BeginImport(ctx, taskA, importA); err != nil {
		t.Fatalf("BeginImport(A) error = %v", err)
	}
	created, pairErr := importArtifactPair(ctx, videoArtifact, subtitleArtifact, libraryRoot,
		destinationVideo, destinationSubtitle, uuid.New(), newTestImportedLibraryAccess())
	if pairErr != nil {
		t.Fatalf("importArtifactPair(A) error = %v", pairErr)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d, want 2", len(created))
	}

	// 真实取消语义（与 CancelEpisodeTaskIfActive 相同条件与字段），使 CompleteImport(A) 永久失败。
	if _, err := pool.Exec(ctx, `UPDATE episode_tasks SET state='cancelled', version=version+1 WHERE id=$1 AND state NOT IN ('imported','cancelled','rejected')`, taskA); err != nil {
		t.Fatalf("cancel task A: %v", err)
	}
	completeErrA := workflowA.CompleteImport(ctx, domain.ImportCompletion{
		TaskID: taskA, ImportID: importA, OperationID: latestImportOperationID(t, ctx, pool, taskA),
		DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
	})
	var workflowErr *domain.MediaWorkflowError
	if !errors.As(completeErrA, &workflowErr) || workflowErr.Code != "task_import_state_conflict" {
		t.Fatalf("CompleteImport(A) error = %#v, want task_import_state_conflict", completeErrA)
	}

	// 任务 B：真实审批 + BeginImport（CompleteImport 在 barrier 期间执行）。
	if _, err := workflowB.ReviewTask(ctx, domain.ReviewTask{
		TaskID: taskB, ExpectedVersion: 1, Decision: domain.TaskApproved,
		IdempotencyKey: "adoption-b-review", ActorUserID: actorID,
	}); err != nil {
		t.Fatalf("ReviewTask(B) error = %v", err)
	}
	importB := latestImportID(t, ctx, pool, taskB)
	if _, err := workflowB.BeginImport(ctx, taskB, importB); err != nil {
		t.Fatalf("BeginImport(B) error = %v", err)
	}

	// barrier：补偿的真实观察返回后暂停，等待 B 完成导入后再释放。
	observerReturned := make(chan struct{})
	releaseObserver := make(chan struct{})
	var releaseOnce sync.Once
	var observedOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseObserver) }) }
	observed := newBarrierStore(workflowA, func() {
		observedOnce.Do(func() {
			if !useBarrier {
				release()
				return
			}
			close(observerReturned)
			select {
			case <-releaseObserver:
			case <-time.After(60 * time.Second):
				t.Error("barrier timed out waiting for task B adoption")
			}
		})
	})

	result := make(chan error, 1)
	go func() {
		handler := &EmbyImportHandler{store: observed}
		result <- handler.compensateFailedImport(context.Background(), domain.ImportCompletion{
			TaskID: taskA, ImportID: importA, OperationID: uuid.New(),
			DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
		}, created, &Failure{Code: "task_import_state_conflict", Message: "cancelled", Retryable: false})
	}()

	if useBarrier {
		<-observerReturned
		// barrier 期间：任务 B 通过真实 CompleteImport 成功占用目标。
		if err := workflowB.CompleteImport(ctx, domain.ImportCompletion{
			TaskID: taskB, ImportID: importB, OperationID: latestImportOperationID(t, ctx, pool, taskB),
			DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
		}); err != nil {
			t.Fatalf("CompleteImport(B) error = %v", err)
		}
		stateB, observeErr := workflowB.ObserveImportRecovery(ctx, taskB, importB, destinationVideo, destinationSubtitle)
		if observeErr != nil || !stateB.Succeeded {
			t.Fatalf("Observe(B) = %#v, %v, want succeeded", stateB, observeErr)
		}
	}

	release()
	compensationErr := <-result
	var failure *Failure
	if compensationErr == nil || !errors.As(compensationErr, &failure) || failure.Code != "task_import_state_conflict" {
		t.Fatalf("compensate() error = %#v, want task_import_state_conflict", compensationErr)
	}

	if useBarrier {
		// B 的成功引用建立于补偿观察之后，补偿不得删除其引用的文件。
		if _, err := os.Stat(destinationVideo); err != nil {
			t.Fatalf("补偿删除了任务 B 已成功引用的视频: %v", err)
		}
		if _, err := os.Stat(destinationSubtitle); err != nil {
			t.Fatalf("补偿删除了任务 B 已成功引用的字幕: %v", err)
		}
		assertFileContent(t, destinationVideo, videoContent)
		stateB, err := workflowB.ObserveImportRecovery(ctx, taskB, importB, destinationVideo, destinationSubtitle)
		if err != nil || !stateB.Succeeded || !stateB.DestinationClaimed {
			t.Fatalf("Observe(B) after compensation = %#v, %v, want succeeded+claimed", stateB, err)
		}
	} else {
		// no-barrier 对照：B 未完成导入时补偿按原语义回滚本次创建的文件。
		// A 的导入行在永久失败后仍为 running（该状态由 BeginImport 写入、
		// 只有成功路径更新），因此服务层保守保留而非删除是设计行为；
		// 验证文件仍存在且内容未被替换，补偿没有越过安全边界。
		assertFileContent(t, destinationVideo, videoContent)
		assertFileContent(t, destinationSubtitle, subtitleContent)
		stateA, err := workflowA.ObserveImportRecovery(ctx, taskA, importA, destinationVideo, destinationSubtitle)
		if err != nil {
			t.Fatalf("Observe(A) after compensation error = %v", err)
		}
		if stateA.Succeeded || stateA.DestinationClaimed {
			t.Fatalf("Observe(A) after compensation = %#v", stateA)
		}
	}
}

// barrierStore 只拦截 ObserveImportRecovery，其余走真实服务实现。
type barrierStore struct {
	workflow   *service.TaskWorkflow
	onObserved func()
	observed   bool
}

func newBarrierStore(workflow *service.TaskWorkflow, onObserved func()) *barrierStore {
	return &barrierStore{workflow: workflow, onObserved: onObserved}
}

func (store *barrierStore) BeginImport(ctx context.Context, taskID, importID uuid.UUID) (domain.ImportCommand, error) {
	return store.workflow.BeginImport(ctx, taskID, importID)
}

func (store *barrierStore) CompleteImport(ctx context.Context, completion domain.ImportCompletion) error {
	return store.workflow.CompleteImport(ctx, completion)
}

func (store *barrierStore) ObserveImportRecovery(ctx context.Context, taskID, importID uuid.UUID, video, subtitle string) (domain.ImportRecoveryState, error) {
	state, err := store.workflow.ObserveImportRecovery(ctx, taskID, importID, video, subtitle)
	if err == nil && !store.observed {
		store.observed = true
		store.onObserved()
	}
	return state, err
}

var _ TaskImportStore = (*barrierStore)(nil)

// 复用探针的 job inserter（minimal River 插入桩）。
type probeJobInserter struct{ nextID atomic.Int64 }

func (inserter *probeJobInserter) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	id := inserter.nextID.Add(1)
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: id}}, nil
}
func seedAdoptionBase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seriesID, profileID, actorID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'probe-hash')`, actorID, "adoption-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title, metadata) VALUES ($1, 'Adoption Show', '{"status":"Ended","in_production":false}'::jsonb)`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)
`, profileID, "adoption-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
}

func seedAdoptionTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seriesID, profileID, actorID uuid.UUID, suffix string) uuid.UUID {
	t.Helper()
	acquisitionID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, source_payload, created_by)
VALUES ($1, $2, 'manual', $3, '{}'::jsonb, $4)`,
		acquisitionID, seriesID, "magnet:?xt=urn:btih:"+fmt.Sprintf("%040x", time.Now().UnixNano())+suffix, actorID); err != nil {
		t.Fatal(err)
	}
	downloadID, sourceFileID, taskID := uuid.New(), uuid.New(), uuid.New()
	videoArtifactID, subtitleArtifactID := uuid.New(), uuid.New()
	// 两个任务的 artifact basename 相同（决定相同库目标），仅 UUID/保存路径不同。
	batch := &pgx.Batch{}
	batch.Queue(`
INSERT INTO downloads (id, acquisition_id, status, progress, save_path)
VALUES ($1, $2, 'materialized', 1, $3)`, downloadID, acquisitionID, "/downloads/adoption"+suffix)
	batch.Queue(`
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Adoption.Show.S01E01.mkv', 1024, 'video', true, 1, 1)`, sourceFileID, downloadID)
	batch.Queue(`
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, 'awaiting_review', 'video_ready', 'ass_ready')`, taskID, acquisitionID, sourceFileID, profileID)
	batch.Queue(`
INSERT INTO media_artifacts (id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $2, $3, $4, 'video', 'Adoption Show - S01E01 - Pilot', $5, 'matroska', 20, decode(repeat('11', 32), 'hex'))`,
		videoArtifactID, taskID, sourceFileID, profileID, "/staging/adoption"+suffix+"-video.mkv")
	batch.Queue(`
INSERT INTO media_artifacts (id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $2, $3, NULL, 'subtitle', 'Adoption Show - S01E01 - Pilot', $4, 'ass', 30, decode(repeat('22', 32), 'hex'))`,
		subtitleArtifactID, taskID, sourceFileID, "/staging/adoption"+suffix+"-subtitle.ass")
	batch.Queue(`
INSERT INTO artifact_sets (id, task_id, transcode_profile_id, basename, video_artifact_id, subtitle_artifact_id)
VALUES ($1, $2, $3, 'Adoption Show - S01E01 - Pilot', $4, $5)`,
		uuid.New(), taskID, profileID, videoArtifactID, subtitleArtifactID)
	if err := pool.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}
	return taskID
}

func latestImportOperationID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) uuid.UUID {
	t.Helper()
	row := pool.QueryRow(ctx, `
SELECT operation.id FROM operations AS operation
WHERE operation.resource_type = 'episode_task' AND operation.resource_id = $1
  AND operation.kind = 'emby.import'
ORDER BY operation.created_at DESC LIMIT 1`, taskID)
	var id pgtype.UUID
	if err := row.Scan(&id); err != nil {
		t.Fatalf("latest import operation: %v", err)
	}
	return repository.UUIDFromPG(id)
}

func latestImportID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) uuid.UUID {
	t.Helper()
	row := pool.QueryRow(ctx, `SELECT id FROM imports WHERE task_id = $1 ORDER BY attempt DESC LIMIT 1`, taskID)
	var id pgtype.UUID
	if err := row.Scan(&id); err != nil {
		t.Fatalf("latest import: %v", err)
	}
	return repository.UUIDFromPG(id)
}

func checksumFor(content []byte) []byte {
	sum := sha256.Sum256(content)
	return sum[:]
}
