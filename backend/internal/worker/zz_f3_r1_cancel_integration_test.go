//go:build integration

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestZZR1CancelRespectsNewScopeAndWholeTaskFact(t *testing.T) {
	t.Run("direct_cancel_does_not_cancel_new_pending_content", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		workflow := service.NewMediaWorkflow(queries, transactor, nil)
		scopeID, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, zzR1Candidates())
		if err != nil {
			t.Fatal(err)
		}
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-cancel-pending-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		started, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-cancel-pending")
		if err != nil || started.SubtitleAttempt == nil {
			t.Fatalf("StartAttempt = %+v/%v", started, err)
		}
		if _, err := queries.ExpireSubtitleVideoMatchScope(ctx, repository.UUIDToPG(scopeID)); err != nil {
			t.Fatal(err)
		}
		reused, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, zzR1ExternalCandidates())
		if err != nil || reused != scopeID {
			t.Fatalf("external scope = %s/%v", reused, err)
		}
		if err := lifecycle.CancelSubtitleAttempt(ctx, *started.SubtitleAttempt); err != nil {
			t.Fatalf("CancelSubtitleAttempt: %v", err)
		}
		var taskState, subtitleState string
		if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState); err != nil {
			t.Fatal(err)
		}
		if taskState != "processing" || subtitleState != "subtitle_queued" {
			t.Fatalf("direct cancel changed task to %s/%s, want processing/subtitle_queued", taskState, subtitleState)
		}
		var cancelledEvents int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = 'task.media_cancelled' AND resource_id = $1`, repository.UUIDToPG(taskID)).Scan(&cancelledEvents); err != nil {
			t.Fatal(err)
		}
		if cancelledEvents != 0 {
			t.Fatalf("direct cancel wrote %d task.media_cancelled events", cancelledEvents)
		}
		candidateIDs := zzR1CandidateIDs(t, ctx, pool, scopeID)
		if len(candidateIDs) != 2 || candidateIDs[0] != "embedded-6" || candidateIDs[1] != "embedded-7" {
			t.Fatalf("new pending content changed: %v", candidateIDs)
		}
	})

	t.Run("whole_task_cancel_is_formal_and_media_cancel_does_not_rewrite_it", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		workflow := service.NewMediaWorkflow(queries, transactor, nil)
		scopeID, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, zzR1Candidates())
		if err != nil {
			t.Fatal(err)
		}
		_ = scopeID
		actorID := uuid.New()
		if _, err := testutil.ExecFixture(ctx, pool, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "zz-r1-cancel-"+actorID.String()); err != nil {
			t.Fatal(err)
		}
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-whole-cancel-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		started, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-whole-cancel")
		if err != nil || started.SubtitleAttempt == nil {
			t.Fatalf("StartAttempt = %+v/%v", started, err)
		}
		tasks := service.NewTaskWorkflow(queries, transactor, scheduler)
		task, err := tasks.GetTask(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		commands := service.NewTaskCommandWorkflow(queries, transactor, scheduler, tasks)
		if _, _, err := commands.Cancel(ctx, taskID, task.Version, "zz-r1-whole-cancel-command-"+uuid.NewString(), actorID); err != nil {
			t.Fatalf("TaskCommandWorkflow.Cancel: %v", err)
		}
		var taskState, subtitleState string
		if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState); err != nil {
			t.Fatal(err)
		}
		if taskState != "cancelled" || subtitleState != "cancelled" {
			t.Fatalf("whole-task cancel = %s/%s, want cancelled/cancelled", taskState, subtitleState)
		}
		if err := lifecycle.CancelSubtitleAttempt(ctx, *started.SubtitleAttempt); err != nil {
			t.Fatalf("CancelSubtitleAttempt after whole-task cancel: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState); err != nil {
			t.Fatal(err)
		}
		if taskState != "cancelled" || subtitleState != "cancelled" {
			t.Fatalf("media cancel rewrote whole-task cancel to %s/%s", taskState, subtitleState)
		}
		var operationStatus string
		if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, repository.UUIDToPG(scheduled.Operation.ID)).Scan(&operationStatus); err != nil {
			t.Fatal(err)
		}
		if operationStatus != "cancelled" {
			t.Fatalf("subtitle operation = %s, want cancelled", operationStatus)
		}
		var taskCancelOperations int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'task.cancel'`, repository.UUIDToPG(taskID)).Scan(&taskCancelOperations); err != nil {
			t.Fatal(err)
		}
		if taskCancelOperations != 1 {
			t.Fatalf("task.cancel operations = %d, want 1", taskCancelOperations)
		}
	})
}
