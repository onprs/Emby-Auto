package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/logging"
	"github.com/onprs/emby-auto/backend/internal/platform/mediatools"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/platform/systemmetrics"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/transport/httpapi"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func main() {
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

	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

type activeRuntime struct {
	mu   sync.Mutex
	pool interface{ Close() }
}

type runtimeResources struct {
	pool   interface{ Close() }
	cancel context.CancelFunc
}

func (resources *runtimeResources) Close() {
	resources.cancel()
	resources.pool.Close()
}

func (runtime *activeRuntime) Activate(
	ctx context.Context,
	cfg config.Config,
	bootstrap *service.Bootstrap,
	switcher *httpapi.SwitchingHandler,
	settings domain.RuntimeBootstrap,
) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.pool != nil {
		return nil
	}
	handler, pool, err := buildRuntimeHandler(ctx, cfg, bootstrap, settings)
	if err != nil {
		return err
	}
	switcher.Swap(handler)
	runtime.pool = pool
	return nil
}

func (runtime *activeRuntime) Active() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.pool != nil
}

func (runtime *activeRuntime) Close() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.pool != nil {
		runtime.pool.Close()
		runtime.pool = nil
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	switcher := httpapi.NewSwitchingHandler(nil)
	runtime := &activeRuntime{}
	defer runtime.Close()

	store := config.NewBootstrapStore(cfg.BootstrapConfigPath)
	var bootstrap *service.Bootstrap
	bootstrap = service.NewBootstrap(service.BootstrapOptions{
		DatabaseURL:               cfg.DatabaseURL,
		ConfigEncryptionKey:       cfg.ConfigEncryptionKey,
		DatabaseManagedExternally: cfg.DatabaseManagedExternally,
		Store:                     store,
		Migrator:                  database.NewMigrator(),
		Passwords:                 service.NewPasswordHasher(),
		Activate: func(activationCtx context.Context, settings domain.RuntimeBootstrap) error {
			return runtime.Activate(activationCtx, cfg, bootstrap, switcher, settings)
		},
	})
	setupServer := httpapi.NewServer(nil, httpapi.WithSetup(bootstrap))
	switcher.Swap(httpapi.SetupOnly(httpapi.NewHandler(setupServer)))

	_, activationErr := bootstrap.ActivateExisting(ctx)
	if activationErr != nil {
		logger.Warn("runtime activation is pending")
	}

	server := &http.Server{
		Addr:              cfg.APIAddress,
		Handler:           switcher,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("api listening", "address", cfg.APIAddress)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErrors <- err
	}()
	go retryRuntimeActivation(ctx, bootstrap, runtime, logger)

	select {
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		logger.Info("api stopped")
		return nil
	}
}

func retryRuntimeActivation(
	ctx context.Context,
	bootstrap *service.Bootstrap,
	runtime *activeRuntime,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if runtime.Active() {
				return
			}
			if _, err := bootstrap.ActivateExisting(ctx); err != nil {
				logger.Warn("runtime activation retry failed")
			}
		}
	}
}

