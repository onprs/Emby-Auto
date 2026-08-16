package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (request *UpdateConfigurationRequest) UnmarshalJSON(data []byte) error {
	type generatedRequest UpdateConfigurationRequest
	var decoded generatedRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	// oapi-codegen 将 required 数值字段生成为值类型，需要单独保留缺失与显式 0 的区别。
	var presence struct {
		Events json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(data, &presence); err != nil {
		return err
	}
	if presence.Events != nil {
		var events struct {
			RetentionDays *int32 `json:"retentionDays"`
		}
		if err := json.Unmarshal(presence.Events, &events); err != nil {
			return err
		}
		if events.RetentionDays == nil {
			return errors.New("events.retentionDays is required")
		}
	}

	*request = UpdateConfigurationRequest(decoded)
	return nil
}

func (server *Server) GetConfiguration(
	ctx context.Context,
	_ GetConfigurationRequestObject,
) (GetConfigurationResponseObject, error) {
	if server.configuration == nil {
		return GetConfiguration503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "configuration")}, nil
	}
	configuration, err := server.configuration.Load(ctx)
	if err != nil {
		return GetConfiguration503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	return GetConfiguration200JSONResponse(configurationResponse(configuration)), nil
}

func (server *Server) RevealConfigurationSecrets(
	ctx context.Context,
	_ RevealConfigurationSecretsRequestObject,
) (RevealConfigurationSecretsResponseObject, error) {
	if _, ok := authenticationFromContext(ctx); !ok {
		return secretRevealUnauthorized(ctx), nil
	}
	if server.configuration == nil {
		return secretRevealUnavailable(ctx, "configuration"), nil
	}

	configuration, err := server.configuration.Load(ctx)
	if err != nil {
		return secretRevealUnavailable(ctx, "postgresql"), nil
	}

	response := RevealedSecrets{}
	for _, secret := range []struct {
		name   string
		assign func(string)
	}{
		{name: domain.SecretQBittorrentPassword, assign: func(value string) { response.QbPassword = &value }},
		{name: domain.SecretEmbyAPIKey, assign: func(value string) { response.EmbyApiKey = &value }},
		{name: domain.SecretTMDbAPIToken, assign: func(value string) { response.TmdbApiToken = &value }},
		{name: domain.SecretAgentAPIKey, assign: func(value string) { response.AgentApiKey = &value }},
	} {
		if !configuration.Secrets[secret.name].Configured {
			continue
		}
		plaintext, decryptErr := server.configuration.ResolveSecret(ctx, secret.name)
		if decryptErr != nil {
			return secretRevealUnavailable(ctx, "postgresql"), nil
		}
		secret.assign(plaintext)
	}
	return RevealConfigurationSecrets200JSONResponse{
		Body:    response,
		Headers: RevealConfigurationSecrets200ResponseHeaders{CacheControl: configurationSecretsCacheControl},
	}, nil
}

func secretRevealUnauthorized(ctx context.Context) RevealConfigurationSecrets401JSONResponse {
	return RevealConfigurationSecrets401JSONResponse{SecretRevealUnauthorizedJSONResponse: SecretRevealUnauthorizedJSONResponse{
		Body:    ApiError(unauthorizedError(ctx, "authentication is required")),
		Headers: SecretRevealUnauthorizedResponseHeaders{CacheControl: configurationSecretsCacheControl},
	}}
}

func secretRevealUnavailable(ctx context.Context, dependency string) RevealConfigurationSecrets503JSONResponse {
	return RevealConfigurationSecrets503JSONResponse{SecretRevealServiceUnavailableJSONResponse: SecretRevealServiceUnavailableJSONResponse{
		Body:    ApiError(serviceUnavailableError(ctx, dependency)),
		Headers: SecretRevealServiceUnavailableResponseHeaders{CacheControl: configurationSecretsCacheControl},
	}}
}

func (server *Server) UpdateConfiguration(
	ctx context.Context,
	request UpdateConfigurationRequestObject,
) (UpdateConfigurationResponseObject, error) {
	if server.configuration == nil {
		return UpdateConfiguration503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "configuration")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return UpdateConfiguration401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return UpdateConfiguration400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}

	configuration, err := server.configuration.Update(ctx, configurationUpdate(*request.Body), authenticated.session.User.ID)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return UpdateConfiguration400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, service.ErrStateConflict):
		return UpdateConfiguration409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return UpdateConfiguration503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		if server.systemMetrics != nil {
			server.systemMetrics.SetDiskPaths(metricsDiskPaths(configuration.Settings.Paths))
		}
		return UpdateConfiguration200JSONResponse(configurationResponse(configuration)), nil
	}
}

