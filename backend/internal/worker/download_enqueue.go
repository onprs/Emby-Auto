package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/service"
)

const (
	qBittorrentRequestTimeout  = 15 * time.Second
	qBittorrentPollInterval    = 500 * time.Millisecond
	qBittorrentConfirmTimeout  = 15 * time.Second
	qBittorrentMetadataTimeout = 90 * time.Second
)

var errTorrentMetadataUnavailable = errors.New("qBittorrent torrent metadata did not become available")

type DownloadConfiguration interface {
	Load(context.Context) (domain.Configuration, error)
	ResolveSecret(context.Context, string) (string, error)
}

type DownloadEnqueueStore interface {
	LoadEnqueueCommand(context.Context, uuid.UUID) (domain.DownloadEnqueueCommand, error)
	CompleteEnqueue(context.Context, domain.DownloadEnqueueCompletion) error
	CompleteLegacyEnqueue(context.Context, domain.DownloadEnqueueCompletion) error
}

type TorrentClient interface {
	Login(context.Context) error
	AddAndConfirm(context.Context, qbittorrent.AddRequest) (qbittorrent.HashResolution, error)
	TorrentFiles(context.Context, string) ([]qbittorrent.TorrentFile, error)
	ListTorrents(context.Context, string) ([]qbittorrent.Torrent, error)
	SetFilePriority(context.Context, string, []int, int) error
	SetTorrentRateLimits(context.Context, string, int64, int64) error
	EnsureCategory(context.Context, string) error
	SetTorrentCategory(context.Context, string, string) error
	DeleteCategory(context.Context, string) error
	ResumeTorrent(context.Context, string) error
	DeleteTorrent(context.Context, string, bool) error
}

type TorrentClientFactory func(qbittorrent.ClientOptions) (TorrentClient, error)

type DownloadAgentResolutionCreator interface {
	CreateAutomatic(context.Context, service.AutomaticAgentResolutionRequest) (service.AgentResolutionCommandResult, error)
}

type DownloadEnqueueHandler struct {
	configuration             DownloadConfiguration
	store                     DownloadEnqueueStore
	agentResolutions          DownloadAgentResolutionCreator
	newClient                 TorrentClientFactory
	torrentFetcher            torrentSourceFetcher
	manifestResolutionEnabled bool
	metadataPollInterval      time.Duration
	metadataTimeout           time.Duration
}

type downloadEnqueuePayload struct {
	DefaultSeason  int  `json:"defaultSeason"`
	DefaultEpisode int  `json:"defaultEpisode"`
	SingleEpisode  bool `json:"singleEpisode"`
}

func NewDownloadEnqueueHandler(
	configuration DownloadConfiguration,
	store DownloadEnqueueStore,
	newClient TorrentClientFactory,
	agentResolutions ...DownloadAgentResolutionCreator,
) *DownloadEnqueueHandler {
	handler := &DownloadEnqueueHandler{
		configuration:             configuration,
		store:                     store,
		newClient:                 newClient,
		manifestResolutionEnabled: true,
		metadataPollInterval:      qBittorrentPollInterval,
		metadataTimeout:           qBittorrentMetadataTimeout,
	}
	if len(agentResolutions) > 0 {
		handler.agentResolutions = agentResolutions[0]
	}
	return handler
}

func (handler *DownloadEnqueueHandler) WithManifestResolutionEnabled(enabled bool) *DownloadEnqueueHandler {
	handler.manifestResolutionEnabled = enabled
	return handler
}

func (handler *DownloadEnqueueHandler) WithTorrentSourceFetcher(fetcher torrentSourceFetcher) *DownloadEnqueueHandler {
	handler.torrentFetcher = fetcher
	return handler
}

