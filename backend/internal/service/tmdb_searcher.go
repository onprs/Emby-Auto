package service

import (
	"context"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/proxyhttp"
	"github.com/onprs/emby-auto/backend/internal/platform/tmdb"
)

const tmdbSearchTimeout = 15 * time.Second

// TMDbClientResolver loads configuration and decrypts the TMDb API token.
type TMDbClientResolver interface {
	Load(ctx context.Context) (domain.Configuration, error)
	ResolveSecret(ctx context.Context, name string) (string, error)
}

// TMDbClientSearcher builds a TMDb client on demand from the saved API token.
type TMDbClientSearcher struct {
	resolver TMDbClientResolver
	baseURL  string
}

func NewTMDbClientSearcher(resolver TMDbClientResolver, baseURL ...string) *TMDbClientSearcher {
	searcher := &TMDbClientSearcher{resolver: resolver}
	if len(baseURL) > 0 {
		searcher.baseURL = baseURL[0]
	}
	return searcher
}

func (searcher *TMDbClientSearcher) SearchTV(ctx context.Context, query string) ([]domain.TMDbSeriesSearchResult, error) {
	client, err := searcher.client(ctx)
	if err != nil {
		return nil, err
	}
	return client.SearchTV(ctx, query)
}

func (searcher *TMDbClientSearcher) SearchMovies(ctx context.Context, query string) ([]domain.TMDbMovieSearchResult, error) {
	client, err := searcher.client(ctx)
	if err != nil {
		return nil, err
	}
	return client.SearchMovies(ctx, query)
}

func (searcher *TMDbClientSearcher) client(ctx context.Context) (*tmdb.Client, error) {
	configuration, err := searcher.resolver.Load(ctx)
	if err != nil {
		return nil, err
	}
	token, err := searcher.resolver.ResolveSecret(ctx, domain.SecretTMDbAPIToken)
	if err != nil {
		return nil, err
	}
	httpClient, err := proxyhttp.NewClient(configuration.Settings.NetworkProxy)
	if err != nil {
		return nil, err
	}
	return tmdb.NewClient(tmdb.ClientOptions{
		BaseURL: searcher.baseURL, APIToken: token, RequestTimeout: tmdbSearchTimeout, HTTPClient: httpClient,
	})
}
