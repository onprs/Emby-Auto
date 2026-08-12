package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

type operationCount struct {
	kind  string
	count int64
}

var safeKindPattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

func main() {
	requireIdle := flag.Bool("require-idle", false, "exit unsuccessfully when long-running work is active")
	flag.Parse()

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	operations, err := runningOperations(ctx, pool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "count running operations: %v\n", err)
		os.Exit(1)
	}
	riverStates, err := runningOperationRiverStates(ctx, pool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "count running operation River states: %v\n", err)
		os.Exit(1)
	}
	longTasks, err := count(ctx, pool, `
SELECT count(*)::bigint
FROM episode_tasks
WHERE state IN ('processing', 'finalizing', 'importing')
   OR video_state = 'transcoding'
   OR subtitle_state = 'extracting_or_converting'`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "count long-running tasks: %v\n", err)
		os.Exit(1)
	}
	activeDownloads, err := count(ctx, pool, `
SELECT count(*)::bigint
FROM downloads
WHERE status IN ('enqueue_pending', 'downloading', 'selecting_files')`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "count active downloads: %v\n", err)
		os.Exit(1)
	}
	activeAgentOperations, err := count(ctx, pool, `
SELECT count(*)::bigint
FROM operations
WHERE kind = 'agent.resolve' AND status IN ('queued', 'running')`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "count active Agent operations: %v\n", err)
		os.Exit(1)
	}
	staleOperations, err := count(ctx, pool, `
SELECT count(*)::bigint
FROM operations
WHERE status = 'running'
  AND (heartbeat_at IS NULL OR heartbeat_at < now() - interval '2 minutes')`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "count stale running operations: %v\n", err)
		os.Exit(1)
	}
	oldestHeartbeatSeconds, err := count(ctx, pool, `
SELECT COALESCE(floor(max(extract(epoch FROM now() - COALESCE(heartbeat_at, started_at, created_at))))::bigint, 0)
FROM operations
WHERE status = 'running'`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "measure running operation heartbeat age: %v\n", err)
		os.Exit(1)
	}

	var runningTotal int64
	for _, item := range operations {
		runningTotal += item.count
	}
	fmt.Printf("operations.running.total=%d\n", runningTotal)
	for _, item := range operations {
		fmt.Printf("operations.running.kind.%s=%d\n", safeKind(item.kind), item.count)
	}
	for _, item := range riverStates {
		fmt.Printf("operations.running.river_state.%s=%d\n", safeKind(item.kind), item.count)
	}
	fmt.Printf("operations.running.stale=%d\n", staleOperations)
	fmt.Printf("operations.running.oldest_heartbeat_seconds=%d\n", oldestHeartbeatSeconds)
	fmt.Printf("tasks.long_running=%d\n", longTasks)
	fmt.Printf("downloads.active=%d\n", activeDownloads)
	fmt.Printf("agent.operations.active=%d\n", activeAgentOperations)

	if *requireIdle && (runningTotal > 0 || longTasks > 0 || activeAgentOperations > 0) {
		fmt.Fprintln(os.Stderr, "release preflight: long-running work is active")
		os.Exit(3)
	}
	fmt.Println("release preflight: idle")
}

func runningOperations(ctx context.Context, pool interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]operationCount, error) {
	rows, err := pool.Query(ctx, `
SELECT kind, count(*)::bigint
FROM operations
WHERE status = 'running'
GROUP BY kind
ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]operationCount, 0)
	for rows.Next() {
		var item operationCount
		if err := rows.Scan(&item.kind, &item.count); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func runningOperationRiverStates(ctx context.Context, pool interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]operationCount, error) {
	rows, err := pool.Query(ctx, `
SELECT COALESCE(job.state::text, 'missing'), count(*)::bigint
FROM operations AS operation
LEFT JOIN river_job AS job ON job.id = operation.river_job_id
WHERE operation.status = 'running'
GROUP BY COALESCE(job.state::text, 'missing')
ORDER BY COALESCE(job.state::text, 'missing')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]operationCount, 0)
	for rows.Next() {
		var item operationCount
		if err := rows.Scan(&item.kind, &item.count); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func count(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, query string) (int64, error) {
	var value int64
	err := pool.QueryRow(ctx, query).Scan(&value)
	return value, err
}

func safeKind(value string) string {
	if safeKindPattern.MatchString(value) {
		return value
	}
	return "other"
}
