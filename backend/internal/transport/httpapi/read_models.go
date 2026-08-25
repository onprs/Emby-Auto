package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

// --- Downloads ---

func (server *Server) ListDownloads(ctx context.Context, request ListDownloadsRequestObject) (ListDownloadsResponseObject, error) {
	if server.read == nil {
		return ListDownloads503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "downloads")}, nil
	}
	limit, cursor := pageParams(request.Params.Limit, request.Params.Cursor)
	var status *string
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		status = &value
	}
	var phase *string
	if request.Params.Phase != nil {
		value := string(*request.Params.Phase)
		phase = &value
	}
	var sortBy, sortOrder *string
	if request.Params.SortBy != nil {
		value := string(*request.Params.SortBy)
		sortBy = &value
	}
	if request.Params.SortOrder != nil {
		value := string(*request.Params.SortOrder)
		sortOrder = &value
	}
	page, err := server.read.ListDownloads(ctx, cursor, limit, status, phase, request.Params.Query, sortBy, sortOrder)
	var serviceErr *service.Error
	if errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput) {
		return ListDownloads400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	}
	if err != nil {
		return ListDownloads503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := DownloadPage{Items: make([]Download, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, downloadResponse(item))
	}
	response.NextCursor = page.NextCursor
	return ListDownloads200JSONResponse(response), nil
}

func (server *Server) GetDownload(ctx context.Context, request GetDownloadRequestObject) (GetDownloadResponseObject, error) {
	if server.read == nil {
		return GetDownload503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "downloads")}, nil
	}
	view, err := server.read.GetDownload(ctx, uuid.UUID(request.DownloadId))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetDownload404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the download was not found")}, nil
	case err != nil:
		return GetDownload503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return GetDownload200JSONResponse(downloadResponse(view)), nil
	}
}

// --- Searches ---

func (server *Server) ListSearches(ctx context.Context, request ListSearchesRequestObject) (ListSearchesResponseObject, error) {
	if server.read == nil {
		return ListSearches503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "searches")}, nil
	}
	limit, cursor := pageParams(request.Params.Limit, request.Params.Cursor)
	var status *string
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		status = &value
	}
	page, err := server.read.ListSearches(ctx, cursor, limit, status, request.Params.Query)
	if err != nil {
		return ListSearches503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := SearchRunPage{Items: make([]SearchRunSummary, 0, len(page.Items))}
	for _, item := range page.Items {
		entry := SearchRunSummary{
			Id: item.ID, Query: item.Query, Status: SearchRunSummaryStatus(item.Status),
			CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		}
		optionalString(&entry.ErrorCode, item.ErrorCode)
		optionalString(&entry.ErrorMessage, item.ErrorMessage)
		response.Items = append(response.Items, entry)
	}
	response.NextCursor = page.NextCursor
	return ListSearches200JSONResponse(response), nil
}

// --- Acquisitions ---

func (server *Server) ListAcquisitions(ctx context.Context, request ListAcquisitionsRequestObject) (ListAcquisitionsResponseObject, error) {
	if server.read == nil {
		return ListAcquisitions503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "acquisitions")}, nil
	}
	limit, cursor := pageParams(request.Params.Limit, request.Params.Cursor)
	var sourceKind *string
	if request.Params.SourceKind != nil {
		value := string(*request.Params.SourceKind)
		sourceKind = &value
	}
	var phase *string
	if request.Params.Phase != nil {
		value := string(*request.Params.Phase)
		phase = &value
	}
	var sortBy, sortOrder *string
	if request.Params.SortBy != nil {
		value := string(*request.Params.SortBy)
		sortBy = &value
	}
	if request.Params.SortOrder != nil {
		value := string(*request.Params.SortOrder)
		sortOrder = &value
	}
	page, err := server.read.ListAcquisitions(ctx, cursor, limit, sourceKind, request.Params.TmdbSeriesId, phase, sortBy, sortOrder)
	var serviceErr *service.Error
	if errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput) {
		return ListAcquisitions400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	}
	if err != nil {
		return ListAcquisitions503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := AcquisitionPage{Items: make([]Acquisition, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, acquisitionResponse(item))
	}
	response.NextCursor = page.NextCursor
	return ListAcquisitions200JSONResponse(response), nil
}

