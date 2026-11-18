//go:build integration

package maintenance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func insertRetentionRSSSubscription(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	seriesID uuid.UUID,
	name string,
	nextPollAt time.Time,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
  id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, next_poll_at
) VALUES ($1, $2, $3, $4, true, 900, 1, $5)`,
		id, seriesID, name, "https://example.test/"+id.String()+".xml", nextPollAt); err != nil {
		t.Fatal(err)
	}
	return id
}

func countRetentionRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arguments ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, arguments...).Scan(&count); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return count
}

// generateRSSPollHistory 用产品调度入口建立多代 rss.poll 历史，并按产品语义把
// 每一代置为终态失败；到期时刻显式递增，保证每代都有独立的幂等键。
func generateRSSPollHistory(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workflow *service.RSSWorkflow,
	subscriptionID uuid.UUID,
	generations int,
	firstDue time.Time,
) []string {
	t.Helper()
	keys := make([]string, 0, generations)
	for generation := range generations {
		due := firstDue.Add(time.Duration(generation) * time.Second)
		if _, err := pool.Exec(ctx, `UPDATE rss_subscriptions SET next_poll_at = $2 WHERE id = $1`, subscriptionID, due); err != nil {
			t.Fatal(err)
		}
		created, err := workflow.ReconcileDuePolls(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if created != 1 {
			t.Fatalf("generation %d: ReconcileDuePolls() = %d, want 1", generation, created)
		}
		var key string
		if err := pool.QueryRow(ctx, `
SELECT idempotency_key FROM operations
WHERE resource_id = $1 AND status = 'queued'
ORDER BY created_at DESC LIMIT 1`, subscriptionID).Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		if _, err := pool.Exec(ctx, `
UPDATE operations
SET status = 'failed', error_code = 'rss_storage_unavailable', error_message = 'fixture generation failure',
    finished_at = now(), updated_at = now()
WHERE idempotency_key = $1`, key); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func markRetentionRowsFinishedAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, keys []string, finishedAt time.Time) {
	t.Helper()
	for _, key := range keys {
		if _, err := pool.Exec(ctx, `UPDATE operations SET finished_at = $2, updated_at = $2 WHERE idempotency_key = $1`, key, finishedAt); err != nil {
			t.Fatal(err)
		}
	}
}

func queueKeyForRSSSubscription(t *testing.T, ctx context.Context, pool *pgxpool.Pool, subscriptionID uuid.UUID) string {
	t.Helper()
	var key string
	if err := pool.QueryRow(ctx, `
SELECT idempotency_key FROM operations
WHERE resource_id = $1 AND kind = 'rss.poll' AND status = 'queued'
ORDER BY created_at DESC LIMIT 1`, subscriptionID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

// TestHistoryRetentionKeepsDueRSSPollSchedulingIntegration 覆盖 P1 回归：
// 真实调度入口产生多代 rss.poll 历史，经真实维护入口 dry-run 与 apply 清理后，
// 到期的订阅仍必须产生新的 operation 与 River 任务；同时核对全删、部分保留、
// 活跃与 RESTRICT 引用保护、有界批次以及旧 due-v1 键的兼容。
func TestHistoryRetentionKeepsDueRSSPollSchedulingIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := service.NewRSSWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, riverClient))
	retention := NewHistoryRetention(db.New(pool), transactor)

	actorID, seriesID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "retention-rss-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Retention RSS Series')`, seriesID); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).UTC()
	partialID := insertRetentionRSSSubscription(t, ctx, pool, seriesID, "Retention Partial", future)
	fullID := insertRetentionRSSSubscription(t, ctx, pool, seriesID, "Retention Full", future)
	clearedID := insertRetentionRSSSubscription(t, ctx, pool, seriesID, "Retention Cleared", future)
	activeID := insertRetentionRSSSubscription(t, ctx, pool, seriesID, "Retention Active", future)

	partialKeys := generateRSSPollHistory(t, ctx, pool, workflow, partialID, 10, time.Now().Add(-2*time.Hour).UTC())
	fullKeys := generateRSSPollHistory(t, ctx, pool, workflow, fullID, 100, time.Now().Add(-2*time.Hour).UTC())
	clearedKeys := generateRSSPollHistory(t, ctx, pool, workflow, clearedID, 5, time.Now().Add(-2*time.Hour).UTC())

	// 旧代际命名空间的终态操作：清理部分历史后，旧代码的 count 会落回 7 并撞上
	// 已生成的 g7；新命名空间的到期键必须照常调度。
	legacyKey := fmt.Sprintf("rss.poll:recovery:due-v1:%s:v1:g1000", partialID)
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (
  id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count,
  timeout_seconds, error_code, error_message, finished_at
) VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'failed', 5, 5, 30,
          'rss_storage_unavailable', 'legacy generation', now() - interval '1 hour')`,
		uuid.New(), partialID, legacyKey); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-100 * 24 * time.Hour).UTC()
	recent := time.Now().Add(-time.Hour).UTC()
	markRetentionRowsFinishedAt(t, ctx, pool, partialKeys[:4], old)
	markRetentionRowsFinishedAt(t, ctx, pool, partialKeys[4:], recent)
	markRetentionRowsFinishedAt(t, ctx, pool, fullKeys[:40], old)
	markRetentionRowsFinishedAt(t, ctx, pool, fullKeys[40:], recent)
	markRetentionRowsFinishedAt(t, ctx, pool, clearedKeys, old)

	// 混合 kind 的历史：与 RSS 无关的终态操作同样参与清理。
	for range 3 {
		id := uuid.New()
		if _, err := pool.Exec(ctx, `
