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

type agentResolutionServiceStub struct {
	resolution domain.AgentResolution
	getCalls   int
	listCalls  int
	err        error
}

func (stub *agentResolutionServiceStub) Get(context.Context, uuid.UUID) (domain.AgentResolution, error) {
	stub.getCalls++
	return stub.resolution, stub.err
}

func (stub *agentResolutionServiceStub) List(context.Context, *uuid.UUID, int, *string, *string, *string, *uuid.UUID) (domain.AgentResolutionPage, error) {
	stub.listCalls++
	return domain.AgentResolutionPage{Items: []domain.AgentResolution{stub.resolution}}, stub.err
}

func fixtureAgentResolution(id, operationID, resourceID uuid.UUID) domain.AgentResolution {
	now := time.Now()
	return domain.AgentResolution{
		ID: id, OperationID: operationID, Version: 2, Capability: domain.AgentCapabilityDownloadFileResolution,
		ResourceType: "download", ResourceID: resourceID, Trigger: "user", Status: domain.AgentResolutionReviewRequired,
		Model: "fixture-model", PromptVersion: "download-file-resolution-v1", ToolsetVersion: "agent-tools-v1",
		Validation: domain.AgentProposalValidation{Verdict: domain.AgentValidationReviewRequired, ReasonCodes: []string{"review_required"}},
		CreatedAt:  now, UpdatedAt: now,
	}
}

func TestQueuedAgentResolutionOmitsValidationAndCommandActions(t *testing.T) {
	resolutionID := uuid.MustParse("44000000-0000-0000-0000-000000000022")
	operationID := uuid.MustParse("44000000-0000-0000-0000-000000000023")
	resourceID := uuid.MustParse("44000000-0000-0000-0000-000000000024")
	authentication, _ := authenticatedServer()
	resolution := fixtureAgentResolution(resolutionID, operationID, resourceID)
	resolution.Status = domain.AgentResolutionQueued
	resolution.Validation = domain.AgentProposalValidation{}
	stub := &agentResolutionServiceStub{resolution: resolution}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithAgentResolutions(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/resolutions/"+resolutionID.String(), nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"validation"`) || strings.Contains(response.Body.String(), `"actions"`) {
		t.Fatalf("read-only queued response contains command metadata: %s", response.Body.String())
	}
	if stub.getCalls != 1 {
		t.Fatalf("Get calls = %d, want 1", stub.getCalls)
	}
}

func TestRemovedAgentMutationRoutesReturnStructuredNotFoundWithoutServiceCalls(t *testing.T) {
	resourceID := uuid.MustParse("44000000-0000-0000-0000-000000000031")
	authentication, _ := authenticatedServer()
	stub := &agentResolutionServiceStub{}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithAgentResolutions(stub)))
	paths := []string{
		"/api/v1/agent/resolutions/" + resourceID.String() + "/accept",
		"/api/v1/agent/resolutions/" + resourceID.String() + "/reject",
		"/api/v1/agent/resolutions/" + resourceID.String() + "/retry",
		"/api/v1/rss/entries/" + resourceID.String() + "/agent-resolution",
		"/api/v1/rss/adjudication-batches/" + resourceID.String() + "/agent-resolution",
		"/api/v1/downloads/" + resourceID.String() + "/agent-resolution",
		"/api/v1/acquisitions/" + resourceID.String() + "/episode-mapping/agent-resolution",
		"/api/v1/acquisitions/" + resourceID.String() + "/catalog/agent-resolution",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expectedVersion":2}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "removed-command")
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var apiError ApiError
			if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if apiError.Code != "not_found" {
				t.Fatalf("error = %#v", apiError)
			}
		})
	}
	if stub.getCalls != 0 || stub.listCalls != 0 {
		t.Fatalf("removed commands reached service: get=%d list=%d", stub.getCalls, stub.listCalls)
	}
}

func TestHistoricalAgentReviewStatesRemainReadableWithoutActions(t *testing.T) {
	resolutionID := uuid.MustParse("44000000-0000-0000-0000-000000000041")
	operationID := uuid.MustParse("44000000-0000-0000-0000-000000000042")
	resourceID := uuid.MustParse("44000000-0000-0000-0000-000000000043")
	authentication, _ := authenticatedServer()

	for _, status := range []domain.AgentResolutionStatus{domain.AgentResolutionReviewRequired, domain.AgentResolutionRejected} {
		t.Run(string(status), func(t *testing.T) {
			resolution := fixtureAgentResolution(resolutionID, operationID, resourceID)
			resolution.Status = status
			stub := &agentResolutionServiceStub{resolution: resolution}
			handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithAgentResolutions(stub)))
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/resolutions/"+resolutionID.String(), nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), `"actions"`) {
				t.Fatalf("historical read response contains actions: %s", response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `"status":"`+string(status)+`"`) {
				t.Fatalf("historical status missing from response: %s", response.Body.String())
			}
		})
	}
}