func (server *Server) GetAcquisition(ctx context.Context, request GetAcquisitionRequestObject) (GetAcquisitionResponseObject, error) {
	if server.read == nil {
		return GetAcquisition503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "acquisitions")}, nil
	}
	view, err := server.read.GetAcquisition(ctx, uuid.UUID(request.AcquisitionId))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetAcquisition404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the acquisition was not found")}, nil
	case err != nil:
		return GetAcquisition503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return GetAcquisition200JSONResponse(acquisitionResponse(view)), nil
	}
}

// --- RSS entries ---

func (server *Server) ListRSSEntries(ctx context.Context, request ListRSSEntriesRequestObject) (ListRSSEntriesResponseObject, error) {
	if server.read == nil {
		return ListRSSEntries503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	limit, cursor := pageParams(request.Params.Limit, request.Params.Cursor)
	var status *string
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		status = &value
	}
	var group *string
	if request.Params.Group != nil {
		value := string(*request.Params.Group)
		group = &value
	}
	var query, rejectReason *string
	if request.Params.Query != nil {
		value := string(*request.Params.Query)
		if value == "" || utf8.RuneCountInString(value) > 256 {
			return ListRSSEntries400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "query must contain between 1 and 256 characters")}, nil
		}
		query = &value
	}
	if request.Params.RejectReason != nil {
		value := string(*request.Params.RejectReason)
		if value == "" || utf8.RuneCountInString(value) > 128 {
			return ListRSSEntries400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "rejectReason must contain between 1 and 128 characters")}, nil
		}
		rejectReason = &value
	}
	var sortBy, sortOrder *string
	if request.Params.SortBy != nil {
		value := string(*request.Params.SortBy)
		sortBy = &value
	}
	if request.Params.SortOrder != nil {
		value := string(*request.Params.SortOrder)
		sortOrder = &value
	}
	page, err := server.read.ListRSSEntries(ctx, uuid.UUID(request.SubscriptionId), cursor, limit, status, group, query, rejectReason, sortBy, sortOrder)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return ListRSSEntries400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return ListRSSEntries404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the RSS subscription was not found")}, nil
	case err != nil:
		return ListRSSEntries503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		response := RSSEntryPage{Items: make([]RSSEntry, 0, len(page.Items))}
		for _, item := range page.Items {
			response.Items = append(response.Items, rssEntryResponse(item))
		}
		response.NextCursor = page.NextCursor
		return ListRSSEntries200JSONResponse(response), nil
	}
}

// --- Operations ---

func (server *Server) ListOperations(ctx context.Context, request ListOperationsRequestObject) (ListOperationsResponseObject, error) {
	if server.read == nil {
		return ListOperations503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "operations")}, nil
	}
	limit, cursor := pageParams(request.Params.Limit, request.Params.Cursor)
	var resourceType *string
	if request.Params.ResourceType != nil {
		resourceType = request.Params.ResourceType
	}
	var resourceID *uuid.UUID
	if request.Params.ResourceId != nil {
		value := uuid.UUID(*request.Params.ResourceId)
		resourceID = &value
	}
	var status *string
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		status = &value
	}
	page, err := server.read.ListOperations(ctx, cursor, limit, resourceType, resourceID, status)
	if err != nil {
		return ListOperations503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := OperationPage{Items: make([]Operation, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, operationResponse(item))
	}
	response.NextCursor = page.NextCursor
	return ListOperations200JSONResponse(response), nil
}

func (server *Server) GetOperation(ctx context.Context, request GetOperationRequestObject) (GetOperationResponseObject, error) {
	if server.read == nil {
		return GetOperation503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "operations")}, nil
	}
	view, err := server.read.GetOperation(ctx, uuid.UUID(request.OperationId))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetOperation404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the operation was not found")}, nil
	case err != nil:
		return GetOperation503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return GetOperation200JSONResponse(operationResponse(view)), nil
	}
}

// --- Event history ---

func (server *Server) ListEvents(ctx context.Context, request ListEventsRequestObject) (ListEventsResponseObject, error) {
	if server.read == nil {
		return ListEvents503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "events")}, nil
	}
	limit, cursor := pageParams(request.Params.Limit, request.Params.Cursor)
	page, err := server.read.ListResourceEvents(ctx, request.Params.ResourceType, uuid.UUID(request.Params.ResourceId), cursor, limit)
	if err != nil {
		return ListEvents503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := EventPage{Items: make([]EventRecord, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, eventRecordResponse(item))
	}
	response.NextCursor = page.NextCursor
	return ListEvents200JSONResponse(response), nil
}

