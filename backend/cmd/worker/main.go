package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
	"github.com/onprs/emby-auto/backend/internal/platform/logging"
	"github.com/onprs/emby-auto/backend/internal/platform/mediatools"
	"github.com/onprs/emby-auto/backend/internal/platform/proxyhttp"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/platform/rssfeed"
	"github.com/onprs/emby-auto/backend/internal/platform/searchsource"
	"github.com/onprs/emby-auto/backend/internal/platform/tmdb"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	appworker "github.com/onprs/emby-auto/backend/internal/worker"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func main() {
	checkOnly := flag.Bool("check", false, "check configuration and database connectivity, then exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}

	logger, err = logging.New(cfg.LogLevel)
	if err != nil {
		logger.Error("configure logging", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, logger, *checkOnly); err != nil {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger, checkOnly bool) error {
	resolvedConfig, pool, err := waitForInstalledRuntime(ctx, cfg, logger, checkOnly)
	if err != nil {
		return err
	}
	cfg = resolvedConfig
	defer pool.Close()

	libraryAccess, err := appworker.ResolveConfiguredImportedLibraryAccess(
		ctx,
		cfg.MediaOwnerUID,
		cfg.HostControlExecutable,
		appworker.OSHostControlExecutor{},
	)
	if err != nil {
		return fmt.Errorf("resolve imported library access: %w", err)
	}
	if checkOnly {
		logger.Info("worker readiness check passed")
		return nil
	}
	workerID, err := processWorkerID()
	if err != nil {
		return err
	}
	secretCipher, err := service.NewSecretCipher(cfg.ConfigEncryptionKey)
	if err != nil {
		return err
	}
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	configuration := service.NewConfigurationService(
		repository.NewConfiguration(queries, transactor),
		secretCipher,
	)
	clientHandle := appqueue.NewClientHandle()
	operationScheduler := service.NewOperationScheduler(transactor, clientHandle)
	downloadWorkflow := service.NewDownloadWorkflow(queries, transactor, operationScheduler)
	rssWorkflow := service.NewRSSWorkflow(queries, transactor, operationScheduler)
	searchOptions := searchsource.ClientOptions{}
	if cfg.SearchProviderURLTemplate != "" {
		searchOptions.Providers = []searchsource.Provider{{Name: cfg.SearchProviderName, SearchURLTemplate: cfg.SearchProviderURLTemplate}}
	}
	newFeedClient := func(ctx context.Context) (appworker.RSSFeedClient, error) {
		runtimeConfiguration, err := configuration.Load(ctx)
		if err != nil {
			return nil, err
		}
		httpClient, err := proxyhttp.NewClient(runtimeConfiguration.Settings.NetworkProxy)
		if err != nil {
			return nil, err
		}
		return rssfeed.NewClient(rssfeed.ClientOptions{HTTPClient: httpClient})
	}
	newSearchClient := func(ctx context.Context) (appworker.SearchClient, error) {
		runtimeConfiguration, err := configuration.Load(ctx)
		if err != nil {
			return nil, err
		}
		httpClient, err := proxyhttp.NewClient(runtimeConfiguration.Settings.NetworkProxy)
		if err != nil {
			return nil, err
		}
		options := searchOptions
		options.HTTPClient = httpClient
		return searchsource.NewClient(options)
	}
	searchWorkflow := service.NewSearchWorkflow(queries, transactor, operationScheduler)
	mediaWorkflow := service.NewMediaWorkflow(queries, transactor, operationScheduler)
	taskWorkflow := service.NewTaskWorkflow(queries, transactor, operationScheduler)
	catalogWorkflow := service.NewCatalogWorkflow(queries, transactor, operationScheduler)
	agentTMDbSearcher := service.NewTMDbClientSearcher(configuration, cfg.TMDbBaseURL)
	agentResolutions := service.NewAgentResolutionService(
		queries, transactor, operationScheduler, configuration, catalogWorkflow, agentTMDbSearcher,
	)
	embyCatalogWorkflow := service.NewEmbyCatalogWorkflow(queries, transactor, operationScheduler)
	acquisitionDeletion := service.NewAcquisitionDeletionWorkflow(queries, transactor, operationScheduler)
	mediaTools := mediatools.New(nil)
	newTorrentClient := func(options qbittorrent.ClientOptions) (appworker.TorrentClient, error) {
		return qbittorrent.NewClient(options)
	}
	newCleanupTorrentClient := func(options qbittorrent.ClientOptions) (appworker.CleanupTorrentClient, error) {
		return qbittorrent.NewClient(options)
	}
	newDownloadCancelClient := func(options qbittorrent.ClientOptions) (appworker.DownloadCancelTorrentClient, error) {
		return qbittorrent.NewClient(options)
	}
	newAcquisitionDeleteClient := func(options qbittorrent.ClientOptions) (appworker.AcquisitionDeleteTorrentClient, error) {
		return qbittorrent.NewClient(options)
	}
	newEmbyClient := func(options emby.ClientOptions) (appworker.EmbyRefreshClient, error) {
		return emby.NewClient(options)
	}
	newEmbyCatalogClient := func(options emby.ClientOptions) (appworker.EmbyCatalogClient, error) {
		return emby.NewClient(options)
	}
	newRSSRealtimeEmbyClient := func(options emby.ClientOptions) (appworker.RSSRealtimeEmbyClient, error) {
		return emby.NewClient(options)
	}
	rssRealtimeVerifier := appworker.NewRSSRealtimeEmbyVerifier(
		configuration, queries, transactor, newRSSRealtimeEmbyClient,
	)
	agentResolutions.WithRSSRealtimeTargetVerifier(rssRealtimeVerifier)
	agentResolutions.WithRSSPreacquisitionMappingAgent(rssWorkflow)
	agentResolutions.WithSubtitleTextInspector(appworker.NewSubtitleTextInspector(configuration, mediaTools))
	newTMDbClient := func(options tmdb.ClientOptions) (appworker.TMDbCatalogClient, error) {
		options.BaseURL = cfg.TMDbBaseURL
		return tmdb.NewClient(options)
	}
	lifecycle := repository.NewOperationLifecycle(queries, transactor)
	acquisitionDeleteHandler := appworker.NewAcquisitionDeleteHandler(configuration, acquisitionDeletion, newAcquisitionDeleteClient)
	embyRefreshHandler := appworker.NewEmbyRefreshHandler(configuration, newEmbyClient)
	workers := appworker.NewWorkers(
		lifecycle,
		map[string]appworker.Handler{
			appqueue.KindAgentResolve: appworker.NewAgentResolveHandler(agentResolutions),
			appqueue.KindSearchRun:    appworker.NewConfiguredSearchRunHandler(newSearchClient, searchWorkflow),
			appqueue.KindRSSPoll: appworker.NewConfiguredRSSPollHandler(
				newFeedClient, rssWorkflow, cfg.RSSScheduleConcurrency, agentResolutions,
			).WithRealtimeTargetVerifier(rssRealtimeVerifier),
			appqueue.KindDownloadEnqueue: appworker.NewDownloadEnqueueHandler(
				configuration, downloadWorkflow, newTorrentClient, agentResolutions,
			).WithManifestResolutionEnabled(cfg.DownloadManifestResolutionEnabled),
			appqueue.KindDownloadSelectionApply: appworker.NewDownloadSelectionApplyHandler(
				configuration, downloadWorkflow, newTorrentClient, agentResolutions,
			),
			appqueue.KindDownloadSync:            appworker.NewDownloadSyncHandler(configuration, downloadWorkflow, newTorrentClient, cfg.DownloadSyncInterval),
			appqueue.KindDownloadMaterialize:     appworker.NewDownloadMaterializeHandler(mediaWorkflow),
			appqueue.KindSubtitlePrepare:         appworker.NewSubtitlePrepareHandler(configuration, mediaTools, mediaWorkflow, agentResolutions),
			appqueue.KindTranscodeRun:            appworker.NewTranscodeRunHandler(configuration, mediaTools, mediaWorkflow),
			appqueue.KindMediaFinalize:           appworker.NewMediaFinalizeHandler(mediaWorkflow),
			appqueue.KindEmbyImport:              appworker.NewEmbyImportHandler(configuration, taskWorkflow, libraryAccess),
			appqueue.KindCleanupRun:              appworker.NewCleanupRunHandler(configuration, taskWorkflow, newCleanupTorrentClient),
			appqueue.KindEmbyRefresh:             embyRefreshHandler,
			appqueue.KindEmbyScan:                appworker.NewEmbyScanHandler(configuration, embyCatalogWorkflow, newEmbyCatalogClient),
			appqueue.KindTMDbSync:                appworker.NewTMDbSyncHandler(configuration, catalogWorkflow, newTMDbClient, agentResolutions),
			appqueue.KindTaskCancel:              appworker.NewTaskCancelHandler(taskWorkflow),
			appqueue.KindDownloadCancel:          appworker.NewDownloadCancelHandler(configuration, downloadWorkflow, newDownloadCancelClient),
			appqueue.KindAcquisitionDelete:       acquisitionDeleteHandler,
			appqueue.KindRSSSubscriptionComplete: appworker.NewRSSSubscriptionDeleteHandler(acquisitionDeletion, acquisitionDeleteHandler, embyRefreshHandler),
			appqueue.KindRSSSubscriptionDelete:   appworker.NewRSSSubscriptionDeleteHandler(acquisitionDeletion, acquisitionDeleteHandler, embyRefreshHandler),
		},
		cfg.OperationHeartbeatInterval,
		workerID,
	)
	transcodeWorkers := cfg.RiverTranscodeWorkers
	if profile, profileErr := queries.GetDefaultTranscodeProfile(ctx); profileErr == nil && profile.MaxConcurrency > 0 {
		transcodeWorkers = int(profile.MaxConcurrency)
	}
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		ID:         workerID,
		Logger:     logger,
		Workers:    workers,
		JobTimeout: 24 * time.Hour,
		Queues: map[string]river.QueueConfig{
			appqueue.QueueGeneral:   {MaxWorkers: cfg.RiverGeneralWorkers},
			appqueue.QueueTranscode: {MaxWorkers: transcodeWorkers},
			appqueue.QueueAgent:     {MaxWorkers: 1},
		},
		MaxAttempts:          3,
		RescueStuckJobsAfter: 25 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("create River client: %w", err)
	}
	if err := clientHandle.Bind(riverClient); err != nil {
		return fmt.Errorf("bind River client: %w", err)
	}
	reconciledAdjudications, err := agentResolutions.ReconcileAutomaticRSSReleaseAdjudications(ctx)
	if err != nil {
		return fmt.Errorf("reconcile automatic RSS release adjudications: %w", err)
	}
	if reconciledAdjudications > 0 {
		logger.Info("reconciled automatic RSS release adjudications", "batch_count", reconciledAdjudications)
	}
	reconciledPreacquisitionMappings, err := agentResolutions.ReconcileAutomaticRSSPreacquisitionMappings(ctx)
	if err != nil {
		return fmt.Errorf("reconcile RSS pre-acquisition Agent Mappings: %w", err)
	}
	if reconciledPreacquisitionMappings > 0 {
		logger.Info("reconciled RSS pre-acquisition Agent Mappings", "scope_count", reconciledPreacquisitionMappings)
	}
	reconciledMappings, err := agentResolutions.ReconcileAutomaticEpisodeMappings(ctx)
	if err != nil {
		return fmt.Errorf("reconcile RSS automatic episode Mappings: %w", err)
	}
	if reconciledMappings > 0 {
		logger.Info("reconciled RSS automatic episode Mappings", "acquisition_count", reconciledMappings)
	}
	reconciledMappingPolls, err := rssWorkflow.ReconcilePreAcquisitionMappingPolls(ctx)
	if err != nil {
		return fmt.Errorf("reconcile RSS pre-acquisition mapping polls: %w", err)
	}
	if reconciledMappingPolls > 0 {
		logger.Info("reconciled RSS pre-acquisition mapping polls", "subscription_count", reconciledMappingPolls)
	}
	reconciledReviews, err := rssWorkflow.ReconcileAutoReviews(ctx)
	if err != nil {
		return fmt.Errorf("reconcile RSS automatic reviews: %w", err)
	}
	if reconciledReviews > 0 {
		logger.Info("reconciled RSS automatic reviews", "task_count", reconciledReviews)
	}
	if err := riverClient.Start(ctx); err != nil {
		return fmt.Errorf("start River client: %w", err)
	}

	logger.Info("worker ready", "worker_id", workerID)
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := riverClient.StopAndCancel(shutdownCtx); err != nil {
		return fmt.Errorf("stop and cancel River client: %w", err)
	}
	logger.Info("worker stopped")
	return nil
}

func waitForInstalledRuntime(
	ctx context.Context,
	initial config.Config,
	logger *slog.Logger,
	checkOnly bool,
) (config.Config, *pgxpool.Pool, error) {
	current := initial
	loggedWaiting := false
	for {
		if current.DatabaseURL != "" && len(current.ConfigEncryptionKey) == 32 {
			pool, err := database.Open(ctx, current.DatabaseURL)
			if err == nil {
				err = repository.NewDatabaseHealth(pool, current.DatabaseConnectTimeout).Ping(ctx)
			}
			if err == nil {
				queries := db.New(pool)
				installation, installationErr := queries.GetInstallationState(ctx)
				if installationErr == nil && installation.CompletedBy.Valid {
					return current, pool, nil
				}
				err = installationErr
			}
			if pool != nil {
				pool.Close()
			}
			if checkOnly {
				if err == nil {
					err = fmt.Errorf("installation is incomplete")
				}
				return config.Config{}, nil, fmt.Errorf("worker runtime is not ready: %w", err)
			}
		} else if checkOnly {
			return config.Config{}, nil, fmt.Errorf("worker runtime is not ready: database or encryption configuration is unavailable")
		}
		if !loggedWaiting {
			logger.Info("worker waiting for completed installation")
			loggedWaiting = true
		}
		select {
		case <-ctx.Done():
			return config.Config{}, nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
		reloaded, err := config.Load()
		if err != nil {
			return config.Config{}, nil, fmt.Errorf("reload bootstrap configuration: %w", err)
		}
		current = reloaded
	}
}

func processWorkerID() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("read worker hostname: %w", err)
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid()), nil
}
