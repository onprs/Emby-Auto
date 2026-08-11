package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/proxyhttp"
	"github.com/onprs/emby-auto/backend/internal/platform/tmdb"
	"github.com/onprs/emby-auto/backend/internal/service"
)

const tmdbRequestTimeout = 30 * time.Second

type TMDbCatalogClient interface {
	Series(context.Context, int64) (domain.TMDbSeriesCatalog, error)
}

type TMDbCatalogClientFactory func(tmdb.ClientOptions) (TMDbCatalogClient, error)

type TMDbCatalogStore interface {
	SaveTMDbCatalog(context.Context, domain.Operation, domain.TMDbSeriesCatalog) error
	ListAgentMappingAcquisitions(context.Context, uuid.UUID) ([]uuid.UUID, error)
}

type RSSPreacquisitionMappingReconciler interface {
	ReconcileAutomaticRSSPreacquisitionMappingsForSeries(context.Context, uuid.UUID) (int, error)
}

type TMDbAgentResolutionService interface {
	DownloadAgentResolutionCreator
	RSSPreacquisitionMappingReconciler
}

type TMDbSyncHandler struct {
	configuration    DownloadConfiguration
	store            TMDbCatalogStore
	newClient        TMDbCatalogClientFactory
	agentResolutions TMDbAgentResolutionService
}

type tmdbSyncPayload struct {
	TMDbSeriesID int64 `json:"tmdbSeriesId"`
}

func NewTMDbSyncHandler(
	configuration DownloadConfiguration,
	store TMDbCatalogStore,
	newClient TMDbCatalogClientFactory,
	agentResolutions ...TMDbAgentResolutionService,
) *TMDbSyncHandler {
	handler := &TMDbSyncHandler{configuration: configuration, store: store, newClient: newClient}
	if len(agentResolutions) > 0 {
		handler.agentResolutions = agentResolutions[0]
	}
	return handler
}

func (handler *TMDbSyncHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "media_series" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_tmdb_sync_operation", "tmdb.sync requires a media series resource", nil)
	}
	if handler.configuration == nil || handler.store == nil || handler.newClient == nil {
		return permanentFailure("tmdb_sync_not_configured", "TMDb synchronization dependencies are unavailable", nil)
	}
	var payload tmdbSyncPayload
	if json.Unmarshal(operation.Payload, &payload) != nil || payload.TMDbSeriesID <= 0 {
		return permanentFailure("invalid_tmdb_sync_operation", "tmdb.sync requires a positive TMDb series ID", nil)
	}
	configuration, err := handler.configuration.Load(ctx)
	if err != nil {
		return retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	apiToken, err := handler.configuration.ResolveSecret(ctx, domain.SecretTMDbAPIToken)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("tmdb_not_configured", "the TMDb API token is not configured", err)
		}
		return retryableFailure("configuration_unavailable", "the TMDb API token is unavailable", err)
	}
	httpClient, err := proxyhttp.NewClient(configuration.Settings.NetworkProxy)
	if err != nil {
		return permanentFailure("tmdb_configuration_invalid", "the TMDb proxy configuration is invalid", err)
	}
	client, err := handler.newClient(tmdb.ClientOptions{
		APIToken:       apiToken,
		RequestTimeout: tmdbRequestTimeout,
		HTTPClient:     httpClient,
	})
	if err != nil {
		return permanentFailure("tmdb_configuration_invalid", "the TMDb configuration is invalid", err)
	}
	catalog, err := client.Series(ctx, payload.TMDbSeriesID)
	if err != nil {
		var httpErr *tmdb.HTTPError
		if errors.As(err, &httpErr) {
			switch {
			case httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden:
				return permanentFailure("tmdb_authentication_failed", "TMDb rejected the configured API token", err)
			case httpErr.StatusCode == http.StatusNotFound:
				return permanentFailure("tmdb_series_not_found", "the TMDb series was not found", err)
			case httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= http.StatusInternalServerError:
				return retryableFailure("tmdb_request_failed", "TMDb catalog synchronization failed", err)
			}
		}
		return retryableFailure("tmdb_request_failed", "TMDb catalog synchronization failed", err)
	}
	if err := handler.store.SaveTMDbCatalog(ctx, operation, catalog); err != nil {
		if errors.Is(err, domain.ErrNotFound) || strings.Contains(err.Error(), "does not match") {
			return permanentFailure("tmdb_sync_conflict", "the TMDb catalog does not match the scheduled series", err)
		}
		return retryableFailure("tmdb_catalog_persist_failed", "the TMDb catalog could not be persisted", err)
	}
	if handler.agentResolutions != nil {
		if _, err := handler.agentResolutions.ReconcileAutomaticRSSPreacquisitionMappingsForSeries(ctx, operation.ResourceID); err != nil {
			return retryableFailure("agent_resolution_schedule_failed", "RSS pre-acquisition Mapping continuation could not be scheduled", err)
		}
	}
	if handler.agentResolutions != nil {
		acquisitionIDs, err := handler.store.ListAgentMappingAcquisitions(ctx, operation.ResourceID)
		if err != nil {
			return retryableFailure("agent_resolution_schedule_failed", "Agent Mapping candidates could not be loaded", err)
		}
		for _, acquisitionID := range acquisitionIDs {
			if _, err := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
				Capability: domain.AgentCapabilityEpisodeMapping, ResourceID: acquisitionID,
			}); err != nil && !errors.Is(err, service.ErrStateConflict) {
				return retryableFailure("agent_resolution_schedule_failed", "Agent Mapping resolution could not be scheduled", err)
			}
		}
	}
	return nil
}
