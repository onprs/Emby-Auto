//go:build integration

package worker

// P4：真实 AgentResolutionService + 本地 Provider + Apply，经实际 River
// executor 运行 child subtitle.prepare；旧 parent operation 在 child 最后失败
// 后成功，不得掩盖 child 的终态。成功 child 也必须真实推进到 ass_ready。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
)

func zzR1P4WriteSubmission(t *testing.T, writer http.ResponseWriter, taskID uuid.UUID) {
	proposal := domain.AgentSubtitleVideoMatchProposal{
		TaskID: taskID, Selected: domain.SubtitleCandidateSelection{CandidateID: "stream:2"},
		EvidenceCodes: []string{"scoped_candidate"}, Decision: "resolved",
	}
	raw, err := json.Marshal(proposal)
	if err != nil {
		t.Error(err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{
		"finish_reason": "tool_calls", "message": map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "call-submit", "type": "function",
				"function": map[string]any{"name": "submit_subtitle_video_match", "arguments": string(raw)},
			}},
		},
	}}})
}

func zzR1P4WaitChild(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID, parentID uuid.UUID) uuid.UUID {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rows, err := pool.Query(ctx, `
SELECT id FROM operations
WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'subtitle.prepare' AND id <> $2
ORDER BY id`, repository.UUIDToPG(taskID), repository.UUIDToPG(parentID))
		if err != nil {
			t.Fatalf("find River child operation: %v", err)
		}
		children := make([]uuid.UUID, 0, 2)
		for rows.Next() {
			var id pgtype.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				t.Fatalf("scan River child operation: %v", err)
			}
			children = append(children, repository.UUIDFromPG(id))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate River child operation: %v", err)
		}
		rows.Close()
		if len(children) > 1 {
			t.Fatalf("Agent Apply created %d child operations, want exactly 1", len(children))
		}
		if len(children) == 1 {
			return children[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("Agent Apply did not schedule a River child operation")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestZZR1P4ActualAgentApplyRiverChild(t *testing.T) {
	for _, mode := range []string{"child_fails_parent_succeeds", "child_succeeds_parent_succeeds"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			_, pool := testutil.NewMigratedPostgres(t)
			t.Logf("pool_max_conns=%d", pool.Config().MaxConns)
			transactor := database.NewTransactor(pool)
			queries := db.New(pool)
			taskID := seedSubtitleGenerationTask(t, ctx, pool)
			holdScheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
			workflow := service.NewMediaWorkflow(queries, transactor, holdScheduler)
			lifecycle := repository.NewOperationLifecycle(queries, transactor)
			calls := &atomic.Int32{}
			provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				zzR1P4WriteSubmission(t, writer, taskID)
			}))
			defer provider.Close()
			configuration := domain.Configuration{Version: 1}
			configuration.Settings.Agent = domain.DefaultAgentSettings()
			configuration.Settings.Agent.Enabled = true
			configuration.Settings.Agent.SubtitleVideoMatchMode = domain.AgentResolutionValidatedAuto
			configuration.Settings.Agent.BaseURL = provider.URL + "/v1"
			configuration.Settings.Agent.Model = "zz-r1-p4-model"
			configuration.Settings.Agent.RequestTimeoutSeconds = 60

			workerID := "zz-r1-p4-" + uuid.NewString()
			var agent *service.AgentResolutionService
			var handler *SubtitlePrepareHandler
			riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
				ID: workerID, Logger: zzR1Logger(),
				Workers: NewWorkers(lifecycle, map[string]Handler{
					appqueue.KindAgentResolve: HandlerFunc(func(ctx context.Context, operation domain.Operation) error {
						return agent.Run(ctx, operation)
					}),
					appqueue.KindSubtitlePrepare: HandlerFunc(func(ctx context.Context, operation domain.Operation) error {
						return handler.Handle(ctx, operation)
					}),
				}, time.Second, workerID),
				RetryPolicy: zzR1Retry{}, FetchCooldown: 10 * time.Millisecond,
				FetchPollInterval: 50 * time.Millisecond, JobTimeout: 2 * time.Minute,
				Queues: map[string]river.QueueConfig{
					appqueue.QueueGeneral: {MaxWorkers: 1},
					appqueue.QueueAgent:   {MaxWorkers: 1},
				},
			})
			if err != nil {
				t.Fatalf("new River client: %v", err)
			}
			riverScheduler := service.NewOperationScheduler(transactor, riverClient)
			agent = service.NewAgentResolutionService(queries, transactor, riverScheduler, subtitleGenerationConfiguration{configuration: configuration}, nil, nil)
			tools := &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) { return zzR1Probe() }}
			if mode == "child_succeeds_parent_succeeds" {
				tools.runOutput = subtitleContentFixture("你好世界")
			} else {
				tools.runErr = errors.New("zz r1 p4 child deterministic FFmpeg failure")
			}
			handler = NewSubtitlePrepareHandler(mediaTestConfiguration(t.TempDir()), tools, workflow, agent)

			scheduled, err := holdScheduler.Schedule(ctx, service.ScheduleOperationRequest{
				Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
				IdempotencyKey: "zz-r1-p4-parent-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
				Payload: map[string]any{},
			})
			if err != nil {
				t.Fatalf("schedule held parent: %v", err)
			}
			parent, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-p4-parent")
			if err != nil || parent.SubtitleAttempt == nil {
				t.Fatalf("start held parent: %+v/%v", parent, err)
			}
			if err := handler.Handle(ctx, parent); err != nil {
				t.Fatalf("held parent handler: %v", err)
			}
			runCtx, runCancel := context.WithCancel(context.Background())
			if err := riverClient.Start(runCtx); err != nil {
				t.Fatalf("start River: %v", err)
			}
			defer func() {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer stopCancel()
				_ = riverClient.Stop(stopCtx)
				runCancel()
			}()

			childID := zzR1P4WaitChild(t, ctx, pool, taskID, parent.ID)
			deadline := time.Now().Add(20 * time.Second)
			var child zzR1OperationState
			var job zzR1JobState
			for {
				child = zzR1ReadOperation(t, ctx, pool, childID)
				if child.RiverJobID != nil {
					job = zzR1ReadJob(t, ctx, pool, *child.RiverJobID)
				}
				if mode == "child_succeeds_parent_succeeds" && child.Status == "succeeded" && job.State == "completed" {
					break
				}
				if mode == "child_fails_parent_succeeds" && child.Status == "failed" && job.State == "discarded" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("[%s] child/job = %s/%d/%d, %s/%d/%d; want terminal", mode, child.Status, child.AttemptCount, child.MaxAttempts, job.State, job.Attempt, job.Max)
				}
				time.Sleep(50 * time.Millisecond)
			}
			if calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", calls.Load())
			}
			artifactCount := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM media_artifacts WHERE task_id = $1 AND kind = 'subtitle'`, repository.UUIDToPG(taskID))
			mediaFailed := zzR1Scalar[int](t, ctx, pool, `SELECT count(*) FROM events WHERE topic = 'task.media_failed' AND resource_id = $1`, repository.UUIDToPG(taskID))
			activeNow := zzR1Scalar[int](t, ctx, pool, `
