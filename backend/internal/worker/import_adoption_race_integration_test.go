//go:build integration

package worker

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
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

// 协议回归（准入方案）：A 准入→FS 提交→取消→CompleteImport 永久失败→
// 持锁补偿回滚并释放登记；随后 B 通过完整协议（Review→Begin→准入→FS 采用→
// Complete）在同一目标成功。断言 B 的成功引用与其引用的文件完好，
// 且 A 的补偿没有删除 B 建立的内容（EM-01-RACE 反例在新协议下关闭）。
// no-barrier 对照：B 不参与时 A 补偿回滚并释放登记，文件按原语义消失。
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

	workflowA := service.NewTaskWorkflow(db.New(pool), database.NewTransactor(pool), service.NewOperationScheduler(database.NewTransactor(pool), &probeJobInserter{}))
	workflowB := service.NewTaskWorkflow(db.New(pool), database.NewTransactor(pool), service.NewOperationScheduler(database.NewTransactor(pool), &probeJobInserter{}))

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

	completionA := domain.ImportCompletion{
		TaskID: taskA, ImportID: uuid.UUID{},
		DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
	}

	// 任务 A：真实审批 + BeginImport + 准入 + FS 提交 + 取消 + Complete 失败 + 补偿。
	if _, err := workflowA.ReviewTask(ctx, domain.ReviewTask{
		TaskID: taskA, ExpectedVersion: 1, Decision: domain.TaskApproved,
		IdempotencyKey: "adoption-a-review", ActorUserID: actorID,
	}); err != nil {
		t.Fatalf("ReviewTask(A) error = %v", err)
	}
	importA := latestImportID(t, ctx, pool, taskA)
	completionA.ImportID = importA
	if _, err := workflowA.BeginImport(ctx, taskA, importA); err != nil {
		t.Fatalf("BeginImport(A) error = %v", err)
	}
	if _, err := workflowA.PrepareImportDestination(ctx, completionA); err != nil {
		t.Fatalf("PrepareImportDestination(A) error = %v", err)
	}
	created, pairErr := importArtifactPair(ctx, videoArtifact, subtitleArtifact, libraryRoot,
		destinationVideo, destinationSubtitle, uuid.New(), newTestImportedLibraryAccess())
	if pairErr != nil {
		t.Fatalf("importArtifactPair(A) error = %v", pairErr)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d, want 2", len(created))
	}

	// 真实取消语义（与 CancelEpisodeTaskIfActive 相同条件与字段）。
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

	// A 补偿（真实服务 RunImportCompensation 持锁回滚并释放登记）。
	if useBarrier {
		// barrier 模式：B 在 A 补偿之后通过完整协议成功采用同一目标。
		handler := &EmbyImportHandler{store: workflowA, libraryAccess: newTestImportedLibraryAccess()}
		compensationErr := handler.compensateFailedImport(ctx, completionA, created,
			&Failure{Code: "task_import_state_conflict", Message: "cancelled", Retryable: false})
		var failure *Failure
		if compensationErr == nil || !errors.As(compensationErr, &failure) || failure.Code != "task_import_state_conflict" {
			t.Fatalf("compensate(A) error = %#v, want task_import_state_conflict", compensationErr)
		}

		// B：完整协议。A 已释放登记，B 可以准入同一目标。
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
		completionB := domain.ImportCompletion{
			TaskID: taskB, ImportID: importB,
			DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
		}
		if _, err := workflowB.PrepareImportDestination(ctx, completionB); err != nil {
			t.Fatalf("PrepareImportDestination(B) error = %v", err)
		}
		createdB, pairErrB := importArtifactPair(ctx, domain.MediaArtifact{TaskID: taskB, Kind: domain.MediaVideo, BaseName: baseName,
			FilePath: videoPath, Format: "matroska", SizeBytes: int64(len(videoContent)), ChecksumSHA256: checksumFor(videoContent)},
			domain.MediaArtifact{TaskID: taskB, Kind: domain.MediaSubtitle, BaseName: baseName,
				FilePath: subtitlePath, Format: "ass", SizeBytes: int64(len(subtitleContent)), ChecksumSHA256: checksumFor(subtitleContent)},
			libraryRoot, destinationVideo, destinationSubtitle, uuid.New(), newTestImportedLibraryAccess())
		if pairErrB != nil {
			t.Fatalf("importArtifactPair(B) error = %v", pairErrB)
		}
		_ = createdB
		if err := workflowB.CompleteImport(ctx, domain.ImportCompletion{
			TaskID: taskB, ImportID: importB, OperationID: latestImportOperationID(t, ctx, pool, taskB),
			DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
		}); err != nil {
			t.Fatalf("CompleteImport(B) error = %v", err)
		}

		// 关键断言：B 的成功引用与其引用的文件完好（A 补偿没有删除 B 的内容）。
		if _, err := os.Stat(destinationVideo); err != nil {
			t.Fatalf("B 成功引用的视频不存在: %v", err)
		}
		if _, err := os.Stat(destinationSubtitle); err != nil {
			t.Fatalf("B 成功引用的字幕不存在: %v", err)
		}
		assertFileContent(t, destinationVideo, videoContent)
		stateB, err := workflowB.ObserveImportRecovery(ctx, taskB, importB, destinationVideo, destinationSubtitle)
		if err != nil || !stateB.Succeeded || stateB.DestinationClaimed {
			t.Fatalf("Observe(B) = %#v, %v, want succeeded without self-claim", stateB, err)
		}
		stateA, err := workflowA.ObserveImportRecovery(ctx, taskA, importA, destinationVideo, destinationSubtitle)
		if err != nil || !stateA.DestinationClaimed {
			t.Fatalf("Observe(A) = %#v, %v, want claimed by B", stateA, err)
		}
	} else {
		// no-barrier 对照：B 不参与，A 补偿回滚并释放登记。
		handler := &EmbyImportHandler{store: workflowA, libraryAccess: newTestImportedLibraryAccess()}
		compensationErr := handler.compensateFailedImport(ctx, completionA, created,
			&Failure{Code: "task_import_state_conflict", Message: "cancelled", Retryable: false})
		var failure *Failure
		if compensationErr == nil || !errors.As(compensationErr, &failure) || failure.Code != "task_import_state_conflict" {
			t.Fatalf("compensate(A) error = %#v, want task_import_state_conflict", compensationErr)
		}
		for _, path := range []string{destinationVideo, destinationSubtitle} {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("无采用时补偿未回滚 %q: %v", path, statErr)
			}
		}
		// 释放登记后 B 可以准入同一目标。
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
		if _, err := workflowB.PrepareImportDestination(ctx, domain.ImportCompletion{
			TaskID: taskB, ImportID: importB,
			DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
		}); err != nil {
			t.Fatalf("PrepareImportDestination(B) after release error = %v", err)
		}
	}
}