func (handler *DownloadEnqueueHandler) Handle(ctx context.Context, operation domain.Operation) (handlerErr error) {
	if operation.ResourceType != "download" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_download_operation", "download.enqueue requires a download resource", nil)
	}
	if handler.configuration == nil || handler.store == nil || handler.newClient == nil {
		return permanentFailure("download_handler_not_configured", "download enqueue handler dependencies are unavailable", nil)
	}

	command, err := handler.store.LoadEnqueueCommand(ctx, operation.ResourceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("download_not_found", "the download no longer exists", err)
		}
		return retryableFailure("download_storage_unavailable", "download storage is unavailable", err)
	}
	if command.DownloadID != operation.ResourceID {
		return permanentFailure("download_resource_mismatch", "the operation does not match its download", nil)
	}
	if command.Status != domain.DownloadEnqueuePending {
		if command.TorrentHash != "" && (command.Status == domain.DownloadFileResolutionPending || command.Status == domain.DownloadDownloading || command.Status == domain.DownloadCompleted || command.Status == domain.DownloadSelectingFiles || command.Status == domain.DownloadMaterialized || command.Status == domain.DownloadFailed) {
			if command.Status == domain.DownloadFileResolutionPending && command.FileResolutionSource == "" {
				return handler.ensureDownloadAgentResolution(ctx, command.DownloadID)
			}
			if command.Status == domain.DownloadDownloading || command.Status == domain.DownloadCompleted || command.Status == domain.DownloadSelectingFiles || command.Status == domain.DownloadMaterialized {
				return handler.ensureAutomaticEpisodeMapping(ctx, command.AcquisitionID)
			}
			return nil
		}
		return permanentFailure("download_state_conflict", fmt.Sprintf("download cannot be enqueued from state %q", command.Status), nil)
	}
	if strings.TrimSpace(command.SourceURI) == "" {
		return permanentFailure("download_source_not_downloadable", "the acquisition has no downloadable URI", nil)
	}

	payload := downloadEnqueuePayload{}
	if len(operation.Payload) > 0 {
		if err := json.Unmarshal(operation.Payload, &payload); err != nil {
			return permanentFailure("invalid_download_operation", "download enqueue payload is invalid", err)
		}
	}
	selectionOptions := domain.FileSelectionOptions{
		DefaultSeason:  payload.DefaultSeason,
		DefaultEpisode: payload.DefaultEpisode,
		SingleEpisode:  payload.SingleEpisode,
	}
	if selectionOptions.DefaultSeason <= 0 {
		return permanentFailure("invalid_download_operation", "download enqueue payload requires a positive default season", nil)
	}

	settings, client, err := loadConfiguredTorrentClient(ctx, handler.configuration, handler.newClient)
	if err != nil {
		return err
	}
	if strings.TrimSpace(settings.Paths.DownloadRoot) == "" {
		return permanentFailure("download_root_not_configured", "the download root is not configured", nil)
	}
	savePath := joinConfiguredPath(settings.Paths.DownloadRoot, command.DownloadID.String())
	correlationCategory := "emby-auto-" + command.DownloadID.String()
	if err := client.EnsureCategory(ctx, qbittorrent.ManagedCategory); err != nil {
		return retryableFailure("qbittorrent_category_failed", "qBittorrent application category could not be prepared", err)
	}
	if err := client.EnsureCategory(ctx, correlationCategory); err != nil {
		return retryableFailure("qbittorrent_category_failed", "qBittorrent temporary category could not be prepared", err)
	}
	categoryNeedsCleanup := true
	defer func() {
		if !categoryNeedsCleanup {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), qBittorrentRequestTimeout)
		defer cancel()
		_ = client.DeleteCategory(cleanupCtx, correlationCategory)
	}()
	sourceURI := strings.TrimSpace(command.SourceURI)
	var resolution qbittorrent.HashResolution
	var torrentOwnedByOperation bool
	if isMagnetSource(sourceURI) {
		addRequest := qbittorrent.AddRequest{
			Source:   sourceURI,
			SavePath: savePath,
			Category: correlationCategory,
		}
		res, err := client.AddAndConfirm(ctx, addRequest)
		if err != nil {
			var httpErr *qbittorrent.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnsupportedMediaType {
				return permanentFailure("qbittorrent_invalid_torrent", "种子文件无效，qBittorrent 无法识别", err)
			}
			return retryableFailure("qbittorrent_enqueue_failed", "qBittorrent did not confirm the added torrent", err)
		}
		resolution = res
		torrentOwnedByOperation = res.Reason != qbittorrent.HashResolutionExisting
	} else if isHTTPSource(sourceURI) {
		existingTorrents, listErr := client.ListTorrents(ctx, "")
		if listErr != nil {
			return retryableFailure("qbittorrent_unavailable", "qBittorrent is unavailable", listErr)
		}
		if res, ok, corrErr := qbittorrent.ResolveTorrentBySavePath(existingTorrents, savePath); corrErr != nil {
			return retryableFailure("qbittorrent_correlation_ambiguous", "qBittorrent savePath correlation is ambiguous", corrErr)
		} else if ok {
			resolution = res
			torrentOwnedByOperation = false
		} else {
			fetcher := handler.torrentFetcher
			if fetcher == nil {
				fetcher = defaultTorrentSourceFetcher
			}
			torrentBytes, fetchErr := fetcher(ctx, sourceURI, settings.NetworkProxy)
			if fetchErr != nil {
				return fetchErr
			}
			addRequest := qbittorrent.AddRequest{
				Torrent:         torrentBytes,
				TorrentFilename: "source.torrent",
				SavePath:        savePath,
				Category:        correlationCategory,
			}
			res, err := client.AddAndConfirm(ctx, addRequest)
			if err != nil {
				var httpErr *qbittorrent.HTTPError
				if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnsupportedMediaType {
					return permanentFailure("qbittorrent_invalid_torrent", "种子文件无效，qBittorrent 无法识别", err)
				}
				return retryableFailure("qbittorrent_enqueue_failed", "qBittorrent did not confirm the added torrent", err)
			}
			resolution = res
			torrentOwnedByOperation = res.Reason != qbittorrent.HashResolutionExisting
		}
	} else {
		return permanentFailure("torrent_source_invalid", "下载链接无效", nil)
	}
	defer func() {
		if handlerErr == nil || !torrentOwnedByOperation {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), qBittorrentRequestTimeout)
		defer cancel()
		if cleanupErr := client.DeleteTorrent(cleanupCtx, resolution.Hash, false); cleanupErr != nil {
			handlerErr = retryableFailure(
				"qbittorrent_compensation_failed",
				"qBittorrent could not remove a newly added torrent after enqueue failed",
				errors.Join(handlerErr, cleanupErr),
			)
		}
	}()

	files, err := handler.waitForTorrentFiles(ctx, client, resolution.Hash)
	if err != nil {
		return retryableFailure("qbittorrent_files_unavailable", "qBittorrent file metadata is unavailable", err)
	}

	downloadFiles := make([]domain.DownloadFile, 0, len(files))
	for _, file := range files {
		downloadFiles = append(downloadFiles, domain.DownloadFile{
			Index:        file.Index,
			RelativePath: file.Name,
			SizeBytes:    file.Size,
		})
	}
	if !handler.manifestResolutionEnabled {
		if err := handler.completeLegacyEnqueue(ctx, operation, command, settings, client, resolution.Hash, savePath, correlationCategory, downloadFiles, selectionOptions); err != nil {
			return err
		}
		torrentOwnedByOperation = false
		categoryNeedsCleanup = false
		return handler.ensureAutomaticEpisodeMapping(ctx, command.AcquisitionID)
	}
	classified, err := domain.ClassifyDownloadFiles(downloadFiles, selectionOptions)
	if err != nil {
		return permanentFailure("download_file_manifest_invalid", "qBittorrent files cannot be persisted safely", err)
	}
	selection, selectionErr := domain.SelectDownloadFiles(downloadFiles, selectionOptions)
	outcome := domain.DownloadManifestResolved
	reasonCode := ""
	persistedFiles := selection.Files
	if selectionErr != nil {
		persistedFiles = classified
		outcome = domain.DownloadManifestUnresolved
		reasonCode = "download_file_resolution_required"
		hasVideo := false
		for _, file := range classified {
			if file.Kind == domain.MediaVideo {
				hasVideo = true
				break
			}
		}
		if !errors.Is(selectionErr, domain.ErrNoMainVideo) || !hasVideo {
			outcome = domain.DownloadManifestHardRejected
			reasonCode = "download_no_main_video"
		}
	}

	if err := handler.store.CompleteEnqueue(ctx, domain.DownloadEnqueueCompletion{
		OperationID: operation.ID, DownloadID: command.DownloadID, TorrentHash: resolution.Hash,
		SavePath: savePath, Files: persistedFiles, Outcome: outcome, ReasonCode: reasonCode,
	}); err != nil {
		if errors.Is(err, domain.ErrDuplicateTorrent) {
			return permanentFailure("duplicate_torrent", "the torrent has already been downloaded", err)
		}
		return retryableFailure("download_storage_unavailable", "the download manifest could not be persisted", err)
	}
	torrentOwnedByOperation = false
	categoryNeedsCleanup = false
	if outcome == domain.DownloadManifestUnresolved {
		return handler.ensureDownloadAgentResolution(ctx, command.DownloadID)
	}
	return nil
}

