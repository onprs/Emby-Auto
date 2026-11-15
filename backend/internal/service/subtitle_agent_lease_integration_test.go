//go:build integration

package service

// 字幕租约回归：真实 AgentResolutionService/Run/Apply/terminal + 合成 Provider。
// 内容 fingerprint 只证明语义兼容；归属由 scope/resolution/operation/version
// 在 S/T→R 锁内的持久租约决定。

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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/agentharness"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type subtitleLeaseInspector struct{}

func (subtitleLeaseInspector) InspectSubtitleText(context.Context, domain.SubtitleInspectionRequest) (domain.SubtitleInspection, error) {
	return domain.SubtitleInspection{Found: true, Sample: "你好世界", HanCount: 4}, nil
}

func subtitleLeaseConfiguration(version int32, model, providerURL string) agentLifecycleConfiguration {
	configuration := domain.Configuration{Version: version}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.SubtitleVideoMatchMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.BaseURL = providerURL
	configuration.Settings.Agent.Model = model
	configuration.Settings.Agent.RequestTimeoutSeconds = 60
	return agentLifecycleConfiguration{load: func() (domain.Configuration, error) { return configuration, nil }}
}

func newSubtitleLeaseService(pool *pgxpool.Pool, transactor *database.Transactor, scheduler *OperationScheduler, configuration agentLifecycleConfiguration) *AgentResolutionService {
	service := NewAgentResolutionService(db.New(pool), transactor, scheduler, configuration, nil, nil)
	return service.WithSubtitleTextInspector(subtitleLeaseInspector{})
}

type subtitleLeaseFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	transactor *database.Transactor
	scheduler  *OperationScheduler
	workflow   *MediaWorkflow
	lifecycle  *repository.OperationLifecycle
	taskID     uuid.UUID
	scopeID    uuid.UUID
}

func newSubtitleLeaseFixture(t *testing.T) *subtitleLeaseFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	taskID, scopeID := seedSubtitleAgentEntities(t, ctx, pool)
	return &subtitleLeaseFixture{
		ctx: ctx, pool: pool, transactor: transactor, scheduler: scheduler,
		workflow:  NewMediaWorkflow(db.New(pool), transactor, scheduler),
		lifecycle: repository.NewOperationLifecycle(db.New(pool), transactor),
		taskID:    taskID, scopeID: scopeID,
	}
}

func (fixture *subtitleLeaseFixture) service(configuration agentLifecycleConfiguration) *AgentResolutionService {
	return newSubtitleLeaseService(fixture.pool, fixture.transactor, fixture.scheduler, configuration)
}

func (fixture *subtitleLeaseFixture) request() AutomaticAgentResolutionRequest {
	return AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: fixture.scopeID}
}

func (fixture *subtitleLeaseFixture) resolution(t *testing.T, id uuid.UUID) db.AgentResolution {
	t.Helper()
	resolution, err := db.New(fixture.pool).GetAgentResolution(fixture.ctx, repository.UUIDToPG(id))
	if err != nil {
		t.Fatal(err)
	}
	return resolution
}

