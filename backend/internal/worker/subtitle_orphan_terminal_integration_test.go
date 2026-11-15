//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type f3TerminalBarrierStore struct {
	*service.MediaWorkflow
	guardReached chan struct{}
	release      chan struct{}
	once         sync.Once
}

func (store *f3TerminalBarrierStore) ExpireOwnerlessSubtitleVideoMatchScope(ctx context.Context, scopeID uuid.UUID, observed domain.SubtitleScopeObservation) (domain.SubtitleScopeExpiry, error) {
	result, err := store.MediaWorkflow.ExpireOwnerlessSubtitleVideoMatchScope(ctx, scopeID, observed)
	if err == nil && result.Outcome == domain.SubtitleScopeExpired {
		store.once.Do(func() {
			close(store.guardReached)
			<-store.release
		})
	}
	return result, err
}

func f3WriteSubmission(t *testing.T, writer http.ResponseWriter, taskID uuid.UUID, candidateID string) {
	t.Helper()
	proposal := domain.AgentSubtitleVideoMatchProposal{
		TaskID: taskID, Selected: domain.SubtitleCandidateSelection{CandidateID: candidateID},
		EvidenceCodes: []string{"scoped_candidate"}, Decision: "resolved",
	}
	raw, _ := json.Marshal(proposal)
	_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{
		"finish_reason": "tool_calls", "message": map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "call-submit", "type": "function",
				"function": map[string]any{"name": "submit_subtitle_video_match", "arguments": string(raw)},
			}},
		},
	}}})
}

func f3NewScopeCandidates() []domain.SubtitleMatchCandidate {
	return []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-6", Source: domain.SubtitleSourceEmbedded, StreamIndex: 6, Format: domain.SubtitleASS, Language: "chi", Title: "孤儿后新代一"},
		{CandidateID: "embedded-7", Source: domain.SubtitleSourceEmbedded, StreamIndex: 7, Format: domain.SubtitleASS, Language: "chi", Title: "孤儿后新代二"},
	}
}

func f3EnabledService(pool *pgxpool.Pool, transactor *database.Transactor, scheduler *service.OperationScheduler, providerURL string) *service.AgentResolutionService {
	configuration := domain.Configuration{Version: 1}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.SubtitleVideoMatchMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.BaseURL = providerURL
	configuration.Settings.Agent.Model = "f3-owner-model"
	configuration.Settings.Agent.RequestTimeoutSeconds = 60
	return service.NewAgentResolutionService(db.New(pool), transactor, scheduler, subtitleGenerationConfiguration{configuration: configuration}, nil, nil)
}

func TestSubtitleOrphanGuardDoesNotLetOldTerminalKillNewOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := service.NewOperationScheduler(transactor, &subtitleGenerationJobInserter{})
	taskID := seedSubtitleGenerationTask(t, ctx, pool)
	workflow := service.NewMediaWorkflow(db.New(pool), transactor, scheduler)
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		f3WriteSubmission(t, writer, taskID, "embedded-6")
	}))
	defer provider.Close()
	// Enabled Agent stub returns a non-retryable failed resolution: the real
	// handler creates a scope, enters the failed-resolution cleanup, and the real
	// guard expires the ownerless scope. The barrier pauses after that commit,
	// before the handler's permanent failure and the real lifecycle task write.
	agent := &subtitleMatchAgentStub{enabled: true, created: service.AgentResolutionCommandResult{Resolution: domain.AgentResolution{
		Capability: domain.AgentCapabilitySubtitleVideoMatch, Status: domain.AgentResolutionFailed,
		Trigger: "automatic", Version: 1, ErrorCode: "agent_tool_scope_violation",
	}}}
	store := &f3TerminalBarrierStore{MediaWorkflow: workflow, guardReached: make(chan struct{}), release: make(chan struct{})}
	tools := &mediaToolsStub{runOutput: validASSFixture, probe: func(string) (domain.MediaProbe, error) {
		return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
			{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi", Title: "简日双语"},
			{Index: 3, Type: "subtitle", Codec: "ass", Language: "chi", Title: "繁日雙語"},
		}}, nil
	}}
	handler := NewSubtitlePrepareHandler(mediaTestConfiguration(t.TempDir()), tools, store, agent)
	operationID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, payload)
VALUES ($1, 'subtitle.prepare', 'episode_task', $2, $3, 'queued', 1, 60, '{}'::jsonb)
`, repository.UUIDToPG(operationID), repository.UUIDToPG(taskID), "f3-terminal-"+operationID.String()); err != nil {
		t.Fatal(err)
	}
	lifecycle := repository.NewOperationLifecycle(db.New(pool), transactor)
	if _, err := lifecycle.StartAttempt(ctx, operationID, 1, "f3-terminal-worker"); err != nil {
		t.Fatal(err)
	}
	handlerErr := make(chan error, 1)
	go func() {
		handlerErr <- handler.Handle(ctx, domain.Operation{ID: operationID, Kind: "subtitle.prepare", ResourceType: "episode_task", ResourceID: taskID})
	}()
	select {
	case <-store.guardReached:
	case callErr := <-handlerErr:
		t.Fatalf("handler returned before guarded orphan expiry: %v", callErr)
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not reach guarded orphan expiry")
	}
	// The old scope is now expired. Create a legitimate new generation and owner
	// while the old handler is paused before its permanent failure.
	newScopeID, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, f3NewScopeCandidates())
	if err != nil {
		t.Fatalf("CreateSubtitleVideoMatchScope() error = %v", err)
	}
	enabled := f3EnabledService(pool, transactor, scheduler, provider.URL+"/v1")
	newOwner, err := enabled.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: newScopeID})
	if err != nil {
		t.Fatalf("new owner CreateAutomatic() error = %v", err)
	}
	close(store.release)
	var handlerFailure *Failure
	select {
	case callErr := <-handlerErr:
		if !errors.As(callErr, &handlerFailure) {
			t.Fatalf("handler error = %v, want *Failure", callErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not return after guard release")
	}
	if err := lifecycle.FailAttempt(ctx, operationID, 1, handlerFailure.Code, handlerFailure.Message, handlerFailure.Retryable); err != nil {
		t.Fatalf("old operation FailAttempt() error = %v", err)
	}
	// The old permanent failure must not mark the task terminal or disturb the new
	// owner; the new owner must still be able to Apply.
	var taskState, subtitleState string
	if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState); err != nil {
		t.Fatal(err)
	}
	if taskState == string(domain.TaskFailed) || taskState == string(domain.TaskCancelled) || subtitleState == string(domain.SubtitleFailed) || subtitleState == string(domain.SubtitleCancelled) {
		t.Fatalf("old terminal killed new owner task: task=%s/%s", taskState, subtitleState)
	}
	owner, err := enabled.Get(ctx, newOwner.Resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Status != domain.AgentResolutionQueued {
		t.Fatalf("new owner status = %s, want queued", owner.Status)
	}
	if _, err := lifecycle.StartAttempt(ctx, newOwner.Operation.ID, 1, "f3-new-owner"); err != nil {
		t.Fatalf("new owner StartAttempt() error = %v", err)
	}
	if err := enabled.Run(ctx, newOwner.Operation); err != nil {
		t.Fatalf("new owner Run() error = %v", err)
	}
	applied, err := enabled.Get(ctx, newOwner.Resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != domain.AgentResolutionApplied {
		t.Fatalf("new owner status after Run = %s, want applied", applied.Status)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", providerCalls.Load())
	}
}
