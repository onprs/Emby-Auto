package httpapi

import (
	"context"
	"errors"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) GetSetupStatus(
	ctx context.Context,
	_ GetSetupStatusRequestObject,
) (GetSetupStatusResponseObject, error) {
	if server.setup == nil {
		return GetSetupStatus503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "setup")}, nil
	}
	status, err := server.setup.Status(ctx)
	if err != nil {
		return GetSetupStatus503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "setup")}, nil
	}
	return GetSetupStatus200JSONResponse(setupStatusResponse(status)), nil
}

func (server *Server) InitializeSetup(
	ctx context.Context,
	request InitializeSetupRequestObject,
) (InitializeSetupResponseObject, error) {
	if server.setup == nil {
		return InitializeSetup503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "setup")}, nil
	}
	if request.Body == nil {
		return InitializeSetup400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	input := domain.InitializeSetup{
		AdministratorUsername: request.Body.Administrator.Username,
		AdministratorPassword: request.Body.Administrator.Password,
		Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{
				URL:      request.Body.Configuration.QBittorrent.Url,
				Username: request.Body.Configuration.QBittorrent.Username,
			},
			Emby: domain.EmbySettings{URL: request.Body.Configuration.Emby.Url},
			Paths: domain.PathSettings{
				DownloadRoot:     request.Body.Configuration.Paths.DownloadRoot,
				WorkRoot:         request.Body.Configuration.Paths.WorkRoot,
				StagingRoot:      request.Body.Configuration.Paths.StagingRoot,
				AnimeLibraryRoot: request.Body.Configuration.Paths.AnimeLibraryRoot,
				MovieLibraryRoot: request.Body.Configuration.Paths.MovieLibraryRoot,
				FFmpegPath:       request.Body.Configuration.Paths.FfmpegPath,
				FFprobePath:      request.Body.Configuration.Paths.FfprobePath,
			},
			Transcode: transcodeProfileFromRequest(request.Body.Configuration.Transcode),
		},
		Secrets: domain.SetupSecrets{
			QBittorrentPassword: request.Body.Configuration.QBittorrent.Password,
			EmbyAPIKey:          request.Body.Configuration.Emby.ApiKey,
			TMDbAPIToken:        request.Body.Configuration.Tmdb.ApiToken,
		},
	}
	if request.Body.Database != nil {
		input.Database = &domain.SetupDatabase{
			Host:     request.Body.Database.Host,
			Port:     int(request.Body.Database.Port),
			Database: request.Body.Database.Database,
			Username: request.Body.Database.Username,
			Password: request.Body.Database.Password,
			SSLMode:  string(request.Body.Database.SslMode),
		}
	}
	status, err := server.setup.Initialize(ctx, input)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return InitializeSetup400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return InitializeSetup409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return InitializeSetup503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "setup")}, nil
	default:
		return InitializeSetup200JSONResponse(setupStatusResponse(status)), nil
	}
}

func setupStatusResponse(status domain.SetupStatus) SetupStatus {
	return SetupStatus{
		State:                     SetupStatusState(status.State),
		DatabaseConfigured:        status.DatabaseConfigured,
		DatabaseManagedExternally: status.DatabaseManagedExternally,
		AdministratorConfigured:   status.AdministratorConfigured,
	}
}
