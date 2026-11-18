//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const staleRescueThreshold = 5 * time.Minute

type staleRescueFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	queries    *db.Queries
	transactor *database.Transactor
	driver     *riverpgxv5.Driver
	lifecycle  *repository.OperationLifecycle
	scheduler  *service.OperationScheduler
}

func newStaleRescueFixture(t *testing.T) staleRescueFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	riverDriver := riverpgxv5.New(pool)
	riverClient, err := river.NewClient(riverDriver, &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	transactor := database.NewTransactor(pool)
	return staleRescueFixture{
		ctx: ctx, pool: pool, queries: db.New(pool), transactor: transactor, driver: riverDriver,
		lifecycle: repository.NewOperationLifecycle(db.New(pool), transactor),
		scheduler: service.NewOperationScheduler(transactor, riverClient),
	}
}

// createStaleOperation 用正式 scheduler 创建 operation/River job，并用真实 lifecycle
// 开始一次 attempt，再把 job 与业务心跳置为阈值之前（合法陈旧夹具）。
func (fixture staleRescueFixture) createStaleOperation(
	t *testing.T,
	kind string,
	resourceType string,
	maxAttempts int32,
	attempt int32,
) service.ScheduleOperationResult {
	t.Helper()
	scheduled, err := fixture.scheduler.Schedule(fixture.ctx, service.ScheduleOperationRequest{
		Kind: kind, ResourceType: resourceType, ResourceID: uuid.New(),
		IdempotencyKey: "stale-rescue-" + uuid.NewString(),
		MaxAttempts:    int(maxAttempts), Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("schedule operation: %v", err)
	}
	// 依次推进到目标 attempt：StartAttempt 之后对非当前 attempt 安排可重试失败，
	// 使 operation.attempt_count、当前 attempt 行与 River job.attempt 全部对齐。
	for completed := int32(1); completed <= attempt; completed++ {
		if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, scheduled.Operation.ID, int(completed), "stale-rescue-worker"); err != nil {
			t.Fatalf("start attempt %d: %v", completed, err)
		}
		if completed == attempt {
			break
		}
		if err := fixture.lifecycle.FailAttempt(fixture.ctx, scheduled.Operation.ID, int(completed), "stale_rescue_fixture", "stale rescue fixture advance", true); err != nil {
			t.Fatalf("advance attempt %d: %v", completed, err)
		}
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
UPDATE river_job SET state = 'running', attempt = $2, attempted_at = now() - interval '6 minutes'
WHERE id = $1`, scheduled.Operation.RiverJobID, attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operations SET heartbeat_at = now() - interval '6 minutes' WHERE id = $1`, scheduled.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operation_attempts SET heartbeat_at = now() - interval '6 minutes' WHERE operation_id = $1`, scheduled.Operation.ID); err != nil {
		t.Fatal(err)
	}
	return scheduled
}

func (fixture staleRescueFixture) riverJob(t *testing.T, jobID int64) rivertype.JobRow {
	t.Helper()
	var (
		state       string
		attempt     int
		maxAttempts int
		attemptedAt *time.Time
	)
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT state, attempt, max_attempts, attempted_at FROM river_job WHERE id = $1`, jobID).Scan(
		&state, &attempt, &maxAttempts, &attemptedAt,
	); err != nil {
		t.Fatal(err)
	}
	return rivertype.JobRow{ID: jobID, State: rivertype.JobState(state), Attempt: attempt, MaxAttempts: maxAttempts, AttemptedAt: attemptedAt}
}

func (fixture staleRescueFixture) lock(t *testing.T) (recoveryCandidate, bool) {
	t.Helper()
	var (
		candidate recoveryCandidate
		found     bool
	)
	err := fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, ok, err := lockCandidate(fixture.ctx, scope.Tx, staleRescueThreshold, "")
		candidate, found = locked, ok
		return err
	})
	if err != nil {
		t.Fatalf("lockCandidate() error = %v", err)
	}
	return candidate, found
}

func (fixture staleRescueFixture) rescue(t *testing.T, candidate recoveryCandidate) error {
	t.Helper()
	return fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		return rescueStaleCandidate(fixture.ctx, scope, fixture.driver, candidate, time.Now().UTC())
	})
}

