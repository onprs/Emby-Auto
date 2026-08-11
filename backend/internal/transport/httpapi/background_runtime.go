package httpapi

import (
	"context"
	"errors"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) GetBackgroundRuntime(
	ctx context.Context,
	_ GetBackgroundRuntimeRequestObject,
) (GetBackgroundRuntimeResponseObject, error) {
	if server.backgroundRuntime == nil {
		return GetBackgroundRuntime503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "host_control")}, nil
	}
	if _, ok := authenticationFromContext(ctx); !ok {
		return GetBackgroundRuntime401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	runtime, err := server.backgroundRuntime.Get(ctx)
	if err != nil {
		return GetBackgroundRuntime503JSONResponse{ServiceUnavailableJSONResponse: backgroundRuntimeError(ctx, err)}, nil
	}
	return GetBackgroundRuntime200JSONResponse(backgroundRuntimeResponse(runtime)), nil
}

func (server *Server) UpdateBackgroundRuntime(
	ctx context.Context,
	request UpdateBackgroundRuntimeRequestObject,
) (UpdateBackgroundRuntimeResponseObject, error) {
	if server.backgroundRuntime == nil {
		return UpdateBackgroundRuntime503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "host_control")}, nil
	}
	if _, ok := authenticationFromContext(ctx); !ok {
		return UpdateBackgroundRuntime401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return UpdateBackgroundRuntime400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}

	runtime, err := server.backgroundRuntime.Set(ctx, domain.BackgroundRuntimeState(request.Body.State))
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return UpdateBackgroundRuntime400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return UpdateBackgroundRuntime503JSONResponse{ServiceUnavailableJSONResponse: backgroundRuntimeError(ctx, err)}, nil
	default:
		return UpdateBackgroundRuntime200JSONResponse(backgroundRuntimeResponse(runtime)), nil
	}
}

func backgroundRuntimeResponse(runtime domain.BackgroundRuntime) BackgroundRuntime {
	return BackgroundRuntime{State: BackgroundRuntimeState(runtime.State)}
}

func backgroundRuntimeError(ctx context.Context, err error) ServiceUnavailableJSONResponse {
	var serviceErr *service.Error
	if errors.As(err, &serviceErr) {
		return ServiceUnavailableJSONResponse(apiErrorFromService(ctx, serviceErr))
	}
	return serviceUnavailableError(ctx, "host_control")
}