func configurationUpdate(request UpdateConfigurationRequest) domain.ConfigurationUpdate {
	update := domain.ConfigurationUpdate{
		ExpectedVersion: request.ExpectedVersion,
		Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{
				URL:                           request.QBittorrent.Url,
				Username:                      request.QBittorrent.Username,
				DownloadRateLimitKibPerSecond: request.QBittorrent.DownloadRateLimitKibPerSecond,
				UploadRateLimitKibPerSecond:   request.QBittorrent.UploadRateLimitKibPerSecond,
			},
			Emby: domain.EmbySettings{URL: request.Emby.Url},
			NetworkProxy: domain.NetworkProxySettings{
				Enabled: request.NetworkProxy.Enabled,
				URL:     request.NetworkProxy.Url,
			},
			Agent: domain.AgentSettings{
				Enabled:                      request.Agent.Enabled,
				Protocol:                     string(request.Agent.Protocol),
				BaseURL:                      request.Agent.BaseUrl,
				Model:                        request.Agent.Model,
				UseNetworkProxy:              request.Agent.UseNetworkProxy,
				RequestTimeoutSeconds:        request.Agent.RequestTimeoutSeconds,
				RSSCoordinateMode:            string(request.Agent.RssCoordinateMode),
				DownloadFileSelectionMode:    string(request.Agent.DownloadFileSelectionMode),
				CatalogMatchEnabled:          request.Agent.CatalogMatchEnabled,
				EpisodeMappingEnabled:        request.Agent.EpisodeMappingEnabled,
				AllowAutomaticEpisodeMapping: request.Agent.AllowAutomaticEpisodeMapping,
				SubtitleVideoMatchMode:       string(request.Agent.SubtitleVideoMatchMode),
			},
			Paths: domain.PathSettings{
				DownloadRoot:     request.Paths.DownloadRoot,
				WorkRoot:         request.Paths.WorkRoot,
				StagingRoot:      request.Paths.StagingRoot,
				AnimeLibraryRoot: request.Paths.AnimeLibraryRoot,
				MovieLibraryRoot: request.Paths.MovieLibraryRoot,
				FFmpegPath:       request.Paths.FfmpegPath,
				FFprobePath:      request.Paths.FfprobePath,
			},
			Transcode: transcodeProfileFromRequest(request.Transcode),
		},
		Secrets: map[string]domain.SecretUpdate{
			domain.SecretQBittorrentPassword: secretUpdate(request.QBittorrent.Password),
			domain.SecretEmbyAPIKey:          secretUpdate(request.Emby.ApiKey),
			domain.SecretTMDbAPIToken:        secretUpdate(request.Tmdb.ApiToken),
			domain.SecretAgentAPIKey:         secretUpdate(request.Agent.ApiKey),
		},
	}
	if request.Events != nil {
		events := domain.EventsSettings{RetentionDays: request.Events.RetentionDays}
		update.Settings.Events = events
		update.Events = &events
	}
	return update
}

func transcodeProfileFromRequest(profile TranscodeProfileConfiguration) domain.TranscodeProfile {
	audioCodec := ""
	if profile.AudioCodec != nil {
		audioCodec = string(*profile.AudioCodec)
	}
	return domain.TranscodeProfile{
		Name:           profile.Name,
		VideoCodec:     string(profile.VideoCodec),
		Encoder:        string(profile.Encoder),
		Container:      string(profile.Container),
		FileExtension:  profile.FileExtension,
		QualityMode:    string(profile.QualityMode),
		QualityValue:   profile.QualityValue,
		AudioPolicy:    string(profile.AudioPolicy),
		AudioCodec:     audioCodec,
		Preset:         profile.Preset,
		PixelFormat:    string(profile.PixelFormat),
		ThreadCount:    int(profile.ThreadCount),
		MaxConcurrency: int(profile.MaxConcurrency),
	}
}

func secretUpdate(update SecretUpdate) domain.SecretUpdate {
	value := ""
	if update.Value != nil {
		value = *update.Value
	}
	return domain.SecretUpdate{Action: domain.SecretAction(update.Action), Value: value}
}

