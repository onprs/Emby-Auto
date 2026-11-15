//go:build integration

package service

// 升级兼容回归：base 版本为 agent.resolve 同一身份写入 60 秒 operation，
// head 按步骤预算写入 390 秒。升级后同一身份再次自动调度时，必须先命中
// 既有决议并按状态复用/重排，而不是拿新命令去撞旧 operation 的幂等键。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

const (
	legacyAgentOperationTimeout = 60
	headAgentOperationTimeout   = 390
)

type upgradeCompatibilityFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	service    *AgentResolutionService
	lifecycle  *repository.OperationLifecycle
	entryID    uuid.UUID
	request    AutomaticAgentResolutionRequest
	created    AgentResolutionCommandResult
	legacyOpID uuid.UUID
}

func newUpgradeCompatibilityFixture(t *testing.T) *upgradeCompatibilityFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	seriesID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, title) VALUES ($1, 'Agent upgrade compatibility');
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($2, $1, 'Agent upgrade compatibility', 'https://example.test/upgrade-compat.xml', true, 900, 1);
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons)
VALUES ($3, $2, 'upgrade-compat-entry', 'Unnumbered release', 'https://example.test/release.torrent', false, ARRAY['episode_not_detected']::text[]);
`, seriesID, subscriptionID, entryID); err != nil {
		t.Fatal(err)
	}
	configuration := domain.Configuration{Version: 1}
	configuration.Settings.Agent = domain.DefaultAgentSettings()
	configuration.Settings.Agent.Enabled = true
	configuration.Settings.Agent.RSSCoordinateMode = domain.AgentResolutionValidatedAuto
	configuration.Settings.Agent.BaseURL = "https://agent.example/v1"
	configuration.Settings.Agent.Model = "upgrade-model"
	configuration.Settings.Agent.RequestTimeoutSeconds = 60
	resolver := agentLifecycleConfiguration{load: func() (domain.Configuration, error) { return configuration, nil }}
	service := NewAgentResolutionService(db.New(pool), transactor, scheduler, resolver, nil, nil)
	request := AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilityRSSCoordinate, ResourceID: entryID}
	created, err := service.CreateAutomatic(ctx, request)
	if err != nil {
		t.Fatalf("initial CreateAutomatic() error = %v", err)
	}
	fixture := &upgradeCompatibilityFixture{
		ctx: ctx, pool: pool, service: service, entryID: entryID, request: request,
		created: created, lifecycle: repository.NewOperationLifecycle(db.New(pool), transactor),
	}
	return fixture
}

// baseOperationShape 把真实创建的 operation 调整为 base 版本写入的形态：
// 命令内容不变，仅整作业时限等于单请求超时（默认 60 秒）。
func (fixture *upgradeCompatibilityFixture) baseOperationShape(t *testing.T, operationID uuid.UUID) {
	t.Helper()
	tag, err := fixture.pool.Exec(fixture.ctx, `
UPDATE operations
SET timeout_seconds = $2
WHERE id = $1 AND kind = 'agent.resolve' AND timeout_seconds = $3
`, repository.UUIDToPG(operationID), legacyAgentOperationTimeout, headAgentOperationTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("legacy operation shape matched %d rows, want 1", tag.RowsAffected())
	}
	fixture.legacyOpID = operationID
}

func (fixture *upgradeCompatibilityFixture) agentOperationIDs(t *testing.T, resolutionID uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := fixture.pool.Query(fixture.ctx, `
SELECT id FROM operations
WHERE kind = 'agent.resolve' AND resource_type = 'agent_resolution' AND resource_id = $1
ORDER BY created_at, id
`, repository.UUIDToPG(resolutionID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := make([]uuid.UUID, 0, 2)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func (fixture *upgradeCompatibilityFixture) operation(t *testing.T, operationID uuid.UUID) db.Operation {
	t.Helper()
	operation, err := db.New(fixture.pool).GetOperation(fixture.ctx, repository.UUIDToPG(operationID))
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func (fixture *upgradeCompatibilityFixture) resolution(t *testing.T, resolutionID uuid.UUID) db.AgentResolution {
	t.Helper()
	resolution, err := db.New(fixture.pool).GetAgentResolution(fixture.ctx, repository.UUIDToPG(resolutionID))
	if err != nil {
		t.Fatal(err)
	}
	return resolution
}

func assertQueuedEventForOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, operationID uuid.UUID, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM events WHERE topic = 'agent.resolution_queued' AND operation_id = $1
`, repository.UUIDToPG(operationID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("agent.resolution_queued events for operation %s = %d, want %d", operationID, count, want)
	}
}

