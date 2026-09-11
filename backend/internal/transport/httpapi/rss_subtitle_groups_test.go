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

type rssSubtitleGroupServiceStub struct {
	page         domain.RSSSubtitleGroupPage
	created      domain.RSSSubtitleGroup
	createdInput domain.CreateRSSSubtitleGroup
	updated      domain.RSSSubtitleGroup
	updatedInput domain.UpdateRSSSubtitleGroup
	deletedID    uuid.UUID
	deletedActor uuid.UUID
	createErr    error
	updateErr    error
	deleteErr    error
}

func (stub *rssSubtitleGroupServiceStub) List(context.Context) (domain.RSSSubtitleGroupPage, error) {
	return stub.page, nil
}

func (stub *rssSubtitleGroupServiceStub) Create(_ context.Context, input domain.CreateRSSSubtitleGroup) (domain.RSSSubtitleGroup, error) {
	stub.createdInput = input
	return stub.created, stub.createErr
}

func (stub *rssSubtitleGroupServiceStub) Update(_ context.Context, input domain.UpdateRSSSubtitleGroup) (domain.RSSSubtitleGroup, error) {
	stub.updatedInput = input
	return stub.updated, stub.updateErr
}

func (stub *rssSubtitleGroupServiceStub) Delete(_ context.Context, id, actor uuid.UUID) error {
	stub.deletedID = id
	stub.deletedActor = actor
	return stub.deleteErr
}

func TestListRSSSubtitleGroupsReturnsMaintainedNames(t *testing.T) {
	groupID := uuid.MustParse("10000000-0000-0000-0000-000000000011")
	createdAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	stub := &rssSubtitleGroupServiceStub{page: domain.RSSSubtitleGroupPage{Items: []domain.RSSSubtitleGroup{{
		ID: groupID, Name: "LoliHouse", CreatedAt: createdAt, UpdatedAt: createdAt,
	}}}}
	handler := NewHandler(NewServer(readinessStub{}, WithRSSSubtitleGroups(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/rss/subtitle-groups", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body RSSSubtitleGroupPage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Id != groupID || body.Items[0].Name != "LoliHouse" {
		t.Fatalf("response = %#v", body)
	}
}

func TestCreateRSSSubtitleGroupUsesAuthenticatedActor(t *testing.T) {
	userID := uuid.MustParse("10000000-0000-0000-0000-000000000012")
	groupID := uuid.MustParse("10000000-0000-0000-0000-000000000013")
	stub := &rssSubtitleGroupServiceStub{created: domain.RSSSubtitleGroup{ID: groupID, Name: "LoliHouse"}}
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: userID, Username: "admin"}}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithRSSSubtitleGroups(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/rss/subtitle-groups", strings.NewReader(`{"name":"LoliHouse"}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.createdInput.Name != "LoliHouse" || stub.createdInput.ActorUserID != userID {
		t.Fatalf("create input = %#v", stub.createdInput)
	}
}
