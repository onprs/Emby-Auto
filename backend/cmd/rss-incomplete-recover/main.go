package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/maintenance"
	"github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	appworker "github.com/onprs/emby-auto/backend/internal/worker"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "rss-incomplete-recover: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("rss-incomplete-recover", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var subscriptionText, episodesText string
	flags.StringVar(&subscriptionText, "subscription-id", "", "incomplete RSS subscription ID")
	flags.StringVar(&episodesText, "source-episodes", "", "comma-separated source episodes to recover")
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
	episodes, err := parseEpisodes(episodesText)
	if err != nil {
		return err
	}
	request := maintenance.IncompleteRSSRecoveryRequest{SubscriptionID: subscriptionID, SourceEpisodes: episodes}
	if err := maintenance.ValidateIncompleteRSSRecoveryRequest(request); err != nil {
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

	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return fmt.Errorf("create River insertion client: %w", err)
	}
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	operations := service.NewOperationScheduler(transactor, riverClient)
	rssWorkflow := service.NewRSSWorkflow(queries, transactor, operations)
	cipher, err := service.NewSecretCipher(runtimeConfig.ConfigEncryptionKey)
	if err != nil {
		return fmt.Errorf("configure secret access: %w", err)
	}
	configuration := service.NewConfigurationService(repository.NewConfiguration(queries, transactor), cipher)
	verifier := appworker.NewRSSRealtimeEmbyVerifier(
		configuration,
		queries,
		transactor,
		func(options emby.ClientOptions) (appworker.RSSRealtimeEmbyClient, error) {
			return emby.NewClient(options)
		},
	)
	recovery := maintenance.NewIncompleteRSSRecovery(queries, transactor, realtimeRecoveryScheduler{
		workflow: rssWorkflow,
		verifier: verifier,
	})
	result, err := recovery.Recover(ctx, request)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		output,
		"prepared RSS subscription %s recovery: requested=%d scheduled=%d existing=%d enabled=false\n",
		result.SubscriptionID,
		result.RequestedCount,
		result.ScheduledCount,
		result.ExistingCount,
	)
	return err
}

type realtimeRecoveryScheduler struct {
	workflow *service.RSSWorkflow
	verifier service.RSSRealtimeTargetVerifier
}

func (scheduler realtimeRecoveryScheduler) ScheduleRSSRecoveryDownload(
	ctx context.Context,
	candidate domain.RSSEnqueueCandidate,
) error {
	checkID, err := scheduler.verifier.VerifyEntry(ctx, candidate.EntryID)
	if err != nil {
		return err
	}
	if checkID == uuid.Nil {
		return fmt.Errorf("mapped RSS target did not produce a real-time Emby check")
	}
	return scheduler.workflow.ScheduleRSSRecoveryDownloadWithRealtimeCheck(ctx, candidate, checkID)
}

func parseEpisodes(value string) ([]int32, error) {
	parts := strings.Split(strings.TrimSpace(value), ",")
	if len(parts) == 1 && strings.TrimSpace(parts[0]) == "" {
		return nil, fmt.Errorf("--source-episodes is required")
	}
	episodes := make([]int32, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.ParseInt(strings.TrimSpace(part), 10, 32)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid source episode %q", strings.TrimSpace(part))
		}
		episodes = append(episodes, int32(parsed))
	}
	sort.Slice(episodes, func(left, right int) bool { return episodes[left] < episodes[right] })
	return episodes, nil
}