func (fixture *subtitleLeaseFixture) scopeAndTask(t *testing.T) (scopeStatus, selectedCandidate string, resolutionID *uuid.UUID, taskState, videoState, subtitleState string) {
	t.Helper()
	var selected *string
	var resolution pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT scope.status, scope.selected_candidate_id, scope.agent_resolution_id,
       task.state, task.video_state, task.subtitle_state
FROM subtitle_video_match_scopes AS scope
JOIN episode_tasks AS task ON task.id = scope.task_id
WHERE scope.id = $1
`, repository.UUIDToPG(fixture.scopeID)).Scan(&scopeStatus, &selected, &resolution, &taskState, &videoState, &subtitleState); err != nil {
		t.Fatal(err)
	}
	if selected != nil {
		selectedCandidate = *selected
	}
	if resolution.Valid {
		value := repository.UUIDFromPG(resolution)
		resolutionID = &value
	}
	return
}

func writeSubtitleToolCall(t *testing.T, writer http.ResponseWriter, name string, arguments any) {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Error(err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{
		"finish_reason": "tool_calls", "message": map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "call-" + name, "type": "function",
				"function": map[string]any{"name": name, "arguments": string(raw)},
			}},
		},
	}}})
}

func subtitleLeaseProposal(taskID uuid.UUID, candidateID string) domain.AgentSubtitleVideoMatchProposal {
	return domain.AgentSubtitleVideoMatchProposal{
		TaskID: taskID, Selected: domain.SubtitleCandidateSelection{CandidateID: candidateID},
		EvidenceCodes: []string{"scoped_candidate"}, Decision: "resolved",
	}
}

func writeSubtitleSubmission(t *testing.T, writer http.ResponseWriter, taskID uuid.UUID, candidateID string) {
	t.Helper()
	writeSubtitleToolCall(t, writer, "submit_subtitle_video_match", subtitleLeaseProposal(taskID, candidateID))
}

// TestSubtitleAgentVideoReadyDuringProviderAppliesIntegration 覆盖 Provider 调用
// 期间正常 VideoReady 推进 task.version：内容未变，租约仍归当前 owner，必须应用。
func TestSubtitleAgentVideoReadyDuringProviderAppliesIntegration(t *testing.T) {
	fixture := newSubtitleLeaseFixture(t)
	var videoReady atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if videoReady.CompareAndSwap(false, true) {
			if _, err := db.New(fixture.pool).MarkTaskVideoReady(fixture.ctx, repository.UUIDToPG(fixture.taskID)); err != nil {
				t.Errorf("MarkTaskVideoReady() error = %v", err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		writeSubtitleSubmission(t, writer, fixture.taskID, "embedded-2")
	}))
	defer server.Close()

	service := fixture.service(subtitleLeaseConfiguration(1, "lease-model", server.URL+"/v1"))
	created, err := service.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("CreateAutomatic() error = %v", err)
	}
	if created.Resolution.ResourceVersion == nil {
		t.Fatal("subtitle resolution resource_version must be non-nil")
	}
	if err := service.Run(fixture.ctx, created.Operation); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	applied, err := service.Get(fixture.ctx, created.Resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != domain.AgentResolutionApplied {
		t.Fatalf("resolution status = %s, want applied", applied.Status)
	}
	scopeStatus, selected, resolutionID, taskState, videoState, subtitleState := fixture.scopeAndTask(t)
	if scopeStatus != "applied" || selected != "embedded-2" || resolutionID == nil || *resolutionID != created.Resolution.ID {
		t.Fatalf("applied scope = %s/%s/%v", scopeStatus, selected, resolutionID)
	}
	if taskState != string(domain.TaskProcessing) || videoState != string(domain.VideoReady) || subtitleState != string(domain.SubtitleExtractingConverting) {
		t.Fatalf("task after apply = %s/%s/%s", taskState, videoState, subtitleState)
	}
	var subtitleOperations int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM operations WHERE kind = 'subtitle.prepare' AND resource_id = $1`, repository.UUIDToPG(fixture.taskID)).Scan(&subtitleOperations); err != nil {
		t.Fatal(err)
	}
	if subtitleOperations != 1 {
		t.Fatalf("subtitle.prepare operations = %d, want 1", subtitleOperations)
	}
}