// TestAgentResolutionLegacyTimeoutActiveCommandIsReusedIntegration 覆盖升级前
// 仍在排队/运行的 60 秒 operation：同一身份不能因预算不同被判成不同命令。
func TestAgentResolutionLegacyTimeoutActiveCommandIsReusedIntegration(t *testing.T) {
	for _, running := range []bool{false, true} {
		name := "queued"
		if running {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newUpgradeCompatibilityFixture(t)
			fixture.baseOperationShape(t, fixture.created.Operation.ID)
			if running {
				if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, fixture.created.Operation.ID, 1, "legacy-active-worker"); err != nil {
					t.Fatalf("StartAttempt() error = %v", err)
				}
			}
			reused, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
			if err != nil {
				t.Fatalf("CreateAutomatic() over a legacy %s operation error = %v", name, err)
			}
			if reused.Resolution.ID != fixture.created.Resolution.ID || reused.Operation.ID != fixture.created.Operation.ID {
				t.Fatalf("reused identity/operation = %s/%s, want %s/%s", reused.Resolution.ID, reused.Operation.ID, fixture.created.Resolution.ID, fixture.created.Operation.ID)
			}
			if reused.Resolution.Version != fixture.created.Resolution.Version || reused.Resolution.Status != domain.AgentResolutionQueued {
				t.Fatalf("reused resolution = version %d status %s, want unchanged queued version %d", reused.Resolution.Version, reused.Resolution.Status, fixture.created.Resolution.Version)
			}
			operations := fixture.agentOperationIDs(t, fixture.created.Resolution.ID)
			if len(operations) != 1 || operations[0] != fixture.created.Operation.ID {
				t.Fatalf("operations = %v, want only the legacy operation", operations)
			}
			legacy := fixture.operation(t, fixture.created.Operation.ID)
			if legacy.TimeoutSeconds != legacyAgentOperationTimeout {
				t.Fatalf("legacy operation timeout = %d, want unchanged %d", legacy.TimeoutSeconds, legacyAgentOperationTimeout)
			}
			assertQueuedEventForOperation(t, fixture.ctx, fixture.pool, fixture.created.Operation.ID, 1)
		})
	}
}

// TestAgentResolutionLegacyTimeoutCancelRequeuesIntegration 覆盖升级前已取消
// 的 60 秒 operation：同身份必须以新 operation 重新排队。
func TestAgentResolutionLegacyTimeoutCancelRequeuesIntegration(t *testing.T) {
	fixture := newUpgradeCompatibilityFixture(t)
	fixture.baseOperationShape(t, fixture.created.Operation.ID)
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, fixture.created.Operation.ID, 1, "legacy-cancel-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := fixture.lifecycle.CancelAttempt(fixture.ctx, fixture.created.Operation.ID, 1); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	cancelled := fixture.resolution(t, fixture.created.Resolution.ID)
	if cancelled.Status != string(domain.AgentResolutionCancelled) {
		t.Fatalf("legacy resolution status = %s, want cancelled", cancelled.Status)
	}

	rebuilt, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
	if err != nil {
		t.Fatalf("CreateAutomatic() after legacy cancellation error = %v", err)
	}
	if rebuilt.Resolution.ID != fixture.created.Resolution.ID || rebuilt.Operation.ID == fixture.created.Operation.ID {
		t.Fatalf("rebuilt identity/operation = %s/%s, want same resolution and new operation", rebuilt.Resolution.ID, rebuilt.Operation.ID)
	}
	if rebuilt.Resolution.Status != domain.AgentResolutionQueued || rebuilt.Resolution.Version != int(cancelled.Version)+1 {
		t.Fatalf("rebuilt resolution = %s version %d, want queued version %d", rebuilt.Resolution.Status, rebuilt.Resolution.Version, int(cancelled.Version)+1)
	}
	rebuiltOperation := fixture.operation(t, rebuilt.Operation.ID)
	if rebuiltOperation.TimeoutSeconds != headAgentOperationTimeout || rebuiltOperation.Status != "queued" {
		t.Fatalf("rebuilt operation = status %s timeout %d, want queued/%d", rebuiltOperation.Status, rebuiltOperation.TimeoutSeconds, headAgentOperationTimeout)
	}
	operations := fixture.agentOperationIDs(t, fixture.created.Resolution.ID)
	if len(operations) != 2 {
		t.Fatalf("operations = %v, want the legacy and rebuilt operation", operations)
	}
	if fixture.operation(t, fixture.created.Operation.ID).TimeoutSeconds != legacyAgentOperationTimeout {
		t.Fatal("legacy operation timeout was rewritten")
	}
	assertQueuedEventForOperation(t, fixture.ctx, fixture.pool, fixture.created.Operation.ID, 1)
	assertQueuedEventForOperation(t, fixture.ctx, fixture.pool, rebuilt.Operation.ID, 1)
}

