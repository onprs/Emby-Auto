package httpapi

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) ListTasks(ctx context.Context, request ListTasksRequestObject) (ListTasksResponseObject, error) {
	if server.tasks == nil {
		return ListTasks503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tasks")}, nil
	}
	limit := 50
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
	}
	var cursor *uuid.UUID
	if request.Params.Cursor != nil {
		value := uuid.UUID(*request.Params.Cursor)
		cursor = &value
	}
	var state *domain.TaskState
	if request.Params.State != nil {
		value := domain.TaskState(*request.Params.State)
		state = &value
	}
	var phase *string
	if request.Params.Phase != nil {
		value := string(*request.Params.Phase)
		phase = &value
	}
	page, err := server.tasks.ListTasks(ctx, cursor, limit, state, phase)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return ListTasks400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return ListTasks400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "the task cursor was not found")}, nil
	case err != nil:
		return ListTasks503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		response := TaskPage{Items: make([]Task, 0, len(page.Items))}
		for _, task := range page.Items {
			response.Items = append(response.Items, taskResponse(task))
		}
		response.NextCursor = page.NextCursor
		return ListTasks200JSONResponse(response), nil
	}
}

func (server *Server) GetTask(ctx context.Context, request GetTaskRequestObject) (GetTaskResponseObject, error) {
	if server.tasks == nil {
		return GetTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tasks")}, nil
	}
	task, err := server.tasks.GetTask(ctx, uuid.UUID(request.TaskId))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetTask404JSONResponse{NotFoundJSONResponse: taskNotFoundError(ctx)}, nil
	case err != nil:
		return GetTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return GetTask200JSONResponse(taskResponse(task)), nil
	}
}

func (server *Server) ReviewTask(ctx context.Context, request ReviewTaskRequestObject) (ReviewTaskResponseObject, error) {
	if server.tasks == nil {
		return ReviewTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tasks")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return ReviewTask401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return ReviewTask400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	task, err := server.tasks.ReviewTask(ctx, domain.ReviewTask{
		TaskID:          uuid.UUID(request.TaskId),
		ExpectedVersion: request.Body.ExpectedVersion,
		Decision:        domain.TaskState(request.Body.Decision),
		Notes:           request.Body.Notes,
		IdempotencyKey:  request.Params.IdempotencyKey,
		ActorUserID:     authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return ReviewTask400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return ReviewTask404JSONResponse{NotFoundJSONResponse: taskNotFoundError(ctx)}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return ReviewTask409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return ReviewTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return ReviewTask200JSONResponse(taskResponse(task)), nil
	}
}

func (server *Server) ImportTask(ctx context.Context, request ImportTaskRequestObject) (ImportTaskResponseObject, error) {
	if server.tasks == nil {
		return ImportTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tasks")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return ImportTask401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return ImportTask400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	result, err := server.tasks.QueueImport(ctx, domain.QueueTaskImport{
		TaskID:          uuid.UUID(request.TaskId),
		ExpectedVersion: request.Body.ExpectedVersion,
		IdempotencyKey:  request.Params.IdempotencyKey,
		ActorUserID:     authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return ImportTask400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return ImportTask404JSONResponse{NotFoundJSONResponse: taskNotFoundError(ctx)}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return ImportTask409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return ImportTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return ImportTask202JSONResponse(TaskCommandAccepted{
			Task:        taskResponse(result.Task),
			OperationId: result.Operation.ID,
			Status:      TaskCommandAcceptedStatus(result.Operation.Status),
		}), nil
	}
}