// --- Dashboard ---

func (server *Server) GetDashboardSummary(ctx context.Context, request GetDashboardSummaryRequestObject) (GetDashboardSummaryResponseObject, error) {
	if server.read == nil {
		return GetDashboardSummary503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "dashboard")}, nil
	}
	summary, err := server.read.DashboardSummary(ctx)
	if err != nil {
		return GetDashboardSummary503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := dashboardResponse(summary)
	if server.configuration != nil {
		if configuration, configErr := server.configuration.Load(ctx); configErr == nil {
			applyDependencyStatus(&response, configuration)
		}
	}
	return GetDashboardSummary200JSONResponse(response), nil
}

func applyDependencyStatus(response *DashboardSummary, configuration domain.Configuration) {
	response.Dependencies.QBittorrent.Configured = configuration.Settings.QBittorrent.URL != "" && configuration.Secrets[domain.SecretQBittorrentPassword].Configured
	response.Dependencies.Emby.Configured = configuration.Settings.Emby.URL != "" && configuration.Secrets[domain.SecretEmbyAPIKey].Configured
	response.Dependencies.Tmdb.Configured = configuration.Secrets[domain.SecretTMDbAPIToken].Configured
	response.Dependencies.MediaTools.Configured = configuration.Settings.Paths.FFmpegPath != "" && configuration.Settings.Paths.FFprobePath != ""
	response.Dependencies.NetworkProxy.Configured = configuration.Settings.NetworkProxy.Enabled && configuration.Settings.NetworkProxy.URL != ""
	response.Dependencies.Agent.Configured = configuration.Settings.Agent.Enabled && configuration.Settings.Agent.BaseURL != "" && configuration.Settings.Agent.Model != "" && configuration.Secrets[domain.SecretAgentAPIKey].Configured
}

// --- helpers ---

func pageParams(limit *int32, cursor *openapi_types.UUID) (int, *uuid.UUID) {
	value := 50
	if limit != nil {
		value = int(*limit)
	}
	var cursorValue *uuid.UUID
	if cursor != nil {
		id := uuid.UUID(*cursor)
		cursorValue = &id
	}
	return value, cursorValue
}

func notFoundError(ctx context.Context, message string) NotFoundJSONResponse {
	return NotFoundJSONResponse(ApiError{Code: "not_found", Message: message, Details: map[string]any{}, RequestId: middleware.GetReqID(ctx)})
}

func downloadResponse(view domain.DownloadView) Download {
	response := Download{
		Id: view.ID, AcquisitionId: view.AcquisitionID, Attempt: int32(view.Attempt), ClientName: view.ClientName,
		Status: DownloadStatus(view.Status), Progress: view.Progress, Version: int32(view.Version),
		CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt, Files: make([]DownloadFile, 0, len(view.Files)),
		Actions: DownloadActions{
			CanRetry: view.Actions.CanRetry, CanCancel: view.Actions.CanCancel, CanDelete: view.Actions.CanDelete,
			CanEditFileSelection: view.Actions.CanEditFileSelection, CanResolveFiles: view.Actions.CanResolveFiles,
			CanRequestAgent: view.Actions.CanRequestAgent,
		},
	}
	optionalString(&response.ClientState, view.ClientState)
	response.LastSyncedAt = view.LastSyncedAt
	optionalString(&response.TorrentHash, view.TorrentHash)
	optionalString(&response.SavePath, view.SavePath)
	if view.FailureStage != "" {
		value := DownloadFailureStage(view.FailureStage)
		response.FailureStage = &value
	}
	if view.FileResolutionSource != "" {
		value := DownloadFileResolutionSource(view.FileResolutionSource)
		response.FileResolutionSource = &value
	}
	if view.AgentResolutionID != nil {
		value := *view.AgentResolutionID
		response.AgentResolutionId = &value
	}
	optionalString(&response.ErrorCode, view.ErrorCode)
	optionalString(&response.ErrorMessage, view.ErrorMessage)
	response.StartedAt = view.StartedAt
	response.CompletedAt = view.CompletedAt
	for _, file := range view.Files {
		item := DownloadFile{
			Id: file.ID, FileIndex: int32(file.FileIndex), RelativePath: file.RelativePath, SizeBytes: file.SizeBytes,
			MediaKind: DownloadFileMediaKind(file.MediaKind), Selected: file.Selected,
		}
		if file.SourceSeason != nil {
			value := int32(*file.SourceSeason)
			item.SourceSeason = &value
		}
		if file.SourceEpisode != nil {
			value := int32(*file.SourceEpisode)
			item.SourceEpisode = &value
		}
		optionalString(&item.Language, file.Language)
		if file.ExclusionReason != "" {
			value := DownloadFileExclusionReason(file.ExclusionReason)
			item.ExclusionReason = &value
		}
		response.Files = append(response.Files, item)
	}
	return response
}