func (fixture staleRescueFixture) recoveryEventCount(t *testing.T, operationID uuid.UUID) int {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT count(*) FROM events WHERE topic = 'operation.recovered' AND operation_id = $1`, operationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestRescueStaleCandidateRewindsRiverAndOperation 验证 running-stale rescue 真实更新
// River 行（available + attempt 回退），并与业务 attempt/预算/事件在同一事务内对齐。
func TestRescueStaleCandidateRewindsRiverAndOperation(t *testing.T) {
	fixture := newStaleRescueFixture(t)
	scheduled := fixture.createStaleOperation(t, appqueue.KindSearchRun, "search_run", 3, 1)
	before := fixture.riverJob(t, scheduled.Operation.RiverJobID)

	candidate, found := fixture.lock(t)
	if !found {
		t.Fatal("stale running candidate was not selected")
	}
	if int64(candidate.riverJobID) != scheduled.Operation.RiverJobID || candidate.attempt != 1 {
		t.Fatalf("candidate = %#v, want job %d attempt 1", candidate, scheduled.Operation.RiverJobID)
	}
	if err := fixture.rescue(t, candidate); err != nil {
		t.Fatalf("rescueStaleCandidate() error = %v", err)
	}

	after := fixture.riverJob(t, scheduled.Operation.RiverJobID)
	if after.State != rivertype.JobStateAvailable || after.Attempt != 0 || after.MaxAttempts != 3 {
		t.Fatalf("river job after rescue = %s attempt %d max %d, want available/0/3", after.State, after.Attempt, after.MaxAttempts)
	}
	if before.AttemptedAt == nil || after.AttemptedAt == nil || !after.AttemptedAt.Equal(*before.AttemptedAt) {
		t.Fatalf("rescue must not rewrite attempted_at (before=%v after=%v)", before.AttemptedAt, after.AttemptedAt)
	}
	var rescueCount int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT coalesce((metadata->>'river:rescue_count')::int, 0) FROM river_job WHERE id = $1`, scheduled.Operation.RiverJobID).Scan(&rescueCount); err != nil {
		t.Fatal(err)
	}
	if rescueCount != 0 {
		t.Fatalf("interrupted rescue must not bump river:rescue_count, got %d", rescueCount)
	}

	operation, err := fixture.queries.GetOperation(fixture.ctx, repository.UUIDToPG(scheduled.Operation.ID))
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != "queued" || operation.AttemptCount != 0 || operation.MaxAttempts != 3 {
		t.Fatalf("operation after rescue = %s count %d max %d, want queued/0/3", operation.Status, operation.AttemptCount, operation.MaxAttempts)
	}
	var runningAttempts int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT count(*) FROM operation_attempts WHERE operation_id = $1`, scheduled.Operation.ID).Scan(&runningAttempts); err != nil {
		t.Fatal(err)
	}
	if runningAttempts != 0 {
		t.Fatalf("interrupted attempt rows after rescue = %d, want 0", runningAttempts)
	}
	if events := fixture.recoveryEventCount(t, scheduled.Operation.ID); events != 1 {
		t.Fatalf("operation.recovered events = %d, want 1", events)
	}

	if _, foundAgain := fixture.lock(t); foundAgain {
		t.Fatal("recovered operation was still selected as a stale candidate")
	}

	// 下一次真实执行使用同一 attempt 编号：job fetch 后 attempt=1，业务 count 0→1。
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, scheduled.Operation.ID, after.Attempt+1, "stale-rescue-next"); err != nil {
		t.Fatalf("next StartAttempt() error = %v", err)
	}
	next, err := fixture.queries.GetOperation(fixture.ctx, repository.UUIDToPG(scheduled.Operation.ID))
	if err != nil {
		t.Fatal(err)
	}
	var nextAttempt int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT attempt FROM operation_attempts WHERE operation_id = $1 AND status = 'running'`, scheduled.Operation.ID).Scan(&nextAttempt); err != nil {
		t.Fatal(err)
	}
	t.Logf("next execution alignment: job attempt=%d operation count=%d attempt_row=%d", after.Attempt+1, next.AttemptCount, nextAttempt)
	if next.AttemptCount != int32(after.Attempt+1) || nextAttempt != after.Attempt+1 {
		t.Fatalf("attempt counters misaligned: count=%d row=%d job=%d", next.AttemptCount, nextAttempt, after.Attempt+1)
	}
}

