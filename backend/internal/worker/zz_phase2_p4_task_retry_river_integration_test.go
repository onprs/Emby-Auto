//go:build integration

package worker

// P4：真实 TaskCommandWorkflow.Retry 在 failed/subtitle failure_stage 任务上
// 创建 subtitle.prepare，并由与 Retry 同一实际 River client 执行到 ass_ready。
// video_ready 会使 CompleteArtifact 同 Tx 进入 finalizing 并排 media.finalize；
// 本测试用受控 snooze handler 明确暂停 finalize，不把缺 handler/异步失败当收敛。

import (
	"context"
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
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func TestZZR1P4TaskCommandRetryRunsRiverChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	t.Logf("pool_max_conns=%d", pool.Config().MaxConns)
	transactor := database.NewTransactor(pool)
	queries := db.New(pool)
	taskID := seedSubtitleGenerationTask(t, ctx, pool)
	actorID := uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`,
		actorID, "p4-retry-"+actorID.String()); err != nil {
		t.Fatalf("insert retry actor: %v", err)
	}
	if _, err := testutil.ExecFixture(ctx, pool, `
UPDATE episode_tasks
SET state = 'failed', video_state = 'video_ready', subtitle_state = 'failed',
    failure_stage = 'subtitle', error_code = 'subtitle_candidates_exhausted',
    error_message = 'fixture subtitle failure', version = version + 1
WHERE id = $1`, repository.UUIDToPG(taskID)); err != nil {
		t.Fatalf("prepare failed subtitle task: %v", err)
	}
	var expectedVersion int32
	if err := pool.QueryRow(ctx, `SELECT version FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&expectedVersion); err != nil {
		t.Fatalf("load retry task version: %v", err)
	}
	workerID := "zz-r1-p4-retry-" + uuid.NewString()
	lifecycle := repository.NewOperationLifecycle(queries, transactor)
	var mediaHandler *SubtitlePrepareHandler
	finalizeHandler := HandlerFunc(func(context.Context, domain.Operation) error {
		return river.JobSnooze(time.Hour)
	})
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		ID: workerID, Logger: zzR1Logger(),
		Workers: NewWorkers(lifecycle, map[string]Handler{
			appqueue.KindSubtitlePrepare: HandlerFunc(func(ctx context.Context, operation domain.Operation) error {
				return mediaHandler.Handle(ctx, operation)
			}),
			appqueue.KindMediaFinalize: finalizeHandler,
		}, time.Second, workerID),
		RetryPolicy: zzR1Retry{}, FetchCooldown: 10 * time.Millisecond,
		FetchPollInterval: 50 * time.Millisecond, JobTimeout: 2 * time.Minute,
		Queues: map[string]river.QueueConfig{appqueue.QueueGeneral: {MaxWorkers: 1}},
	})
	if err != nil {
		t.Fatalf("new retry River client: %v", err)
	}
	scheduler := service.NewOperationScheduler(transactor, riverClient)
	workflow := service.NewMediaWorkflow(queries, transactor, scheduler)
	tools := &mediaToolsStub{
		probe:     func(string) (domain.MediaProbe, error) { return zzR1Probe() },
		runOutput: subtitleContentFixture("你好世界"),
	}
	mediaHandler = NewSubtitlePrepareHandler(mediaTestConfiguration(t.TempDir()), tools, workflow)
	taskWorkflow := service.NewTaskWorkflow(queries, transactor, scheduler)
	commands := service.NewTaskCommandWorkflow(queries, transactor, scheduler, taskWorkflow)
	retryKey := "p4-retry-" + uuid.NewString()
	retried, operation, err := commands.Retry(ctx, taskID, expectedVersion, retryKey, actorID)
	if err != nil {
		t.Fatalf("TaskCommandWorkflow.Retry: %v", err)
	}
	if operation.Kind != appqueue.KindSubtitlePrepare || operation.Status != "queued" {
		t.Fatalf("retry operation = %s/%s, want subtitle.prepare/queued", operation.Kind, operation.Status)
	}
	if retried.SubtitleState != domain.SubtitleQueued || retried.State != domain.TaskProcessing {
		t.Fatalf("retry task = %s/%s, want processing/subtitle_queued", retried.State, retried.SubtitleState)
	}
	_, replayOperation, err := commands.Retry(ctx, taskID, expectedVersion, retryKey, actorID)
	if err != nil {
		t.Fatalf("TaskCommandWorkflow.Retry replay: %v", err)
	}
	if replayOperation.ID != operation.ID {
		t.Fatalf("retry replay operation = %s, want %s", replayOperation.ID, operation.ID)
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	if err := riverClient.Start(runCtx); err != nil {
		t.Fatalf("start retry River: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer stopCancel()
		_ = riverClient.Stop(stopCtx)
		runCancel()
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		state := zzR1ReadOperation(t, ctx, pool, operation.ID)
		task := zzR1ReadTask(t, ctx, pool, taskID)
		var job zzR1JobState
		if state.RiverJobID != nil {
			job = zzR1ReadJob(t, ctx, pool, *state.RiverJobID)
		}
		if state.Status == "succeeded" && job.State == "completed" && task.SubtitleState == string(domain.SubtitleASSReady) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retry child did not complete: operation=%s/%d/%d job=%s/%d/%d task=%s/%s",
				state.Status, state.AttemptCount, state.MaxAttempts, job.State, job.Attempt, job.Max, task.State, task.SubtitleState)
		}
		time.Sleep(50 * time.Millisecond)
	}
	state := zzR1ReadOperation(t, ctx, pool, operation.ID)
	if state.Status != "succeeded" || state.AttemptCount != 1 || state.MaxAttempts != 3 || state.RiverJobID == nil {
		t.Fatalf("retry child operation = %s/%d/%d job=%v, want succeeded/1/3", state.Status, state.AttemptCount, state.MaxAttempts, state.RiverJobID)
	}
	job := zzR1ReadJob(t, ctx, pool, *state.RiverJobID)
	if job.State != "completed" || job.Attempt != 1 || job.Max != 3 {
		t.Fatalf("retry child job = %s/%d/%d, want completed/1/3", job.State, job.Attempt, job.Max)
	}
	task := zzR1ReadTask(t, ctx, pool, taskID)
	if task.SubtitleState != string(domain.SubtitleASSReady) || task.State != string(domain.TaskFinalizing) {
		t.Fatalf("retry task after River child = %s/%s, want finalizing/ass_ready", task.State, task.SubtitleState)
	}
	artifactCount := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM media_artifacts WHERE task_id = $1 AND kind = 'subtitle'`, repository.UUIDToPG(taskID))
	if artifactCount != 1 {
		t.Fatalf("retry child artifacts = %d, want 1", artifactCount)
	}
	retryEvents := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM events WHERE resource_type = 'episode_task' AND resource_id = $1 AND topic = 'task.retry_requested'`, repository.UUIDToPG(taskID))
	if retryEvents != 1 {
		t.Fatalf("task.retry_requested events = %d, want 1", retryEvents)
	}
	subtitleOperations := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'subtitle.prepare'`, repository.UUIDToPG(taskID))
	finalizeOperations := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'media.finalize'`, repository.UUIDToPG(taskID))
	if subtitleOperations != 1 || finalizeOperations != 1 {
		t.Fatalf("retry operations subtitle/finalize = %d/%d, want 1/1", subtitleOperations, finalizeOperations)
	}
	var finalizeStatus, finalizeState string
	var finalizeJobID int64
	var finalizeScheduledAt time.Time
	finalizeDeadline := time.Now().Add(5 * time.Second)
	for {
		if err := pool.QueryRow(ctx, `
SELECT operation.status, operation.river_job_id, job.state, job.scheduled_at
FROM operations AS operation
JOIN river_job AS job ON job.id = operation.river_job_id
WHERE operation.resource_type = 'episode_task' AND operation.resource_id = $1 AND operation.kind = 'media.finalize'
ORDER BY operation.created_at DESC LIMIT 1`, repository.UUIDToPG(taskID)).Scan(&finalizeStatus, &finalizeJobID, &finalizeState, &finalizeScheduledAt); err != nil {
			t.Fatalf("load finalize operation: %v", err)
		}
		if finalizeStatus == "queued" && finalizeState == "scheduled" && finalizeScheduledAt.After(time.Now()) {
			break
		}
		if time.Now().After(finalizeDeadline) {
			t.Fatalf("finalize controlled pause = operation %s job %s scheduled_at %s, want queued/scheduled future", finalizeStatus, finalizeState, finalizeScheduledAt)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Logf("retry_pass subtitle_op=%s/%d/%d subtitle_job=%s/%d/%d task=%s/%s finalize_op=%s/%d finalize_job=%s finalize_future=%t artifacts=%d retry_events=%d",
		state.Status, state.AttemptCount, state.MaxAttempts, job.State, job.Attempt, job.Max,
		task.State, task.SubtitleState, finalizeStatus, finalizeJobID, finalizeState, finalizeScheduledAt.After(time.Now()), artifactCount, retryEvents)
}
