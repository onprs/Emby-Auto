package worker

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
)

const embyRequestTimeout = 30 * time.Second

type EmbyRefreshClient interface {
	RefreshLibrary(context.Context) error
}

type EmbyRefreshClientFactory func(emby.ClientOptions) (EmbyRefreshClient, error)

type EmbyRefreshHandler struct {
	configuration DownloadConfiguration
	newClient     EmbyRefreshClientFactory
}

func NewEmbyRefreshHandler(configuration DownloadConfiguration, newClient EmbyRefreshClientFactory) *EmbyRefreshHandler {
	return &EmbyRefreshHandler{configuration: configuration, newClient: newClient}
}

func (handler *EmbyRefreshHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if (operation.ResourceType != "episode_task" && operation.ResourceType != "emby_catalog") || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_emby_refresh_operation", "emby.refresh requires an episode task or Emby catalog resource", nil)
	}
	if handler.configuration == nil || handler.newClient == nil {
		return permanentFailure("emby_refresh_not_configured", "Emby refresh handler dependencies are unavailable", nil)
	}
	configuration, err := handler.configuration.Load(ctx)
	if err != nil {
		return retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	if strings.TrimSpace(configuration.Settings.Emby.URL) == "" {
		return permanentFailure("emby_not_configured", "Emby is not configured", nil)
	}
	apiKey, err := handler.configuration.ResolveSecret(ctx, domain.SecretEmbyAPIKey)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("emby_not_configured", "the Emby API key is not configured", err)
		}
		return retryableFailure("configuration_unavailable", "the Emby API key is unavailable", err)
	}
	client, err := handler.newClient(emby.ClientOptions{
		BaseURL:        configuration.Settings.Emby.URL,
		APIKey:         apiKey,
		RequestTimeout: embyRequestTimeout,
	})
	if err != nil {
		return permanentFailure("emby_configuration_invalid", "the Emby configuration is invalid", err)
	}
	if err := client.RefreshLibrary(ctx); err != nil {
		return retryableFailure("emby_refresh_failed", "the Emby library refresh request failed", err)
	}
	return nil
}