// 真实取消路径（RR1）：真实 ReviewTask/BeginImport/文件提交后取消任务，
// CompleteImport 永久失败；补偿必须回滚本次创建的文件（原 EM-01 常规缺陷）。
// 真实取消后 task.state='cancelled'、import 行仍 running（River 终态未写），
// 服务层不得因 running 无条件保留。
func TestCompensationRollsBackAfterRealCancellationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	taskA := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "-A")

	transactor := database.NewTransactor(pool)
	workflowA := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))

	baseName := "Adoption Show - S01E01 - Pilot"
	libraryRoot := t.TempDir()
	destinationVideo := filepath.Join(libraryRoot, "Adoption Show", "Season1", baseName+".mkv")
	destinationSubtitle := filepath.Join(libraryRoot, "Adoption Show", "Season1", baseName+".ass")

	videoContent := []byte("cancel-video-bytes")
	subtitleContent := []byte("[Script Info]\nDialogue: cancel\n")
	staging := t.TempDir()
	videoPath := filepath.Join(staging, "cancel.mkv")
	subtitlePath := filepath.Join(staging, "cancel.ass")
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

	if _, err := workflowA.ReviewTask(ctx, domain.ReviewTask{
		TaskID: taskA, ExpectedVersion: 1, Decision: domain.TaskApproved,
		IdempotencyKey: "cancel-a-review", ActorUserID: actorID,
	}); err != nil {
		t.Fatalf("ReviewTask(A) error = %v", err)
	}
	importA := latestImportID(t, ctx, pool, taskA)
	if _, err := workflowA.BeginImport(ctx, taskA, importA); err != nil {
		t.Fatalf("BeginImport(A) error = %v", err)
	}
	if _, err := workflowA.PrepareImportDestination(ctx, domain.ImportCompletion{
		TaskID: taskA, ImportID: importA,
		DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
	}); err != nil {
		t.Fatalf("PrepareImportDestination(A) error = %v", err)
	}
	created, pairErr := importArtifactPair(ctx, videoArtifact, subtitleArtifact, libraryRoot,
		destinationVideo, destinationSubtitle, uuid.New(), newTestImportedLibraryAccess())
	if pairErr != nil {
		t.Fatalf("importArtifactPair(A) error = %v", pairErr)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d, want 2", len(created))
	}

	// 真实取消语义：与 CancelEpisodeTaskIfActive 相同条件与字段。
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

	// 真实取消路径的 failure 分类与 Handle 一致：task_import_state_conflict 为
	// 不可重试（mediaStoreFailure 只包装cause，业务错误保留原分类）。
	classified := mediaStoreFailure("import", completeErrA).(*Failure)
	if classified.Retryable {
		t.Fatalf("永久状态冲突不应重试，实际 %#v", classified)
	}

	handler := &EmbyImportHandler{store: workflowA, libraryAccess: newTestImportedLibraryAccess()}
	compensationErr := handler.compensateFailedImport(ctx, domain.ImportCompletion{
		TaskID: taskA, ImportID: importA,
		DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
	}, created, classified)
	var failure *Failure
	if compensationErr == nil || !errors.As(compensationErr, &failure) || failure.Code != "task_import_state_conflict" {
		t.Fatalf("compensate() error = %#v, want task_import_state_conflict", compensationErr)
	}

	// 原 EM-01 常规缺陷断言：取消后库内不留孤儿文件。
	for _, path := range []string{destinationVideo, destinationSubtitle} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("取消后补偿未回滚 %q（原 EM-01 复现）: %v", path, statErr)
		}
	}
	assertFileContent(t, videoPath, videoContent)
}

