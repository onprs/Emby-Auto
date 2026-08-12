package httpapi

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// ReadinessChecker verifies dependencies required for database-backed API work.
type ReadinessChecker interface {
	Ping(context.Context) error
}

// Server implements the strict interface generated from contracts/openapi.yaml.
type Server struct {
	readiness           ReadinessChecker
	setup               SetupService
	auth                AuthenticationService
	configuration       RuntimeConfigurationService
	events              EventSource
	search              SearchService
	catalog             CatalogService
	embyCatalog         EmbyCatalogService
	rssSubscriptions    RSSSubscriptionService
	rssFeedLookup       RSSFeedLookupService
	tasks               TaskService
	read                ReadModelService
	tmdbSearch          TMDbSearchService
	connectivity        ConnectivityService
	agentResolutions    AgentResolutionService
	backgroundRuntime   BackgroundRuntimeService
	systemMetrics       SystemMetricsService
	artifacts           ArtifactContentService
	acquisitionCommands AcquisitionCommandService
	downloadCommands    DownloadCommandService
	taskCommands        TaskCommandService
	cookieSecure        bool
	now                 func() time.Time
	eventPollInterval   time.Duration
}

var _ StrictServerInterface = (*Server)(nil)

func NewServer(readiness ReadinessChecker, options ...ServerOption) *Server {
	server := &Server{
		readiness:         readiness,
		now:               time.Now,
		eventPollInterval: time.Second,
	}
	for _, option := range options {
		option(server)
	}
	return server
}

func (server *Server) GetHealthLive(
	_ context.Context,
	_ GetHealthLiveRequestObject,
) (GetHealthLiveResponseObject, error) {
	return GetHealthLive200JSONResponse{
		CheckedAt: server.now().UTC(),
		Status:    Live,
	}, nil
}

func (server *Server) GetHealthReady(
	ctx context.Context,
	_ GetHealthReadyRequestObject,
) (GetHealthReadyResponseObject, error) {
	if server.readiness == nil || server.readiness.Ping(ctx) != nil {
		return GetHealthReady503JSONResponse{
			ServiceUnavailableJSONResponse: ServiceUnavailableJSONResponse(ApiError{
				Code:      "service_not_ready",
				Message:   "a required dependency is unavailable",
				Details:   map[string]interface{}{"dependency": "postgresql"},
				RequestId: middleware.GetReqID(ctx),
			}),
		}, nil
	}

	return GetHealthReady200JSONResponse{
		CheckedAt: server.now().UTC(),
		Status:    Ready,
	}, nil
}
