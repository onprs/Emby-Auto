package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const (
	settlementErrorCode    = "deletion_finalization_interrupted"
	settlementErrorMessage = "the deletion failed and its interrupted terminal recovery was settled by maintenance"
)

// staleCandidatePredicate 是两类候选共享的中断条件；$1 为 stale-after 间隔，$2 为可选 kind。
const staleCandidatePredicate = `
WHERE operation.status = 'running'
  AND operation.cancel_requested_at IS NULL
  AND operation.heartbeat_at < now() - $1::interval
  AND ($2::text = '' OR operation.kind = $2::text)`

// terminalSettlementPredicate 只放行被证明已进入 River 终态丢弃、且 operation 与
// 当前 attempt 仍 running 的删除操作；身份以 attempt_count 与 job.attempt 对齐。
const terminalSettlementPredicate = `
  AND job.state = 'discarded'
  AND attempt.attempt = operation.attempt_count
  AND job.attempt = operation.attempt_count
  AND operation.attempt_count BETWEEN 1 AND operation.max_attempts
  AND (
      (operation.kind = 'acquisition.delete' AND operation.resource_type = 'acquisition')
      OR (operation.kind = 'rss.subscription.delete' AND operation.resource_type = 'rss_subscription')
  )`

// runningRecoveryPredicate 只用于 running-stale 中断重排（通用业务 kind）：要求 River job
// 仍在执行、job/operation/当前 attempt 的身份与预算完全对齐、三者时效均陈旧，且没有
// River 侧取消意图。该谓词不参与 discarded 终态结算。
const runningRecoveryPredicate = `
  AND job.state = 'running'
  AND job.kind = operation.kind
  AND job.args->>'operationId' = operation.id::text
  AND job.attempt = operation.attempt_count
  AND job.max_attempts = operation.max_attempts
  AND attempt.attempt = operation.attempt_count
  AND operation.attempt_count BETWEEN 1 AND operation.max_attempts
  AND job.attempted_at IS NOT NULL
  AND job.attempted_at < now() - $1::interval
  AND NOT (job.metadata ? 'cancel_attempted_at')`

const candidateSelect = `
SELECT operation.id, operation.river_job_id, attempt.attempt, operation.kind,
       operation.resource_type, operation.resource_id
FROM operations AS operation
JOIN river_job AS job ON job.id = operation.river_job_id
JOIN operation_attempts AS attempt
  ON attempt.operation_id = operation.id
 AND attempt.status = 'running'`

type recoveryCandidate struct {
	id           pgtype.UUID
	riverJobID   int64
	attempt      int32
	kind         string
	resourceType *string
	resourceID   pgtype.UUID
}

type eligibilityCounts struct {
	staleRecovery int64
	settlement    int64
}

func (counts eligibilityCounts) total() int64 {
	return counts.staleRecovery + counts.settlement
}

func main() {
	apply := flag.Bool("apply", false, "recover eligible operations")
	workerStopped := flag.Bool("worker-stopped", false, "confirm the Worker runtime is stopped")
	staleAfter := flag.Duration("stale-after", 30*time.Minute, "minimum heartbeat age")
	kind := flag.String("kind", "", "optional exact operation kind")
	flag.Parse()

	*kind = strings.TrimSpace(*kind)
	if *staleAfter < 5*time.Minute {
		fmt.Fprintln(os.Stderr, "stale-after must be at least 5m")
		os.Exit(2)
	}
	if *apply && !*workerStopped {
		fmt.Fprintln(os.Stderr, "--apply requires --worker-stopped")
		os.Exit(2)
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "load configuration: %v\n", err)
			os.Exit(2)
		}
		databaseURL = cfg.DatabaseURL
	}
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL or completed bootstrap configuration is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	counts, err := countEligible(ctx, pool, *staleAfter, *kind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "count eligible operations: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("eligible=%d\n", counts.total())
	fmt.Printf("staleRecovery=%d\n", counts.staleRecovery)
	fmt.Printf("terminalSettlement=%d\n", counts.settlement)
	if !*apply || counts.total() == 0 {
		fmt.Println("recover stale operations: dry-run")
		return
	}

	transactor := database.NewTransactor(pool)
	service.RegisterProgressCommitHook(transactor)
	riverDriver := riverpgxv5.New(pool)
	recovered := 0
	settled := 0
	for {
		progressed := false
		rescuedThisRun := false
		settledThisRun := false
		err := transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			terminal, terminalFound, err := lockTerminalSettlementCandidate(ctx, scope.Tx, *staleAfter, *kind)
			if err != nil {
				return err
			}
			if terminalFound {
				locked, err := scope.Queries.GetOperation(ctx, terminal.id)
				if err != nil {
					return fmt.Errorf("load terminal settlement candidate: %w", err)
				}
				settlement, err := repository.SettleTerminalDeletionFailureInTx(
					ctx, scope.Queries, locked, terminal.attempt, settlementErrorCode, settlementErrorMessage,
				)
				if err != nil {
					return fmt.Errorf("settle terminal deletion failure: %w", err)
				}
				if !settlement {
					return nil
				}
				progressed = true
				settledThisRun = true
				return nil
			}

			candidate, found, err := lockCandidate(ctx, scope.Tx, *staleAfter, *kind)
			if err != nil || !found {
				return err
			}
			if err := rescueStaleCandidate(ctx, scope, riverDriver, candidate, time.Now().UTC()); err != nil {
				return err
			}
			progressed = true
			rescuedThisRun = true
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "recover stale operation: %v\n", err)
			os.Exit(1)
		}
		if !progressed {
			break
		}
		if settledThisRun {
			settled++
		} else if rescuedThisRun {
			recovered++
		}
	}
	fmt.Printf("recovered=%d\n", recovered)
	fmt.Printf("settled=%d\n", settled)
}