// TestSubtitleAgentContentChangeDuringProviderExpiresIntegration 覆盖 Provider
// 期间候选或源视频内容真实变化：语义校验失败，不应用，终态收敛任务。
func TestSubtitleAgentContentChangeDuringProviderExpiresIntegration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*subtitleLeaseFixture) error
	}{
		{
			name: "candidate_changed",
			mutate: func(fixture *subtitleLeaseFixture) error {
				_, err := fixture.pool.Exec(fixture.ctx, `UPDATE subtitle_video_match_candidates SET title = 'changed candidate' WHERE scope_id = $1 AND candidate_id = 'embedded-2'`, repository.UUIDToPG(fixture.scopeID))
				return err
			},
		},
		{
			name: "source_path_changed",
			mutate: func(fixture *subtitleLeaseFixture) error {
				_, err := fixture.pool.Exec(fixture.ctx, `UPDATE download_files SET relative_path = 'Show.S01E01.changed.mkv' WHERE id = (SELECT source_video_file_id FROM episode_tasks WHERE id = $1)`, repository.UUIDToPG(fixture.taskID))
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSubtitleLeaseFixture(t)
			var mutated atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if mutated.CompareAndSwap(false, true) {
					if err := test.mutate(fixture); err != nil {
						t.Errorf("mutate content: %v", err)
						writer.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
				writeSubtitleSubmission(t, writer, fixture.taskID, "embedded-2")
			}))
			defer server.Close()
			service := fixture.service(subtitleLeaseConfiguration(1, "lease-model", server.URL+"/v1"))
			created, err := service.CreateAutomatic(fixture.ctx, fixture.request())
			if err != nil {
				t.Fatalf("CreateAutomatic() error = %v", err)
			}
			if err := service.Run(fixture.ctx, created.Operation); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			resolution := fixture.resolution(t, created.Resolution.ID)
			if resolution.Status != string(domain.AgentResolutionExpired) {
				t.Fatalf("resolution status = %s, want expired", resolution.Status)
			}
			scopeStatus, selected, _, taskState, _, subtitleState := fixture.scopeAndTask(t)
			if scopeStatus != "expired" || selected != "" || taskState != string(domain.TaskFailed) || subtitleState != string(domain.SubtitleFailed) {
				t.Fatalf("content change converged to scope=%s selected=%s task=%s/%s", scopeStatus, selected, taskState, subtitleState)
			}
			var failedEvents, subtitleOperations int
			if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM events WHERE topic = 'task.media_failed' AND resource_id = $1`, repository.UUIDToPG(fixture.taskID)).Scan(&failedEvents); err != nil {
				t.Fatal(err)
			}
			if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM operations WHERE kind = 'subtitle.prepare' AND resource_id = $1`, repository.UUIDToPG(fixture.taskID)).Scan(&subtitleOperations); err != nil {
				t.Fatal(err)
			}
			if failedEvents != 1 || subtitleOperations != 0 {
				t.Fatalf("events/apply = %d/%d, want 1/0", failedEvents, subtitleOperations)
			}
		})
	}
}

// TestSubtitleAgentSameContentRetryRejectsOldProviderIntegration 覆盖同内容真实
// 重试：旧 Provider 开始后 scope 撤销/复用、同 resolution 以新 operation 重排，
// 旧 Provider/step/proposal 返回时不得写入新租约。
func TestSubtitleAgentSameContentRetryRejectsOldProviderIntegration(t *testing.T) {
	fixture := newSubtitleLeaseFixture(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-release
			writeSubtitleToolCall(t, writer, "inspect_subtitle_candidate", map[string]any{"candidateId": "embedded-2"})
			return
		}
		writeSubtitleSubmission(t, writer, fixture.taskID, "embedded-2")
	}))
	defer server.Close()
	service := fixture.service(subtitleLeaseConfiguration(1, "lease-model", server.URL+"/v1"))
	created, err := service.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("CreateAutomatic() error = %v", err)
	}
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, created.Operation.ID, 1, "subtitle-old-provider"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- service.Run(fixture.ctx, created.Operation) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("old provider did not start")
	}

	if err := fixture.workflow.ExpireSubtitleVideoMatchScope(fixture.ctx, fixture.scopeID); err != nil {
		t.Fatalf("ExpireSubtitleVideoMatchScope() error = %v", err)
	}
	if err := fixture.lifecycle.CancelAttempt(fixture.ctx, created.Operation.ID, 1); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	cancelled := fixture.resolution(t, created.Resolution.ID)
	if cancelled.Status != string(domain.AgentResolutionCancelled) {
		t.Fatalf("cancelled resolution status = %s", cancelled.Status)
	}
	if _, err := fixture.workflow.CreateSubtitleVideoMatchScope(fixture.ctx, fixture.taskID, subtitleMatchCandidatesForTest()); err != nil {
		t.Fatalf("CreateSubtitleVideoMatchScope() error = %v", err)
	}
	rebuilt, err := service.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("CreateAutomatic() requeue error = %v", err)
	}
	if rebuilt.Resolution.ID != created.Resolution.ID || rebuilt.Operation.ID == created.Operation.ID || rebuilt.Resolution.Status != domain.AgentResolutionQueued {
		t.Fatalf("rebuilt identity/operation/status = %s/%s/%s", rebuilt.Resolution.ID, rebuilt.Operation.ID, rebuilt.Resolution.Status)
	}
	close(release)
	select {
	case runErr := <-runErrCh:
		var agentErr *AgentRunError
		if !errors.As(runErr, &agentErr) || agentErr.Code != "agent_resolution_ownership_lost" {
			t.Fatalf("old provider Run() error = %v, want ownership lost", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old provider Run() did not return")
	}

	current := fixture.resolution(t, created.Resolution.ID)
	if current.Status != string(domain.AgentResolutionQueued) || repository.UUIDFromPG(current.OperationID) != rebuilt.Operation.ID || current.Version != int32(rebuilt.Resolution.Version) {
		t.Fatalf("old provider changed lease: status=%s op=%s version=%d", current.Status, repository.UUIDFromPG(current.OperationID), current.Version)
	}
	if len(current.Proposal) > 2 {
		t.Fatalf("old provider wrote a proposal: %s", current.Proposal)
	}
	var steps int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM agent_resolution_steps WHERE resolution_id = $1`, repository.UUIDToPG(created.Resolution.ID)).Scan(&steps); err != nil {
		t.Fatal(err)
	}
	if steps != 0 {
		t.Fatalf("old provider wrote %d steps", steps)
	}
	var failedEvents int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM events WHERE topic IN ('task.media_failed', 'task.media_cancelled') AND resource_id = $1`, repository.UUIDToPG(fixture.taskID)).Scan(&failedEvents); err != nil {
		t.Fatal(err)
	}
	if failedEvents != 0 {
		t.Fatalf("old provider terminal events = %d, want 0", failedEvents)
	}
	scopeStatus, _, _, taskState, _, subtitleState := fixture.scopeAndTask(t)
	if scopeStatus != "pending" || taskState != string(domain.TaskProcessing) || subtitleState != string(domain.SubtitleExtractingConverting) {
		t.Fatalf("old provider changed scope/task: scope=%s task=%s/%s", scopeStatus, taskState, subtitleState)
	}
}