func acquisitionResponse(view domain.AcquisitionView) Acquisition {
	mediaType := view.MediaType
	if mediaType == "" {
		mediaType = domain.TaskMediaEpisode
	}
	archived := view.Archived
	response := Acquisition{
		Id: view.ID, Archived: &archived, MediaType: TaskMediaType(mediaType), SeriesId: view.SeriesID,
		SourceKind: AcquisitionSourceKind(view.SourceKind), AggregateStatus: AcquisitionAggregateStatus(view.AggregateStatus),
		CurrentStage: AcquisitionStageKey(view.CurrentStage), OverallProgress: view.OverallProgress,
		CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt, Tasks: make([]AcquisitionTaskSummary, 0, len(view.Tasks)),
		Stages: make([]AcquisitionStage, 0, len(view.Stages)),
		Mapping: AcquisitionMappingCompleteness{
			SelectedVideoCount: int32(view.Mapping.SelectedVideoCount), MappedVideoCount: int32(view.Mapping.MappedVideoCount), Complete: view.Mapping.Complete,
		},
	}
	if mediaType == domain.TaskMediaMovie {
		if view.TMDbMovieID > 0 {
			response.TmdbMovieId = &view.TMDbMovieID
		}
		optionalString(&response.MovieTitle, view.MovieTitle)
		if view.ReleaseYear > 0 {
			year := int32(view.ReleaseYear)
			response.ReleaseYear = &year
		}
	} else {
		if view.TMDbSeriesID > 0 {
			response.TmdbSeriesId = &view.TMDbSeriesID
		}
		optionalString(&response.SeriesTitle, view.SeriesTitle)
	}
	if view.SourceSeason != nil {
		value := int32(*view.SourceSeason)
		response.SourceSeason = &value
	}
	if view.SourceEpisode != nil {
		value := int32(*view.SourceEpisode)
		response.SourceEpisode = &value
	}
	response.SingleEpisode = view.SingleEpisode
	optionalString(&response.SourceTitle, view.SourceTitle)
	response.ArchivedAt = view.ArchivedAt
	response.MappingProfileId = view.MappingProfileID
	if view.MappingDecisionSource != "" {
		value := AcquisitionMappingDecisionSource(view.MappingDecisionSource)
		response.MappingDecisionSource = &value
	}
	response.MappingAgentResolutionId = view.MappingAgentResolutionID
	response.ReleaseCandidateId = view.ReleaseCandidateID
	response.RssEntryId = view.RSSEntryID
	response.DownloadId = view.DownloadID
	if view.Download != nil {
		download := AcquisitionDownloadSummary{
			Id: view.Download.ID, Attempt: int32(view.Download.Attempt), Status: DownloadStatus(view.Download.Status),
			Progress: view.Download.Progress, UpdatedAt: view.Download.UpdatedAt,
		}
		optionalString(&download.ClientState, view.Download.ClientState)
		optionalString(&download.ErrorCode, view.Download.ErrorCode)
		optionalString(&download.ErrorMessage, view.Download.ErrorMessage)
		if view.Download.FailureStage != "" {
			value := AcquisitionDownloadSummaryFailureStage(view.Download.FailureStage)
			download.FailureStage = &value
		}
		response.Download = &download
	}
	for _, stage := range view.Stages {
		response.Stages = append(response.Stages, AcquisitionStage{
			Key: AcquisitionStageKey(stage.Key), Status: AcquisitionStageStatus(stage.Status), Progress: stage.Progress,
			CompletedItems: int32(stage.CompletedItems), TotalItems: int32(stage.TotalItems), UpdatedAt: stage.UpdatedAt,
		})
	}
	for _, task := range view.Tasks {
		item := AcquisitionTaskSummary{
			Id: task.ID, MediaType: TaskMediaType(task.MediaType), DownloadId: task.DownloadID,
			State: TaskState(task.State), VideoState: AcquisitionTaskSummaryVideoState(task.VideoState),
			SubtitleState: AcquisitionTaskSummarySubtitleState(task.SubtitleState), CanRetry: task.CanRetry, UpdatedAt: task.UpdatedAt,
			TargetEpisodeTitle: stringPtr(task.TargetEpisodeTitle), ReviewedAt: task.ReviewedAt,
		}
		if task.SourceSeason > 0 {
			value := int32(task.SourceSeason)
			item.SourceSeason = &value
		}
		if task.SourceEpisode > 0 {
			value := int32(task.SourceEpisode)
			item.SourceEpisode = &value
		}
		if task.TargetSeason != nil {
			value := int32(*task.TargetSeason)
			item.TargetSeason = &value
		}
		if task.TargetEpisode != nil {
			value := int32(*task.TargetEpisode)
			item.TargetEpisode = &value
		}
		optionalString(&item.ArtifactBasename, task.ArtifactBasename)
		optionalString(&item.DestinationVideoPath, task.DestinationVideoPath)
		optionalString(&item.DestinationSubtitlePath, task.DestinationSubtitlePath)
		if task.ReviewDecision != "" {
			value := AcquisitionTaskSummaryReviewDecision(task.ReviewDecision)
			item.ReviewDecision = &value
		}
		if task.ImportStatus != "" {
			value := AcquisitionTaskSummaryImportStatus(task.ImportStatus)
			item.ImportStatus = &value
		}
		if task.EmbyRefreshStatus != "" {
			value := AcquisitionTaskSummaryEmbyRefreshStatus(task.EmbyRefreshStatus)
			item.EmbyRefreshStatus = &value
		}
		if task.CleanupStatus != "" {
			value := AcquisitionTaskSummaryCleanupStatus(task.CleanupStatus)
			item.CleanupStatus = &value
		}
		if task.FailureStage != "" {
			value := AcquisitionTaskSummaryFailureStage(task.FailureStage)
			item.FailureStage = &value
		}
		optionalString(&item.ErrorCode, task.ErrorCode)
		optionalString(&item.ErrorMessage, task.ErrorMessage)
		response.Tasks = append(response.Tasks, item)
	}
	return response
}

