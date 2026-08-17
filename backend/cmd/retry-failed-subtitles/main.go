package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

var legacySubtitleFailureCodes = map[string]struct{}{
	"simplified_chinese_subtitle_not_found": {},
	"subtitle_output_invalid":               {},
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "retry-failed-subtitles: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("retry-failed-subtitles", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	apply := flags.Bool("apply", false, "schedule retries for matching tasks")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(connectCtx, databaseURL)
	if err != nil {
		return fmt.Errorf("create database pool")
	}
	defer pool.Close()
	if err := pool.Ping(connectCtx); err != nil {
		return fmt.Errorf("connect to database")
	}

	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return fmt.Errorf("create River insertion client: %w", err)
	}
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	service.RegisterRSSSubscriptionProgressCommitHook(transactor)
	operations := service.NewOperationScheduler(transactor, riverClient)
	tasks := service.NewTaskWorkflow(queries, transactor, operations)
	commands := service.NewTaskCommandWorkflow(queries, transactor, operations, tasks)

	candidates, err := failedSubtitleCandidates(ctx, tasks)
	if err != nil {
		return err
	}
	if !*apply {
		_, err = fmt.Fprintf(output, "matched=%d apply=false\n", len(candidates))
		return err
	}
	actorID, err := uniqueAdminID(ctx, pool)
	if err != nil {
		return err
	}

	failures := make([]error, 0)
	scheduled := 0
	for index, task := range candidates {
		key := fmt.Sprintf("maintenance:retry-failed-subtitles:%s:v%d", task.ID, task.Version)
		if _, _, err := commands.Retry(ctx, task.ID, task.Version, key, actorID); err != nil {
			failures = append(failures, fmt.Errorf("candidate %d: %w", index+1, err))
			continue
		}
		scheduled++
	}
	_, writeErr := fmt.Fprintf(output, "matched=%d scheduled=%d failed=%d apply=true\n", len(candidates), scheduled, len(failures))
	return errors.Join(errors.Join(failures...), writeErr)
}

func failedSubtitleCandidates(ctx context.Context, tasks *service.TaskWorkflow) ([]domain.EpisodeTask, error) {
	state := domain.TaskFailed
	phase := "failed"
	var cursor *uuid.UUID
	candidates := make([]domain.EpisodeTask, 0)
	for {
		page, err := tasks.ListTasks(ctx, cursor, 100, &state, &phase)
		if err != nil {
			return nil, fmt.Errorf("list failed tasks: %w", err)
		}
		for _, task := range page.Items {
			if isLegacySubtitleFailure(task) {
				candidates = append(candidates, task)
			}
		}
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].ID.String() < candidates[right].ID.String()
	})
	return candidates, nil
}

func isLegacySubtitleFailure(task domain.EpisodeTask) bool {
	if task.State != domain.TaskFailed || task.SubtitleState != domain.SubtitleFailed || task.FailureStage != "subtitle" {
		return false
	}
	for _, operation := range task.Operations {
		if operation.Kind != "subtitle.prepare" || operation.Status != "failed" {
			continue
		}
		if _, ok := legacySubtitleFailureCodes[operation.ErrorCode]; ok {
			return true
		}
	}
	return false
}

func uniqueAdminID(ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, error) {
	rows, err := pool.Query(ctx, "SELECT id FROM admin_users ORDER BY created_at LIMIT 2")
	if err != nil {
		return uuid.Nil, fmt.Errorf("read administrator identity")
	}
	defer rows.Close()
	ids := make([]uuid.UUID, 0, 2)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return uuid.Nil, fmt.Errorf("read administrator identity")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, fmt.Errorf("read administrator identity")
	}
	if len(ids) != 1 || ids[0] == uuid.Nil {
		return uuid.Nil, fmt.Errorf("exactly one administrator is required")
	}
	return ids[0], nil
}