SELECT count(*) FROM operations
WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'subtitle.prepare' AND status IN ('queued', 'running')`,
				repository.UUIDToPG(taskID))
			taskNow := zzR1ReadTask(t, ctx, pool, taskID)
			parentNow := zzR1ReadOperation(t, ctx, pool, parent.ID)
			t.Logf("[%s] child_op=%s/%d/%d child_job=%s/%d/%d parent_op=%s/%d/%d task=%s/%s/%s artifacts=%d media_failed=%d active_subtitle=%d",
				mode, child.Status, child.AttemptCount, child.MaxAttempts, job.State, job.Attempt, job.Max,
				parentNow.Status, parentNow.AttemptCount, parentNow.MaxAttempts,
				taskNow.State, taskNow.VideoState, taskNow.SubtitleState, artifactCount, mediaFailed, activeNow)
			if mode == "child_fails_parent_succeeds" {
				if child.AttemptCount != 5 || child.MaxAttempts != 5 {
					t.Fatalf("failed child operation budget = %d/%d, want 5/5", child.AttemptCount, child.MaxAttempts)
				}
				if job.Attempt != 5 || job.Max != 5 {
					t.Fatalf("failed child River job budget = %d/%d, want 5/5", job.Attempt, job.Max)
				}
				if taskNow.State != string(domain.TaskFailed) || taskNow.SubtitleState != string(domain.SubtitleFailed) {
					t.Fatalf("failed child task = %s/%s, want failed/failed", taskNow.State, taskNow.SubtitleState)
				}
				if mediaFailed != 1 {
					t.Fatalf("failed child media_failed = %d, want 1", mediaFailed)
				}
			} else {
				if child.AttemptCount != 1 || child.MaxAttempts != 5 {
					t.Fatalf("success child operation budget = %d/%d, want 1/5", child.AttemptCount, child.MaxAttempts)
				}
				if job.Attempt != 1 || job.Max != 5 {
					t.Fatalf("success child River job budget = %d/%d, want 1/5", job.Attempt, job.Max)
				}
				if taskNow.SubtitleState != string(domain.SubtitleASSReady) {
					t.Fatalf("success child subtitle = %s, want ass_ready", taskNow.SubtitleState)
				}
				if artifactCount != 1 {
					t.Fatalf("success child artifacts = %d, want 1", artifactCount)
				}
			}
			if err := lifecycle.SucceedSubtitleAttempt(ctx, *parent.SubtitleAttempt); err != nil {
				t.Fatalf("held parent succeed: %v", err)
			}
			parentState := zzR1ReadOperation(t, ctx, pool, parent.ID)
			if parentState.Status != "succeeded" {
				t.Fatalf("held parent status = %s, want succeeded", parentState.Status)
			}
			taskAfter := zzR1ReadTask(t, ctx, pool, taskID)
			if mode == "child_fails_parent_succeeds" {
				if taskAfter.State != string(domain.TaskFailed) || taskAfter.SubtitleState != string(domain.SubtitleFailed) {
					t.Fatalf("parent success masked child failure: %s/%s", taskAfter.State, taskAfter.SubtitleState)
				}
			} else if taskAfter.SubtitleState != string(domain.SubtitleASSReady) {
				t.Fatalf("parent success clobbered ready subtitle: %s", taskAfter.SubtitleState)
			}
			active := zzR1Scalar[int](t, ctx, pool, `
SELECT count(*) FROM operations
WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'subtitle.prepare' AND status IN ('queued', 'running')`,
				repository.UUIDToPG(taskID))
			if active != 0 {
				t.Fatalf("active subtitle operations after parent success = %d, want 0", active)
			}
			totalChildren := zzR1Scalar[int](t, ctx, pool, `
SELECT count(*) FROM operations
WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'subtitle.prepare' AND id <> $2`,
				repository.UUIDToPG(taskID), repository.UUIDToPG(parent.ID))
			if totalChildren != 1 {
				t.Fatalf("child operation count after parent success = %d, want exactly 1", totalChildren)
			}
		})
	}
}
