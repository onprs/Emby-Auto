//go:build integration

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestAgentResolutionQueueIsIdempotentAndRebuildsAfterCancelIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	seriesID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, title) VALUES ($1, 'Agent queue identity');
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($2, $1, 'Agent queue identity', 'https://example.test/queue.xml', true, 900, 1);
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons)
VALUES ($3, $2, 'queue-entry', 'Unnumbered release', 'https://example.test/release.torrent', false, ARRAY['episode_not_detected']::text[]);
`, seriesID, subscriptionID, entryID); err != nil {
		t.Fatal(err)
	}
	configuration := domain.Configuration{Version: 1}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.RSSCoordinateMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.BaseURL = "https://agent.example/v1"
	configuration.Settings.Agent.Model = "test-model"
	configuration.Settings.Agent.RequestTimeoutSeconds = 60
	resolver := agentLifecycleConfiguration{load: func() (domain.Configuration, error) { return configuration, nil }}
	service := NewAgentResolutionService(db.New(pool), transactor, scheduler, resolver, nil, nil)
	request := AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilityRSSCoordinate, ResourceID: entryID}
	created, err := service.CreateAutomatic(ctx, request)
	if err != nil {
		t.Fatalf("CreateAutomatic() error = %v", err)
	}
	if created.Resolution.Status != domain.AgentResolutionQueued {
		t.Fatalf("created status = %s", created.Resolution.Status)
	}
	assertQueuedEventCount(t, ctx, pool, created.Resolution.ID, 1)
	var timeoutSeconds int
	if err := pool.QueryRow(ctx, `SELECT timeout_seconds FROM operations WHERE id = $1`, created.Operation.ID).Scan(&timeoutSeconds); err != nil {
		t.Fatal(err)
	}
	// 默认 6 步预算 × 60 秒单次请求 + 30 秒余量；整作业时限不再等于单请求超时。
	if timeoutSeconds != 390 {
		t.Fatalf("operation timeout = %d seconds, want 390", timeoutSeconds)
	}

	repeated, err := service.CreateAutomatic(ctx, request)
	if err != nil {
		t.Fatalf("repeated CreateAutomatic() error = %v", err)
	}
	if repeated.Resolution.ID != created.Resolution.ID || repeated.Operation.ID != created.Operation.ID {
		t.Fatalf("repeated resolution/operation = %s/%s, want %s/%s", repeated.Resolution.ID, repeated.Operation.ID, created.Resolution.ID, created.Operation.ID)
	}
	// 身份命中且未重新排队时只复用决议，不重复写“已排队”事件。
	assertQueuedEventCount(t, ctx, pool, created.Resolution.ID, 1)

	lifecycle := repository.NewOperationLifecycle(db.New(pool), transactor)
	if _, err := lifecycle.StartAttempt(ctx, created.Operation.ID, 1, "queue-identity-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := lifecycle.CancelAttempt(ctx, created.Operation.ID, 1); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	cancelled, err := service.Get(ctx, created.Resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != domain.AgentResolutionCancelled {
		t.Fatalf("cancelled status = %s", cancelled.Status)
	}

	rebuilt, err := service.CreateAutomatic(ctx, request)
	if err != nil {
		t.Fatalf("rebuilt CreateAutomatic() error = %v", err)
	}
	if rebuilt.Resolution.ID != created.Resolution.ID {
		t.Fatalf("rebuilt resolution = %s, want the same identity %s", rebuilt.Resolution.ID, created.Resolution.ID)
	}
	if rebuilt.Resolution.Status != domain.AgentResolutionQueued || rebuilt.Operation.ID == created.Operation.ID {
		t.Fatalf("rebuilt resolution/operation = %s/%s", rebuilt.Resolution.Status, rebuilt.Operation.ID)
	}
	var operationStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, rebuilt.Operation.ID).Scan(&operationStatus); err != nil {
		t.Fatal(err)
	}
	if operationStatus != "queued" {
		t.Fatalf("rebuilt operation status = %s", operationStatus)
	}
	assertQueuedEventCount(t, ctx, pool, created.Resolution.ID, 2)
}

func TestSubtitleAgentTerminalStatesConvergeTaskIntegration(t *testing.T) {
	for _, test := range []struct {
		name           string
		cancel         bool
		wantResolution domain.AgentResolutionStatus
		wantTask       domain.TaskState
		wantSubtitle   domain.SubtitleState
		wantErrorCode  string
		wantEventTopic string
	}{
		{
			name: "failure", wantResolution: domain.AgentResolutionFailed, wantTask: domain.TaskFailed,
			wantSubtitle: domain.SubtitleFailed, wantErrorCode: "agent_request_timeout", wantEventTopic: "task.media_failed",
		},
		{
			name: "cancellation", cancel: true, wantResolution: domain.AgentResolutionCancelled, wantTask: domain.TaskCancelled,
			wantSubtitle: domain.SubtitleCancelled, wantEventTopic: "task.media_cancelled",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, pool := testutil.NewMigratedPostgres(t)
			transactor := database.NewTransactor(pool)
			fixture := seedSubtitleAgentFixture(t, ctx, pool)
			lifecycle := repository.NewOperationLifecycle(db.New(pool), transactor)
			if test.cancel {
				if err := lifecycle.CancelAttempt(ctx, fixture.operationID, 1); err != nil {
					t.Fatalf("CancelAttempt() error = %v", err)
				}
			} else if err := lifecycle.FailAttempt(ctx, fixture.operationID, 1, "agent_request_timeout", "Agent resolution failed", false); err != nil {
				t.Fatalf("FailAttempt() error = %v", err)
			}

			var resolutionStatus, scopeStatus, taskState, subtitleState string
			var errorCode *string
			if err := pool.QueryRow(ctx, `
