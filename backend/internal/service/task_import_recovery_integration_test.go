//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

// 隔离数据库验证：导入确认失败后的补偿依赖的读取状态与“目标已被成功导入占用”判断。
func TestObserveImportRecoveryTracksImportLifecycleIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createReviewableTask(t, fixture)
	workflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	task, err := workflow.ReviewTask(ctx, domain.ReviewTask{
		TaskID: taskID, ExpectedVersion: 1, Decision: domain.TaskApproved,
		IdempotencyKey: "observe-import-recovery", ActorUserID: fixture.actorID,
	})
	if err != nil {
		t.Fatalf("ReviewTask() error = %v", err)
	}
	videoPath := "/library/Review Show - S01E01 - Pilot.mkv"
	subtitlePath := "/library/Review Show - S01E01 - Pilot.ass"

	state, err := workflow.ObserveImportRecovery(ctx, taskID, task.Import.ID, videoPath, subtitlePath)
	if err != nil {
		t.Fatalf("ObserveImportRecovery(queued) error = %v", err)
	}
	if state.Succeeded || state.DestinationClaimed || state.ImportState != "queued" {
		t.Fatalf("queued state = %#v", state)
	}

	if _, err := workflow.BeginImport(ctx, taskID, task.Import.ID); err != nil {
		t.Fatalf("BeginImport() error = %v", err)
	}
	state, err = workflow.ObserveImportRecovery(ctx, taskID, task.Import.ID, videoPath, subtitlePath)
	if err != nil {
		t.Fatalf("ObserveImportRecovery(running) error = %v", err)
	}
	if state.Succeeded || state.DestinationClaimed || state.TaskState != domain.TaskImporting || state.ImportState != "running" {
		t.Fatalf("running state = %#v", state)
	}

	if err := workflow.CompleteImport(ctx, domain.ImportCompletion{
		TaskID: taskID, ImportID: task.Import.ID, OperationID: task.Operations[0].ID,
		DestinationVideoPath: videoPath, DestinationSubtitlePath: subtitlePath,
	}); err != nil {
		t.Fatalf("CompleteImport() error = %v", err)
	}
	state, err = workflow.ObserveImportRecovery(ctx, taskID, task.Import.ID, videoPath, subtitlePath)
	if err != nil {
		t.Fatalf("ObserveImportRecovery(succeeded) error = %v", err)
	}
	if !state.Succeeded || !state.DestinationClaimed || state.TaskState != domain.TaskImported || state.ImportState != "succeeded" {
		t.Fatalf("succeeded state = %#v", state)
	}

	// 同一任务下的未知导入 ID 不会误报成功，但目标路径已被成功导入占用。
	state, err = workflow.ObserveImportRecovery(ctx, taskID, uuid.New(), videoPath, subtitlePath)
	if err != nil {
		t.Fatalf("ObserveImportRecovery(unknown import) error = %v", err)
	}
	if state.Succeeded || state.ImportState != "" || !state.DestinationClaimed {
		t.Fatalf("unknown import state = %#v", state)
	}

	// 任务已删除或不存在时仍能判断目标是否被其它成功导入占用。
	state, err = workflow.ObserveImportRecovery(ctx, uuid.New(), uuid.New(), videoPath, subtitlePath)
	if err != nil {
		t.Fatalf("ObserveImportRecovery(missing task) error = %v", err)
	}
	if state.Succeeded || state.TaskState != "" || state.ImportState != "" || !state.DestinationClaimed {
		t.Fatalf("missing task state = %#v", state)
	}

	state, err = workflow.ObserveImportRecovery(ctx, taskID, task.Import.ID, "/library/other.mkv", "/library/other.ass")
	if err != nil {
		t.Fatalf("ObserveImportRecovery(other paths) error = %v", err)
	}
	if state.DestinationClaimed {
		t.Fatalf("unclaimed paths reported as claimed: %#v", state)
	}
}
