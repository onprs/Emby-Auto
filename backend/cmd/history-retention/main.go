// Command history-retention 按显式截止时间分批清理终态历史操作与 Agent 决议。
// 默认只统计候选；只有传入 --apply 才会删除，且每批有界、可随时中断。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/maintenance"
	"github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "history-retention: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("history-retention", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var olderThan time.Duration
	var batchSize, maxBatches int
	var apply bool
	flags.DurationVar(&olderThan, "older-than", 90*24*time.Hour, "retain records newer than this duration")
	flags.IntVar(&batchSize, "batch-size", 500, "rows deleted per transaction")
	flags.IntVar(&maxBatches, "max-batches", 20, "maximum batches executed by this run")
	flags.BoolVar(&apply, "apply", false, "delete candidates; without this flag the command only counts")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	request := maintenance.HistoryRetentionRequest{
		Before:     time.Now().Add(-olderThan),
		BatchSize:  int32(batchSize),
		MaxBatches: maxBatches,
		Apply:      apply,
	}
	if batchSize <= 0 || batchSize > int(maintenance.MaxHistoryRetentionBatchSize) {
		return fmt.Errorf("batch-size must be between 1 and %d", maintenance.MaxHistoryRetentionBatchSize)
	}
	if err := maintenance.ValidateHistoryRetentionRequest(request, time.Now()); err != nil {
		return err
	}

	runtimeConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("load runtime configuration: %w", err)
	}
	if runtimeConfig.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(connectCtx, runtimeConfig.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create database pool")
	}
	defer pool.Close()
	if err := pool.Ping(connectCtx); err != nil {
		return fmt.Errorf("connect to database")
	}

	retention := maintenance.NewHistoryRetention(db.New(pool), database.NewTransactor(pool))
	result, err := retention.Run(ctx, request)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return nil
}