SELECT resolution.status, scope.status, task.state, task.subtitle_state, resolution.error_code
FROM agent_resolutions AS resolution
JOIN subtitle_video_match_scopes AS scope ON scope.id = resolution.resource_id
JOIN episode_tasks AS task ON task.id = scope.task_id
WHERE resolution.id = $1
`, fixture.resolutionID).Scan(&resolutionStatus, &scopeStatus, &taskState, &subtitleState, &errorCode); err != nil {
				t.Fatal(err)
			}
			if resolutionStatus != string(test.wantResolution) || scopeStatus != "expired" || taskState != string(test.wantTask) || subtitleState != string(test.wantSubtitle) {
				t.Fatalf("terminal state = resolution %s scope %s task %s/%s", resolutionStatus, scopeStatus, taskState, subtitleState)
			}
			if test.wantErrorCode != "" && (errorCode == nil || *errorCode != test.wantErrorCode) {
				t.Fatalf("resolution error code = %v, want %s", errorCode, test.wantErrorCode)
			}
			var eventCount int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = $1 AND resource_id = $2`, test.wantEventTopic, fixture.taskID).Scan(&eventCount); err != nil {
				t.Fatal(err)
			}
			if eventCount != 1 {
				t.Fatalf("%s event count = %d", test.wantEventTopic, eventCount)
			}
			// 等待中的任务不能停留在 processing：重试可以从失败的媒体分支重新进入。
			if test.wantTask == domain.TaskFailed {
				var version int32
				if err := pool.QueryRow(ctx, `SELECT version FROM episode_tasks WHERE id = $1`, fixture.taskID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				if _, err := db.New(pool).RequeueTaskFailedMediaBranches(ctx, db.RequeueTaskFailedMediaBranchesParams{
					ID: repository.UUIDToPG(fixture.taskID), ExpectedVersion: version,
				}); err != nil {
					t.Fatalf("RequeueTaskFailedMediaBranches() error = %v", err)
				}
			}
			revivedScopeID, err := NewMediaWorkflow(db.New(pool), transactor, nil).CreateSubtitleVideoMatchScope(ctx, fixture.taskID, subtitleMatchCandidatesForTest())
			var mediaErr *domain.MediaWorkflowError
			if errors.As(err, &mediaErr) && mediaErr.Code == "subtitle_scope_conflict" {
				t.Fatalf("CreateSubtitleVideoMatchScope() conflict after terminal Agent state: %v", err)
			}
			if err != nil {
				t.Fatalf("CreateSubtitleVideoMatchScope() error = %v", err)
			}
			if revivedScopeID != fixture.scopeID {
				t.Fatalf("revived scope = %s, want %s", revivedScopeID, fixture.scopeID)
			}
			var revivedStatus string
			var candidateCount int
			if err := pool.QueryRow(ctx, `
SELECT scope.status, count(candidate.candidate_id)
FROM subtitle_video_match_scopes AS scope
LEFT JOIN subtitle_video_match_candidates AS candidate ON candidate.scope_id = scope.id
WHERE scope.id = $1
GROUP BY scope.status
`, fixture.scopeID).Scan(&revivedStatus, &candidateCount); err != nil {
				t.Fatal(err)
			}
			if revivedStatus != "pending" || candidateCount != 2 {
				t.Fatalf("revived scope = %s with %d candidates", revivedStatus, candidateCount)
			}
		})
	}
}