// TestAgentResolutionLegacyTimeoutExpiredRequeuesIntegration 覆盖升级前已
// 过期（例如配置或上下文变化被 finishInactive 终止）的 60 秒 operation。
func TestAgentResolutionLegacyTimeoutExpiredRequeuesIntegration(t *testing.T) {
	fixture := newUpgradeCompatibilityFixture(t)
	fixture.baseOperationShape(t, fixture.created.Operation.ID)
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, fixture.created.Operation.ID, 1, "legacy-expired-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	resolution, err := fixture.service.Get(fixture.ctx, fixture.created.Resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.finishInactive(fixture.ctx, resolution, fixture.created.Operation.ID, resolution.Version, domain.AgentResolutionExpired, "agent_resolution_stale", "legacy context changed"); err != nil {
		t.Fatalf("finishInactive() error = %v", err)
	}
	if err := fixture.lifecycle.SucceedAttempt(fixture.ctx, fixture.created.Operation.ID, 1); err != nil {
		t.Fatalf("SucceedAttempt() error = %v", err)
	}
	expired := fixture.resolution(t, fixture.created.Resolution.ID)
	if expired.Status != string(domain.AgentResolutionExpired) {
		t.Fatalf("legacy resolution status = %s, want expired", expired.Status)
	}

	rebuilt, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
	if err != nil {
		t.Fatalf("CreateAutomatic() after legacy expiration error = %v", err)
	}
	if rebuilt.Resolution.ID != fixture.created.Resolution.ID || rebuilt.Operation.ID == fixture.created.Operation.ID {
		t.Fatalf("rebuilt identity/operation = %s/%s, want same resolution and new operation", rebuilt.Resolution.ID, rebuilt.Operation.ID)
	}
	if rebuilt.Resolution.Status != domain.AgentResolutionQueued || rebuilt.Resolution.Version != int(expired.Version)+1 {
		t.Fatalf("rebuilt resolution = %s version %d, want queued version %d", rebuilt.Resolution.Status, rebuilt.Resolution.Version, int(expired.Version)+1)
	}
	if got := fixture.operation(t, rebuilt.Operation.ID).TimeoutSeconds; got != headAgentOperationTimeout {
		t.Fatalf("rebuilt operation timeout = %d, want %d", got, headAgentOperationTimeout)
	}
	assertQueuedEventForOperation(t, fixture.ctx, fixture.pool, rebuilt.Operation.ID, 1)
}

