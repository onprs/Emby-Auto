package worker

import (
	"context"
	"errors"
	"strings"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

func loadConfiguredTorrentClient(
	ctx context.Context,
	configuration DownloadConfiguration,
	newClient TorrentClientFactory,
) (domain.RuntimeSettings, TorrentClient, error) {
	loaded, err := configuration.Load(ctx)
	if err != nil {
		return domain.RuntimeSettings{}, nil, retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	settings := loaded.Settings
	if strings.TrimSpace(settings.QBittorrent.URL) == "" {
		return domain.RuntimeSettings{}, nil, permanentFailure("qbittorrent_not_configured", "qBittorrent is not configured", nil)
	}
	password, err := configuration.ResolveSecret(ctx, domain.SecretQBittorrentPassword)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.RuntimeSettings{}, nil, permanentFailure("qbittorrent_not_configured", "qBittorrent credentials are not configured", err)
		}
		return domain.RuntimeSettings{}, nil, retryableFailure("configuration_unavailable", "qBittorrent credentials are unavailable", err)
	}
	client, err := newClient(qbittorrent.ClientOptions{
		BaseURL:        settings.QBittorrent.URL,
		Username:       settings.QBittorrent.Username,
		Password:       password,
		RequestTimeout: qBittorrentRequestTimeout,
		PollInterval:   qBittorrentPollInterval,
		ConfirmTimeout: qBittorrentConfirmTimeout,
	})
	if err != nil {
		return domain.RuntimeSettings{}, nil, permanentFailure("qbittorrent_configuration_invalid", "qBittorrent configuration is invalid", err)
	}
	if err := client.Login(ctx); err != nil {
		return domain.RuntimeSettings{}, nil, retryableFailure("qbittorrent_unavailable", "qBittorrent login failed", err)
	}
	return settings, client, nil
}
