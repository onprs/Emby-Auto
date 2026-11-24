//go:build integration

package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

type zzR1Retry struct{}

func (zzR1Retry) NextRetry(*rivertype.JobRow) time.Time { return time.Now() }

func zzR1Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func zzR1Candidates() []domain.SubtitleMatchCandidate {
	return []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-2", Source: domain.SubtitleSourceEmbedded, StreamIndex: 2, Format: domain.SubtitleASS, Language: "chi", Title: "简日双语"},
		{CandidateID: "embedded-3", Source: domain.SubtitleSourceEmbedded, StreamIndex: 3, Format: domain.SubtitleASS, Language: "chi", Title: "繁日雙語"},
	}
}

func zzR1Probe() (domain.MediaProbe, error) {
	return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
		{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi", Title: "简日双语"},
		{Index: 3, Type: "subtitle", Codec: "ass", Language: "chi", Title: "繁日雙語"},
	}}, nil
}

type zzR1OperationState struct {
	Status       string
	AttemptCount int32
	MaxAttempts  int32
	StartedAt    *time.Time
	RiverJobID   *int64
}

type zzR1JobState struct {
	State   string
	Attempt int
	Max     int
}

type zzR1TaskState struct {
	State         string
	VideoState    string
	SubtitleState string
}

type zzR1ScopeState struct {
	Status     string
	OwnerCount int
	CreatedAt  time.Time
}

func zzR1ReadOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, operationID uuid.UUID) zzR1OperationState {
	t.Helper()
	var state zzR1OperationState
	if err := pool.QueryRow(ctx, `
SELECT status, attempt_count, max_attempts, started_at, river_job_id
FROM operations WHERE id = $1`, repository.UUIDToPG(operationID)).Scan(
		&state.Status, &state.AttemptCount, &state.MaxAttempts, &state.StartedAt, &state.RiverJobID,
	); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	return state
}

func zzR1ReadJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jobID int64) zzR1JobState {
	t.Helper()
	var state zzR1JobState
	if err := pool.QueryRow(ctx, `
SELECT state, attempt, max_attempts FROM river_job WHERE id = $1`, jobID).Scan(&state.State, &state.Attempt, &state.Max); err != nil {
		t.Fatalf("read river job: %v", err)
	}
	return state
}

func zzR1ReadTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) zzR1TaskState {
	t.Helper()
	var state zzR1TaskState
	if err := pool.QueryRow(ctx, `
SELECT state, video_state, subtitle_state FROM episode_tasks WHERE id = $1`,
		repository.UUIDToPG(taskID)).Scan(&state.State, &state.VideoState, &state.SubtitleState); err != nil {
		t.Fatalf("read task: %v", err)
	}
	return state
}

func zzR1ReadScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scopeID uuid.UUID) zzR1ScopeState {
	t.Helper()
	var state zzR1ScopeState
	if err := pool.QueryRow(ctx, `
SELECT status, created_at FROM subtitle_video_match_scopes WHERE id = $1`,
		repository.UUIDToPG(scopeID)).Scan(&state.Status, &state.CreatedAt); err != nil {
		t.Fatalf("read scope: %v", err)
	}
	owners, err := db.New(pool).ListSubtitleVideoMatchActiveOwners(ctx, repository.UUIDToPG(scopeID))
	if err != nil {
		t.Fatalf("read active owners: %v", err)
	}
	state.OwnerCount = len(owners)
	return state
}

func zzR1Scalar[T any](t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) T {
	t.Helper()
	var value T
	if err := pool.QueryRow(ctx, query, args...).Scan(&value); err != nil {
		t.Fatalf("scalar query failed: %v", err)
	}
	return value
}

