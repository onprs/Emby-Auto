package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

type tmdbSearchResolverStub struct {
	configuration domain.Configuration
	token         string
}

func (stub tmdbSearchResolverStub) Load(context.Context) (domain.Configuration, error) {
	return stub.configuration, nil
}

func (stub tmdbSearchResolverStub) ResolveSecret(context.Context, string) (string, error) {
	return stub.token, nil
}

func TestTMDbClientSearcherUsesConfiguredNetworkProxy(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Host != "tmdb.example.test" || request.URL.Path != "/3/search/movie" || request.URL.Query().Get("include_adult") != "false" {
			t.Fatalf("proxied URL = %s", request.URL.String())
		}
		language := request.URL.Query().Get("language")
		if language != "zh-CN" && language != "zh-TW" {
			t.Fatalf("TMDb language = %q", language)
		}
		if request.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"results":[{"id":42,"title":"Proxy Movie","release_date":"2024-01-02"}]}`))
	}))
	defer proxy.Close()

	searcher := NewTMDbClientSearcher(tmdbSearchResolverStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{NetworkProxy: domain.NetworkProxySettings{
			Enabled: true,
			URL:     proxy.URL,
		}}},
		token: "fixture-token",
	}, "http://tmdb.example.test/3")

	results, err := searcher.SearchMovies(context.Background(), "Proxy Movie")
	if err != nil {
		t.Fatalf("SearchMovies() error = %v", err)
	}
	if len(results) != 1 || results[0].TMDbMovieID != 42 || calls.Load() != 2 {
		t.Fatalf("results = %#v, proxy calls = %d", results, calls.Load())
	}
}