// TestSubtitleAgentNoOwnerWindowOldTerminalCannotReclaimIntegration 覆盖 scope
// 复用提交后、新 CreateAutomatic 之前无 owner 的窗口：旧 operation 的终态不能
// 回收新 scope/任务。
func TestSubtitleAgentNoOwnerWindowOldTerminalCannotReclaimIntegration(t *testing.T) {
	fixture := newSubtitleLeaseFixture(t)
	service := fixture.service(subtitleLeaseConfiguration(1, "lease-model", "https://provider.invalid/v1"))
	created, err := service.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("CreateAutomatic() error = %v", err)
	}
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, created.Operation.ID, 1, "subtitle-no-owner"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := fixture.workflow.ExpireSubtitleVideoMatchScope(fixture.ctx, fixture.scopeID); err != nil {
		t.Fatalf("ExpireSubtitleVideoMatchScope() error = %v", err)
	}
	if _, err := fixture.workflow.CreateSubtitleVideoMatchScope(fixture.ctx, fixture.taskID, []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-6", Source: domain.SubtitleSourceEmbedded, StreamIndex: 6, Format: domain.SubtitleASS, Language: "chi", Title: "新代轨道"},
		{CandidateID: "embedded-7", Source: domain.SubtitleSourceEmbedded, StreamIndex: 7, Format: domain.SubtitleASS, Language: "chi", Title: "新代第二轨"},
	}); err != nil {
		t.Fatalf("CreateSubtitleVideoMatchScope() error = %v", err)
	}
	if err := fixture.lifecycle.FailAttempt(fixture.ctx, created.Operation.ID, 1, "agent_request_timeout", "old generation terminal", false); err != nil {
		t.Fatalf("FailAttempt() error = %v", err)
	}
	old := fixture.resolution(t, created.Resolution.ID)
	if old.Status != string(domain.AgentResolutionCancelled) {
		t.Fatalf("old resolution status = %s, want cancelled", old.Status)
	}
	scopeStatus, _, _, taskState, _, subtitleState := fixture.scopeAndTask(t)
	if scopeStatus != "pending" || taskState != string(domain.TaskProcessing) || subtitleState != string(domain.SubtitleExtractingConverting) {
		t.Fatalf("old terminal changed new generation: scope=%s task=%s/%s", scopeStatus, taskState, subtitleState)
	}
	var failedEvents int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM events WHERE topic IN ('task.media_failed', 'task.media_cancelled') AND resource_id = $1`, repository.UUIDToPG(fixture.taskID)).Scan(&failedEvents); err != nil {
		t.Fatal(err)
	}
	if failedEvents != 0 {
		t.Fatalf("old terminal events = %d, want 0", failedEvents)
	}
	current, err := service.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("new CreateAutomatic() error = %v", err)
	}
	if current.Resolution.ID == created.Resolution.ID {
		t.Fatal("new generation reused the old resolution identity")
	}
	code, message := "agent_request_timeout", "late old terminal"
	_, lateErr := db.New(fixture.pool).MarkAgentResolutionOperationTerminal(fixture.ctx, db.MarkAgentResolutionOperationTerminalParams{
		Status: string(domain.AgentResolutionFailed), ErrorCode: &code, ErrorMessage: &message,
		ID: repository.UUIDToPG(created.Resolution.ID), OperationID: repository.UUIDToPG(created.Operation.ID),
	})
	if !errors.Is(lateErr, pgx.ErrNoRows) {
		t.Fatalf("late old terminal error = %v, want no rows", lateErr)
	}
}

// TestSubtitleAgentStaleConfigurationCannotSupersedeNewOwnerIntegration 覆盖
// 等待 S/T 锁期间配置已更新的旧请求：不得 supersede 新配置 owner。
func TestSubtitleAgentStaleConfigurationCannotSupersedeNewOwnerIntegration(t *testing.T) {
	fixture := newSubtitleLeaseFixture(t)
	v1 := fixture.service(subtitleLeaseConfiguration(1, "lease-model-v1", "https://provider.invalid/v1"))
	first, err := v1.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("v1 CreateAutomatic() error = %v", err)
	}
	v2 := fixture.service(subtitleLeaseConfiguration(2, "lease-model-v2", "https://provider.invalid/v1"))
	second, err := v2.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("v2 CreateAutomatic() error = %v", err)
	}
	if second.Resolution.ID == first.Resolution.ID {
		t.Fatal("new configuration must create a new identity")
	}
	firstRow := fixture.resolution(t, first.Resolution.ID)
	if firstRow.Status != string(domain.AgentResolutionCancelled) {
		t.Fatalf("old owner status = %s, want superseded cancelled", firstRow.Status)
	}
	_, staleErr := v1.CreateAutomatic(fixture.ctx, fixture.request())
	var serviceErr *Error
	if !errors.As(staleErr, &serviceErr) || serviceErr.Code != "state_conflict" {
		t.Fatalf("stale v1 CreateAutomatic() error = %v, want state_conflict", staleErr)
	}
	alt := fixture.service(subtitleLeaseConfiguration(2, "lease-model-alt", "https://provider.invalid/v1"))
	_, altErr := alt.CreateAutomatic(fixture.ctx, fixture.request())
	if !errors.As(altErr, &serviceErr) || serviceErr.Code != "state_conflict" {
		t.Fatalf("same-version different-config CreateAutomatic() error = %v, want state_conflict", altErr)
	}
	owner := fixture.resolution(t, second.Resolution.ID)
	if owner.Status != string(domain.AgentResolutionQueued) || repository.UUIDFromPG(owner.OperationID) != second.Operation.ID {
		t.Fatalf("new owner changed: status=%s operation=%s", owner.Status, repository.UUIDFromPG(owner.OperationID))
	}
	var operations int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM operations WHERE kind = 'agent.resolve' AND resource_id = $1`, repository.UUIDToPG(second.Resolution.ID)).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 1 {
		t.Fatalf("new owner operations = %d, want 1", operations)
	}
}

