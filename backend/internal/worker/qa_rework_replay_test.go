package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

// QA: once the inactive Agent resolution requeue cap is exhausted,
// CreateAutomatic returns a state_conflict error. The handler currently treats
// every state_conflict as "already scheduled" and returns success, leaving the
// task subtitle branch in extracting_or_converting with no active operation.
func TestQAReplayRequeueCapPermanentFailure(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	agent := &subtitleMatchAgentStub{
		enabled: true,
		createErr: service.NewError(
			"agent_resolution_requeue_exhausted",
			"the Agent resolution reached its automatic recovery limit",
			service.ErrStateConflict,
			nil,
		),
	}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(),
		domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "agent_subtitle_match_exhausted" {
		t.Fatalf("Handle() error = %v, want permanent agent_subtitle_match_failed; task would remain processing", err)
	}
}

// QA: when scope creation succeeded but Agent scheduling failed transiently,
// the retry sees a pending scope and returns success without an Agent
// resolution, leaving the task waiting forever.
func TestQAReplayRetryAfterScheduleFailure(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	agent := &subtitleMatchAgentStub{enabled: true, createErr: errors.New("agent scheduler unavailable")}
	handler := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent)
	operation := domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID}
	err := handler.Handle(context.Background(), operation)
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable {
		t.Fatalf("first attempt error = %v, want retryable schedule failure", err)
	}
	// The scope was committed by the first attempt; the retry receives a conflict
	// with no active resolution. The orphan scope must be expired and rescheduled.
	store.scopeErr = &domain.MediaWorkflowError{Code: "subtitle_scope_conflict", Message: "scope already exists"}
	store.schedulingState = domain.SubtitleVideoMatchSchedulingState{
		ScopeID: scopeID, ScopeStatus: "pending", TaskVersion: 1,
	}
	agent.createCalls = 0
	agent.createErr = nil
	err = handler.Handle(context.Background(), operation)
	if err != nil {
		t.Fatalf("retry with recoverable orphan scope error = %v, want a fresh schedule", err)
	}
	if agent.createCalls != 1 {
		t.Fatalf("retry must reschedule after recovering from the orphan scope: createCalls=%d", agent.createCalls)
	}
}