func countEligible(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, staleAfter time.Duration, kind string) (eligibilityCounts, error) {
	var counts eligibilityCounts
	if err := pool.QueryRow(ctx, `
SELECT count(DISTINCT operation.id)::bigint
FROM operations AS operation
JOIN river_job AS job ON job.id = operation.river_job_id
JOIN operation_attempts AS attempt
  ON attempt.operation_id = operation.id
 AND attempt.status = 'running'
`+staleCandidatePredicate+runningRecoveryPredicate, staleAfter.String(), kind).Scan(&counts.staleRecovery); err != nil {
		return counts, err
	}
	if err := pool.QueryRow(ctx, `
SELECT count(DISTINCT operation.id)::bigint
FROM operations AS operation
JOIN river_job AS job ON job.id = operation.river_job_id
JOIN operation_attempts AS attempt
  ON attempt.operation_id = operation.id
 AND attempt.status = 'running'
`+staleCandidatePredicate+terminalSettlementPredicate, staleAfter.String(), kind).Scan(&counts.settlement); err != nil {
		return counts, err
	}
	return counts, nil
}

// lockCandidate 只选取 running-stale 中断重排候选；job 已进入其它状态或存在 River 侧
// 取消意图时不在此路径处理，避免把终态丢弃或取消中的记录误判为可重排。
func lockCandidate(ctx context.Context, tx pgx.Tx, staleAfter time.Duration, kind string) (recoveryCandidate, bool, error) {
	var candidate recoveryCandidate
	err := tx.QueryRow(ctx, candidateSelect+staleCandidatePredicate+runningRecoveryPredicate+"\nORDER BY operation.heartbeat_at, operation.id, attempt.attempt DESC\nLIMIT 1\nFOR UPDATE OF operation, job, attempt SKIP LOCKED", staleAfter.String(), kind).Scan(
		&candidate.id, &candidate.riverJobID, &candidate.attempt, &candidate.kind,
		&candidate.resourceType, &candidate.resourceID,
	)
	if err == pgx.ErrNoRows {
		return recoveryCandidate{}, false, nil
	}
	if err != nil {
		return recoveryCandidate{}, false, err
	}
	return candidate, true, nil
}

// lockTerminalSettlementCandidate 在事务内锁定并复检终态结算候选。dry-run 的
// 观测结果不是授权快照，apply 必须重新满足同一组身份、时效、预算与资源条件。
func lockTerminalSettlementCandidate(ctx context.Context, tx pgx.Tx, staleAfter time.Duration, kind string) (recoveryCandidate, bool, error) {
	var candidate recoveryCandidate
	err := tx.QueryRow(ctx, candidateSelect+staleCandidatePredicate+terminalSettlementPredicate+"\nORDER BY operation.heartbeat_at, operation.id\nLIMIT 1\nFOR UPDATE OF operation, job, attempt SKIP LOCKED", staleAfter.String(), kind).Scan(
		&candidate.id, &candidate.riverJobID, &candidate.attempt, &candidate.kind,
		&candidate.resourceType, &candidate.resourceID,
	)
	if err == pgx.ErrNoRows {
		return recoveryCandidate{}, false, nil
	}
	if err != nil {
		return recoveryCandidate{}, false, err
	}
	return candidate, true, nil
}

