package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// TMDbSeriesSearcher queries the upstream TMDb API.
type TMDbSeriesSearcher interface {
	SearchTV(ctx context.Context, query string) ([]domain.TMDbSeriesSearchResult, error)
	SearchMovies(ctx context.Context, query string) ([]domain.TMDbMovieSearchResult, error)
}

// TMDbQueryService resolves series search and reads the synced catalog.
type TMDbQueryService struct {
	queries  *db.Queries
	searcher TMDbSeriesSearcher
}

func NewTMDbQueryService(queries *db.Queries, searcher TMDbSeriesSearcher) *TMDbQueryService {
	return &TMDbQueryService{queries: queries, searcher: searcher}
}

func (service *TMDbQueryService) SearchSeries(ctx context.Context, query string) ([]domain.TMDbSeriesSearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, NewError("invalid_query", "the search query must not be blank", ErrInvalidInput, map[string]any{"field": "query"})
	}
	if service.searcher == nil {
		return nil, NewError("tmdb_unavailable", "TMDb is not configured", ErrUnavailable, map[string]any{"dependency": "tmdb"})
	}
	results, err := service.searcher.SearchTV(ctx, query)
	if err != nil {
		return nil, NewError("tmdb_search_failed", "the TMDb search failed", err, map[string]any{"dependency": "tmdb"})
	}
	return results, nil
}

func (service *TMDbQueryService) SearchMovies(ctx context.Context, query string) ([]domain.TMDbMovieSearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, NewError("invalid_query", "the search query must not be blank", ErrInvalidInput, map[string]any{"field": "query"})
	}
	if service.searcher == nil {
		return nil, NewError("tmdb_unavailable", "TMDb is not configured", ErrUnavailable, map[string]any{"dependency": "tmdb"})
	}
	results, err := service.searcher.SearchMovies(ctx, query)
	if err != nil {
		return nil, NewError("tmdb_search_failed", "the TMDb movie search failed", err, map[string]any{"dependency": "tmdb"})
	}
	return results, nil
}

func (service *TMDbQueryService) GetSeriesCatalog(ctx context.Context, tmdbSeriesID int64) (domain.TMDbSeriesCatalogView, error) {
	series, err := service.queries.GetMediaSeriesByTMDbID(ctx, &tmdbSeriesID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TMDbSeriesCatalogView{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.TMDbSeriesCatalogView{}, fmt.Errorf("load media series: %w", err)
	}
	view := domain.TMDbSeriesCatalogView{
		SeriesID:     repository.UUIDFromPG(series.ID),
		TMDbSeriesID: tmdbSeriesID,
		Title:        series.Title,
		Seasons:      []domain.TMDbSeasonCatalogView{},
	}
	if series.OriginalTitle != nil {
		view.OriginalTitle = *series.OriginalTitle
	}

	lastSync, err := service.queries.GetSeriesLastSync(ctx, series.ID)
	if err == nil {
		if value, ok := lastSync.(time.Time); ok {
			view.LastSyncedAt = &value
			view.Synced = true
		}
	}

	seasons, err := service.queries.ListSeriesSeasons(ctx, series.ID)
	if err != nil {
		return domain.TMDbSeriesCatalogView{}, fmt.Errorf("list series seasons: %w", err)
	}
	for _, season := range seasons {
		seasonView := domain.TMDbSeasonCatalogView{
			SeasonNumber: int(season.SeasonNumber),
			EpisodeCount: int(season.EpisodeCount),
			Special:      season.SeasonNumber == 0,
			Episodes:     []domain.TMDbEpisodeCatalogView{},
		}
		if season.Name != nil {
			seasonView.Name = *season.Name
		}
		episodes, err := service.queries.ListSeasonEpisodes(ctx, season.ID)
		if err != nil {
			return domain.TMDbSeriesCatalogView{}, fmt.Errorf("list season episodes: %w", err)
		}
		for _, episode := range episodes {
			episodeView := domain.TMDbEpisodeCatalogView{
				EpisodeNumber: int(episode.EpisodeNumber),
				Title:         episode.Title,
			}
			if episode.AirDate.Valid {
				episodeView.AirDate = episode.AirDate.Time.Format("2006-01-02")
			}
			seasonView.Episodes = append(seasonView.Episodes, episodeView)
		}
		view.Seasons = append(view.Seasons, seasonView)
	}
	return view, nil
}

var _ = time.Now
