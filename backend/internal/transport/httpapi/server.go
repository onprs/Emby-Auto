package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type AuthenticationService interface {
	Login(context.Context, string, string) (service.LoginResult, error)
	Authenticate(context.Context, string) (domain.Session, error)
	Logout(context.Context, string) error
}

type SetupService interface {
	Status(context.Context) (domain.SetupStatus, error)
	Initialize(context.Context, domain.InitializeSetup) (domain.SetupStatus, error)
}

type RuntimeConfigurationService interface {
	Load(context.Context) (domain.Configuration, error)
	Update(context.Context, domain.ConfigurationUpdate, uuid.UUID) (domain.Configuration, error)
	ResolveSecret(context.Context, string) (string, error)
}

type EventSource interface {
	List(context.Context, *uuid.UUID, int32) ([]domain.Event, error)
	Stats(context.Context) (domain.EventStats, error)
}

type SearchService interface {
	CreateSearch(context.Context, domain.CreateSearch) (domain.SearchCommandResult, error)
	GetSearch(context.Context, uuid.UUID) (domain.SearchRun, error)
	CreateAcquisition(context.Context, domain.CreateSearchAcquisition) (domain.SearchAcquisitionResult, error)
}

type RSSSubscriptionService interface {
	CreateSubscription(context.Context, domain.CreateRSSSubscription) (domain.RSSSubscription, error)
	ListSubscriptions(context.Context, *uuid.UUID, int, *string, *string, *string) (domain.RSSSubscriptionPage, error)
	GetSubscription(context.Context, uuid.UUID) (domain.RSSSubscription, error)
	UpdateSubscription(context.Context, domain.UpdateRSSSubscription) (domain.RSSSubscription, error)
	ArchiveSubscription(context.Context, uuid.UUID, int32, uuid.UUID) error
	RequestSubscriptionDeletion(context.Context, uuid.UUID, int32, string, bool, uuid.UUID) (domain.Operation, error)
	ScheduleManualPoll(context.Context, uuid.UUID, string, uuid.UUID) (domain.Operation, error)
}

type RSSFeedLookupService interface {
	Lookup(context.Context, string) (domain.RSSFeedLookup, error)
}

type CatalogService interface {
	ScheduleTMDbSync(context.Context, domain.SyncTMDbSeries) (domain.CatalogCommandResult, error)
	PreviewEpisodeMapping(context.Context, domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error)
	SaveEpisodeMapping(context.Context, domain.EpisodeMappingPlanInput) (domain.SavedEpisodeMapping, error)
}

type EmbyCatalogService interface {
	ScheduleRefresh(context.Context, domain.CreateEmbyRefresh) (domain.Operation, error)
	ScheduleScan(context.Context, domain.CreateEmbyScan) (domain.EmbyScanCommandResult, error)
	GetScan(context.Context, uuid.UUID) (domain.EmbyScan, error)
	ListScans(context.Context, *uuid.UUID, int) (domain.EmbyScanPage, error)
	ListLibraries(context.Context) ([]domain.EmbyLibrary, error)
	ListLibraryItems(context.Context, uuid.UUID, domain.EmbyLibraryItemFilter, *uuid.UUID, int) (domain.EmbyLibraryItemPage, error)
}

type TaskService interface {
	ListTasks(context.Context, *uuid.UUID, int, *domain.TaskState, *string) (domain.EpisodeTaskPage, error)
	GetTask(context.Context, uuid.UUID) (domain.EpisodeTask, error)
	ReviewTask(context.Context, domain.ReviewTask) (domain.EpisodeTask, error)
	QueueImport(context.Context, domain.QueueTaskImport) (domain.TaskImportResult, error)
}

// ReadModelService groups the paginated database-backed read models.
type ReadModelService interface {
	ListSearches(context.Context, *uuid.UUID, int, *string, *string) (domain.SearchRunSummaryPage, error)
	ListDownloads(context.Context, *uuid.UUID, int, *string, *string, *string, *string, *string) (domain.DownloadPage, error)
	GetDownload(context.Context, uuid.UUID) (domain.DownloadView, error)
	ListAcquisitions(context.Context, *uuid.UUID, int, *string, *int64, *string, *string, *string) (domain.AcquisitionPage, error)
	GetAcquisition(context.Context, uuid.UUID) (domain.AcquisitionView, error)
	ListRSSEntries(context.Context, uuid.UUID, *uuid.UUID, int, *string, *string, *string, *string, *string, *string) (domain.RSSEntryPage, error)
	ListOperations(context.Context, *uuid.UUID, int, *string, *uuid.UUID, *string) (domain.OperationPage, error)
	GetOperation(context.Context, uuid.UUID) (domain.OperationView, error)
	ListResourceEvents(context.Context, string, uuid.UUID, *uuid.UUID, int) (domain.EventRecordPage, error)
	DashboardSummary(context.Context) (domain.DashboardSummary, error)
}

type TMDbSearchService interface {
	SearchSeries(context.Context, string) ([]domain.TMDbSeriesSearchResult, error)
	SearchMovies(context.Context, string) ([]domain.TMDbMovieSearchResult, error)
	GetSeriesCatalog(context.Context, int64) (domain.TMDbSeriesCatalogView, error)
}

