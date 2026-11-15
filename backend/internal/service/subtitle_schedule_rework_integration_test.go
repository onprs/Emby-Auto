//go:build integration

package service

// 返修回归：scope 已提交但 Agent 调度失败 → 重试后必须重新调度或明确失败；
// 恢复上限 → 返回专用错误且无新 operation。基于真实 service/SQL/事务路径。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func reworkSubtitleResolver() agentLifecycleConfiguration {
	configuration := domain.Configuration{Version: 1}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.RSSCoordinateMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.SubtitleVideoMatchMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.BaseURL = "https://agent.example/v1"
	configuration.Settings.Agent.Model = "test-model"
	configuration.Settings.Agent.RequestTimeoutSeconds = 60
	return agentLifecycleConfiguration{load: func() (domain.Configuration, error) { return configuration, nil }}
}

func TestReworkRequeueCapReturnsDedicatedErrorIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	seriesID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, title) VALUES ($1, 'Agent requeue cap rework');
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($2, $1, 'Agent requeue cap rework', 'https://example.test/requeue-cap-rw.xml', true, 900, 1);
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons)
VALUES ($3, $2, 'requeue-cap-rw', 'Unnumbered release', 'https://example.test/release.torrent', false, ARRAY['episode_not_detected']::text[]);
`, seriesID, subscriptionID, entryID); err != nil {
		t.Fatal(err)
	}
	resolver := reworkSubtitleResolver()
	service := NewAgentResolutionService(db.New(pool), transactor, scheduler, resolver, nil, nil)
	request := AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilityRSSCoordinate, ResourceID: entryID}
	created, err := service.CreateAutomatic(ctx, request)
	if err != nil {
		t.Fatalf("initial CreateAutomatic() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_resolutions SET status = 'cancelled', version = 10, completed_at = now() WHERE id = $1`, created.Resolution.ID); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateAutomatic(ctx, request)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "agent_resolution_requeue_exhausted" {
		t.Fatalf("capped CreateAutomatic() error = %v, want agent_resolution_requeue_exhausted", err)
	}
	var operationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_type = 'agent_resolution' AND resource_id = $1`, created.Resolution.ID).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if operationCount != 1 {
		t.Fatalf("operations after capped requeue = %d, want only the original", operationCount)
	}
}

func TestReworkScheduleFailureRecoversOnRetryIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	seriesID, taskID, scopeID := uuid.New(), uuid.New(), uuid.New()
	var fileID, profileID, acquisitionID, downloadID uuid.UUID
	acquisitionID, downloadID = uuid.New(), uuid.New()
	fileID, profileID = uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, title) VALUES ($1, 'Subtitle schedule rework');
INSERT INTO acquisitions (id, series_id, source_kind, source_uri)
VALUES ($2, $1, 'manual', 'https://example.test/subtitle-rw.torrent');
INSERT INTO downloads (id, acquisition_id, status, save_path)
VALUES ($3, $2, 'completed', $4);
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($5, $3, 0, 'Show.S01E01.mkv', 1024, 'video', true);
INSERT INTO transcode_profiles (
    id, name, version, video_codec, encoder, container, file_extension, quality_mode, quality_value, audio_policy, preset, pixel_format
) VALUES ($6, $7, 1, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 23, 'copy', 'medium', 'yuv420p');
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($8, $2, $5, $6, 'processing', 'transcoding', 'extracting_or_converting');
INSERT INTO subtitle_video_match_scopes (id, task_id) VALUES ($9, $8);
INSERT INTO subtitle_video_match_candidates (scope_id, candidate_id, source, stream_index, format, language, title)
VALUES ($9, 'embedded-2', 'embedded', 2, 'ass', 'chi', '简日双语'),
       ($9, 'embedded-3', 'embedded', 3, 'ass', 'chi', '繁日雙語');
`, seriesID, acquisitionID, downloadID, t.TempDir(), fileID, profileID, "subtitle-rw-"+profileID.String(), taskID, scopeID); err != nil {
		t.Fatal(err)
	}
	queries := db.New(pool)
	workflow := NewMediaWorkflow(queries, transactor, scheduler)

	// 场景一：scope 存在但没有决议/操作（调度失败的孤儿范围）。
	state, err := workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
	if err != nil {
		t.Fatalf("GetSubtitleVideoMatchSchedulingState() error = %v", err)
	}
	if state.ActiveResolution() {
		t.Fatalf("orphan scope reported an active resolution: %#v", state)
	}
	if state.CoversGeneration() {
		t.Fatal("orphan scope must not claim coverage without a unique active owner")
	}

	// 模拟第一次调度失败后用户重试：作废范围并复活（换候选集），随后调度成功。
	expireSubtitleVideoMatchScopeForTest(t, ctx, queries, scopeID)
	revived, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-4", Source: domain.SubtitleSourceEmbedded, StreamIndex: 4, Format: domain.SubtitleASS, Language: "chi", Title: "另一轨"},
		{CandidateID: "embedded-5", Source: domain.SubtitleSourceEmbedded, StreamIndex: 5, Format: domain.SubtitleASS, Language: "chi", Title: "第三轨"},
	})
	if err != nil {
		t.Fatalf("revived CreateSubtitleVideoMatchScope() error = %v", err)
	}
	if revived != scopeID {
		t.Fatalf("revived scope = %s, want %s", revived, scopeID)
	}
	created, err := NewAgentResolutionService(queries, transactor, scheduler, reworkSubtitleResolver(), nil, nil).CreateAutomatic(ctx, AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: scopeID,
	})
	if err != nil {
		t.Fatalf("CreateAutomatic() after revive error = %v", err)
	}
	state, err = workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.CoversGeneration() {
		t.Fatalf("scheduling state after successful schedule = %#v", state)
	}
	if state.ResolutionID == nil || *state.ResolutionID != created.Resolution.ID {
		t.Fatalf("scheduling state resolution = %v, want %s", state.ResolutionID, created.Resolution.ID)
	}
	if state.CancellationRequested {
		t.Fatal("fresh schedule must not report a pending cancellation")
	}

	// 正常 VideoReady/task.version 推进不改变唯一活动 owner；重复请求不新建代。
	if _, err := pool.Exec(ctx, `UPDATE episode_tasks SET version = version + 1 WHERE id = $1`, taskID); err != nil {
		t.Fatal(err)
	}
	state, err = workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.CoversGeneration() || state.ResolutionID == nil || *state.ResolutionID != created.Resolution.ID {
		t.Fatalf("VideoReady task.version advance must not discard the current owner: %#v", state)
	}
	var operationsBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'agent.resolve' AND resource_id = $1`, repository.UUIDToPG(created.Resolution.ID)).Scan(&operationsBefore); err != nil {
		t.Fatal(err)
	}
	if operationsBefore != 1 {
		t.Fatalf("agent operations after VideoReady = %d, want 1", operationsBefore)
	}

	// 真正换代：scope 复用替换候选并 supersede 旧 owner；新 CreateAutomatic 之前
	// 存在无 owner 窗口，旧 operation 晚到终态不得回收新 scope/任务。
	expireSubtitleVideoMatchScopeForTest(t, ctx, queries, scopeID)
	if _, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-6", Source: domain.SubtitleSourceEmbedded, StreamIndex: 6, Format: domain.SubtitleASS, Language: "chi", Title: "新代轨道"},
		{CandidateID: "embedded-7", Source: domain.SubtitleSourceEmbedded, StreamIndex: 7, Format: domain.SubtitleASS, Language: "chi", Title: "新代第二轨"},
	}); err != nil {
		t.Fatalf("new generation CreateSubtitleVideoMatchScope() error = %v", err)
	}
	state, err = workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CoversGeneration() || state.ActiveOwnerCount != 0 {
		t.Fatalf("no-owner window must not report coverage: %#v", state)
	}
	oldCode, oldMessage := "agent_request_timeout", "late old generation terminal"
	_, lateErr := db.New(pool).MarkAgentResolutionOperationTerminal(ctx, db.MarkAgentResolutionOperationTerminalParams{
		Status: string(domain.AgentResolutionFailed), ErrorCode: &oldCode, ErrorMessage: &oldMessage,
		ID: repository.UUIDToPG(created.Resolution.ID), OperationID: repository.UUIDToPG(created.Operation.ID),
	})
	if !errors.Is(lateErr, pgx.ErrNoRows) {
		t.Fatalf("late old terminal error = %v, want no rows", lateErr)
	}
	var taskState, subtitleState, scopeStatus string
	if err := pool.QueryRow(ctx, `
