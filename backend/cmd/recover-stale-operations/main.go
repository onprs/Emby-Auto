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
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

type recoveryCandidate struct {
	id           pgtype.UUID
	riverJobID   int64
	attempt      int32
	kind         string
	resourceType *string
	resourceID   pgtype.UUID
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

	eligible, err := countEligible(ctx, pool, *staleAfter, *kind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "count eligible operations: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("eligible=%d\n", eligible)
	if !*apply || eligible == 0 {
		fmt.Println("recover stale operations: dry-run")
		return
	}

	transactor := database.NewTransactor(pool)
	riverDriver := riverpgxv5.New(pool)
	recovered := 0
	for {
		changed := false
		err := transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			candidate, found, err := lockCandidate(ctx, scope.Tx, *staleAfter, *kind)
			if err != nil || !found {
				return err
			}
			now := time.Now().UTC()
			errorData, err := json.Marshal(rivertype.AttemptError{
				At: now, Attempt: int(candidate.attempt), Error: "Interrupted operation recovered by release preflight", Trace: "",
			})
			if err != nil {
				return fmt.Errorf("encode River recovery error: %w", err)
			}
			if _, err := riverDriver.UnwrapExecutor(scope.Tx).JobRescueMany(ctx, &riverdriver.JobRescueManyParams{
				ID: []int64{candidate.riverJobID}, Error: [][]byte{errorData}, FinalizedAt: []*time.Time{nil},
				ScheduledAt: []time.Time{now}, State: []string{string(rivertype.JobStateRetryable)},
			}); err != nil {
				return fmt.Errorf("rescue River job: %w", err)
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
			if _, err := scope.Queries.SnoozeOperation(ctx, candidate.id); err != nil {
				return fmt.Errorf("snooze interrupted operation: %w", err)
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
			changed = true
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "recover stale operation: %v\n", err)
			os.Exit(1)
		}
		if !changed {
			break
		}
		recovered++
	}
	fmt.Printf("recovered=%d\n", recovered)
}

func countEligible(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, staleAfter time.Duration, kind string) (int64, error) {
	var count int64
	err := pool.QueryRow(ctx, `
SELECT count(DISTINCT operation.id)::bigint
FROM operations AS operation
JOIN river_job AS job ON job.id = operation.river_job_id
JOIN operation_attempts AS attempt
  ON attempt.operation_id = operation.id
 AND attempt.status = 'running'
WHERE operation.status = 'running'
  AND operation.cancel_requested_at IS NULL
  AND operation.heartbeat_at < now() - $1::interval
  AND job.state = 'running'
  AND ($2::text = '' OR operation.kind = $2::text)`, staleAfter.String(), kind).Scan(&count)
	return count, err
}

func lockCandidate(ctx context.Context, tx pgx.Tx, staleAfter time.Duration, kind string) (recoveryCandidate, bool, error) {
	var candidate recoveryCandidate
	err := tx.QueryRow(ctx, `
SELECT operation.id, operation.river_job_id, attempt.attempt, operation.kind,
       operation.resource_type, operation.resource_id
FROM operations AS operation
JOIN river_job AS job ON job.id = operation.river_job_id
JOIN operation_attempts AS attempt
  ON attempt.operation_id = operation.id
 AND attempt.status = 'running'
WHERE operation.status = 'running'
  AND operation.cancel_requested_at IS NULL
  AND operation.heartbeat_at < now() - $1::interval
  AND job.state = 'running'
  AND ($2::text = '' OR operation.kind = $2::text)
ORDER BY operation.heartbeat_at, operation.id, attempt.attempt DESC
LIMIT 1
FOR UPDATE OF operation, job, attempt SKIP LOCKED`, staleAfter.String(), kind).Scan(
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
