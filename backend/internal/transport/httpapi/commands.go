package httpapi

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) DeleteAcquisition(ctx context.Context, request DeleteAcquisitionRequestObject) (DeleteAcquisitionResponseObject, error) {
	if server.acquisitionCommands == nil {
		return DeleteAcquisition503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "acquisitions")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return DeleteAcquisition401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	operation, err := server.acquisitionCommands.RequestDeletion(ctx, uuid.UUID(request.AcquisitionId), request.Params.IdempotencyKey, authenticated.session.User.ID)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return DeleteAcquisition400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return DeleteAcquisition404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the acquisition was not found")}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return DeleteAcquisition409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return DeleteAcquisition503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return DeleteAcquisition202JSONResponse(CommandAccepted{OperationId: operation.ID, Status: CommandAcceptedStatus(operation.Status)}), nil
	}
}

func (server *Server) RetryDownload(ctx context.Context, request RetryDownloadRequestObject) (RetryDownloadResponseObject, error) {
	if server.downloadCommands == nil {
		return RetryDownload503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "downloads")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return RetryDownload401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return RetryDownload400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	download, operation, err := server.downloadCommands.Retry(ctx, uuid.UUID(request.DownloadId), request.Body.ExpectedVersion, request.Params.IdempotencyKey, authenticated.session.User.ID)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return RetryDownload400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return RetryDownload404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the download was not found")}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return RetryDownload409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return RetryDownload503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return RetryDownload202JSONResponse(DownloadCommandAccepted{
			Download: downloadResponse(download), OperationId: operation.ID, Status: DownloadCommandAcceptedStatus(operation.Status),
		}), nil
	}
}

func (server *Server) CancelDownload(ctx context.Context, request CancelDownloadRequestObject) (CancelDownloadResponseObject, error) {
	if server.downloadCommands == nil {
		return CancelDownload503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "downloads")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return CancelDownload401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return CancelDownload400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	download, operation, err := server.downloadCommands.Cancel(ctx, uuid.UUID(request.DownloadId), request.Body.ExpectedVersion, request.Params.IdempotencyKey, authenticated.session.User.ID)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return CancelDownload400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return CancelDownload404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the download was not found")}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return CancelDownload409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return CancelDownload503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return CancelDownload202JSONResponse(DownloadCommandAccepted{
			Download: downloadResponse(download), OperationId: operation.ID, Status: DownloadCommandAcceptedStatus(operation.Status),
		}), nil
	}
}

func (server *Server) DeleteDownload(ctx context.Context, request DeleteDownloadRequestObject) (DeleteDownloadResponseObject, error) {
	if server.acquisitionCommands == nil {
		return DeleteDownload503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "acquisitions")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return DeleteDownload401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	operation, err := server.acquisitionCommands.RequestDownloadDeletion(ctx, uuid.UUID(request.DownloadId), request.Params.ExpectedVersion, request.Params.IdempotencyKey, authenticated.session.User.ID)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return DeleteDownload400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return DeleteDownload404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the download was not found")}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return DeleteDownload409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return DeleteDownload503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return DeleteDownload202JSONResponse(CommandAccepted{OperationId: operation.ID, Status: CommandAcceptedStatus(operation.Status)}), nil
	}
}

func (server *Server) SaveDownloadFileResolution(ctx context.Context, request SaveDownloadFileResolutionRequestObject) (SaveDownloadFileResolutionResponseObject, error) {
	if server.downloadCommands == nil {
		return SaveDownloadFileResolution503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "downloads")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return SaveDownloadFileResolution401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return SaveDownloadFileResolution400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	items := make([]domain.DownloadFileResolutionItem, 0, len(request.Body.Files))
	for _, file := range request.Body.Files {
		items = append(items, domain.DownloadFileResolutionItem{
			FileID: uuid.UUID(file.FileId), Selected: file.Selected,
			SourceSeason: int32PointerToInt(file.SourceSeason), SourceEpisode: int32PointerToInt(file.SourceEpisode),
		})
	}
	download, operation, err := server.downloadCommands.SaveFileResolution(
		ctx, uuid.UUID(request.DownloadId), request.Body.ExpectedVersion, items,
		string(request.Params.IdempotencyKey), authenticated.session.User.ID,
	)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return SaveDownloadFileResolution400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return SaveDownloadFileResolution404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the download was not found")}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return SaveDownloadFileResolution409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return SaveDownloadFileResolution503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return SaveDownloadFileResolution202JSONResponse(DownloadCommandAccepted{
			Download: downloadResponse(download), OperationId: operation.ID, Status: DownloadCommandAcceptedStatus(operation.Status),
		}), nil
	}
}

func int32PointerToInt(value *int32) *int {
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}

func (server *Server) SaveDownloadFileSelection(ctx context.Context, request SaveDownloadFileSelectionRequestObject) (SaveDownloadFileSelectionResponseObject, error) {
	if server.downloadCommands == nil {
		return SaveDownloadFileSelection503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "downloads")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return SaveDownloadFileSelection401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return SaveDownloadFileSelection400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	selections := make(map[uuid.UUID]bool, len(request.Body.Files))
	for _, item := range request.Body.Files {
		selections[uuid.UUID(item.FileId)] = item.Selected
	}
	download, operation, err := server.downloadCommands.SaveFileSelection(ctx, uuid.UUID(request.DownloadId), request.Body.ExpectedVersion, selections, request.Params.IdempotencyKey, authenticated.session.User.ID)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return SaveDownloadFileSelection400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return SaveDownloadFileSelection404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the download was not found")}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return SaveDownloadFileSelection409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return SaveDownloadFileSelection503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return SaveDownloadFileSelection202JSONResponse(DownloadCommandAccepted{
			Download: downloadResponse(download), OperationId: operation.ID, Status: DownloadCommandAcceptedStatus(operation.Status),
		}), nil
	}
}

func (server *Server) RetryTask(ctx context.Context, request RetryTaskRequestObject) (RetryTaskResponseObject, error) {
	if server.taskCommands == nil {
		return RetryTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tasks")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return RetryTask401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return RetryTask400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	task, operation, err := server.taskCommands.Retry(ctx, uuid.UUID(request.TaskId), request.Body.ExpectedVersion, request.Params.IdempotencyKey, authenticated.session.User.ID)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return RetryTask400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return RetryTask404JSONResponse{NotFoundJSONResponse: taskNotFoundError(ctx)}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return RetryTask409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return RetryTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return RetryTask202JSONResponse(TaskCommandAccepted{
			Task: taskResponse(task), OperationId: operation.ID, Status: TaskCommandAcceptedStatus(operation.Status),
		}), nil
	}
}

func (server *Server) CancelTask(ctx context.Context, request CancelTaskRequestObject) (CancelTaskResponseObject, error) {
	if server.taskCommands == nil {
		return CancelTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tasks")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return CancelTask401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return CancelTask400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	task, operation, err := server.taskCommands.Cancel(ctx, uuid.UUID(request.TaskId), request.Body.ExpectedVersion, request.Params.IdempotencyKey, authenticated.session.User.ID)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return CancelTask400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return CancelTask404JSONResponse{NotFoundJSONResponse: taskNotFoundError(ctx)}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return CancelTask409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return CancelTask503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return CancelTask202JSONResponse(TaskCommandAccepted{
			Task: taskResponse(task), OperationId: operation.ID, Status: TaskCommandAcceptedStatus(operation.Status),
		}), nil
	}
}
