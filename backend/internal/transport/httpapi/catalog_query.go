package httpapi

import (
	"context"
	"errors"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

const maxQBittorrentConnectivityRateLimitKibPerSecond int64 = 2147483647

func parseDate(value string) (openapi_types.Date, error) {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return openapi_types.Date{}, err
	}
	return openapi_types.Date{Time: parsed}, nil
}

func (server *Server) SearchTMDbMovies(ctx context.Context, request SearchTMDbMoviesRequestObject) (SearchTMDbMoviesResponseObject, error) {
	if server.tmdbSearch == nil {
		return SearchTMDbMovies503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tmdb")}, nil
	}
	results, err := server.tmdbSearch.SearchMovies(ctx, request.Params.Query)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return SearchTMDbMovies400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrUnavailable):
		return SearchTMDbMovies503JSONResponse{ServiceUnavailableJSONResponse: ServiceUnavailableJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return SearchTMDbMovies503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tmdb")}, nil
	default:
		response := TMDbMovieSearchResultPage{Items: make([]TMDbMovieSearchResult, 0, len(results))}
		for _, item := range results {
			entry := TMDbMovieSearchResult{TmdbMovieId: item.TMDbMovieID, Title: item.Title}
			optionalString(&entry.OriginalTitle, item.OriginalTitle)
			optionalString(&entry.Overview, item.Overview)
			if item.ReleaseDate != "" {
				if parsed, parseErr := parseDate(item.ReleaseDate); parseErr == nil {
					entry.ReleaseDate = &parsed
				}
			}
			if item.ReleaseYear > 0 {
				year := int32(item.ReleaseYear)
				entry.ReleaseYear = &year
			}
			response.Items = append(response.Items, entry)
		}
		return SearchTMDbMovies200JSONResponse(response), nil
	}
}

func (server *Server) SearchTMDbSeries(ctx context.Context, request SearchTMDbSeriesRequestObject) (SearchTMDbSeriesResponseObject, error) {
	if server.tmdbSearch == nil {
		return SearchTMDbSeries503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tmdb")}, nil
	}
	results, err := server.tmdbSearch.SearchSeries(ctx, request.Params.Query)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return SearchTMDbSeries400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrUnavailable):
		return SearchTMDbSeries503JSONResponse{ServiceUnavailableJSONResponse: ServiceUnavailableJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return SearchTMDbSeries503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tmdb")}, nil
	default:
		response := TMDbSeriesSearchResultPage{Items: make([]TMDbSeriesSearchResult, 0, len(results))}
		for _, item := range results {
			entry := TMDbSeriesSearchResult{TmdbSeriesId: item.TMDbSeriesID, Name: item.Name}
			if item.OriginalName != "" {
				entry.OriginalName = &item.OriginalName
			}
			if item.FirstAirDate != "" {
				if parsed, parseErr := parseDate(item.FirstAirDate); parseErr == nil {
					entry.FirstAirDate = &parsed
				}
			}
			if item.Overview != "" {
				entry.Overview = &item.Overview
			}
			response.Items = append(response.Items, entry)
		}
		return SearchTMDbSeries200JSONResponse(response), nil
	}
}

func (server *Server) GetTMDbSeriesCatalog(ctx context.Context, request GetTMDbSeriesCatalogRequestObject) (GetTMDbSeriesCatalogResponseObject, error) {
	if server.tmdbSearch == nil {
		return GetTMDbSeriesCatalog503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "tmdb")}, nil
	}
	view, err := server.tmdbSearch.GetSeriesCatalog(ctx, request.TmdbSeriesId)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetTMDbSeriesCatalog404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the TMDb series catalog was not found")}, nil
	case err != nil:
		return GetTMDbSeriesCatalog503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return GetTMDbSeriesCatalog200JSONResponse(tmdbCatalogResponse(view)), nil
	}
}