// TestAgentResolutionLegacyTimeoutTerminalIdentityIsStableIntegration 覆盖升级
// 前已经成功或失败的身份：再次自动调度只返回既有决议，不新建 operation/事件。
func TestAgentResolutionLegacyTimeoutTerminalIdentityIsStableIntegration(t *testing.T) {
	for _, applied := range []bool{true, false} {
		name := "failed"
		if applied {
			name = "applied"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newUpgradeCompatibilityFixture(t)
			fixture.baseOperationShape(t, fixture.created.Operation.ID)
			if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, fixture.created.Operation.ID, 1, "legacy-terminal-worker"); err != nil {
				t.Fatalf("StartAttempt() error = %v", err)
			}
			if applied {
				started, err := db.New(fixture.pool).StartAgentResolution(fixture.ctx, db.StartAgentResolutionParams{
					ID: repository.UUIDToPG(fixture.created.Resolution.ID), OperationID: repository.UUIDToPG(fixture.created.Operation.ID),
					ExpectedVersion: int32(fixture.created.Resolution.Version),
				})
				if err != nil {
					t.Fatalf("StartAgentResolution() error = %v", err)
				}
				if _, err := db.New(fixture.pool).CompleteAgentResolution(fixture.ctx, db.CompleteAgentResolutionParams{
					Status: string(domain.AgentResolutionApplied), Validation: []byte(`{}`), ID: started.ID,
				}); err != nil {
					t.Fatalf("CompleteAgentResolution() error = %v", err)
				}
				if err := fixture.lifecycle.SucceedAttempt(fixture.ctx, fixture.created.Operation.ID, 1); err != nil {
					t.Fatalf("SucceedAttempt() error = %v", err)
				}
			} else {
				if err := fixture.lifecycle.FailAttempt(fixture.ctx, fixture.created.Operation.ID, 1, "agent_tool_scope_violation", "legacy failure", false); err != nil {
					t.Fatalf("FailAttempt() error = %v", err)
				}
			}
			terminal := fixture.resolution(t, fixture.created.Resolution.ID)
			wantStatus := string(domain.AgentResolutionFailed)
			if applied {
				wantStatus = string(domain.AgentResolutionApplied)
			}
			if terminal.Status != wantStatus {
				t.Fatalf("terminal status = %s, want %s", terminal.Status, wantStatus)
			}

			reused, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
			if err != nil {
				t.Fatalf("CreateAutomatic() over a legacy %s identity error = %v", name, err)
			}
			if reused.Resolution.ID != fixture.created.Resolution.ID || reused.Operation.ID != fixture.created.Operation.ID {
				t.Fatalf("reused identity/operation = %s/%s, want %s/%s", reused.Resolution.ID, reused.Operation.ID, fixture.created.Resolution.ID, fixture.created.Operation.ID)
			}
			if string(reused.Resolution.Status) != terminal.Status || reused.Resolution.Version != int(terminal.Version) {
				t.Fatalf("reused resolution = %s version %d, want unchanged %s version %d", reused.Resolution.Status, reused.Resolution.Version, terminal.Status, terminal.Version)
			}
			operations := fixture.agentOperationIDs(t, fixture.created.Resolution.ID)
			if len(operations) != 1 || operations[0] != fixture.created.Operation.ID {
				t.Fatalf("operations = %v, want only the legacy operation", operations)
			}
			if got := fixture.operation(t, fixture.created.Operation.ID).TimeoutSeconds; got != legacyAgentOperationTimeout {
				t.Fatalf("legacy operation timeout = %d, want unchanged %d", got, legacyAgentOperationTimeout)
			}
			assertQueuedEventForOperation(t, fixture.ctx, fixture.pool, fixture.created.Operation.ID, 1)
		})
	}
}

// TestAgentResolutionRepeatedRequeueIsIdempotentIntegration 覆盖重排后再次
// 自动调度：必须复用重排 operation，不得撞原 operation 或重复写事件。
func TestAgentResolutionRepeatedRequeueIsIdempotentIntegration(t *testing.T) {
	fixture := newUpgradeCompatibilityFixture(t)
	fixture.baseOperationShape(t, fixture.created.Operation.ID)
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, fixture.created.Operation.ID, 1, "legacy-repeat-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := fixture.lifecycle.CancelAttempt(fixture.ctx, fixture.created.Operation.ID, 1); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	first, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
	if err != nil {
		t.Fatalf("first rebuild error = %v", err)
	}
	second, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
	if err != nil {
		t.Fatalf("repeated CreateAutomatic() after requeue error = %v", err)
	}
	if second.Resolution.ID != first.Resolution.ID || second.Operation.ID != first.Operation.ID {
		t.Fatalf("repeated identity/operation = %s/%s, want %s/%s", second.Resolution.ID, second.Operation.ID, first.Resolution.ID, first.Operation.ID)
	}
	if second.Resolution.Status != domain.AgentResolutionQueued || second.Resolution.Version != first.Resolution.Version {
		t.Fatalf("repeated resolution = %s version %d, want unchanged queued version %d", second.Resolution.Status, second.Resolution.Version, first.Resolution.Version)
	}
	operations := fixture.agentOperationIDs(t, fixture.created.Resolution.ID)
	if len(operations) != 2 {
		t.Fatalf("operations = %v, want the legacy and one requeue operation", operations)
	}
	assertQueuedEventForOperation(t, fixture.ctx, fixture.pool, first.Operation.ID, 1)
}