// TestSubtitleAgentVideoReadyDoesNotReplaceOwnerIntegration 覆盖正常 VideoReady
// 只推进 task.version 时不替换同内容/同配置 owner；真实内容变化才换代。
func TestSubtitleAgentVideoReadyDoesNotReplaceOwnerIntegration(t *testing.T) {
	fixture := newSubtitleLeaseFixture(t)
	service := fixture.service(subtitleLeaseConfiguration(1, "lease-model", "https://provider.invalid/v1"))
	created, err := service.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("CreateAutomatic() error = %v", err)
	}
	var beforeVersion int32
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT version FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(fixture.taskID)).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE episode_tasks SET version = version + 1 WHERE id = $1`, repository.UUIDToPG(fixture.taskID)); err != nil {
		t.Fatal(err)
	}
	repeated, err := service.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("VideoReady duplicate CreateAutomatic() error = %v", err)
	}
	if repeated.Resolution.ID != created.Resolution.ID || repeated.Operation.ID != created.Operation.ID || repeated.Resolution.Version != created.Resolution.Version {
		t.Fatalf("VideoReady replaced owner: %s/%s version %d, want %s/%s version %d", repeated.Resolution.ID, repeated.Operation.ID, repeated.Resolution.Version, created.Resolution.ID, created.Operation.ID, created.Resolution.Version)
	}
	var operations int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM operations WHERE kind = 'agent.resolve' AND resource_id = $1`, repository.UUIDToPG(created.Resolution.ID)).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 1 {
		t.Fatalf("VideoReady duplicate operations = %d, want 1", operations)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE subtitle_video_match_candidates SET title = 'changed after VideoReady' WHERE scope_id = $1 AND candidate_id = 'embedded-2'`, repository.UUIDToPG(fixture.scopeID)); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := service.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("content change CreateAutomatic() error = %v", err)
	}
	if rebuilt.Resolution.ID == created.Resolution.ID {
		t.Fatal("content change must create a new generation")
	}
	old := fixture.resolution(t, created.Resolution.ID)
	if old.Status != string(domain.AgentResolutionCancelled) {
		t.Fatalf("old owner status = %s, want superseded cancelled", old.Status)
	}
}

// TestSubtitleAgentDelayedSnapshotRejectedIntegration 分离验证：快照在事务外
// 采集后 scope 复用；事务内用同一 Tx queries 重读内容必须拒绝旧快照，不得
// 创建/重排任何租约。
func TestSubtitleAgentDelayedSnapshotRejectedIntegration(t *testing.T) {
	fixture := newSubtitleLeaseFixture(t)
	configurationSource := subtitleLeaseConfiguration(1, "lease-model", "https://provider.invalid/v1")
	configuration, err := configurationSource.Load(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings := configuration.Settings.Agent.WithDefaults()
	origin := providerOrigin(settings.BaseURL)
	promptVersion, ok := agentharness.PromptVersion(domain.AgentCapabilitySubtitleVideoMatch)
	if !ok {
		t.Fatal("missing subtitle prompt version")
	}
	service := newSubtitleLeaseService(fixture.pool, fixture.transactor, fixture.scheduler, configurationSource)
	stale, err := service.buildAgentContext(fixture.ctx, domain.AgentCapabilitySubtitleVideoMatch, fixture.scopeID)
	if err != nil {
		t.Fatalf("build stale snapshot error = %v", err)
	}
	if err := fixture.workflow.ExpireSubtitleVideoMatchScope(fixture.ctx, fixture.scopeID); err != nil {
		t.Fatalf("ExpireSubtitleVideoMatchScope() error = %v", err)
	}
	if _, err := fixture.workflow.CreateSubtitleVideoMatchScope(fixture.ctx, fixture.taskID, []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-6", Source: domain.SubtitleSourceEmbedded, StreamIndex: 6, Format: domain.SubtitleASS, Language: "chi", Title: "快照后新代一"},
		{CandidateID: "embedded-7", Source: domain.SubtitleSourceEmbedded, StreamIndex: 7, Format: domain.SubtitleASS, Language: "chi", Title: "快照后新代二"},
	}); err != nil {
		t.Fatalf("CreateSubtitleVideoMatchScope() error = %v", err)
	}
	_, err = service.createAutomaticWithSnapshot(fixture.ctx, fixture.request(), configuration, settings, origin, promptVersion, stale, uuid.New(), "stale-snapshot-probe", time.Minute)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "state_conflict" {
		t.Fatalf("stale snapshot CreateAutomatic error = %v, want state_conflict", err)
	}
	var resolutions, operations int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM agent_resolutions WHERE resource_type = 'episode_task' AND resource_id = $1`, repository.UUIDToPG(fixture.scopeID)).Scan(&resolutions); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM operations WHERE resource_type = 'agent_resolution' AND resource_id IN (SELECT id FROM agent_resolutions WHERE resource_id = $1)`, repository.UUIDToPG(fixture.scopeID)).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if resolutions != 0 || operations != 0 {
		t.Fatalf("stale snapshot side effects = resolutions %d operations %d", resolutions, operations)
	}
	current, err := service.CreateAutomatic(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("fresh CreateAutomatic() error = %v", err)
	}
	if current.Resolution.Status != domain.AgentResolutionQueued {
		t.Fatalf("fresh resolution status = %s, want queued", current.Resolution.Status)
	}
}
