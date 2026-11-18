//go:build integration

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/subtitlebinding"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func zzR1MissingOperation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	scheduler *service.OperationScheduler,
	taskID uuid.UUID,
	maxAttempts int,
	attemptCount int,
) (uuid.UUID, subtitlebinding.AttemptHandle) {
	t.Helper()
	scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
		Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
		IdempotencyKey: "zz-r1-missing-" + uuid.NewString(), MaxAttempts: maxAttempts, Timeout: 30 * time.Minute,
		Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("schedule missing operation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE operations SET status = 'running', attempt_count = $2, started_at = now(), heartbeat_at = now()
WHERE id = $1`, repository.UUIDToPG(scheduled.Operation.ID), attemptCount); err != nil {
		t.Fatalf("mark missing operation running: %v", err)
	}
	auditID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operation_attempts (id, operation_id, attempt, status, worker_id)
VALUES ($1, $2, $3, 'running', 'zz-r1-missing')`,
		repository.UUIDToPG(auditID), repository.UUIDToPG(scheduled.Operation.ID), attemptCount); err != nil {
		t.Fatalf("create missing audit: %v", err)
	}
	return scheduled.Operation.ID, subtitlebinding.AttemptHandle{
		OperationID:           scheduled.Operation.ID,
		JobAttempt:            attemptCount,
		AttemptID:             auditID,
		OperationAttemptCount: attemptCount,
		ResourceType:          "episode_task",
		ResourceID:            taskID,
	}
}

func zzR1ReadOperationStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, operationID uuid.UUID) (string, int32) {
	t.Helper()
	var status string
	var attempts int32
	if err := pool.QueryRow(ctx, `SELECT status, attempt_count FROM operations WHERE id = $1`, repository.UUIDToPG(operationID)).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	return status, attempts
}

func zzR1ReadTaskSubtitle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) (string, string) {
	t.Helper()
	var state, subtitle string
	if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&state, &subtitle); err != nil {
		t.Fatal(err)
	}
	return state, subtitle
}

func zzR1MediaFailedCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = 'task.media_failed' AND resource_id = $1`, repository.UUIDToPG(taskID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestZZR1MissingSubtitleBinding(t *testing.T) {
	t.Run("retry_within_budget_then_safe_terminal", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		operationID, handle := zzR1MissingOperation(t, ctx, pool, scheduler, taskID, 3, 1)
		if err := lifecycle.FailSubtitleAttempt(ctx, handle, "subtitle_binding_missing", "missing", false); err == nil {
			t.Fatal("missing binding at attempt 1 must request the bounded same-operation retry")
		}
		status, attempts := zzR1ReadOperationStatus(t, ctx, pool, operationID)
		if status != "running" || attempts != 1 {
			t.Fatalf("missing retry changed operation to %s/%d", status, attempts)
		}
		if state, subtitle := zzR1ReadTaskSubtitle(t, ctx, pool, taskID); state != "processing" || subtitle != "subtitle_queued" {
			t.Fatalf("missing retry changed task to %s/%s", state, subtitle)
		}
		next, err := lifecycle.StartAttempt(ctx, operationID, 2, "zz-r1-missing-retry")
		if err != nil || next.SubtitleAttempt == nil {
			t.Fatalf("retry StartAttempt = %+v/%v", next, err)
		}
		if err := lifecycle.FailSubtitleAttempt(ctx, *next.SubtitleAttempt, "subtitle_candidates_exhausted", "final", false); err != nil {
			t.Fatalf("final FailSubtitleAttempt: %v", err)
		}
		status, attempts = zzR1ReadOperationStatus(t, ctx, pool, operationID)
		if status != "failed" || attempts != 2 {
			t.Fatalf("operation after final = %s/%d, want failed/2", status, attempts)
		}
		if state, subtitle := zzR1ReadTaskSubtitle(t, ctx, pool, taskID); state != "failed" || subtitle != "failed" {
			t.Fatalf("task after final = %s/%s, want failed/failed", state, subtitle)
		}
		if count := zzR1MediaFailedCount(t, ctx, pool, taskID); count != 1 {
			t.Fatalf("task.media_failed count = %d, want 1", count)
		}
	})

	t.Run("max_without_formal_work_safe_task_terminal", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		operationID, handle := zzR1MissingOperation(t, ctx, pool, scheduler, taskID, 3, 3)
		if err := lifecycle.FailSubtitleAttempt(ctx, handle, "subtitle_binding_missing", "missing", false); err != nil {
			t.Fatalf("FailSubtitleAttempt: %v", err)
		}
		status, attempts := zzR1ReadOperationStatus(t, ctx, pool, operationID)
		if status != "failed" || attempts != 3 {
			t.Fatalf("operation = %s/%d, want failed/3", status, attempts)
		}
		if state, subtitle := zzR1ReadTaskSubtitle(t, ctx, pool, taskID); state != "failed" || subtitle != "failed" {
			t.Fatalf("task = %s/%s, want failed/failed", state, subtitle)
		}
		if count := zzR1MediaFailedCount(t, ctx, pool, taskID); count != 1 {
			t.Fatalf("task.media_failed count = %d, want 1", count)
		}
	})

	t.Run("max_with_queued_new_operation_protects", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		operationID, handle := zzR1MissingOperation(t, ctx, pool, scheduler, taskID, 3, 3)
		other, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-missing-other-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{"command": "retry"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := lifecycle.FailSubtitleAttempt(ctx, handle, "subtitle_binding_missing", "missing", false); err != nil {
			t.Fatalf("FailSubtitleAttempt: %v", err)
		}
		status, _ := zzR1ReadOperationStatus(t, ctx, pool, operationID)
		if status != "failed" {
			t.Fatalf("current operation = %s, want failed", status)
		}
		otherStatus, _ := zzR1ReadOperationStatus(t, ctx, pool, other.Operation.ID)
		if otherStatus != "queued" {
			t.Fatalf("other operation = %s, want queued", otherStatus)
		}
		if state, subtitle := zzR1ReadTaskSubtitle(t, ctx, pool, taskID); state != "processing" || subtitle != "subtitle_queued" {
			t.Fatalf("protected task = %s/%s, want processing/subtitle_queued", state, subtitle)
		}
		if count := zzR1MediaFailedCount(t, ctx, pool, taskID); count != 0 {
			t.Fatalf("protected task.media_failed count = %d, want 0", count)
		}
	})
}