func zzR1AssertTerminalOrHandoff(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID, scopeID, operationID uuid.UUID, operationStatus string, jobID int64, label string) {
	t.Helper()
	task := zzR1ReadTask(t, ctx, pool, taskID)
	scope := zzR1ReadScope(t, ctx, pool, scopeID)
	operation := zzR1ReadOperation(t, ctx, pool, operationID)
	job := zzR1JobState{}
	if jobID != 0 {
		job = zzR1ReadJob(t, ctx, pool, jobID)
	}
	activeSubtitleOperations := zzR1Scalar[int](t, ctx, pool, `
SELECT count(*) FROM operations
WHERE resource_type = 'episode_task' AND resource_id = $1
  AND kind = 'subtitle.prepare' AND status IN ('queued', 'running')`,
		repository.UUIDToPG(taskID))
	activeResolutions := zzR1Scalar[int](t, ctx, pool, `
SELECT count(*) FROM agent_resolutions
WHERE resource_id = $1 AND status IN ('queued', 'running')`,
		repository.UUIDToPG(scopeID))
	mediaFailedEvents := zzR1Scalar[int](t, ctx, pool, `
SELECT count(*) FROM events WHERE topic = 'task.media_failed' AND resource_id = $1`,
		repository.UUIDToPG(taskID))
	mediaCancelledEvents := zzR1Scalar[int](t, ctx, pool, `
SELECT count(*) FROM events WHERE topic = 'task.media_cancelled' AND resource_id = $1`,
		repository.UUIDToPG(taskID))
	t.Logf("[%s] operation=%s/%d/%d job=%s/%d/%d task=%s/%s/%s scope=%s owners=%d active_subtitle=%d active_resolutions=%d media_failed=%d media_cancelled=%d",
		label, operation.Status, operation.AttemptCount, operation.MaxAttempts, job.State, job.Attempt, job.Max,
		task.State, task.VideoState, task.SubtitleState, scope.Status, scope.OwnerCount, activeSubtitleOperations,
		activeResolutions, mediaFailedEvents, mediaCancelledEvents)
	if operation.Status != operationStatus {
		t.Fatalf("[%s] operation status = %s, want %s", label, operation.Status, operationStatus)
	}
	terminal := task.State == string(domain.TaskFailed) || task.State == string(domain.TaskCancelled) ||
		task.SubtitleState == string(domain.SubtitleFailed) || task.SubtitleState == string(domain.SubtitleCancelled)
	followUp := activeSubtitleOperations > 0 || activeResolutions > 0 || scope.OwnerCount > 0
	if !terminal && !followUp {
		t.Fatalf("[%s] BOUNDARY-REGRESSION: operation=%s but task stuck at %s/%s without current work",
			label, operation.Status, task.State, task.SubtitleState)
	}
	if label == "fail" && mediaFailedEvents == 0 {
		t.Fatalf("[%s] task failure has no task.media_failed event", label)
	}
	if label == "cancel" && mediaCancelledEvents == 0 {
		t.Fatalf("[%s] task cancellation has no task.media_cancelled event", label)
	}
}

type zzR1JobInserter struct{ nextID int64 }

func (inserter *zzR1JobInserter) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	inserter.nextID++
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: inserter.nextID}}, nil
}