// TestAgentResolutionLegacyTimeoutIdentityChangesIntegration 覆盖配置或资源
// 确实变化时身份必须更新：保留旧历史，另建新决议与 operation。
func TestAgentResolutionLegacyTimeoutIdentityChangesIntegration(t *testing.T) {
	for _, changeConfiguration := range []bool{true, false} {
		name := "resource_changed"
		if changeConfiguration {
			name = "configuration_changed"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newUpgradeCompatibilityFixture(t)
			fixture.baseOperationShape(t, fixture.created.Operation.ID)
			if changeConfiguration {
				changed := domain.Configuration{Version: 2}
				changed.Settings.Agent = domain.DefaultAgentSettings()
				changed.Settings.Agent.Enabled = true
				changed.Settings.Agent.RSSCoordinateMode = domain.AgentResolutionValidatedAuto
				changed.Settings.Agent.BaseURL = "https://agent.example/v1"
				changed.Settings.Agent.Model = "upgrade-model-v2"
				changed.Settings.Agent.RequestTimeoutSeconds = 60
				fixture.service.configuration = agentLifecycleConfiguration{load: func() (domain.Configuration, error) { return changed, nil }}
			} else {
				if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE rss_entries SET title = 'Changed unnumbered release' WHERE id = $1`, fixture.entryID); err != nil {
					t.Fatal(err)
				}
			}
			created, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
			if err != nil {
				t.Fatalf("CreateAutomatic() after %s error = %v", name, err)
			}
			if created.Resolution.ID == fixture.created.Resolution.ID || created.Operation.ID == fixture.created.Operation.ID {
				t.Fatalf("%s reused the legacy identity/operation", name)
			}
			if created.Resolution.Status != domain.AgentResolutionQueued || created.Resolution.Version != 1 {
				t.Fatalf("new resolution = %s version %d, want queued version 1", created.Resolution.Status, created.Resolution.Version)
			}
			if got := fixture.operation(t, created.Operation.ID).TimeoutSeconds; got != headAgentOperationTimeout {
				t.Fatalf("new operation timeout = %d, want %d", got, headAgentOperationTimeout)
			}
			legacy := fixture.resolution(t, fixture.created.Resolution.ID)
			if legacy.Status != string(domain.AgentResolutionQueued) || legacy.Version != int32(fixture.created.Resolution.Version) {
				t.Fatalf("legacy resolution changed to %s version %d", legacy.Status, legacy.Version)
			}
			if got := fixture.operation(t, fixture.created.Operation.ID).TimeoutSeconds; got != legacyAgentOperationTimeout {
				t.Fatalf("legacy operation timeout = %d, want unchanged %d", got, legacyAgentOperationTimeout)
			}
		})
	}
}

// TestAgentResolutionLegacyTimeoutExhaustedRequeueDoesNotCreateOperationIntegration// 覆盖旧身份已到恢复上限：明确返回专用错误，且不留下新的 operation。
func TestAgentResolutionLegacyTimeoutExhaustedRequeueDoesNotCreateOperationIntegration(t *testing.T) {
	fixture := newUpgradeCompatibilityFixture(t)
	fixture.baseOperationShape(t, fixture.created.Operation.ID)
	if _, err := fixture.pool.Exec(fixture.ctx, `
UPDATE agent_resolutions
SET status = 'cancelled', version = 10, completed_at = now()
WHERE id = $1
`, repository.UUIDToPG(fixture.created.Resolution.ID)); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "agent_resolution_requeue_exhausted" {
		t.Fatalf("CreateAutomatic() error = %v, want agent_resolution_requeue_exhausted", err)
	}
	operations := fixture.agentOperationIDs(t, fixture.created.Resolution.ID)
	if len(operations) != 1 || operations[0] != fixture.created.Operation.ID {
		t.Fatalf("operations = %v, want only the legacy operation", operations)
	}
	if got := fixture.resolution(t, fixture.created.Resolution.ID).Status; got != string(domain.AgentResolutionCancelled) {
		t.Fatalf("resolution status = %s, want cancelled", got)
	}
}

// TestSubtitleAgentLegacyTimeoutExpiredRequeuesIntegration 覆盖字幕路径的
// 升级兼容：范围已作废、任务版本未变化时，base 写入的 60 秒 operation 不能
// 阻断同一身份的重新排队。
func TestSubtitleAgentLegacyTimeoutExpiredRequeuesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	taskID, scopeID := seedSubtitleAgentEntities(t, ctx, pool)
	service := NewAgentResolutionService(db.New(pool), transactor, scheduler, reworkSubtitleResolver(), nil, nil)
	request := AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: scopeID}
	created, err := service.CreateAutomatic(ctx, request)
	if err != nil {
		t.Fatalf("initial CreateAutomatic() error = %v", err)
	}
	if created.Resolution.ResourceVersion == nil || *created.Resolution.ResourceVersion <= 0 {
		t.Fatalf("subtitle resolution resource_version = %v, want non-nil task version", created.Resolution.ResourceVersion)
	}
	workflow := NewMediaWorkflow(db.New(pool), transactor, scheduler)
	expireSubtitleVideoMatchScopeForTest(t, ctx, db.New(pool), scopeID)
	lifecycle := repository.NewOperationLifecycle(db.New(pool), transactor)
	if _, err := lifecycle.StartAttempt(ctx, created.Operation.ID, 1, "legacy-subtitle-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := lifecycle.CancelAttempt(ctx, created.Operation.ID, 1); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	cancelled, err := db.New(pool).GetAgentResolution(ctx, repository.UUIDToPG(created.Resolution.ID))
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != string(domain.AgentResolutionCancelled) {
		t.Fatalf("legacy subtitle resolution status = %s, want cancelled", cancelled.Status)
	}
	tag, err := pool.Exec(ctx, `UPDATE operations SET timeout_seconds = $2 WHERE id = $1 AND timeout_seconds = $3`,
		repository.UUIDToPG(created.Operation.ID), legacyAgentOperationTimeout, headAgentOperationTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("legacy subtitle operation shape matched %d rows, want 1", tag.RowsAffected())
	}
	// 文字幕任务重试会以同一候选集复活已作废范围；任务版本此时不变。
	if _, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, subtitleMatchCandidatesForTest()); err != nil {
		t.Fatalf("CreateSubtitleVideoMatchScope() error = %v", err)
	}
	rebuilt, err := service.CreateAutomatic(ctx, request)
	if err != nil {
		t.Fatalf("CreateAutomatic() over legacy subtitle operation error = %v", err)
	}
	if rebuilt.Resolution.ID != created.Resolution.ID || rebuilt.Operation.ID == created.Operation.ID {
		t.Fatalf("rebuilt identity/operation = %s/%s, want same resolution and new operation", rebuilt.Resolution.ID, rebuilt.Operation.ID)
	}
	if rebuilt.Resolution.Status != domain.AgentResolutionQueued || rebuilt.Resolution.Version != int(cancelled.Version)+1 {
		t.Fatalf("rebuilt subtitle resolution = %s version %d, want queued version %d", rebuilt.Resolution.Status, rebuilt.Resolution.Version, int(cancelled.Version)+1)
	}
	var timeoutSeconds int
	if err := pool.QueryRow(ctx, `SELECT timeout_seconds FROM operations WHERE id = $1`, repository.UUIDToPG(rebuilt.Operation.ID)).Scan(&timeoutSeconds); err != nil {
		t.Fatal(err)
	}
	if timeoutSeconds != headAgentOperationTimeout {
		t.Fatalf("rebuilt subtitle operation timeout = %d, want %d", timeoutSeconds, headAgentOperationTimeout)
	}
	if got := seededTaskSubtitleState(t, ctx, pool, taskID); got != string(domain.SubtitleExtractingConverting) {
		t.Fatalf("task subtitle state = %s, want extracting_or_converting (task version unchanged)", got)
	}
}

func seededTaskSubtitleState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(ctx, `SELECT subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// TestAgentResolutionLateOldOperationCannotTerminalizeRequeuedResolutionIntegration
// 覆盖重排后 operation_id 已变化的负例：旧 operation 的晚到终态回调不能把
// 已被新 operation 接管的决议再次改成失败/过期。
func TestAgentResolutionLateOldOperationCannotTerminalizeRequeuedResolutionIntegration(t *testing.T) {
	fixture := newUpgradeCompatibilityFixture(t)
	oldOperationID := fixture.created.Operation.ID
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, oldOperationID, 1, "late-old-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := fixture.lifecycle.CancelAttempt(fixture.ctx, oldOperationID, 1); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	requeued, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
	if err != nil {
		t.Fatalf("CreateAutomatic() requeue error = %v", err)
	}
	if requeued.Operation.ID == oldOperationID {
		t.Fatal("requeue reused the old operation")
	}
	current := fixture.resolution(t, requeued.Resolution.ID)
	if current.Status != string(domain.AgentResolutionQueued) || repository.UUIDFromPG(current.OperationID) != requeued.Operation.ID {
		t.Fatalf("requeued resolution = %s operation %s, want queued operation %s", current.Status, repository.UUIDFromPG(current.OperationID), requeued.Operation.ID)
	}

	// 旧 operation 刚结束的晚到回调：repository 终态入口必须按 operation_id 拒绝。
	code, message := "agent_request_timeout", "late old operation terminal callback"
	_, terminalErr := db.New(fixture.pool).MarkAgentResolutionOperationTerminal(fixture.ctx, db.MarkAgentResolutionOperationTerminalParams{
		Status: string(domain.AgentResolutionFailed), ErrorCode: &code, ErrorMessage: &message,
		ID: repository.UUIDToPG(requeued.Resolution.ID), OperationID: repository.UUIDToPG(oldOperationID),
	})
	if !errors.Is(terminalErr, pgx.ErrNoRows) {
		t.Fatalf("late old operation terminal error = %v, want no rows", terminalErr)
	}
	// 旧 Run 的 finishInactive 同样不能回收新 operation 接管的决议。
	if err := fixture.service.finishInactive(fixture.ctx, requeued.Resolution, oldOperationID, fixture.created.Resolution.Version, domain.AgentResolutionExpired, "agent_resolution_stale", "late old execution"); err != nil {
		t.Fatalf("late old finishInactive() error = %v", err)
	}
	unchanged := fixture.resolution(t, requeued.Resolution.ID)
	if unchanged.Status != string(domain.AgentResolutionQueued) || unchanged.Version != int32(requeued.Resolution.Version) ||
		repository.UUIDFromPG(unchanged.OperationID) != requeued.Operation.ID {
		t.Fatalf("late old terminal changed the requeued resolution: %s version %d operation %s", unchanged.Status, unchanged.Version, repository.UUIDFromPG(unchanged.OperationID))
	}
	assertQueuedEventForOperation(t, fixture.ctx, fixture.pool, requeued.Operation.ID, 1)

	// 当前 operation 仍是合法 owner：其终态必须生效。
	if err := fixture.service.finishInactive(fixture.ctx, requeued.Resolution, requeued.Operation.ID, requeued.Resolution.Version, domain.AgentResolutionExpired, "agent_resolution_stale", "current execution ended"); err != nil {
		t.Fatalf("current finishInactive() error = %v", err)
	}
	terminal := fixture.resolution(t, requeued.Resolution.ID)
	if terminal.Status != string(domain.AgentResolutionExpired) || terminal.Version != int32(requeued.Resolution.Version)+1 {
		t.Fatalf("owner terminal state = %s version %d, want expired version %d", terminal.Status, terminal.Version, int(requeued.Resolution.Version)+1)
	}
}

// TestAgentResolutionLegacyTimeoutProposedCommandIsReusedIntegration 覆盖已
// 保存提案（proposed）的历史 operation：重入只复用，不重排、不重复写事件。
func TestAgentResolutionLegacyTimeoutProposedCommandIsReusedIntegration(t *testing.T) {
	fixture := newUpgradeCompatibilityFixture(t)
	fixture.baseOperationShape(t, fixture.created.Operation.ID)
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, fixture.created.Operation.ID, 1, "legacy-proposed-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	started, err := db.New(fixture.pool).StartAgentResolution(fixture.ctx, db.StartAgentResolutionParams{
		ID: repository.UUIDToPG(fixture.created.Resolution.ID), OperationID: repository.UUIDToPG(fixture.created.Operation.ID),
		ExpectedVersion: int32(fixture.created.Resolution.Version),
	})
	if err != nil {
		t.Fatalf("StartAgentResolution() error = %v", err)
	}
	if _, err := db.New(fixture.pool).SaveAgentResolutionProposal(fixture.ctx, db.SaveAgentResolutionProposalParams{
		Proposal: []byte(`{"decision":"resolved"}`), ToolCallCount: 0, ID: started.ID,
		OperationID: repository.UUIDToPG(fixture.created.Operation.ID), ExpectedVersion: started.Version,
	}); err != nil {
		t.Fatalf("SaveAgentResolutionProposal() error = %v", err)
	}
	proposed := fixture.resolution(t, fixture.created.Resolution.ID)
	if proposed.Status != string(domain.AgentResolutionProposed) {
		t.Fatalf("legacy resolution status = %s, want proposed", proposed.Status)
	}
	reused, err := fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
	if err != nil {
		t.Fatalf("CreateAutomatic() over a legacy proposed operation error = %v", err)
	}
	if reused.Resolution.ID != fixture.created.Resolution.ID || reused.Operation.ID != fixture.created.Operation.ID {
		t.Fatalf("reused identity/operation = %s/%s, want %s/%s", reused.Resolution.ID, reused.Operation.ID, fixture.created.Resolution.ID, fixture.created.Operation.ID)
	}
	if string(reused.Resolution.Status) != proposed.Status || reused.Resolution.Version != int(proposed.Version) {
		t.Fatalf("reused proposed resolution = %s version %d, want unchanged %s version %d", reused.Resolution.Status, reused.Resolution.Version, proposed.Status, proposed.Version)
	}
	operations := fixture.agentOperationIDs(t, fixture.created.Resolution.ID)
	if len(operations) != 1 || operations[0] != fixture.created.Operation.ID {
		t.Fatalf("operations = %v, want only the legacy operation", operations)
	}
	if got := fixture.operation(t, fixture.created.Operation.ID).TimeoutSeconds; got != legacyAgentOperationTimeout {
		t.Fatalf("legacy operation timeout = %d, want unchanged %d", got, legacyAgentOperationTimeout)
	}
	assertQueuedEventForOperation(t, fixture.ctx, fixture.pool, fixture.created.Operation.ID, 1)
}

// TestAgentResolutionConcurrentRequeueCreatesOneOperationIntegration 覆盖
// 同一已取消决议的并发重排：行锁只允许一次 cancelled→queued 转移，最多
// 一个 requeue operation 和一条 queued 事件。
func TestAgentResolutionConcurrentRequeueCreatesOneOperationIntegration(t *testing.T) {
	fixture := newUpgradeCompatibilityFixture(t)
	fixture.baseOperationShape(t, fixture.created.Operation.ID)
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, fixture.created.Operation.ID, 1, "concurrent-requeue-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := fixture.lifecycle.CancelAttempt(fixture.ctx, fixture.created.Operation.ID, 1); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	cancelled := fixture.resolution(t, fixture.created.Resolution.ID)

	const callers = 4
	var wait sync.WaitGroup
	results := make([]AgentResolutionCommandResult, callers)
	errs := make([]error, callers)
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = fixture.service.CreateAutomatic(fixture.ctx, fixture.request)
		}(index)
	}
	wait.Wait()
	for index := 0; index < callers; index++ {
		if errs[index] != nil {
			t.Fatalf("concurrent CreateAutomatic()[%d] error = %v", index, errs[index])
		}
		if results[index].Resolution.ID != fixture.created.Resolution.ID || results[index].Resolution.Status != domain.AgentResolutionQueued {
			t.Fatalf("concurrent result[%d] = %s/%s, want same queued resolution", index, results[index].Resolution.ID, results[index].Resolution.Status)
		}
	}
	operations := fixture.agentOperationIDs(t, fixture.created.Resolution.ID)
	if len(operations) != 2 {
		t.Fatalf("operations = %v, want the legacy and exactly one requeue operation", operations)
	}
	requeued := fixture.resolution(t, fixture.created.Resolution.ID)
	if requeued.Version != cancelled.Version+1 || repository.UUIDFromPG(requeued.OperationID) == fixture.created.Operation.ID {
		t.Fatalf("requeued version/operation = %d/%s, want version %d and a new operation", requeued.Version, repository.UUIDFromPG(requeued.OperationID), cancelled.Version+1)
	}
	assertQueuedEventForOperation(t, fixture.ctx, fixture.pool, repository.UUIDFromPG(requeued.OperationID), 1)
	for _, operation := range operations {
		if operation != fixture.created.Operation.ID {
			if got := fixture.operation(t, operation).TimeoutSeconds; got != headAgentOperationTimeout {
				t.Fatalf("requeue operation timeout = %d, want %d", got, headAgentOperationTimeout)
			}
		}
	}
}