func taskResponse(task domain.EpisodeTask) Task {
	mediaType := task.MediaType
	if mediaType == "" {
		mediaType = domain.TaskMediaEpisode
	}
	response := Task{
		Id: task.ID, AcquisitionId: task.AcquisitionID, DownloadId: task.DownloadID, MediaType: TaskMediaType(mediaType),
		State: TaskState(task.State), VideoState: TaskVideoState(task.VideoState), SubtitleState: TaskSubtitleState(task.SubtitleState),
		Version: task.Version, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		Operations: make([]OperationSummary, 0, len(task.Operations)),
		Actions:    TaskActions{CanRetry: task.Actions.CanRetry, CanCancel: task.Actions.CanCancel, CanReview: task.Actions.CanReview, CanImport: task.Actions.CanImport},
	}
	if mediaType == domain.TaskMediaMovie {
		optionalString(&response.MovieTitle, task.MovieTitle)
		if task.ReleaseYear > 0 {
			year := int32(task.ReleaseYear)
			response.ReleaseYear = &year
		}
	} else {
		optionalString(&response.SeriesTitle, task.SeriesTitle)
		if task.SourceSeason > 0 {
			value := int32(task.SourceSeason)
			response.SourceSeason = &value
		}
		if task.SourceEpisode > 0 {
			value := int32(task.SourceEpisode)
			response.SourceEpisode = &value
		}
		if task.SourceEpisodeFractionHundredths > 0 {
			value := int32(task.SourceEpisodeFractionHundredths)
			response.SourceEpisodeFractionHundredths = &value
		}
		if task.TargetSeason >= 0 {
			value := int32(task.TargetSeason)
			response.TargetSeason = &value
		}
		if task.TargetEpisode > 0 {
			value := int32(task.TargetEpisode)
			response.TargetEpisode = &value
		}
		optionalString(&response.TargetEpisodeTitle, task.TargetEpisodeTitle)
	}
	if task.FailureStage != "" {
		value := TaskFailureStage(task.FailureStage)
		response.FailureStage = &value
	}
	if task.ErrorCode != "" {
		response.ErrorCode = &task.ErrorCode
	}
	if task.ErrorMessage != "" {
		response.ErrorMessage = &task.ErrorMessage
	}
	if task.Artifacts != nil {
		response.Artifacts = &ArtifactSet{
			Id: task.Artifacts.ID, Basename: task.Artifacts.BaseName,
			Video: mediaArtifactResponse(task.Artifacts.Video), Subtitle: mediaArtifactResponse(task.Artifacts.Subtitle),
		}
	}
	if task.Review != nil {
		review := &TaskReview{Id: task.Review.ID, Decision: TaskReviewDecision(task.Review.Decision), Notes: task.Review.Notes, ReviewedAt: task.Review.ReviewedAt}
		if task.Review.ReviewedBy != uuid.Nil {
			reviewedBy := task.Review.ReviewedBy
			review.ReviewedBy = &reviewedBy
		}
		response.Review = review
	}
	if task.Import != nil {
		response.Import = &TaskImport{Id: task.Import.ID, Attempt: int32(task.Import.Attempt), Status: TaskImportStatus(task.Import.Status),
			StartedAt: task.Import.StartedAt, CompletedAt: task.Import.CompletedAt, CreatedAt: task.Import.CreatedAt, UpdatedAt: task.Import.UpdatedAt}
		if task.Import.DestinationVideoPath != "" {
			response.Import.DestinationVideoPath = &task.Import.DestinationVideoPath
		}
		if task.Import.DestinationSubtitlePath != "" {
			response.Import.DestinationSubtitlePath = &task.Import.DestinationSubtitlePath
		}
		if task.Import.ErrorCode != "" {
			response.Import.ErrorCode = &task.Import.ErrorCode
		}
		if task.Import.ErrorMessage != "" {
			response.Import.ErrorMessage = &task.Import.ErrorMessage
		}
	}
	response.EmbyItemId = task.EmbyItemID
	response.EmbyLibraryId = task.EmbyLibraryID
	for _, operation := range task.Operations {
		item := OperationSummary{
			Id: operation.ID, Kind: operation.Kind, Status: OperationSummaryStatus(operation.Status),
			MaxAttempts: int32(operation.MaxAttempts), AttemptCount: int32(operation.AttemptCount),
			StartedAt: operation.StartedAt, FinishedAt: operation.FinishedAt, UpdatedAt: operation.UpdatedAt,
		}
		if operation.ErrorCode != "" {
			item.ErrorCode = &operation.ErrorCode
		}
		if operation.ErrorMessage != "" {
			item.ErrorMessage = &operation.ErrorMessage
		}
		response.Operations = append(response.Operations, item)
	}
	if task.Cleanup != nil {
		response.Cleanup = &TaskCleanup{Id: task.Cleanup.ID, Attempt: int32(task.Cleanup.Attempt), Status: TaskCleanupStatus(task.Cleanup.Status),
			TorrentRemoved: task.Cleanup.TorrentRemoved, StagedFilesRemoved: task.Cleanup.StagedFilesRemoved,
			StartedAt: task.Cleanup.StartedAt, CompletedAt: task.Cleanup.CompletedAt, CreatedAt: task.Cleanup.CreatedAt, UpdatedAt: task.Cleanup.UpdatedAt}
		if task.Cleanup.ErrorCode != "" {
			response.Cleanup.ErrorCode = &task.Cleanup.ErrorCode
		}
		if task.Cleanup.ErrorMessage != "" {
			response.Cleanup.ErrorMessage = &task.Cleanup.ErrorMessage
		}
	}
	return response
}

func mediaArtifactResponse(artifact domain.MediaArtifact) MediaArtifact {
	return MediaArtifact{
		Id: artifact.ID, Kind: MediaArtifactKind(artifact.Kind), FilePath: artifact.FilePath, Format: artifact.Format,
		SizeBytes: artifact.SizeBytes, ChecksumSha256: hex.EncodeToString(artifact.ChecksumSHA256),
	}
}

func taskNotFoundError(ctx context.Context) NotFoundJSONResponse {
	return NotFoundJSONResponse(ApiError{
		Code: "not_found", Message: "the media task was not found", Details: map[string]any{}, RequestId: middleware.GetReqID(ctx),
	})
}
