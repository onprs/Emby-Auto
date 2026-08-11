package httpapi

import (
	"context"
	"errors"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) CreateSearch(
	ctx context.Context,
	request CreateSearchRequestObject,
) (CreateSearchResponseObject, error) {
	if server.search == nil {
		return CreateSearch503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "search")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return CreateSearch401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return CreateSearch400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	result, err := server.search.CreateSearch(ctx, domain.CreateSearch{
		Query:          request.Body.Query,
		IdempotencyKey: request.Params.IdempotencyKey,
		ActorUserID:    authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return CreateSearch400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return CreateSearch409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return CreateSearch503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return CreateSearch202JSONResponse(SearchCommandAccepted{
			Search:      searchRunResponse(result.Search),
			OperationId: result.Operation.ID,
			Status:      SearchCommandAcceptedStatus(result.Operation.Status),
		}), nil
	}
}

func (server *Server) GetSearch(
	ctx context.Context,
	request GetSearchRequestObject,
) (GetSearchResponseObject, error) {
	if server.search == nil {
		return GetSearch503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "search")}, nil
	}
	result, err := server.search.GetSearch(ctx, uuid.UUID(request.SearchId))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetSearch404JSONResponse{NotFoundJSONResponse: searchNotFoundError(ctx)}, nil
	case err != nil:
		return GetSearch503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return GetSearch200JSONResponse(searchRunResponse(result)), nil
	}
}

func (server *Server) CreateAcquisition(
	ctx context.Context,
	request CreateAcquisitionRequestObject,
) (CreateAcquisitionResponseObject, error) {
	if server.search == nil {
		return CreateAcquisition503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "search")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return CreateAcquisition401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return CreateAcquisition400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	mappingProfileID := uuid.Nil
	if request.Body.MappingProfileId != nil {
		mappingProfileID = uuid.UUID(*request.Body.MappingProfileId)
	}
	input := domain.CreateSearchAcquisition{
		CandidateID: uuid.UUID(request.Body.CandidateId), MediaType: domain.TaskMediaType(request.Body.MediaType),
		MappingProfileID: mappingProfileID, IdempotencyKey: request.Params.IdempotencyKey, ActorUserID: authenticated.session.User.ID,
	}
	if request.Body.TmdbSeriesId != nil {
		input.TMDbSeriesID = *request.Body.TmdbSeriesId
	}
	if request.Body.SeriesTitle != nil {
		input.SeriesTitle = *request.Body.SeriesTitle
	}
	if request.Body.TmdbMovieId != nil {
		input.TMDbMovieID = *request.Body.TmdbMovieId
	}
	if request.Body.MovieTitle != nil {
		input.MovieTitle = *request.Body.MovieTitle
	}
	if request.Body.ReleaseYear != nil {
		input.ReleaseYear = int(*request.Body.ReleaseYear)
	}
	if request.Body.SourceSeason != nil {
		input.SourceSeason = int(*request.Body.SourceSeason)
	}
	if request.Body.SourceEpisode != nil {
		input.SourceEpisode = int(*request.Body.SourceEpisode)
	}
	if request.Body.SingleEpisode != nil {
		input.SingleEpisode = *request.Body.SingleEpisode
	}
	result, err := server.search.CreateAcquisition(ctx, input)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return CreateAcquisition400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return CreateAcquisition404JSONResponse{NotFoundJSONResponse: candidateNotFoundError(ctx)}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return CreateAcquisition409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return CreateAcquisition503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return CreateAcquisition202JSONResponse(AcquisitionCommandAccepted{
			AcquisitionId: result.AcquisitionID,
			DownloadId:    result.DownloadID,
			OperationId:   result.Operation.ID,
			Status:        AcquisitionCommandAcceptedStatus(result.Operation.Status),
		}), nil
	}
}

func searchRunResponse(search domain.SearchRun) SearchRun {
	response := SearchRun{
		Id:          search.ID,
		Query:       search.Query,
		Status:      SearchRunStatus(search.Status),
		Candidates:  make([]ReleaseCandidate, 0, len(search.Candidates)),
		StartedAt:   search.StartedAt,
		CompletedAt: search.CompletedAt,
		CreatedAt:   search.CreatedAt,
		UpdatedAt:   search.UpdatedAt,
	}
	if search.ErrorCode != "" {
		response.ErrorCode = &search.ErrorCode
	}
	if search.ErrorMessage != "" {
		response.ErrorMessage = &search.ErrorMessage
	}
	for _, candidate := range search.Candidates {
		item := ReleaseCandidate{
			Id:           candidate.ID,
			SearchRunId:  candidate.SearchRunID,
			Provider:     candidate.Provider,
			Title:        candidate.Title,
			Downloadable: candidate.DownloadURI != "",
			PublishedAt:  candidate.PublishedAt,
			SizeBytes:    candidate.SizeBytes,
			CreatedAt:    candidate.CreatedAt,
		}
		if candidate.DownloadURI != "" {
			item.DownloadUri = &candidate.DownloadURI
		} else {
			reason := DownloadUriMissing
			item.UnavailableReason = &reason
		}
		if candidate.Seeders != nil {
			value := int32(*candidate.Seeders)
			item.Seeders = &value
		}
		response.Candidates = append(response.Candidates, item)
	}
	return response
}

func searchNotFoundError(ctx context.Context) NotFoundJSONResponse {
	return NotFoundJSONResponse(ApiError{
		Code:      "not_found",
		Message:   "the search run was not found",
		Details:   map[string]any{},
		RequestId: middleware.GetReqID(ctx),
	})
}

func candidateNotFoundError(ctx context.Context) NotFoundJSONResponse {
	return NotFoundJSONResponse(ApiError{
		Code:      "not_found",
		Message:   "the release candidate was not found",
		Details:   map[string]any{},
		RequestId: middleware.GetReqID(ctx),
	})
}