SELECT task.state, task.subtitle_state, scope.status
FROM episode_tasks AS task JOIN subtitle_video_match_scopes AS scope ON scope.task_id = task.id
WHERE task.id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState, &scopeStatus); err != nil {
		t.Fatal(err)
	}
	if taskState != string(domain.TaskProcessing) || subtitleState != string(domain.SubtitleExtractingConverting) || scopeStatus != "pending" {
		t.Fatalf("late old terminal changed new generation: task=%s/%s scope=%s", taskState, subtitleState, scopeStatus)
	}

	// 新请求在当前锁内重新验证内容并取得当前租约。
	current, err := NewAgentResolutionService(queries, transactor, scheduler, reworkSubtitleResolver(), nil, nil).CreateAutomatic(ctx, AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: scopeID,
	})
	if err != nil {
		t.Fatalf("CreateAutomatic() for new generation error = %v", err)
	}
	state, err = workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.CoversGeneration() || state.ResolutionID == nil || *state.ResolutionID != current.Resolution.ID {
		t.Fatalf("new generation owner = %#v, want %s", state, current.Resolution.ID)
	}
	_, lateErr = db.New(pool).MarkAgentResolutionOperationTerminal(ctx, db.MarkAgentResolutionOperationTerminalParams{
		Status: string(domain.AgentResolutionFailed), ErrorCode: &oldCode, ErrorMessage: &oldMessage,
		ID: repository.UUIDToPG(created.Resolution.ID), OperationID: repository.UUIDToPG(created.Operation.ID),
	})
	if !errors.Is(lateErr, pgx.ErrNoRows) {
		t.Fatalf("late old terminal with new owner error = %v, want no rows", lateErr)
	}
	state, err = workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.CoversGeneration() || state.ResolutionID == nil || *state.ResolutionID != current.Resolution.ID {
		t.Fatalf("late old terminal reclaimed new owner: %#v", state)
	}
}
