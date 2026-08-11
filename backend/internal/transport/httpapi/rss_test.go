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

type rssSubscriptionServiceStub struct {
	createdInput    domain.CreateRSSSubscription
	created         domain.RSSSubscription
	createErr       error
	updatedInput    domain.UpdateRSSSubscription
	updated         domain.RSSSubscription
	updateErr       error
	page            domain.RSSSubscriptionPage
	listErr         error
	listSortBy      *string
	listSortOrder   *string
	manualID        uuid.UUID
	manualKey       string
	manualActor     uuid.UUID
	manualOperation domain.Operation
	manualErr       error
	deleteID        uuid.UUID
	deleteVersion   int32
	deleteKey       string
	deleteImported  bool
	deleteActor     uuid.UUID
	deleteOperation domain.Operation
	deleteErr       error
}

func (stub *rssSubscriptionServiceStub) CreateSubscription(_ context.Context, input domain.CreateRSSSubscription) (domain.RSSSubscription, error) {
	stub.createdInput = input
	return stub.created, stub.createErr
}
func (stub *rssSubscriptionServiceStub) ListSubscriptions(_ context.Context, _ *uuid.UUID, _ int, sortBy, sortOrder *string) (domain.RSSSubscriptionPage, error) {
	stub.listSortBy, stub.listSortOrder = sortBy, sortOrder
	return stub.page, stub.listErr
}
func (stub *rssSubscriptionServiceStub) GetSubscription(context.Context, uuid.UUID) (domain.RSSSubscription, error) {
	return domain.RSSSubscription{}, domain.ErrNotFound
}
func (stub *rssSubscriptionServiceStub) UpdateSubscription(_ context.Context, input domain.UpdateRSSSubscription) (domain.RSSSubscription, error) {
	stub.updatedInput = input
	return stub.updated, stub.updateErr
}
func (stub *rssSubscriptionServiceStub) ArchiveSubscription(context.Context, uuid.UUID, int32, uuid.UUID) error {
	return nil
}
func (stub *rssSubscriptionServiceStub) RequestSubscriptionDeletion(_ context.Context, id uuid.UUID, version int32, key string, deleteImported bool, actor uuid.UUID) (domain.Operation, error) {
	stub.deleteID = id
	stub.deleteVersion = version
	stub.deleteKey = key
	stub.deleteImported = deleteImported
	stub.deleteActor = actor
	return stub.deleteOperation, stub.deleteErr
}
func (stub *rssSubscriptionServiceStub) ScheduleManualPoll(_ context.Context, id uuid.UUID, key string, actor uuid.UUID) (domain.Operation, error) {
	stub.manualID = id
	stub.manualKey = key
	stub.manualActor = actor
	return stub.manualOperation, stub.manualErr
}