type subtitleAgentFixture struct {
	taskID       uuid.UUID
	scopeID      uuid.UUID
	resolutionID uuid.UUID
	operationID  uuid.UUID
}

func TestEpisodeMappingAgentResolutionRebuildsIdentityAfterCancelIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	seriesID, seasonID, episodeID := uuid.New(), uuid.New(), uuid.New()
	acquisitionID, downloadID, fileID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Agent Mapping identity');
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($3, $1, 1, 1);
INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($4, $3, 1, 'Episode 1');
INSERT INTO acquisitions (id, series_id, source_kind, source_uri)
VALUES ($5, $1, 'manual', 'https://example.test/mapping-identity.torrent');
INSERT INTO downloads (id, acquisition_id, status) VALUES ($6, $5, 'completed');
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind, selected,
    source_season, source_episode
) VALUES ($7, $6, 0, 'Show.S01E01.mkv', 1000, 'video', true, 1, 1);
`, seriesID, time.Now().UnixNano(), seasonID, episodeID, acquisitionID, downloadID, fileID); err != nil {
		t.Fatal(err)
	}
	configuration := domain.Configuration{Version: 1}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.EpisodeMappingEnabled = true
	configuration.Settings.Agent.BaseURL = "https://agent.example/v1"
	configuration.Settings.Agent.Model = "test-model"
	resolver := agentLifecycleConfiguration{load: func() (domain.Configuration, error) { return configuration, nil }}
	service := NewAgentResolutionService(db.New(pool), transactor, scheduler, resolver, agentCatalogStub{}, nil)
	request := AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilityEpisodeMapping, ResourceID: acquisitionID}
	created, err := service.CreateAutomatic(ctx, request)
	if err != nil {
		t.Fatalf("CreateAutomatic() error = %v", err)
	}
	lifecycle := repository.NewOperationLifecycle(db.New(pool), transactor)
	if _, err := lifecycle.StartAttempt(ctx, created.Operation.ID, 1, "mapping-identity-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := lifecycle.CancelAttempt(ctx, created.Operation.ID, 1); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	rebuilt, err := service.CreateAutomatic(ctx, request)
	if err != nil {
		t.Fatalf("rebuilt CreateAutomatic() error = %v", err)
	}
	if rebuilt.Resolution.ID != created.Resolution.ID || rebuilt.Operation.ID == created.Operation.ID {
		t.Fatalf("rebuilt identity/operation = %s/%s, original %s/%s", rebuilt.Resolution.ID, rebuilt.Operation.ID, created.Resolution.ID, created.Operation.ID)
	}
	if rebuilt.Resolution.Status != domain.AgentResolutionQueued || rebuilt.Resolution.Capability != domain.AgentCapabilityEpisodeMapping {
		t.Fatalf("rebuilt resolution = %s/%s", rebuilt.Resolution.Capability, rebuilt.Resolution.Status)
	}
}

func TestSubtitleAgentProviderFailureConvergesTaskIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	taskID, scopeID := seedSubtitleAgentEntities(t, ctx, pool)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"error":"temporarily unavailable"}`))
	}))
	defer server.Close()
	configuration := domain.Configuration{Version: 1}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.SubtitleVideoMatchMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.BaseURL = server.URL + "/v1"
	configuration.Settings.Agent.Model = "test-model"
	resolver := agentLifecycleConfiguration{load: func() (domain.Configuration, error) { return configuration, nil }}
	service := NewAgentResolutionService(db.New(pool), transactor, scheduler, resolver, nil, nil)
	created, err := service.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: scopeID})
	if err != nil {
		t.Fatalf("CreateAutomatic() error = %v", err)
	}
	lifecycle := repository.NewOperationLifecycle(db.New(pool), transactor)
	operation, err := lifecycle.StartAttempt(ctx, created.Operation.ID, 1, "subtitle-agent-worker")
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	runErr := service.Run(ctx, operation)
	var agentErr *AgentRunError
	if !errors.As(runErr, &agentErr) || agentErr.Code != "agent_provider_unavailable" || !agentErr.Retryable {
		t.Fatalf("Run() error = %v, want retryable provider failure", runErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}
	// River 重试用尽后操作永久失败，资源必须同事务收敛。
	if err := lifecycle.FailAttempt(ctx, created.Operation.ID, 1, agentErr.Code, agentErr.Message, false); err != nil {
		t.Fatalf("FailAttempt() error = %v", err)
	}
	var resolutionStatus, scopeStatus, taskState, subtitleState string
	if err := pool.QueryRow(ctx, `
SELECT resolution.status, scope.status, task.state, task.subtitle_state
FROM agent_resolutions AS resolution
JOIN subtitle_video_match_scopes AS scope ON scope.id = resolution.resource_id
JOIN episode_tasks AS task ON task.id = scope.task_id
WHERE resolution.id = $1 AND task.id = $2
`, created.Resolution.ID, taskID).Scan(&resolutionStatus, &scopeStatus, &taskState, &subtitleState); err != nil {
		t.Fatal(err)
	}
	if resolutionStatus != string(domain.AgentResolutionFailed) || scopeStatus != "expired" || taskState != string(domain.TaskFailed) || subtitleState != string(domain.SubtitleFailed) {
		t.Fatalf("terminal state = resolution %s scope %s task %s/%s", resolutionStatus, scopeStatus, taskState, subtitleState)
	}
}