type ConnectivityService interface {
	Test(context.Context, domain.ConnectivityTestRequest) (domain.ConnectivityTestResult, error)
}

type AgentResolutionService interface {
	Get(context.Context, uuid.UUID) (domain.AgentResolution, error)
	List(context.Context, *uuid.UUID, int, *string, *string, *string, *uuid.UUID) (domain.AgentResolutionPage, error)
}

type BackgroundRuntimeService interface {
	Get(context.Context) (domain.BackgroundRuntime, error)
	Set(context.Context, domain.BackgroundRuntimeState) (domain.BackgroundRuntime, error)
}

type SystemMetricsService interface {
	Snapshot() domain.SystemMetricsSnapshot
	SetDiskPaths([]string)
}

type ArtifactContentService interface {
	OpenArtifact(context.Context, uuid.UUID, uuid.UUID) (service.ArtifactContent, error)
}

type AcquisitionCommandService interface {
	RequestDeletion(context.Context, uuid.UUID, string, uuid.UUID) (domain.Operation, error)
	RequestDownloadDeletion(context.Context, uuid.UUID, int32, string, uuid.UUID) (domain.Operation, error)
}

type DownloadCommandService interface {
	Retry(context.Context, uuid.UUID, int32, string, uuid.UUID) (domain.DownloadView, domain.Operation, error)
	Cancel(context.Context, uuid.UUID, int32, string, uuid.UUID) (domain.DownloadView, domain.Operation, error)
	Remove(context.Context, uuid.UUID, int32, string, uuid.UUID) (domain.DownloadView, domain.Operation, error)
	SaveFileResolution(context.Context, uuid.UUID, int32, []domain.DownloadFileResolutionItem, string, uuid.UUID) (domain.DownloadView, domain.Operation, error)
	SaveFileSelection(context.Context, uuid.UUID, int32, map[uuid.UUID]bool, string, uuid.UUID) (domain.DownloadView, domain.Operation, error)
}

type TaskCommandService interface {
	Retry(context.Context, uuid.UUID, int32, string, uuid.UUID) (domain.EpisodeTask, domain.Operation, error)
	Cancel(context.Context, uuid.UUID, int32, string, uuid.UUID) (domain.EpisodeTask, domain.Operation, error)
}

type ServerOption func(*Server)

func WithAuthentication(authentication AuthenticationService, cookieSecure bool) ServerOption {
	return func(server *Server) {
		server.auth = authentication
		server.cookieSecure = cookieSecure
	}
}

func WithSetup(setup SetupService) ServerOption {
	return func(server *Server) {
		server.setup = setup
	}
}

func WithRuntimeConfiguration(configuration RuntimeConfigurationService) ServerOption {
	return func(server *Server) {
		server.configuration = configuration
	}
}

func WithEvents(events EventSource) ServerOption {
	return func(server *Server) {
		server.events = events
	}
}

func WithSearch(search SearchService) ServerOption {
	return func(server *Server) {
		server.search = search
	}
}

func WithRSSSubscriptions(subscriptions RSSSubscriptionService) ServerOption {
	return func(server *Server) {
		server.rssSubscriptions = subscriptions
	}
}

func WithRSSFeedLookup(lookup RSSFeedLookupService) ServerOption {
	return func(server *Server) {
		server.rssFeedLookup = lookup
	}
}

func WithCatalog(catalog CatalogService) ServerOption {
	return func(server *Server) {
		server.catalog = catalog
	}
}

func WithEmbyCatalog(catalog EmbyCatalogService) ServerOption {
	return func(server *Server) {
		server.embyCatalog = catalog
	}
}

func WithTasks(tasks TaskService) ServerOption {
	return func(server *Server) {
		server.tasks = tasks
	}
}

func WithReadModels(read ReadModelService) ServerOption {
	return func(server *Server) {
		server.read = read
	}
}

func WithTMDbSearch(search TMDbSearchService) ServerOption {
	return func(server *Server) {
		server.tmdbSearch = search
	}
}

func WithConnectivity(connectivity ConnectivityService) ServerOption {
	return func(server *Server) {
		server.connectivity = connectivity
	}
}

func WithAgentResolutions(resolutions AgentResolutionService) ServerOption {
	return func(server *Server) {
		server.agentResolutions = resolutions
	}
}

func WithBackgroundRuntime(runtime BackgroundRuntimeService) ServerOption {
	return func(server *Server) {
		server.backgroundRuntime = runtime
	}
}

func WithSystemMetrics(metrics SystemMetricsService) ServerOption {
	return func(server *Server) {
		server.systemMetrics = metrics
	}
}

func WithArtifactContent(artifacts ArtifactContentService) ServerOption {
	return func(server *Server) {
		server.artifacts = artifacts
	}
}

func WithAcquisitionCommands(commands AcquisitionCommandService) ServerOption {
	return func(server *Server) {
		server.acquisitionCommands = commands
	}
}

func WithDownloadCommands(commands DownloadCommandService) ServerOption {
	return func(server *Server) {
		server.downloadCommands = commands
	}
}

func WithTaskCommands(commands TaskCommandService) ServerOption {
	return func(server *Server) {
		server.taskCommands = commands
	}
}