func TestListRSSSubscriptionsForwardsColumnSort(t *testing.T) {
	stub := &rssSubscriptionServiceStub{page: domain.RSSSubscriptionPage{Items: []domain.RSSSubscription{}}}
	handler := NewHandler(NewServer(readinessStub{}, WithRSSSubscriptions(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/rss/subscriptions?sortBy=progress&sortOrder=desc", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.listSortBy == nil || *stub.listSortBy != "progress" || stub.listSortOrder == nil || *stub.listSortOrder != "desc" {
		t.Fatalf("sort = %#v/%#v", stub.listSortBy, stub.listSortOrder)
	}
}

func TestCreateRSSSubscriptionUsesAuthenticatedActorAndGeneratedContract(t *testing.T) {
	userID := uuid.MustParse("60000000-0000-0000-0000-000000000001")
	subscriptionID := uuid.MustParse("60000000-0000-0000-0000-000000000002")
	seriesID := uuid.MustParse("60000000-0000-0000-0000-000000000003")
	createdAt := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	stub := &rssSubscriptionServiceStub{created: domain.RSSSubscription{
		ID:                        subscriptionID,
		SeriesID:                  seriesID,
		SeriesTitle:               "Canonical Show",
		TMDbSeriesID:              42,
		Name:                      "Weekly feed",
		FeedURL:                   "https://example.test/feed.xml",
		IncludeKeywords:           []string{"简日", "1080p"},
		ExcludeKeywords:           []string{"720p"},
		Enabled:                   true,
		AutoEpisodeMapping:        true,
		CleanupSourceOnCompletion: true,
		SourceSeason:              2,
		PollInterval:              5 * time.Minute,
		OverallProgress:           0.362,
		TaskCount:                 3,
		CompletedTaskCount:        1,
		AttentionTaskCount:        1,
		Version:                   1,
		CreatedAt:                 createdAt,
		UpdatedAt:                 createdAt,
	}}
	authentication := &authenticationStub{authenticated: domain.Session{
		User:      domain.AdminUser{ID: userID, Username: "admin"},
		ExpiresAt: createdAt.Add(time.Hour),
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithRSSSubscriptions(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/rss/subscriptions", strings.NewReader(`{
		"tmdbSeriesId":42,
		"seriesTitle":"Canonical Show",
		"name":"Weekly feed",
		"feedUrl":"https://example.test/feed.xml",
		"includeKeywords":["简日","1080p"],
		"excludeKeywords":["720p"],
		"enabled":true,
		"autoEpisodeMapping":true,
		"autoReview":true,
		"cleanupSourceOnCompletion":true,
		"sourceSeason":2,
		"pollIntervalSeconds":300
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.createdInput.ActorUserID != userID || stub.createdInput.TMDbSeriesID != 42 || strings.Join(stub.createdInput.IncludeKeywords, ",") != "简日,1080p" || strings.Join(stub.createdInput.ExcludeKeywords, ",") != "720p" || !stub.createdInput.AutoEpisodeMapping || !stub.createdInput.AutoReview || !stub.createdInput.CleanupSourceOnCompletion || stub.createdInput.SourceSeason != 2 || stub.createdInput.PollInterval != 5*time.Minute {
		t.Fatalf("create input = %#v", stub.createdInput)
	}
	var body RSSSubscription
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Id != subscriptionID || body.SeriesId != seriesID || strings.Join(body.IncludeKeywords, ",") != "简日,1080p" || strings.Join(body.ExcludeKeywords, ",") != "720p" || !body.AutoEpisodeMapping || !body.CleanupSourceOnCompletion || body.Version != 1 {
		t.Fatalf("response = %#v", body)
	}
	if body.OverallProgress != 0.362 || body.TaskCount != 3 || body.CompletedTaskCount != 1 || body.AttentionTaskCount != 1 {
		t.Fatalf("response progress = %#v", body)
	}
}

func TestUpdateRSSSubscriptionForwardsAutomaticPolicies(t *testing.T) {
	userID := uuid.MustParse("60000000-0000-0000-0000-000000000020")
	subscriptionID := uuid.MustParse("60000000-0000-0000-0000-000000000021")
	stub := &rssSubscriptionServiceStub{updated: domain.RSSSubscription{
		ID: subscriptionID, IncludeKeywords: []string{"简日"}, ExcludeKeywords: []string{"720p"}, AutoEpisodeMapping: true, AutoReview: true, CleanupSourceOnCompletion: true, SourceSeason: 1, PollInterval: 15 * time.Minute,
		Version: 4, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	authentication := &authenticationStub{authenticated: domain.Session{
		User: domain.AdminUser{ID: userID, Username: "admin"}, ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithRSSSubscriptions(stub)))
	request := httptest.NewRequest(http.MethodPut, "/api/v1/rss/subscriptions/"+subscriptionID.String(), strings.NewReader(`{
		"expectedVersion":3,
		"name":"Weekly feed",
		"feedUrl":"https://example.test/feed.xml",
		"includeKeywords":["简日"],
		"excludeKeywords":["720p"],
		"enabled":true,
		"autoEpisodeMapping":true,
		"autoReview":true,
		"cleanupSourceOnCompletion":true,
		"sourceSeason":1,
		"pollIntervalSeconds":900
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !stub.updatedInput.AutoEpisodeMapping || !stub.updatedInput.AutoReview || strings.Join(stub.updatedInput.IncludeKeywords, ",") != "简日" || strings.Join(stub.updatedInput.ExcludeKeywords, ",") != "720p" || !stub.updatedInput.CleanupSourceOnCompletion || stub.updatedInput.ExpectedVersion != 3 || stub.updatedInput.ActorUserID != userID {
		t.Fatalf("update input = %#v", stub.updatedInput)
	}
	var body RSSSubscription
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.AutoEpisodeMapping || !body.AutoReview || strings.Join(body.IncludeKeywords, ",") != "简日" || strings.Join(body.ExcludeKeywords, ",") != "720p" || !body.CleanupSourceOnCompletion || body.Version != 4 {
		t.Fatalf("response = %#v", body)
	}
}

func TestPollRSSSubscriptionRequiresAndForwardsIdempotencyKey(t *testing.T) {
	userID := uuid.MustParse("60000000-0000-0000-0000-000000000004")
	subscriptionID := uuid.MustParse("60000000-0000-0000-0000-000000000005")
	operationID := uuid.MustParse("60000000-0000-0000-0000-000000000006")
	stub := &rssSubscriptionServiceStub{manualOperation: domain.Operation{ID: operationID, Status: "queued"}}
	authentication := &authenticationStub{authenticated: domain.Session{
		User:      domain.AdminUser{ID: userID, Username: "admin"},
		ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithRSSSubscriptions(stub)))

	missingRequest := httptest.NewRequest(http.MethodPost, "/api/v1/rss/subscriptions/"+subscriptionID.String()+"/poll", nil)
	missingRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missingRequest)
	if missingResponse.Code != http.StatusBadRequest {
		t.Fatalf("missing key status = %d, want 400", missingResponse.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/rss/subscriptions/"+subscriptionID.String()+"/poll", nil)
	request.Header.Set("Idempotency-Key", "manual-poll-7")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.manualID != subscriptionID || stub.manualKey != "manual-poll-7" || stub.manualActor != userID {
		t.Fatalf("manual poll input = id %s key %q actor %s", stub.manualID, stub.manualKey, stub.manualActor)
	}
	var body CommandAccepted
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.OperationId != operationID || body.Status != CommandAcceptedStatusQueued {
		t.Fatalf("response = %#v", body)
	}
}

func TestDeleteRSSSubscriptionSchedulesCascadeOperation(t *testing.T) {
	userID := uuid.MustParse("60000000-0000-0000-0000-000000000010")
	subscriptionID := uuid.MustParse("60000000-0000-0000-0000-000000000011")
	operationID := uuid.MustParse("60000000-0000-0000-0000-000000000012")
	stub := &rssSubscriptionServiceStub{deleteOperation: domain.Operation{ID: operationID, Status: "queued"}}
	authentication := &authenticationStub{authenticated: domain.Session{
		User:      domain.AdminUser{ID: userID, Username: "admin"},
		ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithRSSSubscriptions(stub)))

	request := httptest.NewRequest(http.MethodDelete, "/api/v1/rss/subscriptions/"+subscriptionID.String()+"?expectedVersion=3&deleteImported=true", nil)
	request.Header.Set("Idempotency-Key", "rss-delete-9")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.deleteID != subscriptionID || stub.deleteVersion != 3 || stub.deleteKey != "rss-delete-9" || !stub.deleteImported || stub.deleteActor != userID {
		t.Fatalf("delete input = id %s version %d key %q delete imported %t actor %s", stub.deleteID, stub.deleteVersion, stub.deleteKey, stub.deleteImported, stub.deleteActor)
	}
	var body CommandAccepted
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.OperationId != operationID || body.Status != CommandAcceptedStatusQueued {
		t.Fatalf("response = %#v", body)
	}
}