func buildRuntimeHandler(
	ctx context.Context,
	cfg config.Config,
	bootstrap *service.Bootstrap,
	settings domain.RuntimeBootstrap,
) (http.Handler, interface{ Close() }, error) {
	pool, err := database.Open(ctx, settings.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("check PostgreSQL readiness: %w", err)
	}
	if len(settings.ConfigEncryptionKey) != 32 {
		pool.Close()
		return nil, nil, fmt.Errorf("configuration encryption key is unavailable")
	}
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	jobClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("create River insertion client: %w", err)
	}
	operationScheduler := service.NewOperationScheduler(transactor, jobClient)
	searchWorkflow := service.NewSearchWorkflow(queries, transactor, operationScheduler)
	rssWorkflow := service.NewRSSWorkflow(queries, transactor, operationScheduler)
	if _, err := rssWorkflow.ReconcileSubscriptionProgress(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("reconcile RSS subscription progress: %w", err)
	}
	taskWorkflow := service.NewTaskWorkflow(queries, transactor, operationScheduler)
	catalogWorkflow := service.NewCatalogWorkflow(queries, transactor, operationScheduler)
	embyCatalogWorkflow := service.NewEmbyCatalogWorkflow(queries, transactor, operationScheduler)
	authentication := service.NewAuthentication(repository.NewAuth(queries), service.NewPasswordHasher(), cfg.SessionTTL)
	secretCipher, err := service.NewSecretCipher(settings.ConfigEncryptionKey)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	configuration := service.NewConfigurationService(repository.NewConfiguration(queries, transactor), secretCipher)
	tmdbSearcher := service.NewTMDbClientSearcher(configuration, cfg.TMDbBaseURL)
	agentResolutions := service.NewAgentResolutionService(
		queries, transactor, operationScheduler, configuration, catalogWorkflow, tmdbSearcher,
	)
	readService := service.NewReadService(queries)
	rssFeedLookup := service.NewRSSFeedLookup(configuration).WithCatalogMatching(tmdbSearcher, queries, agentResolutions)
	tmdbQuery := service.NewTMDbQueryService(queries, tmdbSearcher)
	mediaTools := mediatools.New(mediatools.OSExecutor{})
	connectivity := service.NewConnectivityService(configuration, mediaTools, queries, cfg.TMDbBaseURL)
	backgroundTransfers := service.NewQBittorrentBackgroundTransfers(
		configuration,
		func(options qbittorrent.ClientOptions) (service.BackgroundTorrentClient, error) {
			return qbittorrent.NewClient(options)
		},
	)
	backgroundRuntime := service.NewBackgroundRuntimeController(
		cfg.HostControlExecutable,
		service.OSBackgroundRuntimeExecutor{},
		backgroundTransfers,
	)
	artifactContent := service.NewArtifactContentService(queries)
	acquisitionCommands := service.NewAcquisitionDeletionWorkflow(queries, transactor, operationScheduler)
	downloadCommands := service.NewDownloadCommandWorkflow(transactor, operationScheduler)
	taskCommands := service.NewTaskCommandWorkflow(queries, transactor, operationScheduler, taskWorkflow)
	var hostControlNetworkSource systemmetrics.HostControlNetworkReader
	if cfg.HostControlExecutable != "" {
		hostControlNetworkSource = systemmetrics.CommandHostControlNetworkReader{Executable: cfg.HostControlExecutable}
	}
	metrics := systemmetrics.NewCollector(systemmetrics.NewGopsutilSource(hostControlNetworkSource), systemmetrics.Options{})
	if current, loadErr := configuration.Load(ctx); loadErr == nil {
		metrics.SetDiskPaths(systemMetricPaths(current.Settings.Paths))
	}
	metrics.Sample(ctx)
	metricsCtx, cancelMetrics := context.WithCancel(context.Background())
	go metrics.Run(metricsCtx)
	server := httpapi.NewServer(
		repository.NewDatabaseHealth(pool, cfg.DatabaseConnectTimeout),
		httpapi.WithSetup(bootstrap),
		httpapi.WithAuthentication(authentication, cfg.SessionCookieSecure),
		httpapi.WithRuntimeConfiguration(configuration),
		httpapi.WithEvents(repository.NewEvents(queries)),
		httpapi.WithSearch(searchWorkflow),
		httpapi.WithRSSSubscriptions(rssWorkflow),
		httpapi.WithRSSFeedLookup(rssFeedLookup),
		httpapi.WithTasks(taskWorkflow),
		httpapi.WithCatalog(catalogWorkflow),
		httpapi.WithEmbyCatalog(embyCatalogWorkflow),
		httpapi.WithReadModels(readService),
		httpapi.WithTMDbSearch(tmdbQuery),
		httpapi.WithConnectivity(connectivity),
		httpapi.WithAgentResolutions(agentResolutions),
		httpapi.WithBackgroundRuntime(backgroundRuntime),
		httpapi.WithSystemMetrics(metrics),
		httpapi.WithArtifactContent(artifactContent),
		httpapi.WithAcquisitionCommands(acquisitionCommands),
		httpapi.WithDownloadCommands(downloadCommands),
		httpapi.WithTaskCommands(taskCommands),
	)
	return httpapi.NewHandler(server), &runtimeResources{pool: pool, cancel: cancelMetrics}, nil
}

func systemMetricPaths(paths domain.PathSettings) []string {
	return []string{
		paths.DownloadRoot,
		paths.WorkRoot,
		paths.StagingRoot,
		paths.EffectiveAnimeLibraryRoot(),
		paths.MovieLibraryRoot,
	}
}