// ACK 丢失恢复（RR2）：真实 CompleteImport 成功写入 succeeded，随后
// “连接故障”使 Handle 以为失败；补偿必须识别 Succeeded 并按成功返回 nil。
func TestCompensationReturnsSuccessAfterAcknowledgementLossIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	seriesID, profileID, actorID := uuid.New(), uuid.New(), uuid.New()
	seedAdoptionBase(t, ctx, pool, seriesID, profileID, actorID)
	taskA := seedAdoptionTask(t, ctx, pool, seriesID, profileID, actorID, "-A")

	transactor := database.NewTransactor(pool)
	workflowA := service.NewTaskWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &probeJobInserter{}))

	baseName := "Adoption Show - S01E01 - Pilot"
	libraryRoot := t.TempDir()
	destinationVideo := filepath.Join(libraryRoot, "Adoption Show", "Season1", baseName+".mkv")
	destinationSubtitle := filepath.Join(libraryRoot, "Adoption Show", "Season1", baseName+".ass")

	videoContent := []byte("ack-video-bytes")
	subtitleContent := []byte("[Script Info]\nDialogue: ack\n")
	staging := t.TempDir()
	videoPath := filepath.Join(staging, "ack.mkv")
	subtitlePath := filepath.Join(staging, "ack.ass")
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

	if _, err := workflowA.ReviewTask(ctx, domain.ReviewTask{
		TaskID: taskA, ExpectedVersion: 1, Decision: domain.TaskApproved,
		IdempotencyKey: "ack-a-review", ActorUserID: actorID,
	}); err != nil {
		t.Fatalf("ReviewTask(A) error = %v", err)
	}
	importA := latestImportID(t, ctx, pool, taskA)
	if _, err := workflowA.BeginImport(ctx, taskA, importA); err != nil {
		t.Fatalf("BeginImport(A) error = %v", err)
	}
	if _, err := workflowA.PrepareImportDestination(ctx, domain.ImportCompletion{
		TaskID: taskA, ImportID: importA,
		DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
	}); err != nil {
		t.Fatalf("PrepareImportDestination(A) error = %v", err)
	}
	created, pairErr := importArtifactPair(ctx, videoArtifact, subtitleArtifact, libraryRoot,
		destinationVideo, destinationSubtitle, uuid.New(), newTestImportedLibraryAccess())
	if pairErr != nil {
		t.Fatalf("importArtifactPair(A) error = %v", pairErr)
	}

	// 真实 CompleteImport 成功（succeeded 已提交），随后注入连接故障。
	if err := workflowA.CompleteImport(ctx, domain.ImportCompletion{
		TaskID: taskA, ImportID: importA, OperationID: latestImportOperationID(t, ctx, pool, taskA),
		DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
	}); err != nil {
		t.Fatalf("CompleteImport(A) error = %v", err)
	}
	injected := errors.New("connection reset after commit (injected)")

	handler := &EmbyImportHandler{store: workflowA, libraryAccess: newTestImportedLibraryAccess()}
	compensationErr := handler.compensateFailedImport(ctx, domain.ImportCompletion{
		TaskID: taskA, ImportID: importA,
		DestinationVideoPath: destinationVideo, DestinationSubtitlePath: destinationSubtitle,
	}, created, &Failure{Code: "media_storage_unavailable", Message: injected.Error(), Retryable: true, Cause: injected})
	if compensationErr != nil {
		t.Fatalf("ACK 丢失后补偿应按成功返回 nil，实际 %#v", compensationErr)
	}

	// 文件保持，导入记录确知成功。
	assertFileContent(t, destinationVideo, videoContent)
	assertFileContent(t, destinationSubtitle, subtitleContent)
	state, err := workflowA.ObserveImportRecovery(ctx, taskA, importA, destinationVideo, destinationSubtitle)
	if err != nil || !state.Succeeded || state.DestinationClaimed {
		// 自身成功引用不计入占用（观察排除自身），仅 Succeeded 判定。
		t.Fatalf("Observe(A) = %#v, %v, want succeeded without self-claim", state, err)
	}
}