// TestRescueStaleCandidateKeepsMaxBudget 验证接近/位于 max 的中断不会提高预算或立即耗尽。
func TestRescueStaleCandidateKeepsMaxBudget(t *testing.T) {
	fixture := newStaleRescueFixture(t)
	scheduled := fixture.createStaleOperation(t, appqueue.KindSubtitlePrepare, "episode_task", 3, 3)
	candidate, found := fixture.lock(t)
	if !found {
		t.Fatal("at-max stale candidate was not selected")
	}
	if err := fixture.rescue(t, candidate); err != nil {
		t.Fatalf("rescueStaleCandidate() error = %v", err)
	}
	after := fixture.riverJob(t, scheduled.Operation.RiverJobID)
	operation, err := fixture.queries.GetOperation(fixture.ctx, repository.UUIDToPG(scheduled.Operation.ID))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("at-max rescue: river state=%s attempt=%d max=%d operation count=%d max=%d", after.State, after.Attempt, after.MaxAttempts, operation.AttemptCount, operation.MaxAttempts)
	if after.State != rivertype.JobStateAvailable || after.Attempt != 2 || after.MaxAttempts != 3 {
		t.Fatalf("at-max river job = %s attempt %d max %d, want available/2/3", after.State, after.Attempt, after.MaxAttempts)
	}
	if operation.AttemptCount != 2 || operation.MaxAttempts != 3 {
		t.Fatalf("at-max operation = count %d max %d, want 2/3", operation.AttemptCount, operation.MaxAttempts)
	}
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, scheduled.Operation.ID, after.Attempt+1, "stale-rescue-max"); err != nil {
		t.Fatalf("final attempt StartAttempt() error = %v", err)
	}
	final, err := fixture.queries.GetOperation(fixture.ctx, repository.UUIDToPG(scheduled.Operation.ID))
	if err != nil {
		t.Fatal(err)
	}
	if final.AttemptCount != 3 || final.MaxAttempts != 3 {
		t.Fatalf("final attempt operation = count %d max %d, want 3/3 (budget not raised)", final.AttemptCount, final.MaxAttempts)
	}
}

// TestRescueStaleCandidateRejectsUnsafeCandidates 覆盖身份/时效/取消/预算/取消意图负例，
// 并确认其它 kind 的 running-stale 候选不会被 discarded 结算路径误选。
func TestRescueStaleCandidateRejectsUnsafeCandidates(t *testing.T) {
	type mutation struct {
		name  string
		apply func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult)
	}
	mutations := []mutation{
		{name: "fresh attempted_at", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE river_job SET attempted_at = now() WHERE id = $1`, scheduled.Operation.RiverJobID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "null attempted_at", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE river_job SET attempted_at = NULL WHERE id = $1`, scheduled.Operation.RiverJobID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "river cancel intent", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE river_job SET metadata = metadata || '{"cancel_attempted_at":"2026-01-01T00:00:00Z"}'::jsonb WHERE id = $1`, scheduled.Operation.RiverJobID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "job attempt mismatch", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE river_job SET attempt = 2 WHERE id = $1`, scheduled.Operation.RiverJobID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "operation count mismatch", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operations SET attempt_count = 2 WHERE id = $1`, scheduled.Operation.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "attempt row mismatch", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operation_attempts SET attempt = 2 WHERE operation_id = $1`, scheduled.Operation.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "river budget mismatch", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE river_job SET max_attempts = 4 WHERE id = $1`, scheduled.Operation.RiverJobID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "job args owner mismatch", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE river_job SET args = jsonb_set(args, '{operationId}', to_jsonb($2::text)) WHERE id = $1`, scheduled.Operation.RiverJobID, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fresh operation heartbeat", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operations SET heartbeat_at = now() WHERE id = $1`, scheduled.Operation.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "operation cancel requested", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operations SET cancel_requested_at = now() WHERE id = $1`, scheduled.Operation.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "job no longer running", apply: func(t *testing.T, fixture staleRescueFixture, scheduled service.ScheduleOperationResult) {
			if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE river_job SET state = 'discarded', finalized_at = now() WHERE id = $1`, scheduled.Operation.RiverJobID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStaleRescueFixture(t)
			scheduled := fixture.createStaleOperation(t, appqueue.KindSearchRun, "search_run", 3, 1)
			testCase.apply(t, fixture, scheduled)
			if _, found := fixture.lock(t); found {
				t.Fatalf("%s: unsafe candidate was selected for stale recovery", testCase.name)
			}
		})
	}
}

// TestRescueStaleCandidateNonDeletionKind 验证通用 running-stale 恢复仍覆盖非删除 kind。
func TestRescueStaleCandidateNonDeletionKind(t *testing.T) {
	fixture := newStaleRescueFixture(t)
	scheduled := fixture.createStaleOperation(t, appqueue.KindTranscodeRun, "episode_task", 2, 1)
	candidate, found := fixture.lock(t)
	if !found {
		t.Fatal("non-deletion stale candidate was not selected")
	}
	if candidate.kind != appqueue.KindTranscodeRun {
		t.Fatalf("candidate kind = %q, want %q", candidate.kind, appqueue.KindTranscodeRun)
	}
	if err := fixture.rescue(t, candidate); err != nil {
		t.Fatalf("rescueStaleCandidate() error = %v", err)
	}
	if after := fixture.riverJob(t, scheduled.Operation.RiverJobID); after.State != rivertype.JobStateAvailable || after.Attempt != 0 {
		t.Fatalf("non-deletion rescue job = %s attempt %d, want available/0", after.State, after.Attempt)
	}
}

// TestRescueStaleCandidateFaultsRollBack 覆盖 River 更新、attempt 删除与事件写入三处故障：
// 任一处失败都整笔回滚且候选仍可再次选中，清除故障后同一入口收敛一次。
func TestRescueStaleCandidateFaultsRollBack(t *testing.T) {
	cases := []struct {
		name      string
		table     string
		event     string
		condition string
	}{
		{name: "river update failure", table: "river_job", event: "UPDATE", condition: "true"},
		{name: "attempt delete failure", table: "operation_attempts", event: "DELETE", condition: "true"},
		{name: "event insert failure", table: "events", event: "INSERT", condition: "true"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStaleRescueFixture(t)
			scheduled := fixture.createStaleOperation(t, appqueue.KindSearchRun, "search_run", 3, 1)
			function := "stale_rescue_fault_" + uuid.NewString()[:8]
			if _, err := fixture.pool.Exec(fixture.ctx, `
CREATE FUNCTION `+function+`() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
    IF `+testCase.condition+` THEN
        RAISE EXCEPTION 'stale rescue forced failure';
    END IF;
    RETURN NEW;
END
$function$`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.pool.Exec(fixture.ctx, `
CREATE TRIGGER aa_`+function+` BEFORE `+testCase.event+` ON `+testCase.table+` FOR EACH ROW EXECUTE FUNCTION `+function+`()`); err != nil {
				t.Fatal(err)
			}
			candidate, found := fixture.lock(t)
			if !found {
				t.Fatal("candidate missing before fault injection")
			}
			if err := fixture.rescue(t, candidate); err == nil {
				t.Fatal("rescueStaleCandidate() succeeded despite injected fault")
			}
			operation, err := fixture.queries.GetOperation(fixture.ctx, repository.UUIDToPG(scheduled.Operation.ID))
			if err != nil {
				t.Fatal(err)
			}
			job := fixture.riverJob(t, scheduled.Operation.RiverJobID)
			var attempts int
			if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM operation_attempts WHERE operation_id = $1 AND status = 'running'`, scheduled.Operation.ID).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if operation.Status != "running" || operation.AttemptCount != 1 || job.State != rivertype.JobStateRunning || job.Attempt != 1 || attempts != 1 {
				t.Fatalf("fault did not roll back: op=%s/%d job=%s/%d running_attempts=%d", operation.Status, operation.AttemptCount, job.State, job.Attempt, attempts)
			}
			if events := fixture.recoveryEventCount(t, scheduled.Operation.ID); events != 0 {
				t.Fatalf("faulted rescue wrote %d recovery events", events)
			}
			if _, err := fixture.pool.Exec(fixture.ctx, `DROP TRIGGER aa_`+function+` ON `+testCase.table); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.pool.Exec(fixture.ctx, `DROP FUNCTION `+function+`()`); err != nil {
				t.Fatal(err)
			}
			retried, found := fixture.lock(t)
			if !found {
				t.Fatal("candidate was not selectable after rollback")
			}
			if err := fixture.rescue(t, retried); err != nil {
				t.Fatalf("post-fault rescue error = %v", err)
			}
			if events := fixture.recoveryEventCount(t, scheduled.Operation.ID); events != 1 {
				t.Fatalf("post-fault recovery events = %d, want 1", events)
			}
		})
	}
}