func TestZZR1SubtitleTerminal(t *testing.T) {
	t.Run("fail_real_river_preexisting_pending_ownerless", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		t.Logf("pool_max_conns=%d", pool.Config().MaxConns)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		workflow := service.NewMediaWorkflow(queries, transactor, nil)
		scopeID, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, zzR1Candidates())
		if err != nil {
			t.Fatalf("pre-existing scope: %v", err)
		}
		tools := &mediaToolsStub{
			probe:  func(string) (domain.MediaProbe, error) { return zzR1Probe() },
			runErr: errors.New("zz r1 deterministic ffmpeg failure"),
		}
		agent := &subtitleMatchAgentStub{enabled: false}
		handler := NewSubtitlePrepareHandler(mediaTestConfiguration(t.TempDir()), tools, workflow, agent)
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		workerID := "zz-r1-" + uuid.NewString()
		riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
			ID:                workerID,
			Logger:            zzR1Logger(),
			Workers:           NewWorkers(lifecycle, map[string]Handler{appqueue.KindSubtitlePrepare: handler}, time.Second, workerID),
			RetryPolicy:       zzR1Retry{},
			FetchCooldown:     10 * time.Millisecond,
			FetchPollInterval: 50 * time.Millisecond,
			JobTimeout:        2 * time.Minute,
			Queues:            map[string]river.QueueConfig{appqueue.QueueGeneral: {MaxWorkers: 1}},
		})
		if err != nil {
			t.Fatalf("new river client: %v", err)
		}
		scheduler := service.NewOperationScheduler(transactor, riverClient)
		scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-fail-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatalf("schedule: %v", err)
		}
		runCtx, runCancel := context.WithCancel(context.Background())
		if err := riverClient.Start(runCtx); err != nil {
			t.Fatalf("start river: %v", err)
		}
		defer func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer stopCancel()
			_ = riverClient.Stop(stopCtx)
			runCancel()
		}()
		deadline := time.Now().Add(20 * time.Second)
		for {
			state := zzR1ReadOperation(t, ctx, pool, scheduled.Operation.ID)
			job := zzR1ReadJob(t, ctx, pool, scheduled.Operation.RiverJobID)
			if state.Status == "failed" && job.State == "discarded" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("operation/job did not reach failed/discarded: %+v/%+v", state, job)
			}
			time.Sleep(50 * time.Millisecond)
		}
		zzR1AssertTerminalOrHandoff(t, ctx, pool, taskID, scopeID, scheduled.Operation.ID, "failed", scheduled.Operation.RiverJobID, "fail")
		state := zzR1ReadOperation(t, ctx, pool, scheduled.Operation.ID)
		job := zzR1ReadJob(t, ctx, pool, scheduled.Operation.RiverJobID)
		if state.AttemptCount != 3 || state.MaxAttempts != 3 || job.Attempt != 3 || job.Max != 3 {
			t.Fatalf("operation/job budget = %d/%d, %d/%d; want 3/3, 3/3", state.AttemptCount, state.MaxAttempts, job.Attempt, job.Max)
		}
		failedAudits := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM operation_attempts WHERE operation_id = $1 AND status = 'failed'`, repository.UUIDToPG(scheduled.Operation.ID))
		if failedAudits != 3 {
			t.Fatalf("failed audit rows = %d, want 3", failedAudits)
		}
		task := zzR1ReadTask(t, ctx, pool, taskID)
		if task.State != "failed" || task.SubtitleState != "failed" {
			t.Fatalf("task = %s/%s, want failed/failed", task.State, task.SubtitleState)
		}
		activeSubtitle := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'subtitle.prepare' AND status IN ('queued','running')`, repository.UUIDToPG(taskID))
		if activeSubtitle != 0 {
			t.Fatalf("active subtitle operations = %d, want 0", activeSubtitle)
		}
		mediaFailed := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM events WHERE topic = 'task.media_failed' AND resource_id = $1`, repository.UUIDToPG(taskID))
		if mediaFailed != 1 {
			t.Fatalf("task.media_failed = %d, want exactly 1", mediaFailed)
		}
	})

	t.Run("cancel_direct_lifecycle_preexisting_pending_ownerless", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		t.Logf("pool_max_conns=%d", pool.Config().MaxConns)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		workflow := service.NewMediaWorkflow(queries, transactor, nil)
		scopeID, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, zzR1Candidates())
		if err != nil {
			t.Fatalf("pre-existing scope: %v", err)
		}
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-cancel-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatalf("schedule: %v", err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		operation, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-cancel")
		if err != nil {
			t.Fatalf("StartAttempt: %v", err)
		}
		if operation.SubtitleAttempt == nil {
			t.Fatal("StartAttempt did not capture the subtitle attempt handle")
		}
		if err := lifecycle.CancelSubtitleAttempt(ctx, *operation.SubtitleAttempt); err != nil {
			t.Fatalf("CancelSubtitleAttempt: %v", err)
		}
		zzR1AssertTerminalOrHandoff(t, ctx, pool, taskID, scopeID, scheduled.Operation.ID, "cancelled", 0, "cancel")
		state := zzR1ReadOperation(t, ctx, pool, scheduled.Operation.ID)
		if state.AttemptCount != 1 || state.MaxAttempts != 3 {
			t.Fatalf("cancel operation budget = %d/%d, want 1/3", state.AttemptCount, state.MaxAttempts)
		}
		task := zzR1ReadTask(t, ctx, pool, taskID)
		if task.State != "cancelled" || task.SubtitleState != "cancelled" {
			t.Fatalf("cancel task = %s/%s, want cancelled/cancelled", task.State, task.SubtitleState)
		}
		mediaCancelled := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM events WHERE topic = 'task.media_cancelled' AND resource_id = $1`, repository.UUIDToPG(taskID))
		if mediaCancelled != 1 {
			t.Fatalf("task.media_cancelled = %d, want exactly 1", mediaCancelled)
		}
		activeSubtitle := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'subtitle.prepare' AND status IN ('queued','running')`, repository.UUIDToPG(taskID))
		if activeSubtitle != 0 {
			t.Fatalf("active subtitle operations after cancel = %d, want 0", activeSubtitle)
		}
	})
}