// —— 真实业务入口 fixture（与 service 层 recovery fixture 同构，符合 schema 约束） ——

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
	acquisitionID, downloadID, sourceFileID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	videoArtifactID, subtitleArtifactID := uuid.New(), uuid.New()
	baseName := "Adoption Show - S01E01 - Pilot" + suffix
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, source_payload, created_by)
VALUES ($1, $2, 'manual', $3, '{}'::jsonb, $4)
`, acquisitionID, seriesID, "magnet:?xt=urn:btih:"+uuid.NewString(), actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, progress, save_path)
VALUES ($1, $2, 'materialized', 1, '/downloads/adoption-fixture')
`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Adoption.Show.S01E01.mkv', 1024, 'video', true, 1, 1)
`, sourceFileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, 'awaiting_review', 'video_ready', 'ass_ready')
`, taskID, acquisitionID, sourceFileID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_artifacts (id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $2, $3, $4, 'video', $5, '/staging/' || $5 || '-video.mkv', 'matroska', 10, decode(repeat('01', 32), 'hex'))
`, videoArtifactID, taskID, sourceFileID, profileID, baseName); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_artifacts (id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $2, $3, NULL, 'subtitle', $4, '/staging/' || $4 || '-subtitle.ass', 'ass', 10, decode(repeat('02', 32), 'hex'))
`, subtitleArtifactID, taskID, sourceFileID, baseName); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO artifact_sets (id, task_id, transcode_profile_id, basename, video_artifact_id, subtitle_artifact_id)
VALUES ($1, $2, $3, $4, $5, $6)
`, uuid.New(), taskID, profileID, baseName, videoArtifactID, subtitleArtifactID); err != nil {
		t.Fatal(err)
	}
	return taskID
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

func latestImportOperationID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) uuid.UUID {
	t.Helper()
	row := pool.QueryRow(ctx, `SELECT id FROM operations WHERE resource_type='episode_task' AND resource_id=$1 AND kind='emby.import' ORDER BY created_at DESC LIMIT 1`, taskID)
	var id pgtype.UUID
	if err := row.Scan(&id); err != nil {
		t.Fatalf("latest import operation: %v", err)
	}
	return repository.UUIDFromPG(id)
}

func checksumFor(content []byte) []byte {
	sum := sha256.Sum256(content)
	return sum[:]
}

// River 作业插入桩：本集成测试只验证数据库状态与文件系统，作业实际不执行。
type probeJobInserter struct{ nextID atomic.Int64 }

func (inserter *probeJobInserter) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	id := inserter.nextID.Add(1)
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: id}}, nil
}
