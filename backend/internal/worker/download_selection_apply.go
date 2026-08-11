package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type DownloadSelectionApplyStore interface {
	LoadSelectionApplyCommand(context.Context, uuid.UUID) (domain.DownloadSelectionApplyCommand, error)
	CompleteSelectionApply(context.Context, uuid.UUID, uuid.UUID) error
}

type DownloadSelectionApplyHandler struct {
	configuration    DownloadConfiguration
	store            DownloadSelectionApplyStore
	newClient        TorrentClientFactory
	agentResolutions DownloadAgentResolutionCreator
}

func NewDownloadSelectionApplyHandler(
	configuration DownloadConfiguration,
	store DownloadSelectionApplyStore,
	newClient TorrentClientFactory,
	agentResolutions ...DownloadAgentResolutionCreator,
) *DownloadSelectionApplyHandler {
	handler := &DownloadSelectionApplyHandler{configuration: configuration, store: store, newClient: newClient}
	if len(agentResolutions) > 0 {
		handler.agentResolutions = agentResolutions[0]
	}
	return handler
}

func (handler *DownloadSelectionApplyHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "download" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_download_operation", "download.selection.apply requires a download resource", nil)
	}
	if handler.configuration == nil || handler.store == nil || handler.newClient == nil {
		return permanentFailure("download_handler_not_configured", "download selection apply dependencies are unavailable", nil)
	}
	command, err := handler.store.LoadSelectionApplyCommand(ctx, operation.ResourceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("download_not_found", "the download no longer exists", err)
		}
		return retryableFailure("download_storage_unavailable", "download selection is unavailable", err)
	}
	if command.DownloadID != operation.ResourceID {
		return permanentFailure("download_resource_mismatch", "the operation does not match its download", nil)
	}
	if command.Status != domain.DownloadFileResolutionPending {
		if command.Status == domain.DownloadDownloading || command.Status == domain.DownloadCompleted || command.Status == domain.DownloadSelectingFiles || command.Status == domain.DownloadMaterialized {
			return handler.ensureEpisodeMappingResolution(ctx, command.AcquisitionID)
		}
		return permanentFailure("download_state_conflict", fmt.Sprintf("download selection cannot be applied from state %q", command.Status), nil)
	}
	if strings.TrimSpace(command.TorrentHash) == "" || len(command.AllFileIndexes) == 0 || len(command.SelectedFileIndexes) == 0 {
		return permanentFailure("download_file_resolution_invalid", "download selection is incomplete", nil)
	}
	settings, client, err := loadConfiguredTorrentClient(ctx, handler.configuration, handler.newClient)
	if err != nil {
		return err
	}
	downloadRateLimit, err := rateLimitBytesPerSecond(settings.QBittorrent.DownloadRateLimitKibPerSecond)
	if err != nil {
		return permanentFailure("qbittorrent_configuration_invalid", "the qBittorrent download rate limit is invalid", err)
	}
	uploadRateLimit, err := rateLimitBytesPerSecond(settings.QBittorrent.UploadRateLimitKibPerSecond)
	if err != nil {
		return permanentFailure("qbittorrent_configuration_invalid", "the qBittorrent upload rate limit is invalid", err)
	}
	if err := client.SetFilePriority(ctx, command.TorrentHash, command.AllFileIndexes, 0); err != nil {
		return retryableFailure("qbittorrent_file_priority_failed", "qBittorrent file selection failed", err)
	}
	if err := client.SetFilePriority(ctx, command.TorrentHash, command.SelectedFileIndexes, 1); err != nil {
		return retryableFailure("qbittorrent_file_priority_failed", "qBittorrent file selection failed", err)
	}
	if err := client.SetTorrentRateLimits(ctx, command.TorrentHash, downloadRateLimit, uploadRateLimit); err != nil {
		return retryableFailure("qbittorrent_rate_limit_failed", "qBittorrent torrent rate limits could not be applied", err)
	}
	if err := client.EnsureCategory(ctx, qbittorrent.ManagedCategory); err != nil {
		return retryableFailure("qbittorrent_category_failed", "qBittorrent application category could not be prepared", err)
	}
	if err := client.SetTorrentCategory(ctx, command.TorrentHash, qbittorrent.ManagedCategory); err != nil {
		return retryableFailure("qbittorrent_category_failed", "qBittorrent torrent could not be moved to the application category", err)
	}
	correlationCategory := "emby-auto-" + command.DownloadID.String()
	if err := client.DeleteCategory(ctx, correlationCategory); err != nil {
		return retryableFailure("qbittorrent_category_failed", "qBittorrent temporary category could not be removed", err)
	}
	if err := client.ResumeTorrent(ctx, command.TorrentHash); err != nil {
		return retryableFailure("qbittorrent_resume_failed", "qBittorrent torrent could not be resumed", err)
	}
	if err := handler.store.CompleteSelectionApply(ctx, command.DownloadID, operation.ID); err != nil {
		return retryableFailure("download_storage_unavailable", "the applied download selection could not be persisted", err)
	}
	return handler.ensureEpisodeMappingResolution(ctx, command.AcquisitionID)
}

func (handler *DownloadSelectionApplyHandler) ensureEpisodeMappingResolution(ctx context.Context, acquisitionID uuid.UUID) error {
	if handler.agentResolutions == nil || acquisitionID == uuid.Nil {
		return nil
	}
	_, err := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilityEpisodeMapping, ResourceID: acquisitionID,
	})
	if err == nil || errors.Is(err, service.ErrStateConflict) {
		return nil
	}
	return retryableFailure("agent_resolution_schedule_failed", "Agent episode Mapping resolution could not be scheduled", err)
}
