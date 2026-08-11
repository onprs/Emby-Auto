package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type readinessStub struct {
	err error
}

func (stub readinessStub) Ping(context.Context) error {
	return stub.err
}

func TestHealthRoutesDistinguishLiveAndReady(t *testing.T) {
	checkedAt := time.Date(2026, time.July, 21, 10, 0, 0, 0, time.UTC)
	server := NewServer(readinessStub{})
	server.now = func() time.Time { return checkedAt }
	handler := NewHandler(server)

	for _, test := range []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{name: "live", path: "/api/v1/health/live", wantStatus: http.StatusOK, wantBody: `{"checkedAt":"2026-07-21T10:00:00Z","status":"live"}`},
		{name: "ready", path: "/api/v1/health/ready", wantStatus: http.StatusOK, wantBody: `{"checkedAt":"2026-07-21T10:00:00Z","status":"ready"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("X-Request-Id", "request-123")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if got := response.Header().Get("X-Request-Id"); got != "request-123" {
				t.Fatalf("X-Request-Id = %q, want request-123", got)
			}
			if got := response.Body.String(); got != test.wantBody+"\n" {
				t.Fatalf("body = %q, want %q", got, test.wantBody+"\n")
			}
		})
	}
}

func TestUnknownRouteReturnsStructuredError(t *testing.T) {
	handler := NewHandler(NewServer(readinessStub{}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil)
	request.Header.Set("X-Request-Id", "request-404")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}

	var body struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Details   map[string]any `json:"details"`
		RequestID string         `json:"requestId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "not_found" || body.Message != "the requested resource was not found" {
		t.Fatalf("error = %q %q, want structured not_found error", body.Code, body.Message)
	}
	if len(body.Details) != 0 {
		t.Fatalf("details = %#v, want empty object", body.Details)
	}
	if body.RequestID != "request-404" {
		t.Fatalf("requestId = %q, want request-404", body.RequestID)
	}
}

func TestReadyReturnsStructuredErrorWhenDatabaseIsUnavailable(t *testing.T) {
	server := NewServer(readinessStub{err: errors.New("connection refused")})
	handler := NewHandler(server)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	request.Header.Set("X-Request-Id", "request-456")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}

	var body struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Details   map[string]any `json:"details"`
		RequestID string         `json:"requestId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "service_not_ready" || body.Message != "a required dependency is unavailable" {
		t.Fatalf("error = %q %q, want service_not_ready dependency message", body.Code, body.Message)
	}
	if body.Details["dependency"] != "postgresql" {
		t.Fatalf("dependency = %#v, want postgresql", body.Details["dependency"])
	}
	if body.RequestID != "request-456" {
		t.Fatalf("requestId = %q, want request-456", body.RequestID)
	}
}
