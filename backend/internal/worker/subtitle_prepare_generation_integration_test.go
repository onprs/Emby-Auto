//go:build integration

package worker

// 真实字幕入口回归：SubtitlePrepareHandler → MediaWorkflow → AgentResolutionService
// → PG。字幕匹配期间视频分支正常完成会递增 episode_tasks.version；该推进不是
// 字幕代际变化，Agent 终态必须仍能收敛等待中的任务。

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type subtitleGenerationJobInserter struct {
	nextID int64
}

func (inserter *subtitleGenerationJobInserter) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	inserter.nextID++
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: inserter.nextID}}, nil
}

type subtitleGenerationConfiguration struct {
	configuration domain.Configuration
}

func (stub subtitleGenerationConfiguration) Load(context.Context) (domain.Configuration, error) {
	return stub.configuration, nil
}

func (subtitleGenerationConfiguration) ResolveSecret(context.Context, string) (string, error) {
	return "probe-key", nil
}

func seedSubtitleGenerationTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	taskID := uuid.New()
	seriesID, seasonID, episodeID := uuid.New(), uuid.New(), uuid.New()
	mappingProfileID, mappingID := uuid.New(), uuid.New()
	acquisitionID, downloadID, fileID, profileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	savePath := t.TempDir()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, title) VALUES ($1, 'Subtitle generation series');
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($2, $1, 1, 1);
INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($3, $2, 1, 'Pilot');
INSERT INTO episode_mapping_profiles (id, series_id, name, version, active, decision_source)
VALUES ($4, $1, 'Subtitle generation profile', 1, true, 'legacy');
INSERT INTO episode_mappings (
    id, profile_id, source_season, source_episode, target_episode_id, mapping_status, match_source
) VALUES ($5, $4, 1, 1, $3, 'mapped', 'explicit');
INSERT INTO acquisitions (id, series_id, source_kind, source_uri)
VALUES ($6, $1, 'manual', 'https://example.test/subtitle-generation.torrent');
INSERT INTO downloads (id, acquisition_id, status, save_path) VALUES ($7, $6, 'completed', $8);
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind, selected
) VALUES ($9, $7, 0, 'Show.S01E01.mkv', 1024, 'video', true);
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container,
    file_extension, quality_mode, quality_value, audio_policy, preset,
    pixel_format, thread_count, max_concurrency
) VALUES ($10, $11, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1);
INSERT INTO episode_tasks (
    id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id,
    state, video_state, subtitle_state, media_type
) VALUES ($12, $6, $9, $5, $10, 'processing', 'transcoding', 'subtitle_queued', 'episode');
`, seriesID, seasonID, episodeID, mappingProfileID, mappingID, acquisitionID, downloadID, savePath, fileID, profileID, "subtitle-gen-"+profileID.String(), taskID); err != nil {
		t.Fatal(err)
	}
	return taskID
}

type subtitleGenerationFixture struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	handler   *SubtitlePrepareHandler
	lifecycle *repository.OperationLifecycle
	scheduler *service.OperationScheduler
	taskID    uuid.UUID
}

func newSubtitleGenerationFixture(t *testing.T) *subtitleGenerationFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := service.NewOperationScheduler(transactor, &subtitleGenerationJobInserter{})
	taskID := seedSubtitleGenerationTask(t, ctx, pool)
	configuration := domain.Configuration{Version: 1}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.SubtitleVideoMatchMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.BaseURL = "https://agent.example/v1"
	configuration.Settings.Agent.Model = "subtitle-generation-model"
	configuration.Settings.Agent.RequestTimeoutSeconds = 60
	workflow := service.NewMediaWorkflow(db.New(pool), transactor, scheduler)
	agent := service.NewAgentResolutionService(db.New(pool), transactor, scheduler, subtitleGenerationConfiguration{configuration: configuration}, nil, nil)
	tools := &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) {
		return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
			{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi", Title: "简日双语"},
			{Index: 3, Type: "subtitle", Codec: "ass", Language: "chi", Title: "繁日雙語"},
		}}, nil
	}}
	handler := NewSubtitlePrepareHandler(mediaTestConfiguration(t.TempDir()), tools, workflow, agent)
	return &subtitleGenerationFixture{
		ctx: ctx, pool: pool, handler: handler, scheduler: scheduler, taskID: taskID,
		lifecycle: repository.NewOperationLifecycle(db.New(pool), transactor),
	}
}

type subtitleGenerationState struct {
	taskVersion       int32
	resolutionVersion int32
	resolutionStatus  string
	scopeStatus       string
	taskState         string
	subtitleState     string
	operationID       uuid.UUID
	resolutionID      uuid.UUID
	scopeID           uuid.UUID
}

func (fixture *subtitleGenerationFixture) scheduling(t *testing.T) subtitleGenerationState {
	t.Helper()
	var state subtitleGenerationState
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT task.version, resolution.resource_version, resolution.status, scope.status,
       task.state, task.subtitle_state, resolution.operation_id, resolution.id, scope.id
FROM episode_tasks AS task
JOIN subtitle_video_match_scopes AS scope ON scope.task_id = task.id
JOIN agent_resolutions AS resolution ON resolution.resource_id = scope.id AND resolution.capability = 'subtitle_video_match'
WHERE task.id = $1
ORDER BY resolution.created_at DESC, resolution.id DESC
LIMIT 1
`, fixture.taskID).Scan(
		&state.taskVersion, &state.resolutionVersion, &state.resolutionStatus, &state.scopeStatus,
		&state.taskState, &state.subtitleState, &state.operationID, &state.resolutionID, &state.scopeID,
	); err != nil {
		t.Fatalf("load scheduling state: %v", err)
	}
	return state
}

