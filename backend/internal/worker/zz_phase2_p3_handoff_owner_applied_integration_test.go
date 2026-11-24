//go:build integration

package worker

// 003d：合法新 owner/applied 必须由真实 Handler cleanup 交接；multiple/changed
// 保持结构化冲突且零写入。正向交接后由真实 AgentResolutionService + 合成
// Provider + Apply 排出的新字幕工作必须实际推进到 ass_ready。

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
)

func zzR1P3Require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func zzR1P3WriteSubmission(t *testing.T, writer http.ResponseWriter, taskID uuid.UUID) {
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

type zzR1P3Producer struct {
	agent     *service.AgentResolutionService
	lifecycle *repository.OperationLifecycle
	queries   *db.Queries
	workflow  *service.MediaWorkflow
	pool      *pgxpool.Pool
	taskID    uuid.UUID
	mode      string
	created   service.AgentResolutionCommandResult
}

func (*zzR1P3Producer) CapabilityEnabled(context.Context, domain.AgentCapability) (bool, error) {
	return true, nil
}

func (producer *zzR1P3Producer) CreateAutomatic(ctx context.Context, input service.AutomaticAgentResolutionRequest) (service.AgentResolutionCommandResult, error) {
	if producer.mode == "changed" {
		_, err := producer.queries.ExpireSubtitleVideoMatchScope(ctx, repository.UUIDToPG(input.ResourceID))
		if err != nil {
			return service.AgentResolutionCommandResult{}, err
		}
		_, err = producer.workflow.CreateSubtitleVideoMatchScope(ctx, producer.taskID, zzR1ExternalCandidates())
		if err != nil {
			return service.AgentResolutionCommandResult{}, err
		}
		return service.AgentResolutionCommandResult{}, service.NewError("agent_disabled", "synthetic disabled after changed scope", service.ErrStateConflict, nil)
	}
	created, err := producer.agent.CreateAutomatic(ctx, input)
	if err != nil {
		return service.AgentResolutionCommandResult{}, err
	}
	producer.created = created
	switch producer.mode {
	case "applied":
		if _, err := producer.lifecycle.StartAttempt(ctx, created.Operation.ID, 1, "zz-r1-p3-producer"); err != nil {
			return service.AgentResolutionCommandResult{}, err
		}
		if err := producer.agent.Run(ctx, created.Operation); err != nil {
			return service.AgentResolutionCommandResult{}, err
		}
	case "multiple":
		if err := producer.insertSecondOwner(ctx, input.ResourceID); err != nil {
			return service.AgentResolutionCommandResult{}, err
		}
	}
	return created, service.NewError("agent_disabled", "synthetic disabled after real producer", service.ErrStateConflict, nil)
}

func (*zzR1P3Producer) RetryAutomatic(context.Context, uuid.UUID, int) (service.AgentResolutionCommandResult, error) {
	return service.AgentResolutionCommandResult{}, errors.New("unexpected automatic retry")
}

func (producer *zzR1P3Producer) insertSecondOwner(ctx context.Context, scopeID uuid.UUID) error {
	operationID, resolutionID := uuid.New(), uuid.New()
	_, err := testutil.ExecFixture(ctx, producer.pool, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count, timeout_seconds, started_at, heartbeat_at)
VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'queued', 3, 0, 60, now(), now());
INSERT INTO agent_resolutions (
    id, operation_id, capability, resource_type, resource_id, trigger, status,
    input_fingerprint, configuration_version, protocol, provider_origin, model,
    prompt_version, toolset_version
) VALUES ($2, $1, 'subtitle_video_match', 'episode_task', $4, 'automatic', 'queued',
    $5, 1, 'openai_chat_completions', 'https://agent.example', 'test-model',
    'subtitle-video-match-v1', 'agent-tools-v1');
`, operationID, resolutionID, "zz-r1-p3-second-"+resolutionID.String(), scopeID, make([]byte, 32))
	return err
}

type zzR1P3Fixture struct {
	ctx          context.Context
	pool         *pgxpool.Pool
	workflow     *service.MediaWorkflow
	lifecycle    *repository.OperationLifecycle
	agent        *service.AgentResolutionService
	producer     *zzR1P3Producer
	calls        *atomic.Int32
	handler      *SubtitlePrepareHandler
	taskID       uuid.UUID
	oldOperation domain.Operation
}

func newZZR1P3Fixture(t *testing.T, mode string) *zzR1P3Fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	t.Logf("pool_max_conns=%d", pool.Config().MaxConns)
	transactor := database.NewTransactor(pool)
	queries := db.New(pool)
	taskID := seedSubtitleGenerationTask(t, ctx, pool)
	scheduler := service.NewOperationScheduler(transactor, &subtitleGenerationJobInserter{})
	workflow := service.NewMediaWorkflow(queries, transactor, scheduler)
	lifecycle := repository.NewOperationLifecycle(queries, transactor)
	scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
		Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
		IdempotencyKey: "zz-r1-p3-old-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
		Payload: map[string]any{},
	})
	zzR1P3Require(t, err)
	started, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-p3-old")
	if err != nil || started.SubtitleAttempt == nil {
		t.Fatalf("start old subtitle.prepare: %+v/%v", started, err)
	}
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		zzR1P3WriteSubmission(t, writer, taskID)
	}))
	t.Cleanup(server.Close)
	configuration := domain.Configuration{Version: 1}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.SubtitleVideoMatchMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.BaseURL = server.URL + "/v1"
	configuration.Settings.Agent.Model = "zz-r1-p3-model"
	configuration.Settings.Agent.RequestTimeoutSeconds = 60
	agent := service.NewAgentResolutionService(queries, transactor, scheduler, subtitleGenerationConfiguration{configuration: configuration}, nil, nil)
	producer := &zzR1P3Producer{agent: agent, lifecycle: lifecycle, queries: queries, workflow: workflow, pool: pool, taskID: taskID, mode: mode}
	tools := &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) { return zzR1Probe() }}
	handler := NewSubtitlePrepareHandler(mediaTestConfiguration(t.TempDir()), tools, workflow, producer)
	return &zzR1P3Fixture{ctx: ctx, pool: pool, workflow: workflow, lifecycle: lifecycle, agent: agent, producer: producer, calls: calls, handler: handler, taskID: taskID, oldOperation: started}
}

func zzR1P3State(t *testing.T, fixture *zzR1P3Fixture) domain.SubtitleVideoMatchSchedulingState {
	t.Helper()
	state, err := fixture.workflow.GetSubtitleVideoMatchSchedulingState(fixture.ctx, fixture.taskID)
	zzR1P3Require(t, err)
	return state
}

func zzR1P3Scope(t *testing.T, fixture *zzR1P3Fixture, scopeID uuid.UUID) (string, *string) {
	t.Helper()
	var status string
	var selected *string
	err := fixture.pool.QueryRow(fixture.ctx, `SELECT status, selected_candidate_id FROM subtitle_video_match_scopes WHERE id = $1`, repository.UUIDToPG(scopeID)).Scan(&status, &selected)
	zzR1P3Require(t, err)
	return status, selected
}

func zzR1P3MediaEvents(t *testing.T, fixture *zzR1P3Fixture) int {
	t.Helper()
	return zzR1Scalar[int](t, fixture.ctx, fixture.pool, `SELECT count(*) FROM events WHERE resource_id = $1 AND topic IN ('task.media_failed', 'task.media_cancelled')`, repository.UUIDToPG(fixture.taskID))
}

func zzR1P3NewSubtitleOperation(t *testing.T, fixture *zzR1P3Fixture, oldOperationID uuid.UUID) uuid.UUID {
	t.Helper()
	var id pgtype.UUID
	err := fixture.pool.QueryRow(fixture.ctx, `
SELECT id FROM operations
WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'subtitle.prepare'
  AND id <> $2 AND status IN ('queued', 'running')
ORDER BY created_at DESC LIMIT 1`, repository.UUIDToPG(fixture.taskID), repository.UUIDToPG(oldOperationID)).Scan(&id)
	zzR1P3Require(t, err)
	return repository.UUIDFromPG(id)
}

func TestZZR1P3HandoffOwnerAndAppliedRealHandler(t *testing.T) {
	for _, mode := range []string{"owner", "applied"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newZZR1P3Fixture(t, mode)
			zzR1P3Require(t, fixture.handler.Handle(fixture.ctx, fixture.oldOperation))
			created := fixture.producer.created
			if created.Resolution.ID == uuid.Nil || created.Operation.ID == uuid.Nil {
				t.Fatal("real producer did not create an owner operation")
			}
			state := zzR1P3State(t, fixture)
			scopeStatus, selected := zzR1P3Scope(t, fixture, state.ScopeID)
			owners := zzR1ReadScope(t, fixture.ctx, fixture.pool, state.ScopeID).OwnerCount
			candidates := zzR1CandidateIDs(t, fixture.ctx, fixture.pool, state.ScopeID)
			if len(candidates) != 2 || candidates[0] != "stream:2" || candidates[1] != "stream:3" {
				t.Fatalf("handler changed candidates: %v", candidates)
			}
			if zzR1P3MediaEvents(t, fixture) != 0 {
				t.Fatal("handoff emitted a task media terminal event")
			}
			oldState := zzR1ReadOperation(t, fixture.ctx, fixture.pool, fixture.oldOperation.ID)
			if oldState.Status != "running" || oldState.AttemptCount != 1 {
				t.Fatalf("old operation = %s/%d, want running/1", oldState.Status, oldState.AttemptCount)
			}
			phase := zzR1Scalar[string](t, fixture.ctx, fixture.pool, `SELECT phase FROM operation_subtitle_bindings WHERE operation_id = $1`, repository.UUIDToPG(fixture.oldOperation.ID))
			if phase != "scope" {
				t.Fatalf("old binding phase = %s, want scope", phase)
			}
			if mode == "owner" {
				if scopeStatus != "pending" || selected != nil || owners != 1 {
					t.Fatalf("owner handoff = %s/%v/owners%d, want pending/nil/1", scopeStatus, selected, owners)
				}
				if fixture.calls.Load() != 0 {
					t.Fatalf("provider calls = %d, want 0 before apply", fixture.calls.Load())
				}
				_, err := fixture.lifecycle.StartAttempt(fixture.ctx, created.Operation.ID, 1, "zz-r1-p3-producer")
				zzR1P3Require(t, err)
				zzR1P3Require(t, fixture.agent.Run(fixture.ctx, created.Operation))
			} else {
				if scopeStatus != "applied" || selected == nil || *selected != "stream:2" || owners != 0 {
					t.Fatalf("applied handoff = %s/%v/owners%d, want applied/stream:2/0", scopeStatus, selected, owners)
				}
				if fixture.calls.Load() != 1 {
					t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
				}
			}
			appliedState := zzR1P3State(t, fixture)
			appliedStatus, appliedSelected := zzR1P3Scope(t, fixture, appliedState.ScopeID)
			if appliedStatus != "applied" || appliedSelected == nil || *appliedSelected != "stream:2" {
				t.Fatalf("producer apply = %s/%v, want applied/stream:2", appliedStatus, appliedSelected)
			}
			resolution, err := fixture.agent.Get(fixture.ctx, created.Resolution.ID)
			zzR1P3Require(t, err)
			if resolution.Status != domain.AgentResolutionApplied {
				t.Fatalf("producer resolution = %s, want applied", resolution.Status)
			}
			newOperationID := zzR1P3NewSubtitleOperation(t, fixture, fixture.oldOperation.ID)
			newStarted, err := fixture.lifecycle.StartAttempt(fixture.ctx, newOperationID, 1, "zz-r1-p3-new")
			if err != nil || newStarted.SubtitleAttempt == nil {
				t.Fatalf("start new subtitle.prepare: %+v/%v", newStarted, err)
			}
			successTools := &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) { return zzR1Probe() }, runOutput: subtitleContentFixture("你好世界")}
			successHandler := NewSubtitlePrepareHandler(mediaTestConfiguration(t.TempDir()), successTools, fixture.workflow, fixture.agent)
			zzR1P3Require(t, successHandler.Handle(fixture.ctx, newStarted))
			zzR1P3Require(t, fixture.lifecycle.SucceedSubtitleAttempt(fixture.ctx, *newStarted.SubtitleAttempt))
			task := zzR1ReadTask(t, fixture.ctx, fixture.pool, fixture.taskID)
			if task.SubtitleState != string(domain.SubtitleASSReady) {
				t.Fatalf("task subtitle after producer work = %s, want ass_ready", task.SubtitleState)
			}
			artifacts := zzR1Scalar[int](t, fixture.ctx, fixture.pool, `SELECT count(*) FROM media_artifacts WHERE task_id = $1 AND kind = 'subtitle'`, repository.UUIDToPG(fixture.taskID))
			if artifacts != 1 {
				t.Fatalf("subtitle artifacts = %d, want 1", artifacts)
			}
			zzR1P3Require(t, fixture.lifecycle.SucceedSubtitleAttempt(fixture.ctx, *fixture.oldOperation.SubtitleAttempt))
			task = zzR1ReadTask(t, fixture.ctx, fixture.pool, fixture.taskID)
			if task.SubtitleState != string(domain.SubtitleASSReady) {
				t.Fatalf("old success clobbered subtitle = %s, want ass_ready", task.SubtitleState)
			}
			oldState = zzR1ReadOperation(t, fixture.ctx, fixture.pool, fixture.oldOperation.ID)
			if oldState.Status != "succeeded" {
				t.Fatalf("old operation after handoff = %s, want succeeded", oldState.Status)
			}
		})
	}
}

func TestZZR1P3MultipleAndChangedScopeOutcomes(t *testing.T) {
	for _, mode := range []string{"multiple", "changed"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newZZR1P3Fixture(t, mode)
			err := fixture.handler.Handle(fixture.ctx, fixture.oldOperation)
			var failure *Failure
			if !errors.As(err, &failure) || !failure.Retryable {
				t.Fatalf("handler = %v, want retryable conflict", err)
			}
			state := zzR1P3State(t, fixture)
			result, err := fixture.workflow.ExpireOwnerlessSubtitleVideoMatchScopeForOperation(fixture.ctx, *fixture.oldOperation.SubtitleAttempt, state.ScopeID, domain.SubtitleScopeObservation{
				TaskVersion: state.TaskVersion, TaskState: state.TaskState, SubtitleState: state.SubtitleState, ContentHash: state.ContentObservation,
			})
			zzR1P3Require(t, err)
			want := domain.SubtitleScopeChanged
			if mode == "multiple" {
				want = domain.SubtitleScopeMultipleOwners
			}
			if result.Outcome != want {
				t.Fatalf("scope outcome = %s, want %s", result.Outcome, want)
			}
			scopeStatus, selected := zzR1P3Scope(t, fixture, state.ScopeID)
			if scopeStatus != "pending" || selected != nil {
				t.Fatalf("scope after conflict = %s/%v, want pending/nil", scopeStatus, selected)
			}
			candidates := zzR1CandidateIDs(t, fixture.ctx, fixture.pool, state.ScopeID)
			wantCandidates := []string{"stream:2", "stream:3"}
			if mode == "changed" {
				wantCandidates = []string{"embedded-6", "embedded-7"}
			}
			if len(candidates) != 2 || candidates[0] != wantCandidates[0] || candidates[1] != wantCandidates[1] {
				t.Fatalf("candidates = %v, want %v", candidates, wantCandidates)
			}
			owners := zzR1ReadScope(t, fixture.ctx, fixture.pool, state.ScopeID).OwnerCount
			wantOwners := 0
			if mode == "multiple" {
				wantOwners = 2
			}
			if owners != wantOwners {
				t.Fatalf("owners = %d, want %d", owners, wantOwners)
			}
			if zzR1P3MediaEvents(t, fixture) != 0 {
				t.Fatal("conflict emitted a task media terminal event")
			}
			oldState := zzR1ReadOperation(t, fixture.ctx, fixture.pool, fixture.oldOperation.ID)
			if oldState.Status != "running" || oldState.AttemptCount != 1 {
				t.Fatalf("old operation = %s/%d, want running/1", oldState.Status, oldState.AttemptCount)
			}
			others := zzR1Scalar[int](t, fixture.ctx, fixture.pool, `SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'subtitle.prepare' AND id <> $2`, repository.UUIDToPG(fixture.taskID), repository.UUIDToPG(fixture.oldOperation.ID))
			if others != 0 {
				t.Fatalf("conflict created %d subtitle operations, want 0", others)
			}
		})
	}
}
