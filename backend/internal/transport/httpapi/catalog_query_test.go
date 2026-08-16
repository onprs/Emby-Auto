package httpapi

import (
	"bytes"
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
	calls   int
	request domain.ConnectivityTestRequest
}

func (stub *connectivityServiceStub) Test(_ context.Context, request domain.ConnectivityTestRequest) (domain.ConnectivityTestResult, error) {
	stub.calls++
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

func TestQBittorrentConnectivityCompatibilityLimitsMatchRequestContract(t *testing.T) {
	document, err := GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger() error = %v", err)
	}
	path := document.Paths.Value("/api/v1/config/test")
	if path == nil || path.Post == nil || path.Post.RequestBody == nil || path.Post.RequestBody.Value == nil {
		t.Fatal("POST /api/v1/config/test request contract is missing")
	}
	mediaType := path.Post.RequestBody.Value.Content.Get("application/json")
	if mediaType == nil || mediaType.Schema == nil || mediaType.Schema.Value == nil {
		t.Fatal("POST /api/v1/config/test JSON request schema is missing")
	}

	for _, test := range []struct {
		name                string
		compatibilityFields map[string]int64
		wantSchemaValid     bool
		wantStatus          int
		wantCalls           int
	}{
		{
			name:            "omitted compatibility fields are accepted",
			wantSchemaValid: true, wantStatus: http.StatusOK, wantCalls: 1,
		},
		{
			name: "explicit zero compatibility fields are accepted",
			compatibilityFields: map[string]int64{
				"downloadRateLimitKibPerSecond": 0,
				"uploadRateLimitKibPerSecond":   0,
			},
			wantSchemaValid: true, wantStatus: http.StatusOK, wantCalls: 1,
		},
		{
			name: "legacy value above the update boundary is accepted",
			compatibilityFields: map[string]int64{
				"downloadRateLimitKibPerSecond": 2097152,
			},
			wantSchemaValid: true, wantStatus: http.StatusOK, wantCalls: 1,
		},
		{
			name: "legacy public maximum is accepted",
			compatibilityFields: map[string]int64{
				"uploadRateLimitKibPerSecond": 2147483647,
			},
			wantSchemaValid: true, wantStatus: http.StatusOK, wantCalls: 1,
		},
		{
			name: "negative compatibility value is rejected",
			compatibilityFields: map[string]int64{
				"downloadRateLimitKibPerSecond": -1,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "value above the legacy public boundary is rejected",
			compatibilityFields: map[string]int64{
				"uploadRateLimitKibPerSecond": 2147483648,
			},
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			qbittorrent := map[string]any{
				"url": "http://qb.test", "username": "downloader",
				"password": map[string]any{"action": "set", "value": "fixture-password"},
			}
			for field, value := range test.compatibilityFields {
				qbittorrent[field] = value
			}
			payload := map[string]any{
				"target":      "qbittorrent",
				"qBittorrent": qbittorrent,
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("encode connectivity request: %v", err)
			}
			var schemaPayload any
			if err := json.Unmarshal(encoded, &schemaPayload); err != nil {
				t.Fatalf("decode connectivity request for schema validation: %v", err)
			}
			schemaErr := mediaType.Schema.Value.VisitJSON(schemaPayload)
			if got := schemaErr == nil; got != test.wantSchemaValid {
				t.Errorf("request schema valid = %t, want %t, error = %v", got, test.wantSchemaValid, schemaErr)
			}

			stub := &connectivityServiceStub{}
			handler := NewHandler(NewServer(readinessStub{}, WithConnectivity(stub)))
			request := httptest.NewRequest(http.MethodPost, "/api/v1/config/test", bytes.NewReader(encoded))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("runtime status = %d, want %d, body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if stub.calls != test.wantCalls {
				t.Fatalf("connectivity service calls = %d, want %d", stub.calls, test.wantCalls)
			}
			if test.wantCalls == 1 {
				if stub.request.Target != "qbittorrent" || stub.request.QBittorrent == nil ||
					stub.request.QBittorrent.URL != "http://qb.test" ||
					stub.request.QBittorrent.Username != "downloader" || stub.request.QBittorrent.Password == nil ||
					*stub.request.QBittorrent.Password != "fixture-password" {
					t.Fatalf("mapped qBittorrent connectivity request = %#v", stub.request.QBittorrent)
				}
				return
			}
			var apiError ApiError
			if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
				t.Fatalf("decode runtime rejection: %v", err)
			}
			if apiError.Code != "invalid_request" {
				t.Fatalf("runtime error = %#v, want invalid_request", apiError)
			}
		})
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
