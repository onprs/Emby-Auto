package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type searchServiceStub struct {
	createInput       domain.CreateSearch
	createResult      domain.SearchCommandResult
	createErr         error
	getID             uuid.UUID
	getResult         domain.SearchRun
	getErr            error
	acquisitionInput  domain.CreateSearchAcquisition
	acquisitionResult domain.SearchAcquisitionResult
	acquisitionErr    error
}

func (stub *searchServiceStub) CreateSearch(_ context.Context, input domain.CreateSearch) (domain.SearchCommandResult, error) {
	stub.createInput = input
	return stub.createResult, stub.createErr
}

func (stub *searchServiceStub) GetSearch(_ context.Context, id uuid.UUID) (domain.SearchRun, error) {
	stub.getID = id
	return stub.getResult, stub.getErr
}

func (stub *searchServiceStub) CreateAcquisition(_ context.Context, input domain.CreateSearchAcquisition) (domain.SearchAcquisitionResult, error) {
	stub.acquisitionInput = input
	return stub.acquisitionResult, stub.acquisitionErr
}

func TestCreateSearchUsesAuthenticatedActorAndIdempotencyKey(t *testing.T) {
	actorID := uuid.MustParse("73000000-0000-0000-0000-000000000001")
	searchID := uuid.MustParse("73000000-0000-0000-0000-000000000002")
	operationID := uuid.MustParse("73000000-0000-0000-0000-000000000003")
	now := time.Date(2026, time.July, 21, 21, 0, 0, 0, time.UTC)
	stub := &searchServiceStub{createResult: domain.SearchCommandResult{
		Search:    domain.SearchRun{ID: searchID, Query: "Canonical Show", Status: domain.SearchQueued, CreatedAt: now, UpdatedAt: now},
		Operation: domain.Operation{ID: operationID, Status: "queued"},
	}}
	authentication := &authenticationStub{authenticated: domain.Session{
		User:      domain.AdminUser{ID: actorID, Username: "admin"},
		ExpiresAt: now.Add(time.Hour),
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithSearch(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/searches", strings.NewReader(`{"query":"Canonical Show"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "search-request-1")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.createInput.Query != "Canonical Show" || stub.createInput.IdempotencyKey != "search-request-1" || stub.createInput.ActorUserID != actorID {
		t.Fatalf("create input = %#v", stub.createInput)
	}
	var body SearchCommandAccepted
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Search.Id != searchID || body.OperationId != operationID || body.Status != SearchCommandAcceptedStatusQueued {
		t.Fatalf("response = %#v", body)
	}
}

func TestGetSearchReturnsPersistedCandidates(t *testing.T) {
	searchID := uuid.MustParse("73000000-0000-0000-0000-000000000004")
	candidateID := uuid.MustParse("73000000-0000-0000-0000-000000000005")
	now := time.Date(2026, time.July, 21, 21, 30, 0, 0, time.UTC)
	uri := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	stub := &searchServiceStub{getResult: domain.SearchRun{
		ID: searchID, Query: "Show", Status: domain.SearchCompleted, CreatedAt: now, UpdatedAt: now,
		Candidates: []domain.ReleaseCandidate{
			{ID: candidateID, SearchRunID: searchID, Provider: "dmhy", Title: "Show 01", DownloadURI: uri, CreatedAt: now},
			{ID: uuid.MustParse("73000000-0000-0000-0000-000000000015"), SearchRunID: searchID, Provider: "mikan", Title: "Show 02", CreatedAt: now},
		},
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithSearch(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/searches/"+searchID.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body SearchRun
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if stub.getID != searchID || len(body.Candidates) != 2 || !body.Candidates[0].Downloadable || body.Candidates[0].DownloadUri == nil || *body.Candidates[0].DownloadUri != uri {
		t.Fatalf("response = %#v", body)
	}
	if body.Candidates[1].Downloadable || body.Candidates[1].UnavailableReason == nil || *body.Candidates[1].UnavailableReason != DownloadUriMissing {
		t.Fatalf("unavailable candidate = %#v", body.Candidates[1])
	}
}

func TestCreateAcquisitionForwardsSelectionAndReturnsDownloadOperation(t *testing.T) {
	actorID := uuid.MustParse("73000000-0000-0000-0000-000000000006")
	candidateID := uuid.MustParse("73000000-0000-0000-0000-000000000007")
	acquisitionID := uuid.MustParse("73000000-0000-0000-0000-000000000008")
	downloadID := uuid.MustParse("73000000-0000-0000-0000-000000000009")
	operationID := uuid.MustParse("73000000-0000-0000-0000-000000000010")
	stub := &searchServiceStub{acquisitionResult: domain.SearchAcquisitionResult{
		AcquisitionID: acquisitionID,
		DownloadID:    downloadID,
		Operation:     domain.Operation{ID: operationID, Status: "queued"},
	}}
	authentication := &authenticationStub{authenticated: domain.Session{
		User: actorIDUser(actorID), ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithSearch(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/acquisitions", strings.NewReader(`{
		"candidateId":"`+candidateID.String()+`",
		"mediaType":"episode",
		"tmdbSeriesId":42,
		"seriesTitle":"Canonical Show",
		"sourceSeason":2,
		"sourceEpisode":1,
		"singleEpisode":true
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "acquire-request-1")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	input := stub.acquisitionInput
	if input.CandidateID != candidateID || input.MediaType != domain.TaskMediaEpisode || input.TMDbSeriesID != 42 || input.SourceSeason != 2 || input.SourceEpisode != 1 || !input.SingleEpisode || input.IdempotencyKey != "acquire-request-1" || input.ActorUserID != actorID {
		t.Fatalf("acquisition input = %#v", input)
	}
	var body AcquisitionCommandAccepted
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.AcquisitionId != acquisitionID || body.DownloadId != downloadID || body.OperationId != operationID || body.Status != AcquisitionCommandAcceptedStatusQueued {
		t.Fatalf("response = %#v", body)
	}
}

func TestCreateMovieAcquisitionForwardsCanonicalMovieMetadata(t *testing.T) {
	actorID := uuid.MustParse("73000000-0000-0000-0000-000000000016")
	candidateID := uuid.MustParse("73000000-0000-0000-0000-000000000017")
	stub := &searchServiceStub{acquisitionResult: domain.SearchAcquisitionResult{
		AcquisitionID: uuid.New(), DownloadID: uuid.New(), Operation: domain.Operation{ID: uuid.New(), Status: "queued"},
	}}
	authentication := &authenticationStub{authenticated: domain.Session{User: actorIDUser(actorID), ExpiresAt: time.Now().Add(time.Hour)}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithSearch(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/acquisitions", strings.NewReader(`{
		"candidateId":"`+candidateID.String()+`",
		"mediaType":"movie",
		"tmdbMovieId":12345,
		"movieTitle":"Fixture Movie",
		"releaseYear":2024
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "acquire-movie-1")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	input := stub.acquisitionInput
	if input.CandidateID != candidateID || input.MediaType != domain.TaskMediaMovie || input.TMDbMovieID != 12345 || input.MovieTitle != "Fixture Movie" || input.ReleaseYear != 2024 || input.TMDbSeriesID != 0 || input.MappingProfileID != uuid.Nil || input.ActorUserID != actorID {
		t.Fatalf("movie acquisition input = %#v", input)
	}
}

func actorIDUser(id uuid.UUID) domain.AdminUser {
	return domain.AdminUser{ID: id, Username: "admin"}
}
