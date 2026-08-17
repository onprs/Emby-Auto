package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/onprs/emby-auto/backend/internal/maintenance"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "rss-history-restore: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("rss-history-restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var subscriptionText, mappingProfileText string
	var expectedEntryCount, expectedFinalEpisode int
	var validateOnly bool
	flags.StringVar(&subscriptionText, "subscription-id", "", "deleted RSS subscription ID")
	flags.StringVar(&mappingProfileText, "mapping-profile-id", "", "active mapping profile ID")
	flags.IntVar(&expectedEntryCount, "expected-entry-count", 0, "exact number of history entries")
	flags.IntVar(&expectedFinalEpisode, "expected-final-episode", 0, "exact final source episode")
	flags.BoolVar(&validateOnly, "validate-only", false, "validate backup input without connecting to the database")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	subscriptionID, err := uuid.Parse(subscriptionText)
	if err != nil || subscriptionID == uuid.Nil {
		return fmt.Errorf("a valid --subscription-id is required")
	}
	mappingProfileID, err := uuid.Parse(mappingProfileText)
	if err != nil || mappingProfileID == uuid.Nil {
		return fmt.Errorf("a valid --mapping-profile-id is required")
	}
	if expectedEntryCount <= 0 || expectedFinalEpisode <= 0 {
		return fmt.Errorf("positive --expected-entry-count and --expected-final-episode are required")
	}

	snapshot, err := maintenance.ParseRSSHistoryDump(input, subscriptionID)
	if err != nil {
		return err
	}
	request := maintenance.RestoreCompletedRSSHistoryRequest{
		Snapshot:             snapshot,
		MappingProfileID:     mappingProfileID,
		ExpectedEntryCount:   expectedEntryCount,
		ExpectedFinalEpisode: int32(expectedFinalEpisode),
	}
	if err := maintenance.ValidateRestoreRequest(request); err != nil {
		return err
	}
	if validateOnly {
		_, err := fmt.Fprintf(output, "validated RSS subscription %s with %d history entries; enabled=false; scheduled=0\n", subscriptionID, len(snapshot.Entries))
		return err
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

	transactor := database.NewTransactor(pool)
	service.RegisterRSSSubscriptionProgressCommitHook(transactor)
	result, err := maintenance.NewRSSHistoryRestorer(transactor).Restore(ctx, request)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "restored RSS subscription %s with %d history entries; enabled=false; scheduled=0\n", result.SubscriptionID, result.EntryCount)
	return err
}
