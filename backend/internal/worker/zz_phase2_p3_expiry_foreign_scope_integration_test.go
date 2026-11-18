//go:build integration

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestZZR1P3ExpiryForeignScopeNoWrite(t *testing.T) {
	t.Run("matching_own_scope_but_foreign_request", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		workflow := service.NewMediaWorkflow(queries, transactor, nil)
		taskA, taskB := seedSubtitleGenerationTask(t, ctx, pool), seedSubtitleGenerationTask(t, ctx, pool)
		scopeA, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskA, zzR1Candidates())
		if err != nil {
			t.Fatal(err)
		}
		scopeB, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskB, zzR1Candidates())
		if err != nil {
			t.Fatal(err)
		}
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		scheduledB, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskB,
			IdempotencyKey: "zz-r1-foreign-own-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		startedB, err := lifecycle.StartAttempt(ctx, scheduledB.Operation.ID, 1, "zz-r1-foreign-own")
		if err != nil || startedB.SubtitleAttempt == nil {
			t.Fatalf("StartAttempt B = %+v/%v", startedB, err)
		}
		stateA, err := workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskA)
		if err != nil {
			t.Fatal(err)
		}
		result, err := workflow.ExpireOwnerlessSubtitleVideoMatchScopeForOperation(ctx, *startedB.SubtitleAttempt, stateA.ScopeID, domain.SubtitleScopeObservation{
			TaskVersion: stateA.TaskVersion, TaskState: stateA.TaskState, SubtitleState: stateA.SubtitleState, ContentHash: stateA.ContentObservation,
		})
		if err != nil {
			t.Fatalf("foreign cleanup: %v", err)
		}
		if result.Outcome != domain.SubtitleScopeChanged {
			t.Fatalf("foreign cleanup outcome = %s, want scope_changed", result.Outcome)
		}
		assertScopePendingWithCandidates(t, ctx, pool, scopeA)
		assertScopePendingWithCandidates(t, ctx, pool, scopeB)
		assertSubtitleOperationBudgetRunning(t, ctx, pool, scheduledB.Operation.ID)
		assertTaskMediaEventsZero(t, ctx, pool, taskA, taskB)
	})

	t.Run("absent_current_task_foreign_request", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		workflow := service.NewMediaWorkflow(queries, transactor, nil)
		taskA, taskB := seedSubtitleGenerationTask(t, ctx, pool), seedSubtitleGenerationTask(t, ctx, pool)
		scopeA, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskA, zzR1Candidates())
		if err != nil {
			t.Fatal(err)
		}
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		scheduledB, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskB,
			IdempotencyKey: "zz-r1-foreign-absent-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		startedB, err := lifecycle.StartAttempt(ctx, scheduledB.Operation.ID, 1, "zz-r1-foreign-absent")
		if err != nil || startedB.SubtitleAttempt == nil {
			t.Fatalf("StartAttempt B = %+v/%v", startedB, err)
		}
		stateA, err := workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskA)
		if err != nil {
			t.Fatal(err)
		}
		result, err := workflow.ExpireOwnerlessSubtitleVideoMatchScopeForOperation(ctx, *startedB.SubtitleAttempt, stateA.ScopeID, domain.SubtitleScopeObservation{
			TaskVersion: stateA.TaskVersion, TaskState: stateA.TaskState, SubtitleState: stateA.SubtitleState, ContentHash: stateA.ContentObservation,
		})
		if err != nil {
			t.Fatalf("absent cleanup: %v", err)
		}
		if result.Outcome != domain.SubtitleScopeAbsent {
			t.Fatalf("absent cleanup outcome = %s, want absent", result.Outcome)
		}
		assertScopePendingWithCandidates(t, ctx, pool, scopeA)
		var scopeCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM subtitle_video_match_scopes WHERE task_id = $1`, repository.UUIDToPG(taskB)).Scan(&scopeCount); err != nil {
			t.Fatal(err)
		}
		if scopeCount != 0 {
			t.Fatalf("task B scope count = %d, want 0", scopeCount)
		}
		assertSubtitleOperationBudgetRunning(t, ctx, pool, scheduledB.Operation.ID)
		assertTaskMediaEventsZero(t, ctx, pool, taskA, taskB)
	})
}

func assertScopePendingWithCandidates(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scopeID uuid.UUID) {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM subtitle_video_match_scopes WHERE id = $1`, repository.UUIDToPG(scopeID)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("scope %s status = %s, want pending", scopeID, status)
	}
	candidateIDs := zzR1CandidateIDs(t, ctx, pool, scopeID)
	if len(candidateIDs) != 2 || candidateIDs[0] != "embedded-2" || candidateIDs[1] != "embedded-3" {
		t.Fatalf("scope %s candidates = %v, want embedded-2/3", scopeID, candidateIDs)
	}
}

func assertSubtitleOperationBudgetRunning(t *testing.T, ctx context.Context, pool *pgxpool.Pool, operationID uuid.UUID) {
	t.Helper()
	var status string
	var attempts int32
	if err := pool.QueryRow(ctx, `SELECT status, attempt_count FROM operations WHERE id = $1`, repository.UUIDToPG(operationID)).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "running" || attempts != 1 {
		t.Fatalf("operation = %s/%d, want running/1", status, attempts)
	}
}

func assertTaskMediaEventsZero(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskIDs ...uuid.UUID) {
	t.Helper()
	for _, taskID := range taskIDs {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE resource_id = $1 AND topic IN ('task.media_failed','task.media_cancelled')`, repository.UUIDToPG(taskID)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("task %s media events = %d, want 0", taskID, count)
		}
	}
}