func configurationResponse(configuration domain.Configuration) Configuration {
	return Configuration{
		Version: configuration.Version,
		QBittorrent: QBittorrentConfiguration{
			Url:                           configuration.Settings.QBittorrent.URL,
			Username:                      configuration.Settings.QBittorrent.Username,
			Password:                      secretStatus(configuration, domain.SecretQBittorrentPassword),
			DownloadRateLimitKibPerSecond: configuration.Settings.QBittorrent.DownloadRateLimitKibPerSecond,
			UploadRateLimitKibPerSecond:   configuration.Settings.QBittorrent.UploadRateLimitKibPerSecond,
		},
		Emby: EmbyConfiguration{
			Url:    configuration.Settings.Emby.URL,
			ApiKey: secretStatus(configuration, domain.SecretEmbyAPIKey),
		},
		Tmdb: TMDbConfiguration{
			ApiToken: secretStatus(configuration, domain.SecretTMDbAPIToken),
		},
		NetworkProxy: NetworkProxyConfiguration{
			Enabled: configuration.Settings.NetworkProxy.Enabled,
			Url:     configuration.Settings.NetworkProxy.URL,
		},
		Agent: AgentConfiguration{
			Enabled:                      configuration.Settings.Agent.Enabled,
			Protocol:                     AgentConfigurationProtocol(configuration.Settings.Agent.Protocol),
			BaseUrl:                      configuration.Settings.Agent.BaseURL,
			Model:                        configuration.Settings.Agent.Model,
			ApiKey:                       secretStatus(configuration, domain.SecretAgentAPIKey),
			UseNetworkProxy:              configuration.Settings.Agent.UseNetworkProxy,
			RequestTimeoutSeconds:        configuration.Settings.Agent.RequestTimeoutSeconds,
			RssCoordinateMode:            AgentResolutionMode(configuration.Settings.Agent.RSSCoordinateMode),
			DownloadFileSelectionMode:    AgentResolutionMode(configuration.Settings.Agent.DownloadFileSelectionMode),
			CatalogMatchEnabled:          configuration.Settings.Agent.CatalogMatchEnabled,
			EpisodeMappingEnabled:        configuration.Settings.Agent.EpisodeMappingEnabled,
			AllowAutomaticEpisodeMapping: configuration.Settings.Agent.AllowAutomaticEpisodeMapping,
			SubtitleVideoMatchMode:       AgentResolutionMode(configuration.Settings.Agent.SubtitleVideoMatchMode),
		},
		Paths: PathConfiguration{
			DownloadRoot:     configuration.Settings.Paths.DownloadRoot,
			WorkRoot:         configuration.Settings.Paths.WorkRoot,
			StagingRoot:      configuration.Settings.Paths.StagingRoot,
			AnimeLibraryRoot: configuration.Settings.Paths.EffectiveAnimeLibraryRoot(),
			MovieLibraryRoot: configuration.Settings.Paths.MovieLibraryRoot,
			FfmpegPath:       configuration.Settings.Paths.FFmpegPath,
			FfprobePath:      configuration.Settings.Paths.FFprobePath,
		},
		Transcode: transcodeProfileResponse(configuration.Settings.Transcode),
		Events: EventsConfiguration{
			RetentionDays: configuration.Settings.Events.RetentionDays,
		},
	}
}

func transcodeProfileResponse(profile domain.TranscodeProfile) TranscodeProfileConfiguration {
	response := TranscodeProfileConfiguration{
		Name:           profile.Name,
		VideoCodec:     TranscodeProfileConfigurationVideoCodec(profile.VideoCodec),
		Encoder:        TranscodeProfileConfigurationEncoder(profile.Encoder),
		Container:      TranscodeProfileConfigurationContainer(profile.Container),
		FileExtension:  profile.FileExtension,
		QualityMode:    TranscodeProfileConfigurationQualityMode(profile.QualityMode),
		QualityValue:   profile.QualityValue,
		AudioPolicy:    TranscodeProfileConfigurationAudioPolicy(profile.AudioPolicy),
		Preset:         profile.Preset,
		PixelFormat:    TranscodeProfileConfigurationPixelFormat(profile.PixelFormat),
		ThreadCount:    int32(profile.ThreadCount),
		MaxConcurrency: int32(profile.MaxConcurrency),
	}
	if profile.AudioCodec != "" {
		audioCodec := TranscodeProfileConfigurationAudioCodec(profile.AudioCodec)
		response.AudioCodec = &audioCodec
	}
	return response
}

func secretStatus(configuration domain.Configuration, name string) SecretStatus {
	metadata := configuration.Secrets[name]
	return SecretStatus{Configured: metadata.Configured, Masked: metadata.MaskedHint}
}

func apiErrorFromService(ctx context.Context, err *service.Error) ApiError {
	if err == nil {
		return ApiError{
			Code:      "internal_error",
			Message:   "an internal error occurred",
			Details:   map[string]any{},
			RequestId: middleware.GetReqID(ctx),
		}
	}
	return ApiError{
		Code:      err.Code,
		Message:   err.Message,
		Details:   err.Details,
		RequestId: middleware.GetReqID(ctx),
	}
}