func seedSubtitleAgentEntities(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (taskID, scopeID uuid.UUID) {
	t.Helper()
	taskID, scopeID = uuid.New(), uuid.New()
	seriesID, acquisitionID, downloadID, fileID, profileID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, title) VALUES ($1, 'Subtitle Agent convergence');
INSERT INTO acquisitions (id, series_id, source_kind, source_uri)
VALUES ($2, $1, 'manual', 'https://example.test/subtitle-agent.torrent');
INSERT INTO downloads (id, acquisition_id, status, save_path)
VALUES ($3, $2, 'completed', $4);
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($5, $3, 0, 'Show.S01E01.mkv', 1024, 'video', true);
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container,
    file_extension, quality_mode, quality_value, audio_policy, preset,
    pixel_format, thread_count, max_concurrency
) VALUES ($6, $7, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1);
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($8, $2, $5, $6, 'processing', 'transcoding', 'extracting_or_converting');
INSERT INTO subtitle_video_match_scopes (id, task_id) VALUES ($9, $8);
INSERT INTO subtitle_video_match_candidates (scope_id, candidate_id, source, stream_index, format, language, title)
VALUES ($9, 'embedded-2', 'embedded', 2, 'ass', 'chi', '简日双语'),
       ($9, 'embedded-3', 'embedded', 3, 'ass', 'chi', '繁日雙語');
`, seriesID, acquisitionID, downloadID, t.TempDir(), fileID, profileID, "subtitle-agent-"+profileID.String(), taskID, scopeID); err != nil {
		t.Fatal(err)
	}
	return taskID, scopeID
}

func seedSubtitleAgentFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) subtitleAgentFixture {
	t.Helper()
	fixture := subtitleAgentFixture{resolutionID: uuid.New(), operationID: uuid.New()}
	fixture.taskID, fixture.scopeID = seedSubtitleAgentEntities(t, ctx, pool)
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, attempt_count, timeout_seconds, started_at, heartbeat_at
) VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'running', 3, 1, 60, now(), now());
INSERT INTO operation_attempts (id, operation_id, attempt, status, worker_id)
VALUES ($4, $1, 1, 'running', 'subtitle-agent-worker');
INSERT INTO agent_resolutions (
    id, operation_id, capability, resource_type, resource_id, trigger, status,
    input_fingerprint, configuration_version, protocol, provider_origin, model,
    prompt_version, toolset_version, started_at
) VALUES ($2, $1, 'subtitle_video_match', 'episode_task', $5, 'automatic', 'running',
    $6, 1, 'openai_chat_completions', 'https://agent.example', 'test-model',
    'subtitle-video-match-v1', 'agent-tools-v1', now());
`, fixture.operationID, fixture.resolutionID, "subtitle-agent-"+fixture.resolutionID.String(), uuid.New(), fixture.scopeID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func subtitleMatchCandidatesForTest() []domain.SubtitleMatchCandidate {
	return []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-2", Source: domain.SubtitleSourceEmbedded, StreamIndex: 2, Format: domain.SubtitleASS, Language: "chi", Title: "简日双语"},
		{CandidateID: "embedded-3", Source: domain.SubtitleSourceEmbedded, StreamIndex: 3, Format: domain.SubtitleASS, Language: "chi", Title: "繁日雙語"},
	}
}

func assertQueuedEventCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, resolutionID uuid.UUID, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM events
WHERE topic = 'agent.resolution_queued'
  AND resource_id = (SELECT resource_id FROM agent_resolutions WHERE id = $1)
`, resolutionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("agent.resolution_queued event count = %d, want %d", count, want)
	}
}
