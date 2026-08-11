package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

type tmdbSearchServiceStub struct {
	movieQuery  string
	movieResult []domain.TMDbMovieSearchResult
	movieErr    error
}

func (stub *tmdbSearchServiceStub) SearchSeries(context.Context, string) ([]domain.TMDbSeriesSearchResult, error) {
	return nil, nil
}

func (stub *tmdbSearchServiceStub) SearchMovies(_ context.Context, query string) ([]domain.TMDbMovieSearchResult, error) {
	stub.movieQuery = query
	return stub.movieResult, stub.movieErr
}

func (stub *tmdbSearchServiceStub) GetSeriesCatalog(context.Context, int64) (domain.TMDbSeriesCatalogView, error) {
	return domain.TMDbSeriesCatalogView{}, nil
}

type connectivityServiceStub struct {
	request domain.ConnectivityTestRequest
}

func (stub *connectivityServiceStub) Test(_ context.Context, request domain.ConnectivityTestRequest) (domain.ConnectivityTestResult, error) {
	stub.request = request
	return domain.ConnectivityTestResult{Target: request.Target, Success: true, Code: "ok", CheckedAt: time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)}, nil
}

func TestNetworkProxyConnectivityMapsCandidateSettings(t *testing.T) {
	stub := &connectivityServiceStub{}
	server := NewServer(readinessStub{}, WithConnectivity(stub))
	response, err := server.TestConnectivity(context.Background(), TestConnectivityRequestObject{Body: &ConnectivityTestRequest{
		Target: ConnectivityTestRequestTargetNetworkProxy,
		NetworkProxy: &NetworkProxyConfiguration{
			Enabled: true,
			Url:     "http://127.0.0.1:7897",
		},
	}})
	if err != nil {
		t.Fatalf("TestConnectivity() error = %v", err)
	}
	if _, ok := response.(TestConnectivity200JSONResponse); !ok {
		t.Fatalf("response = %#v", response)
	}
	if stub.request.Target != "network_proxy" || stub.request.NetworkProxy == nil || !stub.request.NetworkProxy.Enabled || stub.request.NetworkProxy.URL != "http://127.0.0.1:7897" {
		t.Fatalf("mapped request = %#v", stub.request)
	}
}

func TestAgentConnectivityMapsCandidateWithoutPersistingSecret(t *testing.T) {
	stub := &connectivityServiceStub{}
	candidateKey := "candidate-agent-key"
	response, err := NewServer(readinessStub{}, WithConnectivity(stub)).TestConnectivity(
		context.Background(),
		TestConnectivityRequestObject{Body: &ConnectivityTestRequest{
			Target: ConnectivityTestRequestTargetAgent,
			Agent: &AgentConnectivityTestConfiguration{
				Protocol: AgentConnectivityTestConfigurationProtocolOpenaiChatCompletions,
				BaseUrl:  "https://agent.example/v1", Model: "fixture-model", UseNetworkProxy: true,
				ApiKey: SecretUpdate{Action: Set, Value: &candidateKey},
			},
		}},
	)
	if err != nil {
		t.Fatalf("TestConnectivity() error = %v", err)
	}
	if _, ok := response.(TestConnectivity200JSONResponse); !ok {
		t.Fatalf("response = %#v", response)
	}
	if stub.request.Agent == nil || stub.request.Agent.APIKey == nil || *stub.request.Agent.APIKey != candidateKey || stub.request.Agent.Model != "fixture-model" || !stub.request.Agent.UseNetworkProxy {
		t.Fatalf("mapped Agent request = %#v", stub.request.Agent)
	}
}

func TestSearchTMDbMoviesMapsCanonicalMetadata(t *testing.T) {
	stub := &tmdbSearchServiceStub{movieResult: []domain.TMDbMovieSearchResult{{
		TMDbMovieID: 12345, Title: "Fixture Movie", OriginalTitle: "Original Movie",
		Overview: "Overview", ReleaseDate: "2024-03-08", ReleaseYear: 2024,
	}}}
	handler := NewHandler(NewServer(readinessStub{}, WithTMDbSearch(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tmdb/movies/search?query=Fixture%20Movie", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body TMDbMovieSearchResultPage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if stub.movieQuery != "Fixture Movie" || len(body.Items) != 1 {
		t.Fatalf("query/result = %q/%#v", stub.movieQuery, body)
	}
	item := body.Items[0]
	if item.TmdbMovieId != 12345 || item.Title != "Fixture Movie" || item.OriginalTitle == nil || *item.OriginalTitle != "Original Movie" || item.ReleaseYear == nil || *item.ReleaseYear != 2024 || item.ReleaseDate == nil || item.ReleaseDate.String() != "2024-03-08" {
		t.Fatalf("movie search response = %#v", item)
	}
}
