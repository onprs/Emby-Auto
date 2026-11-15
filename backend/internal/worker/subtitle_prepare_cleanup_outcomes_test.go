package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func TestQASubtitlePrepareDisabledOwnerPresentHandoffs(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	store.expiryResult = domain.SubtitleScopeExpiry{Outcome: domain.SubtitleScopeOwnerPresent, OwnerCount: 1, ScopeStatus: "pending"}
	agent := &subtitleMatchAgentStub{enabled: true, createErr: service.NewError("agent_disabled", "Agent disabled", nil, nil)}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil handoff with owner present", err)
	}
	if store.scopeCalls != 1 {
		t.Fatalf("scope creations = %d, want 1 (no recreate after owner handoff)", store.scopeCalls)
	}
	if len(store.expiredScopes) != 1 {
		t.Fatalf("guarded cleanup calls = %d, want 1", len(store.expiredScopes))
	}
}

func TestQASubtitlePrepareFailedOwnerPresentHandoffs(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	store.expiryResult = domain.SubtitleScopeExpiry{Outcome: domain.SubtitleScopeOwnerPresent, OwnerCount: 1, ScopeStatus: "pending"}
	agent := &subtitleMatchAgentStub{enabled: true, created: service.AgentResolutionCommandResult{Resolution: domain.AgentResolution{
		Status: domain.AgentResolutionFailed, Trigger: "automatic", Version: 1, ErrorCode: "agent_tool_scope_violation",
	}}}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil handoff for non-retryable failed owner present", err)
	}
	if store.scopeCalls != 1 || len(store.expiredScopes) != 1 {
		t.Fatalf("scope calls/guarded cleanup = %d/%d, want 1/1", store.scopeCalls, len(store.expiredScopes))
	}
}

func TestQASubtitlePrepareScopeConflictChangedIsRetryable(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	store.scopeErr = &domain.MediaWorkflowError{Code: "subtitle_scope_conflict", Message: "scope already exists"}
	store.schedulingState = domain.SubtitleVideoMatchSchedulingState{ScopeID: scopeID, ScopeStatus: "pending", TaskVersion: 1}
	store.expiryResult = domain.SubtitleScopeExpiry{Outcome: domain.SubtitleScopeChanged, ScopeStatus: "pending"}
	agent := &subtitleMatchAgentStub{enabled: true}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable || failure.Code != "agent_resolution_schedule_conflict" {
		t.Fatalf("Handle() error = %v, want retryable schedule conflict for changed scope", err)
	}
	if store.scopeCalls != 1 || agent.createCalls != 0 {
		t.Fatalf("scope/agent calls = %d/%d, want no recreate/schedule", store.scopeCalls, agent.createCalls)
	}
}

func TestQASubtitlePrepareScopeConflictAlreadyAppliedHandoffs(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	store.scopeErr = &domain.MediaWorkflowError{Code: "subtitle_scope_conflict", Message: "scope already exists"}
	store.schedulingState = domain.SubtitleVideoMatchSchedulingState{ScopeID: scopeID, ScopeStatus: "pending", TaskVersion: 1}
	store.expiryResult = domain.SubtitleScopeExpiry{Outcome: domain.SubtitleScopeAlreadyApplied, ScopeStatus: "applied"}
	agent := &subtitleMatchAgentStub{enabled: true}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil handoff for applied scope", err)
	}
	if store.scopeCalls != 1 || agent.createCalls != 0 {
		t.Fatalf("scope/agent calls = %d/%d, want no recreate/schedule", store.scopeCalls, agent.createCalls)
	}
}

func TestQASubtitlePrepareScopeConflictAlreadyExpiredRebuilds(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	store.scopeErr = &domain.MediaWorkflowError{Code: "subtitle_scope_conflict", Message: "scope already exists"}
	store.schedulingState = domain.SubtitleVideoMatchSchedulingState{ScopeID: scopeID, ScopeStatus: "pending", TaskVersion: 1}
	store.expiryResult = domain.SubtitleScopeExpiry{Outcome: domain.SubtitleScopeAlreadyExpired, ScopeStatus: "expired"}
	agent := &subtitleMatchAgentStub{enabled: true}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	if err != nil {
		t.Fatalf("Handle() error = %v, want rebuild after already-expired scope", err)
	}
	if store.scopeCalls != 2 || agent.createCalls != 1 {
		t.Fatalf("scope/agent calls = %d/%d, want 2/1", store.scopeCalls, agent.createCalls)
	}
}

func TestQASubtitlePrepareScopeConflictMultipleOwnersConflict(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	store.scopeErr = &domain.MediaWorkflowError{Code: "subtitle_scope_conflict", Message: "scope already exists"}
	store.schedulingState = domain.SubtitleVideoMatchSchedulingState{ScopeID: scopeID, ScopeStatus: "pending", TaskVersion: 1}
	store.expiryResult = domain.SubtitleScopeExpiry{Outcome: domain.SubtitleScopeMultipleOwners, OwnerCount: 2, ScopeStatus: "pending"}
	agent := &subtitleMatchAgentStub{enabled: true}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable || failure.Code != "agent_resolution_schedule_conflict" {
		t.Fatalf("Handle() error = %v, want conservative retryable conflict", err)
	}
	if store.scopeCalls != 1 || agent.createCalls != 0 {
		t.Fatalf("scope/agent calls = %d/%d, want no recreate/schedule", store.scopeCalls, agent.createCalls)
	}
}

func TestQASubtitlePrepareNotRequiredOwnerPresentNil(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	store.expiryResult = domain.SubtitleScopeExpiry{Outcome: domain.SubtitleScopeOwnerPresent, OwnerCount: 1, ScopeStatus: "pending"}
	agent := &subtitleMatchAgentStub{enabled: true, createErr: service.NewError("agent_resolution_not_required", "deterministic rules resolved", nil, nil)}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil for not-required with owner present", err)
	}
}