func rssEntryResponse(view domain.RSSEntryView) RSSEntry {
	response := RSSEntry{
		Id: view.ID, SubscriptionId: view.SubscriptionID, Title: view.Title, Status: RSSEntryStatus(view.Status),
		Classification: RSSEntryClassification(view.Classification), DuplicateCount: int32(view.DuplicateCount), DownloadUriAvailable: view.DownloadURIAvailable,
		AdjudicationState: RSSEntryAdjudicationState(view.AdjudicationState), CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
	}
	response.ReleaseCandidateId = view.ReleaseCandidateID
	response.AcquisitionId = view.AcquisitionID
	if view.AcquisitionProgress != nil {
		response.AcquisitionProgress = &AcquisitionProgress{
			AggregateStatus: AcquisitionAggregateStatus(view.AcquisitionProgress.AggregateStatus),
			CurrentStage:    AcquisitionStageKey(view.AcquisitionProgress.CurrentStage),
			OverallProgress: view.AcquisitionProgress.OverallProgress,
		}
	}
	response.DownloadId = view.DownloadID
	response.PublishedAt = view.PublishedAt
	if view.SourceSeason != nil {
		value := int32(*view.SourceSeason)
		response.SourceSeason = &value
	}
	if view.SourceEpisode != nil {
		value := int32(*view.SourceEpisode)
		response.SourceEpisode = &value
	}
	if view.CoordinateSource != "" {
		value := RSSEntryCoordinateSource(view.CoordinateSource)
		response.CoordinateSource = &value
	}
	response.AgentResolutionId = view.AgentResolutionID
	response.AdjudicationBatchId = view.AdjudicationBatchID
	if view.AdjudicationSource != "" {
		value := RSSEntryAdjudicationSource(view.AdjudicationSource)
		response.AdjudicationSource = &value
	}
	response.AdjudicationResolutionId = view.AdjudicationResolutionID
	response.RelatedEntryId = view.RelatedEntryID
	optionalString(&response.RejectReason, view.RejectReason)
	optionalString(&response.ErrorCode, view.ErrorCode)
	optionalString(&response.ErrorMessage, view.ErrorMessage)
	response.ImportedAt = view.ImportedAt
	return response
}

