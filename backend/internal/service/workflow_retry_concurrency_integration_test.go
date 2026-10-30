//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestConcurrentStoppedTaskRetryQueuesOneAttemptIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	queries := db.New(fixture.pool)
	tasks := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	commands := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, tasks)
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, "cancelled", "cancelled", "cancelled", "")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, key := range []string{"parallel-retry-one", "parallel-retry-two"} {
		go func(key string) {
			<-start
			_, _, err := commands.Retry(ctx, taskID, 1, key, fixture.actorID)
			results <- err
		}(key)
	}
	close(start)
	succeeded, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrStateConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicts != 1 || countOperationsForTask(t, fixture, taskID) != 2 {
		t.Fatalf("并发恢复重复启动: 成功 %d，冲突 %d，操作 %d", succeeded, conflicts, countOperationsForTask(t, fixture, taskID))
	}
}

func TestDeletionWinningTheLockPreventsConcurrentRetryIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	queries := db.New(fixture.pool)
	tasks := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	commands := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, tasks)
	acquisitionID, _, _, taskID := createTaskWithMediaStates(t, fixture, "cancelled", "cancelled", "ass_ready", "")
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := db.New(tx).LockAcquisitionForDeletion(ctx, repository.UUIDToPG(acquisitionID)); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, _, err := commands.Retry(ctx, taskID, 1, "retry-during-concurrent-delete", fixture.actorID)
		finished <- err
	}()
	waitForBlockedIntegrationQuery(t, ctx, fixture.pool, "LockAcquisitionForDeletion")
	if _, err := tx.Exec(ctx, `UPDATE acquisitions SET deletion_requested_at=now() WHERE id=$1`, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, ErrStateConflict) {
		t.Fatalf("已获得删除锁的记录仍被恢复: %v", err)
	}
	if countOperationsForTask(t, fixture, taskID) != 0 {
		t.Fatal("删除并发产生了额外操作")
	}
}

func TestStoppedImportResumesTheApprovedImportIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx := context.Background()
	queries := db.New(fixture.pool)
	tasks := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	commands := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, tasks)
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, "cancelled", "video_ready", "ass_ready", "")
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO reviews(id,task_id,decision,reviewed_by,idempotency_key,expected_task_version) VALUES(gen_random_uuid(),$1,'approved',$2,'stopped-import-review',1)`, taskID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	var importID string
	if err := fixture.pool.QueryRow(ctx, `INSERT INTO imports(id,task_id,status,attempt) VALUES(gen_random_uuid(),$1,'running',1) RETURNING id::text`, taskID).Scan(&importID); err != nil {
		t.Fatal(err)
	}
	view, operation, err := commands.Retry(ctx, taskID, 1, "retry-stopped-import", fixture.actorID)
	if err != nil || view.State != "import_queued" || view.Import == nil || view.Import.ID.String() != importID || view.Import.Status != "queued" || operation.Kind != riverqueue.KindEmbyImport {
		t.Fatalf("已审核入库没有从原阶段恢复: %#v / %#v / %v", view, operation, err)
	}
}
