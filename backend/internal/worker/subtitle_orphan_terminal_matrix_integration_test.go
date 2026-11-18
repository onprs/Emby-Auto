//go:build integration

package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

type f3TerminalCase struct {
	name        string
	failure     string
	generation  string
	terminal    string
	wantTaskEnd bool
}

func f3TerminalCases() []f3TerminalCase {
	return []f3TerminalCase{
		{name: "owner_queued_permanent", failure: "permanent", generation: "owner", terminal: "fail"},
		{name: "owner_queued_retryable_last_attempt", failure: "retryable", generation: "owner", terminal: "fail"},
		{name: "owner_queued_cancel", failure: "permanent", generation: "owner", terminal: "cancel"},
		{name: "new_owner_applied_permanent", failure: "permanent", generation: "applied", terminal: "fail"},
		{name: "new_scope_pending_permanent", failure: "permanent", generation: "pending", terminal: "fail"},
		{name: "true_orphan_permanent", failure: "permanent", generation: "none", terminal: "fail", wantTaskEnd: true},
	}
}

func TestSubtitleOrphanGuardLifecycleMatrix(t *testing.T) {
	for _, testCase := range f3TerminalCases() {
		t.Run(testCase.name, func(t *testing.T) {
			f3RunTerminalCase(t, testCase)
		})
	}
}

func f3RunTerminalCase(t *testing.T, testCase f3TerminalCase) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := service.NewOperationScheduler(transactor, &subtitleGenerationJobInserter{})
	taskID := seedSubtitleGenerationTask(t, ctx, pool)
	workflow := service.NewMediaWorkflow(db.New(pool), transactor, scheduler)
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		f3WriteSubmission(t, writer, taskID, "embedded-6")
	}))
	defer provider.Close()
	enabled := f3EnabledService(pool, transactor, scheduler, provider.URL+"/v1")
	agent := &subtitleMatchAgentStub{enabled: true}
	switch testCase.failure {
	case "permanent":
		agent.created = service.AgentResolutionCommandResult{Resolution: domain.AgentResolution{
			Capability: domain.AgentCapabilitySubtitleVideoMatch, Status: domain.AgentResolutionFailed,
			Trigger: "automatic", Version: 1, ErrorCode: "agent_tool_scope_violation",
		}}
	case "retryable":
		agent.createErr = service.NewError("agent_resolution_schedule_failed", "synthetic schedule failure", nil, nil)
	}
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
`, repository.UUIDToPG(operationID), repository.UUIDToPG(taskID), "f3-matrix-"+operationID.String()); err != nil {
		t.Fatal(err)
	}
	lifecycle := repository.NewOperationLifecycle(db.New(pool), transactor)
	started, err := lifecycle.StartAttempt(ctx, operationID, 1, "f3-matrix-worker")
	if err != nil || started.SubtitleAttempt == nil {
		t.Fatalf("start matrix worker: %+v/%v", started, err)
	}
	handlerErr := make(chan error, 1)
	go func() {
		handlerErr <- handler.Handle(ctx, started)
	}()
	select {
	case <-store.guardReached:
	case callErr := <-handlerErr:
		t.Fatalf("handler returned before guarded orphan expiry: %v", callErr)
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not reach guarded orphan expiry")
	}
	var newScopeID uuid.UUID
	var newOwner service.AgentResolutionCommandResult
	if testCase.generation != "none" {
		var scopeErr error
		newScopeID, scopeErr = workflow.CreateSubtitleVideoMatchScope(ctx, taskID, f3NewScopeCandidates())
		if scopeErr != nil {
			t.Fatalf("CreateSubtitleVideoMatchScope() error = %v", scopeErr)
		}
		if testCase.generation != "pending" {
			newOwner, scopeErr = enabled.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: newScopeID})
			if scopeErr != nil {
				t.Fatalf("new owner CreateAutomatic() error = %v", scopeErr)
			}
		}
		if testCase.generation == "applied" {
			if _, err := lifecycle.StartAttempt(ctx, newOwner.Operation.ID, 1, "f3-matrix-new-owner"); err != nil {
				t.Fatal(err)
			}
			if err := enabled.Run(ctx, newOwner.Operation); err != nil {
				t.Fatalf("new owner Run() error = %v", err)
			}
		}
	}
	close(store.release)
	var handlerFailure *Failure
	select {
	case callErr := <-handlerErr:
		if callErr != nil && !errors.As(callErr, &handlerFailure) {
			t.Fatalf("handler error = %v, want *Failure or nil", callErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not return after guard release")
	}
	switch testCase.terminal {
	case "fail":
		code, message := "agent_subtitle_match_failed", "synthetic old terminal"
		retryable := false
		if handlerFailure != nil {
			code, message, retryable = handlerFailure.Code, handlerFailure.Message, handlerFailure.Retryable
		}
		if err := lifecycle.FailSubtitleAttempt(ctx, *started.SubtitleAttempt, code, message, retryable); err != nil {
			t.Fatalf("old operation FailSubtitleAttempt() error = %v", err)
		}
	case "cancel":
		if err := lifecycle.CancelSubtitleAttempt(ctx, *started.SubtitleAttempt); err != nil {
			t.Fatalf("old operation CancelSubtitleAttempt() error = %v", err)
		}
	}
	var taskState, subtitleState string
	if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState); err != nil {
		t.Fatal(err)
	}
	terminal := taskState == string(domain.TaskFailed) || taskState == string(domain.TaskCancelled) ||
		subtitleState == string(domain.SubtitleFailed) || subtitleState == string(domain.SubtitleCancelled)
	if testCase.wantTaskEnd {
		if !terminal {
			t.Fatalf("true orphan old terminal did not end task: %s/%s", taskState, subtitleState)
		}
		return
	}
	if terminal {
		t.Fatalf("old terminal killed new generation task: %s/%s", taskState, subtitleState)
	}
	if testCase.generation == "pending" {
		var err error
		newOwner, err = enabled.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: newScopeID})
		if err != nil {
			t.Fatalf("pending generation CreateAutomatic() error = %v", err)
		}
	}
	if testCase.generation != "applied" && testCase.generation != "none" {
		if _, err := lifecycle.StartAttempt(ctx, newOwner.Operation.ID, 1, "f3-matrix-new-owner"); err != nil {
			t.Fatal(err)
		}
		if err := enabled.Run(ctx, newOwner.Operation); err != nil {
			t.Fatalf("new owner Run() error = %v", err)
		}
	}
	if testCase.generation != "none" {
		owner, err := enabled.Get(ctx, newOwner.Resolution.ID)
		if err != nil {
			t.Fatal(err)
		}
		if owner.Status != domain.AgentResolutionApplied {
			t.Fatalf("new owner status = %s, want applied", owner.Status)
		}
	}
}