func operationResponse(view domain.OperationView) Operation {
	response := Operation{
		Id: view.ID, Kind: view.Kind, Status: OperationStatus(view.Status), IdempotencyKey: view.IdempotencyKey,
		MaxAttempts: int32(view.MaxAttempts), AttemptCount: int32(view.AttemptCount),
		CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt, Attempts: make([]OperationAttempt, 0, len(view.Attempts)),
	}
	optionalString(&response.ResourceType, view.ResourceType)
	response.ResourceId = view.ResourceID
	optionalString(&response.ResourceHref, view.ResourceHref)
	response.HeartbeatAt = view.HeartbeatAt
	optionalString(&response.ErrorCode, view.ErrorCode)
	optionalString(&response.ErrorMessage, view.ErrorMessage)
	response.StartedAt = view.StartedAt
	response.FinishedAt = view.FinishedAt
	for _, attempt := range view.Attempts {
		item := OperationAttempt{
			Id: attempt.ID, Attempt: int32(attempt.Attempt), Status: OperationAttemptStatus(attempt.Status), StartedAt: attempt.StartedAt,
		}
		optionalString(&item.WorkerId, attempt.WorkerID)
		optionalString(&item.ErrorCode, attempt.ErrorCode)
		optionalString(&item.ErrorMessage, attempt.ErrorMessage)
		item.HeartbeatAt = attempt.HeartbeatAt
		item.FinishedAt = attempt.FinishedAt
		response.Attempts = append(response.Attempts, item)
	}
	return response
}

func eventRecordResponse(view domain.EventRecordView) EventRecord {
	response := EventRecord{Id: view.ID, Topic: view.Topic, OccurredAt: view.OccurredAt}
	optionalString(&response.ResourceType, view.ResourceType)
	response.ResourceId = view.ResourceID
	response.OperationId = view.OperationID
	if len(view.Data) > 0 {
		data := map[string]any{}
		if err := json.Unmarshal(view.Data, &data); err == nil {
			response.Data = &data
		}
	}
	return response
}

