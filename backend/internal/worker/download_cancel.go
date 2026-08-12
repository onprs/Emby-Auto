package worker

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/riverqueue/river"
)

const cancellationReconcileInterval = time.Second

// DownloadCancelStore resolves the torrent hash for a cancelled download.
type DownloadCancelStore interface {
	LoadSyncCommand(context.Context, uuid.UUID) (domain.DownloadSyncCommand, error)
	DownloadCancellationReady(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	CompleteRemoval(context.Context, uuid.UUID, uuid.UUID) error
}

type DownloadCancelTorrentClient interface {
	Login(context.Context) error
	DeleteTorrent(context.Context, string, bool) error
	DeleteCategory(context.Context, string) error
}

type DownloadCancelClientFactory func(qbittorrent.ClientOptions) (DownloadCancelTorrentClient, error)

// DownloadCancelHandler reconciles cancellation and user-requested removal
// after all other download operations have stopped.
type DownloadCancelHandler struct {
	configuration DownloadConfiguration
	store         DownloadCancelStore
	newClient     DownloadCancelClientFactory
}

func NewDownloadCancelHandler(
	configuration DownloadConfiguration,
	store DownloadCancelStore,
	newClient DownloadCancelClientFactory,
) *DownloadCancelHandler {
	return &DownloadCancelHandler{configuration: configuration, store: store, newClient: newClient}
}

func (handler *DownloadCancelHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "download" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_download_operation", "download.cancel requires a download resource", nil)
	}
	if handler.configuration == nil || handler.store == nil || handler.newClient == nil {
		return permanentFailure("download_handler_not_configured", "download cancel handler dependencies are unavailable", nil)
	}
	ready, err := handler.store.DownloadCancellationReady(ctx, operation.ResourceID, operation.ID)
	if err != nil {
		return retryableFailure("download_storage_unavailable", "download cancellation could not be reconciled", err)
	}
	if !ready {
		return river.JobSnooze(cancellationReconcileInterval)
	}
	var payload struct {
		Command         string `json:"command"`
		DeleteFiles     bool   `json:"deleteFiles"`
		PreserveTorrent bool   `json:"preserveTorrent"`
	}
	if len(operation.Payload) > 0 {
		if err := json.Unmarshal(operation.Payload, &payload); err != nil {
			return permanentFailure("invalid_download_operation", "download cancellation payload is invalid", err)
		}
	}
	remove := payload.Command == "remove"
	if remove && payload.PreserveTorrent {
		if err := handler.store.CompleteRemoval(ctx, operation.ResourceID, operation.ID); err != nil {
			return retryableFailure("download_storage_unavailable", "download removal could not be completed", err)
		}
		return nil
	}
	command, err := handler.store.LoadSyncCommand(ctx, operation.ResourceID)
	if err != nil {
		if remove {
			return retryableFailure("download_storage_unavailable", "download removal state is unavailable", err)
		}
		return nil
	}
	if strings.TrimSpace(command.TorrentHash) == "" {
		if remove {
			return handler.store.CompleteRemoval(ctx, operation.ResourceID, operation.ID)
		}
		return nil
	}
	settings, err := handler.configuration.Load(ctx)
	if err != nil {
		return retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	password, err := handler.configuration.ResolveSecret(ctx, domain.SecretQBittorrentPassword)
	if err != nil {
		return retryableFailure("configuration_unavailable", "qBittorrent credentials are unavailable", err)
	}
	client, err := handler.newClient(qbittorrent.ClientOptions{
		BaseURL:        settings.Settings.QBittorrent.URL,
		Username:       settings.Settings.QBittorrent.Username,
		Password:       password,
		RequestTimeout: qBittorrentRequestTimeout,
	})
	if err != nil {
		return permanentFailure("qbittorrent_configuration_invalid", "qBittorrent configuration is invalid", err)
	}
	if err := client.Login(ctx); err != nil {
		return retryableFailure("qbittorrent_unavailable", "qBittorrent login failed", err)
	}
	if err := client.DeleteTorrent(ctx, command.TorrentHash, payload.DeleteFiles); err != nil {
		return retryableFailure("qbittorrent_unavailable", "failed to remove the torrent from qBittorrent", err)
	}
	if err := client.DeleteCategory(ctx, "emby-auto-"+operation.ResourceID.String()); err != nil {
		return retryableFailure("qbittorrent_category_failed", "qBittorrent temporary category could not be removed", err)
	}
	if remove {
		if err := handler.store.CompleteRemoval(ctx, operation.ResourceID, operation.ID); err != nil {
			return retryableFailure("download_storage_unavailable", "download removal could not be completed", err)
		}
	}
	return nil
}

var _ = qbittorrent.ClientOptions{}
