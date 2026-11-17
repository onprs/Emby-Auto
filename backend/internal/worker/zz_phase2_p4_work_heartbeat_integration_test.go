//go:build integration

package worker

// P4：真实 PG + RepositoryLifecycle + Work 的心跳持久结果。DB heartbeat 错误
// 必须走可重试失败并保留 operation/attempt/task 状态；cancel_requested 导致的
// heartbeat false 必须持久 cancel operation/attempt/task，而不是普通失败。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func zzR1P4WorkJob(operationID uuid.UUID) *river.Job[appqueue.SubtitlePrepareArgs] {
	return &river.Job[appqueue.SubtitlePrepareArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args: appqueue.SubtitlePrepareArgs{OperationArgs: appqueue.OperationArgs{
			OperationID: operationID, TimeoutSecond: 30,
		}},
	}
}

func TestZZR1P4WorkHeartbeatPersistence(t *testing.T) {
	t.Run("db_error_retryable_persists_running", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-p4-hb-error-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatalf("schedule heartbeat operation: %v", err)
		}
		suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
		functionName := "zz_p4_hb_block_" + suffix
		triggerName := "zz_p4_hb_trigger_" + suffix
		if _, err := testutil.ExecFixture(ctx, pool, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id = '%s'::uuid AND NEW.status = OLD.status AND NEW.attempt_count = OLD.attempt_count THEN
        RETURN NULL;
    END IF;
    RETURN NEW;
END
$$`, functionName, scheduled.Operation.ID.String())); err != nil {
			t.Fatalf("create heartbeat block function: %v", err)
		}
		if _, err := testutil.ExecFixture(ctx, pool, fmt.Sprintf(`
CREATE TRIGGER %s BEFORE UPDATE ON operations FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)); err != nil {
			t.Fatalf("create heartbeat block trigger: %v", err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		handler := HandlerFunc(func(ctx context.Context, _ domain.Operation) error {
			<-ctx.Done()
			return ctx.Err()
		})
		worker := newOperationWorker[appqueue.SubtitlePrepareArgs](lifecycle, handler, 10*time.Millisecond, "zz-r1-p4-hb-error")
		err = worker.Work(ctx, zzR1P4WorkJob(scheduled.Operation.ID))
		var failure *Failure
		if !errors.As(err, &failure) || !failure.Retryable {
			t.Fatalf("heartbeat DB error Work() = %v, want retryable *Failure", err)
		}
		var jobCancel *river.JobCancelError
		if errors.As(err, &jobCancel) {
			t.Fatalf("heartbeat DB error was treated as cancellation: %v", err)
		}
		if !strings.Contains(err.Error(), "heartbeat") {
			t.Fatalf("heartbeat DB error lost cause: %v", err)
		}
		state := zzR1ReadOperation(t, ctx, pool, scheduled.Operation.ID)
		if state.Status != "queued" || state.AttemptCount != 1 || state.MaxAttempts != 3 {
			t.Fatalf("operation after heartbeat error = %s/%d/%d, want queued/1/3", state.Status, state.AttemptCount, state.MaxAttempts)
		}
		attemptStatus := zzR1Scalar[string](t, ctx, pool, `SELECT status FROM operation_attempts WHERE operation_id = $1 AND attempt = 1`, repository.UUIDToPG(scheduled.Operation.ID))
		if attemptStatus != "failed" {
			t.Fatalf("attempt status after heartbeat error = %s, want failed", attemptStatus)
		}
		task := zzR1ReadTask(t, ctx, pool, taskID)
		if task.State != string(domain.TaskProcessing) || task.SubtitleState != string(domain.SubtitleQueued) {
			t.Fatalf("task changed by heartbeat DB error = %s/%s", task.State, task.SubtitleState)
		}
		if events := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM events WHERE resource_id = $1 AND topic IN ('task.media_failed','task.media_cancelled')`, repository.UUIDToPG(taskID)); events != 0 {
			t.Fatalf("heartbeat DB error emitted terminal events = %d, want 0", events)
		}
	})

	t.Run("heartbeat_false_cancel_persists_task_cancel", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		workflow := service.NewMediaWorkflow(queries, transactor, scheduler)
		scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-p4-hb-cancel-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatalf("schedule cancel operation: %v", err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		started := make(chan struct{})
		handler := HandlerFunc(func(ctx context.Context, operation domain.Operation) error {
			if _, err := workflow.BeginSubtitleForOperation(ctx, *operation.SubtitleAttempt, operation.ResourceID); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
		worker := newOperationWorker[appqueue.SubtitlePrepareArgs](lifecycle, handler, 10*time.Millisecond, "zz-r1-p4-hb-cancel")
		done := make(chan error, 1)
		go func() { done <- worker.Work(ctx, zzR1P4WorkJob(scheduled.Operation.ID)) }()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("handler did not Begin subtitle")
		}
		if _, err := queries.RequestOperationCancellation(ctx, repository.UUIDToPG(scheduled.Operation.ID)); err != nil {
			t.Fatalf("request operation cancellation: %v", err)
		}
		var workErr error
		select {
		case workErr = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Work did not return after heartbeat false")
		}
		var jobCancel *river.JobCancelError
		if !errors.As(workErr, &jobCancel) || !errors.Is(workErr, ErrCancellationRequested) {
			t.Fatalf("heartbeat false Work() = %v, want *river.JobCancelError cancellation", workErr)
		}
		state := zzR1ReadOperation(t, ctx, pool, scheduled.Operation.ID)
		if state.Status != "cancelled" {
			t.Fatalf("operation after heartbeat false = %s, want cancelled", state.Status)
		}
		attemptStatus := zzR1Scalar[string](t, ctx, pool, `SELECT status FROM operation_attempts WHERE operation_id = $1 AND attempt = 1`, repository.UUIDToPG(scheduled.Operation.ID))
		if attemptStatus != "cancelled" {
			t.Fatalf("attempt after heartbeat false = %s, want cancelled", attemptStatus)
		}
		task := zzR1ReadTask(t, ctx, pool, taskID)
		if task.State != string(domain.TaskCancelled) || task.SubtitleState != string(domain.SubtitleCancelled) {
			t.Fatalf("task after heartbeat false = %s/%s, want cancelled/cancelled", task.State, task.SubtitleState)
		}
		mediaCancelled := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM events WHERE topic = 'task.media_cancelled' AND resource_id = $1`, repository.UUIDToPG(taskID))
		mediaFailed := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM events WHERE topic = 'task.media_failed' AND resource_id = $1`, repository.UUIDToPG(taskID))
		if mediaCancelled != 1 || mediaFailed != 0 {
			t.Fatalf("task events cancelled/failed = %d/%d, want 1/0", mediaCancelled, mediaFailed)
		}
	})
}