// TestRescueStaleCandidateConcurrentApply 验证两个并发事务至多恢复一次。
func TestRescueStaleCandidateConcurrentApply(t *testing.T) {
	fixture := newStaleRescueFixture(t)
	scheduled := fixture.createStaleOperation(t, appqueue.KindSearchRun, "search_run", 3, 1)
	type outcome struct {
		rescued bool
		err     error
	}
	results := make(chan outcome, 2)
	for index := 0; index < 2; index++ {
		go func() {
			var rescued bool
			err := fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
				candidate, found, err := lockCandidate(fixture.ctx, scope.Tx, staleRescueThreshold, "")
				if err != nil || !found {
					return err
				}
				rescued = true
				return rescueStaleCandidate(fixture.ctx, scope, fixture.driver, candidate, time.Now().UTC())
			})
			results <- outcome{rescued: rescued, err: err}
		}()
	}
	rescued := 0
	for index := 0; index < 2; index++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent rescue error = %v", result.err)
		}
		if result.rescued {
			rescued++
		}
	}
	if rescued != 1 {
		t.Fatalf("concurrent rescues = %d, want exactly 1", rescued)
	}
	if events := fixture.recoveryEventCount(t, scheduled.Operation.ID); events != 1 {
		t.Fatalf("concurrent recovery events = %d, want 1", events)
	}
}