func (handler *DownloadEnqueueHandler) completeLegacyEnqueue(
	ctx context.Context,
	operation domain.Operation,
	command domain.DownloadEnqueueCommand,
	settings domain.RuntimeSettings,
	client TorrentClient,
	torrentHash string,
	savePath string,
	correlationCategory string,
	files []domain.DownloadFile,
	selectionOptions domain.FileSelectionOptions,
) error {
	selection, err := domain.SelectDownloadFiles(files, selectionOptions)
	if err != nil {
		code := "download_file_selection_invalid"
		message := "qBittorrent files cannot be selected safely"
		if errors.Is(err, domain.ErrNoMainVideo) {
			code = "download_no_main_video"
			message = "the torrent contains no selectable main video"
		}
		return permanentFailure(code, message, err)
	}
	allIndexes := make([]int, 0, len(selection.Files))
	selectedIndexes := make([]int, 0, len(selection.Files))
	for _, file := range selection.Files {
		allIndexes = append(allIndexes, file.Index)
		if file.Selected {
			selectedIndexes = append(selectedIndexes, file.Index)
		}
	}
	downloadRateLimit, err := rateLimitBytesPerSecond(settings.QBittorrent.DownloadRateLimitKibPerSecond)
	if err != nil {
		return permanentFailure("qbittorrent_configuration_invalid", "the qBittorrent download rate limit is invalid", err)
	}
	uploadRateLimit, err := rateLimitBytesPerSecond(settings.QBittorrent.UploadRateLimitKibPerSecond)
	if err != nil {
		return permanentFailure("qbittorrent_configuration_invalid", "the qBittorrent upload rate limit is invalid", err)
	}
	if err := client.SetFilePriority(ctx, torrentHash, allIndexes, 0); err != nil {
		return retryableFailure("qbittorrent_file_priority_failed", "qBittorrent file selection failed", err)
	}
	if err := client.SetFilePriority(ctx, torrentHash, selectedIndexes, 1); err != nil {
		return retryableFailure("qbittorrent_file_priority_failed", "qBittorrent file selection failed", err)
	}
	if err := client.SetTorrentRateLimits(ctx, torrentHash, downloadRateLimit, uploadRateLimit); err != nil {
		return retryableFailure("qbittorrent_rate_limit_failed", "qBittorrent torrent rate limits could not be applied", err)
	}
	if err := client.SetTorrentCategory(ctx, torrentHash, qbittorrent.ManagedCategory); err != nil {
		return retryableFailure("qbittorrent_category_failed", "qBittorrent torrent could not be moved to the application category", err)
	}
	if err := client.DeleteCategory(ctx, correlationCategory); err != nil {
		return retryableFailure("qbittorrent_category_failed", "qBittorrent temporary category could not be removed", err)
	}
	if err := client.ResumeTorrent(ctx, torrentHash); err != nil {
		return retryableFailure("qbittorrent_resume_failed", "qBittorrent torrent could not be resumed", err)
	}
	if err := handler.store.CompleteLegacyEnqueue(ctx, domain.DownloadEnqueueCompletion{
		OperationID: operation.ID, DownloadID: command.DownloadID, TorrentHash: torrentHash,
		SavePath: savePath, Files: selection.Files,
	}); err != nil {
		if errors.Is(err, domain.ErrDuplicateTorrent) {
			return permanentFailure("duplicate_torrent", "the torrent has already been downloaded", err)
		}
		return retryableFailure("download_storage_unavailable", "the enqueued download could not be persisted", err)
	}
	return nil
}

