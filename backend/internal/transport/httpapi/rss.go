package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) LookupRSSFeed(
	ctx context.Context,
	request LookupRSSFeedRequestObject,
) (LookupRSSFeedResponseObject, error) {
	if server.rssFeedLookup == nil {
		return LookupRSSFeed503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	if request.Body == nil {
		return LookupRSSFeed400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	lookup, err := server.rssFeedLookup.Lookup(ctx, request.Body.FeedUrl)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return LookupRSSFeed400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr):
		return LookupRSSFeed503JSONResponse{ServiceUnavailableJSONResponse: ServiceUnavailableJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return LookupRSSFeed503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	default:
		response := LookupRSSFeed200JSONResponse{
			FeedUrl:            lookup.FeedURL,
			SuggestedQuery:     lookup.SuggestedQuery,
			SuggestedQueries:   lookup.SuggestedQueries,
			SampleTitles:       lookup.SampleTitles,
			Candidates:         make([]TMDbSeriesSearchResult, 0, len(lookup.Candidates)),
			CatalogMatchSource: RSSFeedLookupCatalogMatchSource(lookup.CatalogMatchSource),
		}
		for _, item := range lookup.Candidates {
			candidate := TMDbSeriesSearchResult{TmdbSeriesId: item.TMDbSeriesID, Name: item.Name}
			if item.OriginalName != "" {
				candidate.OriginalName = &item.OriginalName
			}
			if item.FirstAirDate != "" {
				if parsed, parseErr := parseDate(item.FirstAirDate); parseErr == nil {
					candidate.FirstAirDate = &parsed
				}
			}
			if item.Overview != "" {
				candidate.Overview = &item.Overview
			}
			response.Candidates = append(response.Candidates, candidate)
		}
		if lookup.FeedTitle != "" {
			response.FeedTitle = &lookup.FeedTitle
		}
		if lookup.AgentResolutionID != nil {
			resolutionID := *lookup.AgentResolutionID
			response.AgentResolutionId = &resolutionID
		}
		return response, nil
	}
}

func (server *Server) ListRSSSubscriptions(
	ctx context.Context,
	request ListRSSSubscriptionsRequestObject,
) (ListRSSSubscriptionsResponseObject, error) {
	if server.rssSubscriptions == nil {
		return ListRSSSubscriptions503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
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
	var sortBy, sortOrder *string
	if request.Params.SortBy != nil {
		value := string(*request.Params.SortBy)
		sortBy = &value
	}
	if request.Params.SortOrder != nil {
		value := string(*request.Params.SortOrder)
		sortOrder = &value
	}
	page, err := server.rssSubscriptions.ListSubscriptions(ctx, cursor, limit, sortBy, sortOrder)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return ListRSSSubscriptions400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return ListRSSSubscriptions400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "the RSS cursor was not found")}, nil
	case err != nil:
		return ListRSSSubscriptions503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		response := RSSSubscriptionPage{Items: make([]RSSSubscription, 0, len(page.Items))}
		for _, subscription := range page.Items {
			response.Items = append(response.Items, rssSubscriptionResponse(subscription))
		}
		if page.NextCursor != nil {
			response.NextCursor = page.NextCursor
		}
		return ListRSSSubscriptions200JSONResponse(response), nil
	}
}

