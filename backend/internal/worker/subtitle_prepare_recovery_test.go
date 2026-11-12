package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

// 复核 E-ZOMBIE-01：scope 已提交但 Agent 调度失败后，重试不得把
// subtitle_scope_conflict 当成成功；孤儿范围必须作废并重新调度或明确失败。
func TestQASubtitlePrepareRetryAfterScheduleFailureDoesNotHang(t *testing.T) {
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
	if len(store.expiredScopes) != 1 || store.expiredScopes[0] != scopeID {
		t.Fatalf("first attempt must expire the orphan scope, expired = %#v", store.expiredScopes)
	}
	// 重试时范围仍存在（作废失败或并发残留），调度状态确认没有活动决议。
	store.scopeErr = &domain.MediaWorkflowError{Code: "subtitle_scope_conflict", Message: "scope already exists"}
	store.schedulingState = domain.SubtitleVideoMatchSchedulingState{
		ScopeID: scopeID, ScopeStatus: "pending", TaskVersion: 1,
	}
	agent.createCalls = 0
	agent.createErr = nil
	// 本次重试调度同样失败，验证任务不会陷入无限静默重试。
	agent.createErr = errors.New("agent scheduler unavailable again")
	err = handler.Handle(context.Background(), operation)
	var retryFailure *Failure
	if !errors.As(err, &retryFailure) || !retryFailure.Retryable {
		t.Fatalf("retry error = %v, want retryable schedule failure", err)
	}
	if len(store.expiredScopes) != 2 {
		t.Fatalf("retry must expire the orphan scope and the failed reschedule, expired = %#v", store.expiredScopes)
	}
	if agent.createCalls != 1 {
		t.Fatalf("retry must reschedule after expiring the orphan scope, createCalls = %d", agent.createCalls)
	}
	if store.scopeCalls != 2 {
		t.Fatalf("retry must recreate the scope after expiring it, scopeCalls = %d", store.scopeCalls)
	}
}

// 复核 E-ZOMBIE-02：恢复上限耗尽返回专用错误后，handler 必须明确失败并作废范围。
func TestQASubtitlePrepareStateConflictLeavesTaskProcessing(t *testing.T) {
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
	if !errors.As(err, &failure) || failure.Retryable || failure.Code != "agent_subtitle_match_exhausted" {
		t.Fatalf("Handle() error = %v, want permanent agent_subtitle_match_exhausted", err)
	}
	if len(store.expiredScopes) != 1 || store.expiredScopes[0] != scopeID {
		t.Fatalf("exhausted requeue must expire the scope, expired = %#v", store.expiredScopes)
	}
}

// 有活动决议与操作承接当前任务版本时，scope 冲突仍是良性并发，保持成功等待。
func TestQASubtitlePrepareScopeConflictWithActiveResolutionSucceeds(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	resolutionID := uuid.New()
	operationID := uuid.New()
	taskVersion := 1
	store.scopeErr = &domain.MediaWorkflowError{Code: "subtitle_scope_conflict", Message: "scope already exists"}
	store.schedulingState = domain.SubtitleVideoMatchSchedulingState{
		ScopeID: scopeID, ScopeStatus: "pending", TaskVersion: taskVersion,
		ResolutionID: &resolutionID, ResolutionStatus: "queued", ResolutionVersion: 1,
		ResolutionTaskVersion: &taskVersion, OperationID: &operationID, OperationStatus: "queued",
	}
	agent := &subtitleMatchAgentStub{enabled: true}
	if err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(),
		domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	); err != nil {
		t.Fatalf("Handle() error = %v, want success while an active resolution covers this task version", err)
	}
	if len(store.expiredScopes) != 0 {
		t.Fatalf("active resolution must keep the scope, expired = %#v", store.expiredScopes)
	}
	if agent.createCalls != 0 {
		t.Fatalf("active resolution must not trigger a duplicate schedule")
	}
}

// 旧 generation（任务版本已推进）的活动决议不承接新范围：作废后重新调度。
func TestQASubtitlePrepareScopeConflictWithStaleGenerationReschedules(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	resolutionID := uuid.New()
	operationID := uuid.New()
	store.scopeErr = &domain.MediaWorkflowError{Code: "subtitle_scope_conflict", Message: "scope already exists"}
	staleVersion := 0
	store.schedulingState = domain.SubtitleVideoMatchSchedulingState{
		ScopeID: scopeID, ScopeStatus: "pending", TaskVersion: 1,
		ResolutionID: &resolutionID, ResolutionStatus: "queued", ResolutionVersion: 2,
		ResolutionTaskVersion: &staleVersion, OperationID: &operationID, OperationStatus: "queued",
	}
	agent := &subtitleMatchAgentStub{enabled: true}
	// 作废后重新创建会成功；stub 的 scopeErr 只模拟第一次冲突。
	if err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(),
		domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	); err != nil {
		t.Fatalf("Handle() error = %v, want rescheduling after expiring the stale scope", err)
	}
	if len(store.expiredScopes) != 1 || store.expiredScopes[0] != scopeID {
		t.Fatalf("stale generation scope must be expired, expired = %#v", store.expiredScopes)
	}
	if agent.createCalls != 1 || len(agent.createdScopes) != 1 || agent.createdScopes[0] != scopeID {
		t.Fatalf("stale generation must be replaced by a fresh schedule, createCalls = %d", agent.createCalls)
	}
	if store.scopeCalls != 2 {
		t.Fatalf("scope creation calls = %d, want conflict + recreation", store.scopeCalls)
	}
}

// Agent 能力在调度时被关闭：作废范围并明确失败，不留下等待中的范围。
func TestQASubtitlePrepareDisabledAgentFailsInsteadOfHanging(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	agent := &subtitleMatchAgentStub{enabled: true, createErr: service.NewError(
		"agent_capability_disabled", "the requested Agent capability is disabled", service.ErrStateConflict,
		map[string]any{"capability": domain.AgentCapabilitySubtitleVideoMatch},
	)}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(
		context.Background(),
		domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Retryable || failure.Code != "agent_capability_disabled" {
		t.Fatalf("Handle() error = %v, want permanent agent_capability_disabled", err)
	}
	if len(store.expiredScopes) != 1 || store.expiredScopes[0] != scopeID {
		t.Fatalf("disabled agent must expire the scope, expired = %#v", store.expiredScopes)
	}
}
