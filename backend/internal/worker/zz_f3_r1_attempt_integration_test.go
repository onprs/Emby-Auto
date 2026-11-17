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

func TestZZR1SnoozeReusesAttemptNumberAndOldCallbacksAreStale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	queries := db.New(pool)
	taskID := seedSubtitleGenerationTask(t, ctx, pool)
	workflow := service.NewMediaWorkflow(queries, transactor, nil)
	scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
	scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
		Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
		IdempotencyKey: "zz-r1-stale-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
		Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	lifecycle := repository.NewOperationLifecycle(queries, transactor)
	first, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-first")
	if err != nil {
		t.Fatalf("first StartAttempt: %v", err)
	}
	if first.SubtitleAttempt == nil {
		t.Fatal("first StartAttempt has no handle")
	}
	if err := lifecycle.SnoozeSubtitleAttempt(ctx, *first.SubtitleAttempt); err != nil {
		t.Fatalf("SnoozeSubtitleAttempt: %v", err)
	}
	second, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-second")
	if err != nil {
		t.Fatalf("second StartAttempt: %v", err)
	}
	if second.SubtitleAttempt == nil {
		t.Fatal("second StartAttempt has no handle")
	}
	if first.SubtitleAttempt.AttemptID == second.SubtitleAttempt.AttemptID {
		t.Fatal("snooze must create a new audit UUID even when the numeric attempt is reused")
	}
	if first.SubtitleAttempt.JobAttempt != second.SubtitleAttempt.JobAttempt || first.SubtitleAttempt.OperationAttemptCount != second.SubtitleAttempt.OperationAttemptCount {
		t.Fatalf("numeric attempt was not reused: first=%+v second=%+v", *first.SubtitleAttempt, *second.SubtitleAttempt)
	}
	if _, err := workflow.BeginSubtitleForOperation(ctx, *first.SubtitleAttempt, taskID); err == nil {
		t.Fatal("old handle must not begin the new attempt")
	}
	if _, err := workflow.CreateSubtitleVideoMatchScopeForOperation(ctx, *first.SubtitleAttempt, taskID, zzR1Candidates()); err == nil {
		t.Fatal("old handle must not create a scope for the new attempt")
	}
	if err := lifecycle.FailSubtitleAttempt(ctx, *first.SubtitleAttempt, "old_failure", "old callback", false); err != nil {
		t.Fatalf("old FailSubtitleAttempt should be a no-op: %v", err)
	}
	if err := lifecycle.CancelSubtitleAttempt(ctx, *first.SubtitleAttempt); err != nil {
		t.Fatalf("old CancelSubtitleAttempt should be a no-op: %v", err)
	}
	if err := lifecycle.SucceedSubtitleAttempt(ctx, *first.SubtitleAttempt); err != nil {
		t.Fatalf("old SucceedSubtitleAttempt should be a no-op: %v", err)
	}
	if err := lifecycle.SnoozeSubtitleAttempt(ctx, *first.SubtitleAttempt); err != nil {
		t.Fatalf("old SnoozeSubtitleAttempt should be a no-op: %v", err)
	}
	if ok, err := lifecycle.HeartbeatSubtitleAttempt(ctx, *first.SubtitleAttempt); err != nil || ok {
		t.Fatalf("old heartbeat = %v/%v, want false/nil", ok, err)
	}
	var status string
	var attemptCount int32
	if err := pool.QueryRow(ctx, `SELECT status, attempt_count FROM operations WHERE id = $1`, repository.UUIDToPG(scheduled.Operation.ID)).Scan(&status, &attemptCount); err != nil {
		t.Fatal(err)
	}
	if status != "running" || attemptCount != 1 {
		t.Fatalf("old callbacks changed operation: %s/%d", status, attemptCount)
	}
	if ok, err := lifecycle.HeartbeatSubtitleAttempt(ctx, *second.SubtitleAttempt); err != nil || !ok {
		t.Fatalf("new heartbeat = %v/%v, want true/nil", ok, err)
	}
	if err := lifecycle.SucceedSubtitleAttempt(ctx, *second.SubtitleAttempt); err != nil {
		t.Fatalf("new SucceedSubtitleAttempt: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, repository.UUIDToPG(scheduled.Operation.ID)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Fatalf("operation status = %s, want succeeded", status)
	}
	var oldAuditCount, newAuditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_attempts WHERE operation_id = $1 AND id = $2`, repository.UUIDToPG(scheduled.Operation.ID), repository.UUIDToPG(first.SubtitleAttempt.AttemptID)).Scan(&oldAuditCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_attempts WHERE operation_id = $1 AND id = $2 AND status = 'succeeded'`, repository.UUIDToPG(scheduled.Operation.ID), repository.UUIDToPG(second.SubtitleAttempt.AttemptID)).Scan(&newAuditCount); err != nil {
		t.Fatal(err)
	}
	if oldAuditCount != 0 || newAuditCount != 1 {
		t.Fatalf("audit rows old=%d new-succeeded=%d", oldAuditCount, newAuditCount)
	}
}