func (handler *DownloadEnqueueHandler) ensureAutomaticEpisodeMapping(ctx context.Context, acquisitionID uuid.UUID) error {
	if handler.agentResolutions == nil || acquisitionID == uuid.Nil {
		return nil
	}
	_, err := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilityEpisodeMapping,
		ResourceID: acquisitionID,
	})
	if err == nil || errors.Is(err, service.ErrStateConflict) {
		return nil
	}
	return retryableFailure("episode_mapping_schedule_failed", "automatic episode Mapping could not be scheduled", err)
}

func (handler *DownloadEnqueueHandler) ensureDownloadAgentResolution(ctx context.Context, downloadID uuid.UUID) error {
	if handler.agentResolutions == nil {
		return nil
	}
	configuration, err := handler.configuration.Load(ctx)
	if err != nil {
		return retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	settings := configuration.Settings.Agent.WithDefaults()
	if !settings.Enabled || settings.DownloadFileSelectionMode == domain.AgentResolutionOff {
		return nil
	}
	if _, err := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilityDownloadFileResolution,
		ResourceID: downloadID,
	}); err != nil {
		if errors.Is(err, service.ErrStateConflict) {
			return nil
		}
		return retryableFailure("agent_resolution_schedule_failed", "Agent file resolution could not be scheduled", err)
	}
	return nil
}