// rescueStaleCandidate 在调用方事务内把已陈旧的 running River job 按 River 原生中断
// 语义重新变为可执行，并同步 operation/attempt 与事件。River v0.49 的 soft stop 使用
// JobSetStateInterrupted(id, now, max(attempt-1,0))：状态回到 available、attempt 回退
// 一格，使作业再次执行时复用同一 attempt 编号。业务侧删除中断 attempt 行并把
// attempt_count 回退一格，与 River 对齐，不消耗中断预算、不改动 max_attempts。
func rescueStaleCandidate(
	ctx context.Context,
	scope database.TxScope,
	driver *riverpgxv5.Driver,
	candidate recoveryCandidate,
	now time.Time,
) error {
	operation, err := scope.Queries.GetOperation(ctx, candidate.id)
	if err != nil {
		return fmt.Errorf("load stale recovery candidate: %w", err)
	}
	if operation.RiverJobID == nil || *operation.RiverJobID != candidate.riverJobID {
		return fmt.Errorf("stale recovery candidate River job no longer matches the operation")
	}
	rewoundAttempt := int(candidate.attempt) - 1
	if rewoundAttempt < 0 {
		rewoundAttempt = 0
	}
	interrupted := riverdriver.JobSetStateInterrupted(candidate.riverJobID, now, rewoundAttempt, nil)
	updated, err := driver.UnwrapExecutor(scope.Tx).JobSetStateIfRunningMany(ctx, &riverdriver.JobSetStateIfRunningManyParams{
		ID:              []int64{interrupted.ID},
		Attempt:         []*int{interrupted.Attempt},
		ErrData:         [][]byte{interrupted.ErrData},
		FinalizedAt:     []*time.Time{interrupted.FinalizedAt},
		MetadataDoMerge: []bool{interrupted.MetadataDoMerge},
		MetadataUpdates: [][]byte{interrupted.MetadataUpdates},
		ScheduledAt:     []*time.Time{interrupted.ScheduledAt},
		State:           []rivertype.JobState{interrupted.State},
	})
	if err != nil {
		return fmt.Errorf("interrupt stale River job: %w", err)
	}
	// Many 会对未命中的行回退当前行，因此必须逐项核对身份、状态与预算，
	// 不能用“无错误”或 len==1 推断更新成功。
	if len(updated) != 1 {
		return fmt.Errorf("interrupt stale River job: expected one row, got %d", len(updated))
	}
	row := updated[0]
	if int64(row.ID) != candidate.riverJobID || row.Kind != operation.Kind {
		return fmt.Errorf("interrupt stale River job: returned job identity does not match the candidate")
	}
	if row.State != rivertype.JobStateAvailable || row.Attempt != rewoundAttempt || int32(row.Attempt) != operation.AttemptCount-1 {
		return fmt.Errorf("interrupt stale River job: expected available attempt %d, got %s attempt %d", rewoundAttempt, row.State, row.Attempt)
	}
	if row.MaxAttempts != int(operation.MaxAttempts) {
		return fmt.Errorf("interrupt stale River job: River budget %d does not match operation budget %d", row.MaxAttempts, operation.MaxAttempts)
	}
	deleted, err := scope.Queries.DeleteSnoozedOperationAttempt(ctx, db.DeleteSnoozedOperationAttemptParams{
		OperationID: candidate.id, Attempt: candidate.attempt,
	})
	if err != nil {
		return fmt.Errorf("delete interrupted operation attempt: %w", err)
	}
	if deleted != 1 {
		return fmt.Errorf("delete interrupted operation attempt: expected one row, got %d", deleted)
	}
	dequeued, err := scope.Queries.SnoozeOperation(ctx, candidate.id)
	if err != nil {
		return fmt.Errorf("snooze interrupted operation: %w", err)
	}
	if dequeued.AttemptCount != operation.AttemptCount-1 || dequeued.Status != "queued" {
		return fmt.Errorf("snooze interrupted operation: expected queued attempt %d, got %s attempt %d", operation.AttemptCount-1, dequeued.Status, dequeued.AttemptCount)
	}
	eventData, err := json.Marshal(map[string]any{
		"kind": candidate.kind, "status": "queued", "reason": "stale_heartbeat_recovery",
	})
	if err != nil {
		return fmt.Errorf("encode recovery event: %w", err)
	}
	if _, err := scope.Queries.AppendEvent(ctx, db.AppendEventParams{
		ID: repository.UUIDToPG(uuid.New()), Topic: "operation.recovered",
		ResourceType: candidate.resourceType, ResourceID: candidate.resourceID,
		OperationID: candidate.id, Data: eventData,
	}); err != nil {
		return fmt.Errorf("append recovery event: %w", err)
	}
	return nil
}