func dashboardResponse(summary domain.DashboardSummary) DashboardSummary {
	response := DashboardSummary{
		Counts: DashboardStatusCounts{
			Downloading: int32(summary.Counts.Downloading), Processing: int32(summary.Counts.Processing),
			AwaitingReview: int32(summary.Counts.AwaitingReview), Importing: int32(summary.Counts.Importing),
			Attention: int32(summary.Counts.Attention), Failed: int32(summary.Counts.Failed), CleanupFailed: int32(summary.Counts.CleanupFailed),
			MappingPending: int32(summary.Counts.MappingPending),
		},
		AgentResolutions: DashboardAgentResolutionStats{
			Total: int32(summary.AgentResolutions.Total), ReviewPending: int32(summary.AgentResolutions.ReviewPending),
			Applied: int32(summary.AgentResolutions.Applied), AutoApplied: int32(summary.AgentResolutions.AutoApplied),
			Accepted: int32(summary.AgentResolutions.Accepted), Rejected: int32(summary.AgentResolutions.Rejected), Failed: int32(summary.AgentResolutions.Failed),
			InputTokens: summary.AgentResolutions.InputTokens, OutputTokens: summary.AgentResolutions.OutputTokens,
			AverageLatencyMilliseconds: summary.AgentResolutions.AverageLatencyMilliseconds,
		},
		Links: DashboardLinks{
			Downloading: summary.Links.Downloading, Processing: summary.Links.Processing, AwaitingReview: summary.Links.AwaitingReview,
			Importing: summary.Links.Importing, Failed: summary.Links.Failed, CleanupFailed: summary.Links.CleanupFailed,
			MappingPending: summary.Links.MappingPending,
		},
		AttentionItems:   make([]DashboardAttentionItem, 0, len(summary.AttentionItems)),
		RecentOperations: make([]DashboardRecentOperation, 0, len(summary.RecentOperations)),
		RecentImports:    make([]DashboardRecentImport, 0, len(summary.RecentImports)),
		RecentScans:      make([]DashboardRecentScan, 0, len(summary.RecentScans)),
	}
	applyDependencyResult(&response.Dependencies.QBittorrent, summary.Dependencies.QBittorrent)
	applyDependencyResult(&response.Dependencies.Tmdb, summary.Dependencies.TMDb)
	applyDependencyResult(&response.Dependencies.Emby, summary.Dependencies.Emby)
	applyDependencyResult(&response.Dependencies.MediaTools, summary.Dependencies.MediaTools)
	applyDependencyResult(&response.Dependencies.NetworkProxy, summary.Dependencies.NetworkProxy)
	applyDependencyResult(&response.Dependencies.Agent, summary.Dependencies.Agent)
	for _, item := range summary.AttentionItems {
		entry := DashboardAttentionItem{
			Acquisition: acquisitionResponse(item.Acquisition),
			Reason:      DashboardAttentionItemReason(item.Reason),
		}
		optionalString(&entry.ErrorCode, item.ErrorCode)
		optionalString(&entry.ErrorMessage, item.ErrorMessage)
		response.AttentionItems = append(response.AttentionItems, entry)
	}
	for _, item := range summary.RecentOperations {
		response.RecentOperations = append(response.RecentOperations, dashboardOperationResponse(item))
	}
	for _, item := range summary.RecentImports {
		entry := DashboardRecentImport{
			TaskId: item.TaskID, AcquisitionId: item.AcquisitionID, MediaType: TaskMediaType(item.MediaType), CompletedAt: item.CompletedAt,
		}
		optionalString(&entry.SeriesTitle, item.SeriesTitle)
		optionalString(&entry.MovieTitle, item.MovieTitle)
		if item.ReleaseYear > 0 {
			year := int32(item.ReleaseYear)
			entry.ReleaseYear = &year
		}
		if item.SeasonNumber != nil {
			value := int32(*item.SeasonNumber)
			entry.SeasonNumber = &value
		}
		if item.EpisodeNumber != nil {
			value := int32(*item.EpisodeNumber)
			entry.EpisodeNumber = &value
		}
		optionalString(&entry.DestinationPath, item.DestinationPath)
		response.RecentImports = append(response.RecentImports, entry)
	}
	for _, item := range summary.RecentScans {
		entry := DashboardRecentScan{
			Id: item.ID, OperationId: item.OperationID, Status: DashboardRecentScanStatus(item.Status),
			LibraryCount: int32(item.LibraryCount), ItemCount: int32(item.ItemCount), CreatedAt: item.CreatedAt, CompletedAt: item.CompletedAt,
		}
		optionalString(&entry.ErrorCode, item.ErrorCode)
		optionalString(&entry.ErrorMessage, item.ErrorMessage)
		response.RecentScans = append(response.RecentScans, entry)
	}
	return response
}

func dashboardOperationResponse(item domain.DashboardRecentOperation) DashboardRecentOperation {
	entry := DashboardRecentOperation{
		Id: item.ID, Kind: item.Kind, Status: DashboardRecentOperationStatus(item.Status), UpdatedAt: item.UpdatedAt,
	}
	optionalString(&entry.ResourceType, item.ResourceType)
	entry.ResourceId = item.ResourceID
	optionalString(&entry.ResourceHref, item.ResourceHref)
	optionalString(&entry.ErrorCode, item.ErrorCode)
	optionalString(&entry.ErrorMessage, item.ErrorMessage)
	return entry
}

func applyDependencyResult(target *DashboardDependencyStatus, result domain.DashboardDependencyStatus) {
	if !result.HasTest {
		return
	}
	target.LastTestSuccess = &result.Success
	optionalString(&target.LastTestCode, result.Code)
	optionalString(&target.LastTestMessage, result.Message)
	testedAt := result.TestedAt
	target.LastTestedAt = &testedAt
}

func optionalString(target **string, value string) {
	if value != "" {
		*target = &value
	}
}

var _ = service.ErrInvalidInput