func (server *Server) CreateRSSSubscription(
	ctx context.Context,
	request CreateRSSSubscriptionRequestObject,
) (CreateRSSSubscriptionResponseObject, error) {
	if server.rssSubscriptions == nil {
		return CreateRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return CreateRSSSubscription401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return CreateRSSSubscription400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	mappingProfileID := uuid.Nil
	if request.Body.MappingProfileId != nil {
		mappingProfileID = uuid.UUID(*request.Body.MappingProfileId)
	}
	subscription, err := server.rssSubscriptions.CreateSubscription(ctx, domain.CreateRSSSubscription{
		TMDbSeriesID:              request.Body.TmdbSeriesId,
		SeriesTitle:               request.Body.SeriesTitle,
		MappingProfileID:          mappingProfileID,
		Name:                      request.Body.Name,
		FeedURL:                   request.Body.FeedUrl,
		IncludeKeywords:           request.Body.IncludeKeywords,
		ExcludeKeywords:           request.Body.ExcludeKeywords,
		Enabled:                   request.Body.Enabled,
		AutoEpisodeMapping:        request.Body.AutoEpisodeMapping,
		AutoReview:                request.Body.AutoReview,
		CleanupSourceOnCompletion: request.Body.CleanupSourceOnCompletion,
		SourceSeason:              int(request.Body.SourceSeason),
		PollInterval:              time.Duration(request.Body.PollIntervalSeconds) * time.Second,
		ActorUserID:               authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return CreateRSSSubscription400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return CreateRSSSubscription409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return CreateRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return CreateRSSSubscription201JSONResponse(rssSubscriptionResponse(subscription)), nil
	}
}

func (server *Server) GetRSSSubscription(
	ctx context.Context,
	request GetRSSSubscriptionRequestObject,
) (GetRSSSubscriptionResponseObject, error) {
	if server.rssSubscriptions == nil {
		return GetRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	subscription, err := server.rssSubscriptions.GetSubscription(ctx, uuid.UUID(request.SubscriptionId))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetRSSSubscription404JSONResponse{NotFoundJSONResponse: rssNotFoundError(ctx)}, nil
	case err != nil:
		return GetRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return GetRSSSubscription200JSONResponse(rssSubscriptionResponse(subscription)), nil
	}
}

func (server *Server) UpdateRSSSubscription(
	ctx context.Context,
	request UpdateRSSSubscriptionRequestObject,
) (UpdateRSSSubscriptionResponseObject, error) {
	if server.rssSubscriptions == nil {
		return UpdateRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return UpdateRSSSubscription401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return UpdateRSSSubscription400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	mappingProfileID := uuid.Nil
	if request.Body.MappingProfileId != nil {
		mappingProfileID = uuid.UUID(*request.Body.MappingProfileId)
	}
	subscription, err := server.rssSubscriptions.UpdateSubscription(ctx, domain.UpdateRSSSubscription{
		ID:                        uuid.UUID(request.SubscriptionId),
		ExpectedVersion:           request.Body.ExpectedVersion,
		MappingProfileID:          mappingProfileID,
		Name:                      request.Body.Name,
		FeedURL:                   request.Body.FeedUrl,
		IncludeKeywords:           request.Body.IncludeKeywords,
		ExcludeKeywords:           request.Body.ExcludeKeywords,
		Enabled:                   request.Body.Enabled,
		AutoEpisodeMapping:        request.Body.AutoEpisodeMapping,
		AutoReview:                request.Body.AutoReview,
		CleanupSourceOnCompletion: request.Body.CleanupSourceOnCompletion,
		SourceSeason:              int(request.Body.SourceSeason),
		PollInterval:              time.Duration(request.Body.PollIntervalSeconds) * time.Second,
		ActorUserID:               authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return UpdateRSSSubscription400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return UpdateRSSSubscription404JSONResponse{NotFoundJSONResponse: rssNotFoundError(ctx)}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return UpdateRSSSubscription409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return UpdateRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return UpdateRSSSubscription200JSONResponse(rssSubscriptionResponse(subscription)), nil
	}
}

func (server *Server) DeleteRSSSubscription(
	ctx context.Context,
	request DeleteRSSSubscriptionRequestObject,
) (DeleteRSSSubscriptionResponseObject, error) {
	if server.rssSubscriptions == nil {
		return DeleteRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return DeleteRSSSubscription401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	deleteImported := request.Params.DeleteImported != nil && *request.Params.DeleteImported
	operation, err := server.rssSubscriptions.RequestSubscriptionDeletion(
		ctx,
		uuid.UUID(request.SubscriptionId),
		request.Params.ExpectedVersion,
		request.Params.IdempotencyKey,
		deleteImported,
		authenticated.session.User.ID,
	)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return DeleteRSSSubscription400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return DeleteRSSSubscription404JSONResponse{NotFoundJSONResponse: rssNotFoundError(ctx)}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return DeleteRSSSubscription409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return DeleteRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return DeleteRSSSubscription202JSONResponse(CommandAccepted{
			OperationId: operation.ID,
			Status:      CommandAcceptedStatus(operation.Status),
		}), nil
	}
}

func (server *Server) PollRSSSubscription(
	ctx context.Context,
	request PollRSSSubscriptionRequestObject,
) (PollRSSSubscriptionResponseObject, error) {
	if server.rssSubscriptions == nil {
		return PollRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return PollRSSSubscription401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	operation, err := server.rssSubscriptions.ScheduleManualPoll(
		ctx,
		uuid.UUID(request.SubscriptionId),
		request.Params.IdempotencyKey,
		authenticated.session.User.ID,
	)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return PollRSSSubscription400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return PollRSSSubscription404JSONResponse{NotFoundJSONResponse: rssNotFoundError(ctx)}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return PollRSSSubscription409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return PollRSSSubscription503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return PollRSSSubscription202JSONResponse(CommandAccepted{
			OperationId: operation.ID,
			Status:      CommandAcceptedStatus(operation.Status),
		}), nil
	}
}

func rssSubscriptionResponse(subscription domain.RSSSubscription) RSSSubscription {
	response := RSSSubscription{
		Id:                        subscription.ID,
		SeriesId:                  subscription.SeriesID,
		TmdbSeriesId:              subscription.TMDbSeriesID,
		SeriesTitle:               subscription.SeriesTitle,
		Name:                      subscription.Name,
		FeedUrl:                   subscription.FeedURL,
		IncludeKeywords:           subscription.IncludeKeywords,
		ExcludeKeywords:           subscription.ExcludeKeywords,
		Enabled:                   subscription.Enabled,
		AutoEpisodeMapping:        subscription.AutoEpisodeMapping,
		AutoReview:                subscription.AutoReview,
		CleanupSourceOnCompletion: subscription.CleanupSourceOnCompletion,
		SourceSeason:              int32(subscription.SourceSeason),
		PollIntervalSeconds:       int32(subscription.PollInterval / time.Second),
		LastPolledAt:              subscription.LastPolledAt,
		NextPollAt:                subscription.NextPollAt,
		CompletedAt:               subscription.CompletedAt,
		OverallProgress:           subscription.OverallProgress,
		TaskCount:                 int32(subscription.TaskCount),
		CompletedTaskCount:        int32(subscription.CompletedTaskCount),
		AttentionTaskCount:        int32(subscription.AttentionTaskCount),
		RetryableTaskCount:        int32Ptr(int32(subscription.RetryableTaskCount)),
		Version:                   subscription.Version,
		CreatedAt:                 subscription.CreatedAt,
		UpdatedAt:                 subscription.UpdatedAt,
	}
	if subscription.MappingProfileID != uuid.Nil {
		mappingProfileID := subscription.MappingProfileID
		response.MappingProfileId = &mappingProfileID
	}
	return response
}

func rssNotFoundError(ctx context.Context) NotFoundJSONResponse {
	return NotFoundJSONResponse(ApiError{
		Code:      "not_found",
		Message:   "the RSS subscription was not found",
		Details:   map[string]any{},
		RequestId: middleware.GetReqID(ctx),
	})
}

func int32Ptr(value int32) *int32 {
	return &value
}
