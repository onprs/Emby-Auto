package httpapi

import (
	"context"
	"errors"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) ListRSSSubtitleGroups(
	ctx context.Context,
	_ ListRSSSubtitleGroupsRequestObject,
) (ListRSSSubtitleGroupsResponseObject, error) {
	if server.rssSubtitleGroups == nil {
		return ListRSSSubtitleGroups503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	groups, err := server.rssSubtitleGroups.List(ctx)
	if err != nil {
		return ListRSSSubtitleGroups503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := RSSSubtitleGroupPage{Items: make([]RSSSubtitleGroup, 0, len(groups.Items))}
	for _, group := range groups.Items {
		response.Items = append(response.Items, rssSubtitleGroupResponse(group))
	}
	return ListRSSSubtitleGroups200JSONResponse(response), nil
}

func (server *Server) CreateRSSSubtitleGroup(
	ctx context.Context,
	request CreateRSSSubtitleGroupRequestObject,
) (CreateRSSSubtitleGroupResponseObject, error) {
	if server.rssSubtitleGroups == nil {
		return CreateRSSSubtitleGroup503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return CreateRSSSubtitleGroup401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return CreateRSSSubtitleGroup400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	group, err := server.rssSubtitleGroups.Create(ctx, domain.CreateRSSSubtitleGroup{
		Name:        request.Body.Name,
		ActorUserID: authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return CreateRSSSubtitleGroup400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return CreateRSSSubtitleGroup409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return CreateRSSSubtitleGroup503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return CreateRSSSubtitleGroup201JSONResponse(rssSubtitleGroupResponse(group)), nil
	}
}

func (server *Server) UpdateRSSSubtitleGroup(
	ctx context.Context,
	request UpdateRSSSubtitleGroupRequestObject,
) (UpdateRSSSubtitleGroupResponseObject, error) {
	if server.rssSubtitleGroups == nil {
		return UpdateRSSSubtitleGroup503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return UpdateRSSSubtitleGroup401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return UpdateRSSSubtitleGroup400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	group, err := server.rssSubtitleGroups.Update(ctx, domain.UpdateRSSSubtitleGroup{
		ID:          uuid.UUID(request.GroupId),
		Name:        request.Body.Name,
		ActorUserID: authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return UpdateRSSSubtitleGroup400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return UpdateRSSSubtitleGroup404JSONResponse{NotFoundJSONResponse: rssSubtitleGroupNotFoundError(ctx)}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return UpdateRSSSubtitleGroup409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return UpdateRSSSubtitleGroup503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return UpdateRSSSubtitleGroup200JSONResponse(rssSubtitleGroupResponse(group)), nil
	}
}

func (server *Server) DeleteRSSSubtitleGroup(
	ctx context.Context,
	request DeleteRSSSubtitleGroupRequestObject,
) (DeleteRSSSubtitleGroupResponseObject, error) {
	if server.rssSubtitleGroups == nil {
		return DeleteRSSSubtitleGroup503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "rss")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return DeleteRSSSubtitleGroup401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	err := server.rssSubtitleGroups.Delete(ctx, uuid.UUID(request.GroupId), authenticated.session.User.ID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return DeleteRSSSubtitleGroup404JSONResponse{NotFoundJSONResponse: rssSubtitleGroupNotFoundError(ctx)}, nil
	case err != nil:
		return DeleteRSSSubtitleGroup503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return DeleteRSSSubtitleGroup204Response{}, nil
	}
}

func rssSubtitleGroupResponse(group domain.RSSSubtitleGroup) RSSSubtitleGroup {
	return RSSSubtitleGroup{
		Id:        group.ID,
		Name:      group.Name,
		CreatedAt: group.CreatedAt,
		UpdatedAt: group.UpdatedAt,
	}
}

func rssSubtitleGroupNotFoundError(ctx context.Context) NotFoundJSONResponse {
	return NotFoundJSONResponse(ApiError{
		Code:      "not_found",
		Message:   "the RSS subtitle group was not found",
		Details:   map[string]any{},
		RequestId: middleware.GetReqID(ctx),
	})
}