func (handler *DownloadEnqueueHandler) waitForTorrentFiles(
	ctx context.Context,
	client TorrentClient,
	hash string,
) ([]qbittorrent.TorrentFile, error) {
	pollInterval := handler.metadataPollInterval
	if pollInterval <= 0 {
		pollInterval = qBittorrentPollInterval
	}
	metadataTimeout := handler.metadataTimeout
	if metadataTimeout <= 0 {
		metadataTimeout = qBittorrentMetadataTimeout
	}
	deadline := time.NewTimer(metadataTimeout)
	defer deadline.Stop()

	for {
		files, err := client.TorrentFiles(ctx, hash)
		if err != nil {
			return nil, err
		}
		if len(files) > 0 {
			return files, nil
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			stopDownloadTimer(timer)
			return nil, ctx.Err()
		case <-deadline.C:
			stopDownloadTimer(timer)
			return nil, errTorrentMetadataUnavailable
		case <-timer.C:
		}
	}
}

func stopDownloadTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func rateLimitBytesPerSecond(kibPerSecond int64) (int64, error) {
	if kibPerSecond < 0 || kibPerSecond > domain.MaxQBittorrentRateLimitKibPerSecond {
		return 0, fmt.Errorf("rate limit must be between 0 and %d KiB/s", domain.MaxQBittorrentRateLimitKibPerSecond)
	}
	return kibPerSecond * 1024, nil
}

func joinConfiguredPath(root, element string) string {
	root = strings.TrimSpace(root)
	separator := "/"
	if strings.Contains(root, `\`) && !strings.Contains(root, "/") {
		separator = `\`
	}
	return strings.TrimRight(root, `/\`) + separator + element
}

func permanentFailure(code, message string, cause error) *Failure {
	return &Failure{Code: code, Message: message, Retryable: false, Cause: cause}
}

func retryableFailure(code, message string, cause error) *Failure {
	return &Failure{Code: code, Message: message, Retryable: true, Cause: cause}
}