func (fixture *subtitleGenerationFixture) scheduleAgent(t *testing.T) subtitleGenerationState {
	t.Helper()
	scheduled, err := fixture.scheduler.Schedule(fixture.ctx, service.ScheduleOperationRequest{
		Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: fixture.taskID,
		IdempotencyKey: "phase2-generation-handler-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
		Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("schedule subtitle.prepare: %v", err)
	}
	started, err := fixture.lifecycle.StartAttempt(fixture.ctx, scheduled.Operation.ID, 1, "subtitle-generation-handler")
	if err != nil {
		t.Fatalf("start subtitle.prepare: %v", err)
	}
	if started.SubtitleAttempt == nil {
		t.Fatal("subtitle.prepare StartAttempt did not capture the attempt handle")
	}
	if err := fixture.handler.Handle(fixture.ctx, started); err != nil {
		t.Fatalf("SubtitlePrepareHandler.Handle() error = %v", err)
	}
	if err := fixture.lifecycle.SucceedSubtitleAttempt(fixture.ctx, *started.SubtitleAttempt); err != nil {
		t.Fatalf("SucceedSubtitleAttempt() error = %v", err)
	}
	state := fixture.scheduling(t)
	if state.resolutionVersion == 0 || state.resolutionVersion != state.taskVersion {
		t.Fatalf("resolution resource_version = %d, task version = %d, want equal non-zero", state.resolutionVersion, state.taskVersion)
	}
	if state.scopeStatus != "pending" || state.resolutionStatus != string(domain.AgentResolutionQueued) {
		t.Fatalf("scheduled scope/resolution = %s/%s, want pending/queued", state.scopeStatus, state.resolutionStatus)
	}
	return state
}

func (fixture *subtitleGenerationFixture) terminalEventCount(t *testing.T, topic string) int {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT count(*) FROM events WHERE topic = $1 AND resource_id = $2
`, topic, fixture.taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestSubtitlePrepareAgentFailureAfterVideoReadyConvergesTaskIntegration 覆盖
// 正常转码完成后 Agent 才失败：任务必须离开 processing 并记录终态事件。
func TestSubtitlePrepareAgentFailureAfterVideoReadyConvergesTaskIntegration(t *testing.T) {
	fixture := newSubtitleGenerationFixture(t)
	scheduled := fixture.scheduleAgent(t)
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, scheduled.operationID, 1, "subtitle-generation-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if _, err := db.New(fixture.pool).MarkTaskVideoReady(fixture.ctx, repository.UUIDToPG(fixture.taskID)); err != nil {
		t.Fatalf("MarkTaskVideoReady() error = %v", err)
	}
	afterVideo := fixture.scheduling(t)
	if afterVideo.taskVersion != scheduled.taskVersion+1 {
		t.Fatalf("task version after video ready = %d, want %d", afterVideo.taskVersion, scheduled.taskVersion+1)
	}
	if err := fixture.lifecycle.FailAttempt(fixture.ctx, scheduled.operationID, 1, "agent_request_timeout", "Agent resolution failed", false); err != nil {
		t.Fatalf("FailAttempt() error = %v", err)
	}
	state := fixture.scheduling(t)
	if state.resolutionStatus != string(domain.AgentResolutionFailed) || state.scopeStatus != "expired" ||
		state.taskState != string(domain.TaskFailed) || state.subtitleState != string(domain.SubtitleFailed) {
		t.Fatalf("terminal state = resolution %s scope %s task %s/%s", state.resolutionStatus, state.scopeStatus, state.taskState, state.subtitleState)
	}
	if got := fixture.terminalEventCount(t, "task.media_failed"); got != 1 {
		t.Fatalf("task.media_failed events = %d, want 1", got)
	}
	// 失败后任务可以重试：复活范围并换用新候选集，重新调度 Agent。
	fixture.retryTask(t)
}

func (fixture *subtitleGenerationFixture) retryTask(t *testing.T) {
	t.Helper()
	before := fixture.scheduling(t)
	if _, err := db.New(fixture.pool).RequeueTaskFailedMediaBranches(fixture.ctx, db.RequeueTaskFailedMediaBranchesParams{
		ID: repository.UUIDToPG(fixture.taskID), ExpectedVersion: before.taskVersion,
	}); err != nil {
		t.Fatalf("RequeueTaskFailedMediaBranches() error = %v", err)
	}
	rescheduled := fixture.scheduleAgent(t)
	if rescheduled.resolutionID == before.resolutionID {
		t.Fatal("task retry must create a new subtitle Agent generation")
	}
	if rescheduled.taskState != string(domain.TaskProcessing) || rescheduled.subtitleState != string(domain.SubtitleExtractingConverting) {
		t.Fatalf("task after retry = %s/%s, want processing/extracting_or_converting", rescheduled.taskState, rescheduled.subtitleState)
	}
}

// TestSubtitlePrepareAgentFailureBeforeVideoReadyKeepsTerminalStateIntegration
// 覆盖 Agent 先失败、视频随后完成：终态保持，视频完成不得复活字幕分支。
func TestSubtitlePrepareAgentFailureBeforeVideoReadyKeepsTerminalStateIntegration(t *testing.T) {
	fixture := newSubtitleGenerationFixture(t)
	scheduled := fixture.scheduleAgent(t)
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, scheduled.operationID, 1, "subtitle-generation-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := fixture.lifecycle.FailAttempt(fixture.ctx, scheduled.operationID, 1, "agent_request_timeout", "Agent resolution failed", false); err != nil {
		t.Fatalf("FailAttempt() error = %v", err)
	}
	converged := fixture.scheduling(t)
	if converged.taskState != string(domain.TaskFailed) || converged.subtitleState != string(domain.SubtitleFailed) ||
		converged.scopeStatus != "expired" || converged.resolutionStatus != string(domain.AgentResolutionFailed) {
		t.Fatalf("converged state = resolution %s scope %s task %s/%s", converged.resolutionStatus, converged.scopeStatus, converged.taskState, converged.subtitleState)
	}
	if _, err := db.New(fixture.pool).MarkTaskVideoReady(fixture.ctx, repository.UUIDToPG(fixture.taskID)); err != nil {
		t.Fatalf("MarkTaskVideoReady() error = %v", err)
	}
	afterVideo := fixture.scheduling(t)
	if afterVideo.taskState != string(domain.TaskFailed) || afterVideo.subtitleState != string(domain.SubtitleFailed) {
		t.Fatalf("video ready changed terminal subtitle state to %s/%s", afterVideo.taskState, afterVideo.subtitleState)
	}
	if got := fixture.terminalEventCount(t, "task.media_failed"); got != 1 {
		t.Fatalf("task.media_failed events after video ready = %d, want 1", got)
	}
	fixture.retryTask(t)
}

// TestSubtitlePrepareAgentCancellationAfterVideoReadyConvergesTaskIntegration
// 覆盖视频完成后再取消：任务进入取消终态，范围作废。
func TestSubtitlePrepareAgentCancellationAfterVideoReadyConvergesTaskIntegration(t *testing.T) {
	fixture := newSubtitleGenerationFixture(t)
	scheduled := fixture.scheduleAgent(t)
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, scheduled.operationID, 1, "subtitle-generation-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if _, err := db.New(fixture.pool).MarkTaskVideoReady(fixture.ctx, repository.UUIDToPG(fixture.taskID)); err != nil {
		t.Fatalf("MarkTaskVideoReady() error = %v", err)
	}
	if err := fixture.lifecycle.CancelAttempt(fixture.ctx, scheduled.operationID, 1); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	state := fixture.scheduling(t)
	if state.resolutionStatus != string(domain.AgentResolutionCancelled) || state.scopeStatus != "expired" ||
		state.taskState != string(domain.TaskCancelled) || state.subtitleState != string(domain.SubtitleCancelled) {
		t.Fatalf("cancelled state = resolution %s scope %s task %s/%s", state.resolutionStatus, state.scopeStatus, state.taskState, state.subtitleState)
	}
	if got := fixture.terminalEventCount(t, "task.media_cancelled"); got != 1 {
		t.Fatalf("task.media_cancelled events = %d, want 1", got)
	}
}