INSERT INTO operations (
  id, kind, idempotency_key, status, max_attempts, timeout_seconds, finished_at
) VALUES ($1, 'retention.fixture', $2, 'succeeded', 1, 60, $3)`, id, "retention-rss-mixed-"+id.String(), old); err != nil {
			t.Fatal(err)
		}
	}

	// 活跃 poll 与 RESTRICT 引用：两者都不能被清理，活跃 poll 还会阻止到期补调度。
	activeKey := "rss.poll.manual:" + activeID.String() + ":" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (
  id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count, timeout_seconds, created_at
) VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'queued', 5, 0, 30, $4)`,
		uuid.New(), activeID, activeKey, old); err != nil {
		t.Fatal(err)
	}
	protectedKey := fmt.Sprintf("rss.poll:recovery:due-v1:%s:v1:g0", activeID)
	protectedID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (
  id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count,
  timeout_seconds, error_code, error_message, finished_at
) VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'failed', 5, 5, 30,
          'rss_storage_unavailable', 'scanned operation', $4)`,
		protectedID, activeID, protectedKey, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO emby_scan_runs (id, operation_id, status, created_by, completed_at)
VALUES ($1, $2, 'succeeded', $3, $4)`, uuid.New(), protectedID, actorID, old); err != nil {
		t.Fatal(err)
	}

	const expectedCandidates = 4 + 40 + 5 + 3
	before := time.Now().Add(-90 * 24 * time.Hour)
	dryRun, err := retention.Run(ctx, HistoryRetentionRequest{Before: before, BatchSize: 100, MaxBatches: 5})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.OperationCandidates != expectedCandidates || dryRun.OperationsDeleted != 0 || dryRun.Batches != 0 || dryRun.Apply {
		t.Fatalf("dry-run = %+v, want %d candidates without deletions", dryRun, expectedCandidates)
	}
	for name, subscriptionID := range map[string]uuid.UUID{"partial": partialID, "full": fullID, "cleared": clearedID} {
		want := map[string]int{"partial": 11, "full": 100, "cleared": 5}[name]
		if count := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM operations WHERE kind = 'rss.poll' AND resource_id = $1`, subscriptionID); count != want {
			t.Fatalf("dry-run %s rss.poll rows = %d, want %d", name, count, want)
		}
	}

	bounded, err := retention.Run(ctx, HistoryRetentionRequest{Before: before, BatchSize: 2, MaxBatches: 1, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if bounded.OperationsDeleted != 2 || bounded.Batches != 1 || bounded.OperationCandidates != expectedCandidates {
		t.Fatalf("bounded apply = %+v, want 2 deletions in 1 batch of %d candidates", bounded, expectedCandidates)
	}

	applied, err := retention.Run(ctx, HistoryRetentionRequest{Before: before, BatchSize: 100, MaxBatches: 5, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if applied.OperationsDeleted != expectedCandidates-2 || applied.Batches != 1 || applied.OperationCandidates != expectedCandidates-2 {
		t.Fatalf("full apply = %+v, want %d deletions in 1 batch", applied, expectedCandidates-2)
	}
	for name, want := range map[string]int{"partial": 7, "full": 60, "cleared": 0} {
		subscriptionID := map[string]uuid.UUID{"partial": partialID, "full": fullID, "cleared": clearedID}[name]
		if count := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM operations WHERE kind = 'rss.poll' AND resource_id = $1`, subscriptionID); count != want {
			t.Fatalf("after apply %s rss.poll rows = %d, want %d", name, count, want)
		}
	}
	for _, deletedKey := range append(append(append([]string{}, partialKeys[:4]...), fullKeys[:40]...), clearedKeys...) {
		if count := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM operations WHERE idempotency_key = $1`, deletedKey); count != 0 {
			t.Fatalf("expired operation %q was not deleted", deletedKey)
		}
	}
	if count := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM operations WHERE idempotency_key = $1`, legacyKey); count != 1 {
		t.Fatalf("recent legacy due-v1 operation rows = %d, want 1", count)
	}
	if count := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM operations WHERE idempotency_key = ANY($1::text[])`, []string{activeKey, protectedKey}); count != 2 {
		t.Fatalf("active/restricted poll rows = %d, want 2", count)
	}

	// 修复后的业务期望：清理历史后，到期订阅必须继续调度 operation 与 River 任务。
	operationsBefore := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM operations`)
	riverBefore := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM river_job`)
	partialDue := time.Now().Add(-3 * time.Minute).UTC()
	fullDue := time.Now().Add(-2 * time.Minute).UTC()
	clearedDue := time.Now().Add(-time.Minute).UTC()
	dueBySubscription := map[uuid.UUID]time.Time{partialID: partialDue, fullID: fullDue, clearedID: clearedDue}
	for subscriptionID, due := range dueBySubscription {
		if _, err := pool.Exec(ctx, `UPDATE rss_subscriptions SET next_poll_at = $2 WHERE id = $1`, subscriptionID, due); err != nil {
			t.Fatal(err)
		}
		var stored time.Time
		if err := pool.QueryRow(ctx, `SELECT next_poll_at FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		dueBySubscription[subscriptionID] = stored
	}
	created, err := workflow.ReconcileDuePolls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if created != 3 {
		t.Fatalf("post-retention ReconcileDuePolls() = %d, want 3", created)
	}
	if operationsAfter := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM operations`); operationsAfter != operationsBefore+3 {
		t.Fatalf("post-retention operations = %d, want %d", operationsAfter, operationsBefore+3)
	}
	if riverAfter := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM river_job`); riverAfter != riverBefore+3 {
		t.Fatalf("post-retention River jobs = %d, want %d", riverAfter, riverBefore+3)
	}
	for subscriptionID, due := range dueBySubscription {
		key := queueKeyForRSSSubscription(t, ctx, pool, subscriptionID)
		wantKey := fmt.Sprintf("rss.poll:recovery:due-v2:%s:v1:t%d", subscriptionID, due.UnixMicro())
		if key != wantKey {
			t.Fatalf("post-retention key = %q, want %q", key, wantKey)
		}
		var riverJobID int64
		if err := pool.QueryRow(ctx, `
SELECT river_job_id FROM operations WHERE idempotency_key = $1`, key).Scan(&riverJobID); err != nil {
			t.Fatal(err)
		}
		if count := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM river_job WHERE id = $1`, riverJobID); count != 1 {
			t.Fatalf("operation %q River job = %d rows, want 1", key, count)
		}
	}

	// 活跃 poll 阻止重复调度；已有排队任务时不产生第二个。
	if _, err := pool.Exec(ctx, `UPDATE rss_subscriptions SET next_poll_at = now() - interval '1 minute' WHERE id = $1`, activeID); err != nil {
		t.Fatal(err)
	}
	created, err = workflow.ReconcileDuePolls(ctx)
	if err != nil || created != 0 {
		t.Fatalf("active poll ReconcileDuePolls() = %d, %v, want 0/nil", created, err)
	}

	// 同一到期时刻重复巡检：上一代已终态时键仍相同，不得重复调度。
	for subscriptionID, due := range dueBySubscription {
		key := queueKeyForRSSSubscription(t, ctx, pool, subscriptionID)
		if _, err := pool.Exec(ctx, `
UPDATE operations SET status = 'failed', error_code = 'rss_storage_unavailable', error_message = 'post-retention failure',
    finished_at = now(), updated_at = now() WHERE idempotency_key = $1`, key); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE rss_subscriptions SET next_poll_at = $2 WHERE id = $1`, subscriptionID, due); err != nil {
			t.Fatal(err)
		}
	}
	operationsBefore = countRetentionRows(t, ctx, pool, `SELECT count(*) FROM operations`)
	riverBefore = countRetentionRows(t, ctx, pool, `SELECT count(*) FROM river_job`)
	created, err = workflow.ReconcileDuePolls(ctx)
	if err != nil || created != 0 {
		t.Fatalf("duplicate due ReconcileDuePolls() = %d, %v, want 0/nil", created, err)
	}
	if operationsAfter := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM operations`); operationsAfter != operationsBefore {
		t.Fatalf("duplicate due operations = %d, want %d", operationsAfter, operationsBefore)
	}
	if riverAfter := countRetentionRows(t, ctx, pool, `SELECT count(*) FROM river_job`); riverAfter != riverBefore {
		t.Fatalf("duplicate due River jobs = %d, want %d", riverAfter, riverBefore)
	}

	// 终态失败后的下一轮到期继续产生新键。
	nextDue := time.Now().Add(-time.Minute).UTC()
	if _, err := pool.Exec(ctx, `UPDATE rss_subscriptions SET next_poll_at = $2 WHERE id = $1`, partialID, nextDue); err != nil {
		t.Fatal(err)
	}
	created, err = workflow.ReconcileDuePolls(ctx)
	if err != nil || created != 1 {
		t.Fatalf("next-due ReconcileDuePolls() = %d, %v, want 1/nil", created, err)
	}
	if key := queueKeyForRSSSubscription(t, ctx, pool, partialID); key == fmt.Sprintf("rss.poll:recovery:due-v2:%s:v1:t%d", partialID, dueBySubscription[partialID].UnixMicro()) {
		t.Fatalf("next-due key did not advance past the previous due instant: %q", key)
	}

	// 活跃 poll 终态后恢复调度；完成订阅不再调度。
	if _, err := pool.Exec(ctx, `UPDATE operations SET status = 'failed', error_code = 'rss_storage_unavailable', error_message = 'active poll finished', finished_at = now(), updated_at = now() WHERE idempotency_key = $1`, activeKey); err != nil {
		t.Fatal(err)
	}
	activeDue := time.Now().Add(-time.Minute).UTC()
	if _, err := pool.Exec(ctx, `UPDATE rss_subscriptions SET next_poll_at = $2 WHERE id = $1`, activeID, activeDue); err != nil {
		t.Fatal(err)
	}
	created, err = workflow.ReconcileDuePolls(ctx)
	if err != nil || created != 1 {
		t.Fatalf("released active poll ReconcileDuePolls() = %d, %v, want 1/nil", created, err)
	}
	if key := queueKeyForRSSSubscription(t, ctx, pool, activeID); key != fmt.Sprintf("rss.poll:recovery:due-v2:%s:v1:t%d", activeID, activeDue.UnixMicro()) {
		t.Fatalf("released active poll key = %q", key)
	}
	if _, err := pool.Exec(ctx, `UPDATE rss_subscriptions SET enabled = false, completed_at = now(), next_poll_at = NULL WHERE id = $1`, activeID); err != nil {
		t.Fatal(err)
	}
	created, err = workflow.ReconcileDuePolls(ctx)
	if err != nil || created != 0 {
		t.Fatalf("completed subscription ReconcileDuePolls() = %d, %v, want 0/nil", created, err)
	}
}
