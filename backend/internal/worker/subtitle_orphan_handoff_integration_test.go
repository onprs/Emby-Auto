//go:build integration

package worker

// F3 回归：真实 Handler 的 GetState(active0/pending) 之后，真实 CreateAutomatic
// 提交 queued owner，再放行 handler 继续 cleanup。handler 必须在 S/T 锁内看到
// 当前 owner 并 handoff，不得作废/重排它；owner 随后可继续 Provider Apply。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type f3HandoffBarrierStore struct {
	*service.MediaWorkflow
	read    chan struct{}
	release chan struct{}
	once    sync.Once
}

func (store *f3HandoffBarrierStore) GetSubtitleVideoMatchSchedulingState(ctx context.Context, taskID uuid.UUID) (domain.SubtitleVideoMatchSchedulingState, error) {
	state, err := store.MediaWorkflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
	store.once.Do(func() {
		close(store.read)
		<-store.release
	})
	return state, err
}

func f3HandoffWriteSubmission(t *testing.T, writer http.ResponseWriter, taskID uuid.UUID) {
	t.Helper()
	proposal := domain.AgentSubtitleVideoMatchProposal{
		TaskID: taskID, Selected: domain.SubtitleCandidateSelection{CandidateID: "embedded-2"},
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

func f3HandoffCandidates() []domain.SubtitleMatchCandidate {
	return []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-2", Source: domain.SubtitleSourceEmbedded, StreamIndex: 2, Format: domain.SubtitleASS, Language: "chi", Title: "简日双语"},
		{CandidateID: "embedded-3", Source: domain.SubtitleSourceEmbedded, StreamIndex: 3, Format: domain.SubtitleASS, Language: "chi", Title: "繁日雙語"},
	}
}

func TestSubtitlePrepareOrphanGuardHandsOffToConcurrentOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	taskID := seedSubtitleGenerationTask(t, ctx, pool)
	workflow := service.NewMediaWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, &subtitleGenerationJobInserter{}))
	scopeID, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, f3HandoffCandidates())
	if err != nil {
		t.Fatalf("CreateSubtitleVideoMatchScope() error = %v", err)
	}
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		f3HandoffWriteSubmission(t, writer, taskID)
	}))
	defer server.Close()
	configuration := domain.Configuration{Version: 1}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.SubtitleVideoMatchMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.BaseURL = server.URL + "/v1"
	configuration.Settings.Agent.Model = "f3-handoff-model"
	configuration.Settings.Agent.RequestTimeoutSeconds = 60
	agent := service.NewAgentResolutionService(db.New(pool), transactor, service.NewOperationScheduler(transactor, &subtitleGenerationJobInserter{}), subtitleGenerationConfiguration{configuration: configuration}, nil, nil)
	store := &f3HandoffBarrierStore{MediaWorkflow: workflow, read: make(chan struct{}), release: make(chan struct{})}
	tools := &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) {
		return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
			{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi", Title: "简日双语"},
			{Index: 3, Type: "subtitle", Codec: "ass", Language: "chi", Title: "繁日雙語"},
		}}, nil
	}}
	handler := NewSubtitlePrepareHandler(mediaTestConfiguration(t.TempDir()), tools, store, agent)
	handlerErr := make(chan error, 1)
	go func() {
		handlerErr <- handler.Handle(ctx, domain.Operation{ID: uuid.New(), Kind: "subtitle.prepare", ResourceType: "episode_task", ResourceID: taskID})
	}()
	select {
	case <-store.read:
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not reach GetState")
	}
	created, err := agent.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: scopeID})
	if err != nil {
		t.Fatalf("concurrent CreateAutomatic() error = %v", err)
	}
	if created.Resolution.Status != domain.AgentResolutionQueued {
		t.Fatalf("created owner status = %s, want queued", created.Resolution.Status)
	}
	ownerOperationID := created.Operation.ID
	ownerVersion := created.Resolution.Version
	close(store.release)
	select {
	case callErr := <-handlerErr:
		if callErr != nil {
			t.Fatalf("handler returned error = %v, want handoff nil", callErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not return")
	}
	owner, err := agent.Get(ctx, created.Resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Status != domain.AgentResolutionQueued || owner.OperationID != ownerOperationID || owner.Version != ownerVersion {
		t.Fatalf("handler changed the concurrent owner: status=%s operation=%s version=%d, want queued/%s/%d", owner.Status, owner.OperationID, owner.Version, ownerOperationID, ownerVersion)
	}
	var scopeStatus string
	var selected *string
	if err := pool.QueryRow(ctx, `SELECT status, selected_candidate_id FROM subtitle_video_match_scopes WHERE id = $1`, repository.UUIDToPG(scopeID)).Scan(&scopeStatus, &selected); err != nil {
		t.Fatal(err)
	}
	if scopeStatus != "pending" || selected != nil {
		t.Fatalf("handler changed scope: status=%s selected=%v, want pending/nil", scopeStatus, selected)
	}
	var candidates []string
	rows, err := pool.Query(ctx, `SELECT candidate_id FROM subtitle_video_match_candidates WHERE scope_id = $1 ORDER BY candidate_id`, repository.UUIDToPG(scopeID))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var candidateID string
		if err := rows.Scan(&candidateID); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, candidateID)
	}
	rows.Close()
	if len(candidates) != 2 || candidates[0] != "embedded-2" || candidates[1] != "embedded-3" {
		t.Fatalf("handler changed candidates: %v", candidates)
	}
	var ownerOps, queuedEvents, taskEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'agent.resolve' AND resource_id = $1`, repository.UUIDToPG(created.Resolution.ID)).Scan(&ownerOps); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = 'agent.resolution_queued' AND operation_id = $1`, repository.UUIDToPG(ownerOperationID)).Scan(&queuedEvents); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic IN ('task.media_failed','task.media_cancelled') AND resource_id = $1`, repository.UUIDToPG(taskID)).Scan(&taskEvents); err != nil {
		t.Fatal(err)
	}
	if ownerOps != 1 || queuedEvents != 1 || taskEvents != 0 {
		t.Fatalf("handler side effects: ownerOps=%d queuedEvents=%d taskEvents=%d, want 1/1/0", ownerOps, queuedEvents, taskEvents)
	}
	lifecycle := repository.NewOperationLifecycle(db.New(pool), transactor)
	if _, err := lifecycle.StartAttempt(ctx, ownerOperationID, 1, "f3-handoff-owner"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := agent.Run(ctx, created.Operation); err != nil {
		t.Fatalf("owner Run() error = %v", err)
	}
	applied, err := agent.Get(ctx, created.Resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != domain.AgentResolutionApplied {
		t.Fatalf("owner status = %s, want applied", applied.Status)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", providerCalls.Load())
	}
	finalScope, err := workflow.GetSubtitleVideoMatchSelection(ctx, taskID)
	if err != nil {
		t.Fatalf("GetSubtitleVideoMatchSelection() error = %v", err)
	}
	if finalScope.CandidateID != "embedded-2" {
		t.Fatalf("applied candidate = %q, want embedded-2", finalScope.CandidateID)
	}
}