func (server *Server) TestConnectivity(ctx context.Context, request TestConnectivityRequestObject) (TestConnectivityResponseObject, error) {
	if server.connectivity == nil {
		return TestConnectivity503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "connectivity")}, nil
	}
	if request.Body == nil {
		return TestConnectivity400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	domainRequest := domain.ConnectivityTestRequest{Target: string(request.Body.Target)}
	if request.Body.QBittorrent != nil {
		if !validQBittorrentConnectivityRateLimits(*request.Body.QBittorrent) {
			return TestConnectivity400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "the qBittorrent connectivity test request is invalid")}, nil
		}
		config := &domain.QBittorrentTestConfig{URL: request.Body.QBittorrent.Url, Username: request.Body.QBittorrent.Username}
		if request.Body.QBittorrent.Password.Action == Set && request.Body.QBittorrent.Password.Value != nil {
			config.Password = request.Body.QBittorrent.Password.Value
		}
		domainRequest.QBittorrent = config
	}
	if request.Body.Emby != nil {
		config := &domain.EmbyTestConfig{URL: request.Body.Emby.Url}
		if request.Body.Emby.ApiKey.Action == Set && request.Body.Emby.ApiKey.Value != nil {
			config.APIKey = request.Body.Emby.ApiKey.Value
		}
		domainRequest.Emby = config
	}
	if request.Body.Tmdb != nil {
		config := &domain.TMDbTestConfig{}
		if request.Body.Tmdb.ApiToken.Action == Set && request.Body.Tmdb.ApiToken.Value != nil {
			config.APIToken = request.Body.Tmdb.ApiToken.Value
		}
		domainRequest.TMDb = config
	}
	if request.Body.NetworkProxy != nil {
		domainRequest.NetworkProxy = &domain.NetworkProxySettings{
			Enabled: request.Body.NetworkProxy.Enabled,
			URL:     request.Body.NetworkProxy.Url,
		}
	}
	if request.Body.Agent != nil {
		config := &domain.AgentTestConfig{
			Protocol:        string(request.Body.Agent.Protocol),
			BaseURL:         request.Body.Agent.BaseUrl,
			Model:           request.Body.Agent.Model,
			UseNetworkProxy: request.Body.Agent.UseNetworkProxy,
		}
		switch request.Body.Agent.ApiKey.Action {
		case Set:
			config.APIKey = request.Body.Agent.ApiKey.Value
		case Clear:
			empty := ""
			config.APIKey = &empty
		}
		domainRequest.Agent = config
	}
	result, err := server.connectivity.Test(ctx, domainRequest)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return TestConnectivity400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return TestConnectivity503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "connectivity")}, nil
	default:
		return TestConnectivity200JSONResponse(ConnectivityTestResult{
			Target: ConnectivityTestResultTarget(result.Target), Success: result.Success,
			Code: stringPtr(result.Code), Message: stringPtr(result.Message), CheckedAt: result.CheckedAt,
		}), nil
	}
}

func validQBittorrentConnectivityRateLimits(configuration QBittorrentConnectivityTestConfiguration) bool {
	for _, value := range []*int64{
		configuration.DownloadRateLimitKibPerSecond,
		configuration.UploadRateLimitKibPerSecond,
	} {
		if value != nil && (*value < 0 || *value > maxQBittorrentConnectivityRateLimitKibPerSecond) {
			return false
		}
	}
	return true
}

func tmdbCatalogResponse(view domain.TMDbSeriesCatalogView) TMDbSeriesCatalog {
	response := TMDbSeriesCatalog{
		SeriesId: view.SeriesID, TmdbSeriesId: view.TMDbSeriesID, Title: view.Title,
		Synced: view.Synced, Seasons: make([]TMDbSeasonCatalog, 0, len(view.Seasons)),
	}
	if view.OriginalTitle != "" {
		response.OriginalTitle = &view.OriginalTitle
	}
	response.LastSyncedAt = view.LastSyncedAt
	for _, season := range view.Seasons {
		seasonResponse := TMDbSeasonCatalog{
			SeasonNumber: int32(season.SeasonNumber), Name: season.Name, EpisodeCount: int32(season.EpisodeCount),
			Special: season.Special, Episodes: make([]TMDbEpisodeCatalog, 0, len(season.Episodes)),
		}
		for _, episode := range season.Episodes {
			episodeResponse := TMDbEpisodeCatalog{EpisodeNumber: int32(episode.EpisodeNumber), Title: episode.Title}
			if episode.AirDate != "" {
				if parsed, parseErr := parseDate(episode.AirDate); parseErr == nil {
					episodeResponse.AirDate = &parsed
				}
			}
			seasonResponse.Episodes = append(seasonResponse.Episodes, episodeResponse)
		}
		response.Seasons = append(response.Seasons, seasonResponse)
	}
	return response
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
