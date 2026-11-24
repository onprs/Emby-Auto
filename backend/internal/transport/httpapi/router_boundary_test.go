package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestRequestBodyLimitRejectsDeclaredOversize(t *testing.T) {
	handler := NewHandler(NewServer(readinessStub{}))
	body := strings.Repeat("a", int(maxRequestBodyBytes)+1)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup/initialize", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-Id", "oversize-request")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var apiError ApiError
	if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if apiError.Code != "request_too_large" || apiError.RequestId != "oversize-request" {
		t.Fatalf("error = %#v, want request_too_large with request id", apiError)
	}
}

func TestRequestBodyLimitRejectsStreamedOversize(t *testing.T) {
	handler := NewHandler(NewServer(readinessStub{}))
	// ContentLength 未知（等价于分块传输）时由解码阶段截断并返回 413。
	raw := []byte(`{"administratorUsername":"` + strings.Repeat("a", int(maxRequestBodyBytes)) + `"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup/initialize", bytes.NewReader(raw))
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, http.StatusRequestEntityTooLarge, response.Body.String())
	}
	var apiError ApiError
	if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if apiError.Code != "request_too_large" {
		t.Fatalf("code = %q, want request_too_large", apiError.Code)
	}
}

func TestRequestIDDefaultsToOpaqueRandomValue(t *testing.T) {
	handler := NewHandler(NewServer(readinessStub{}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	id := response.Header().Get("X-Request-Id")
	if len(id) != 2*requestIDBytes {
		t.Fatalf("X-Request-Id = %q, want %d characters", id, 2*requestIDBytes)
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			t.Fatalf("X-Request-Id = %q, want lowercase hexadecimal only", id)
		}
	}
}

func TestRequestIDUsesValidClientValueOnly(t *testing.T) {
	handler := NewHandler(NewServer(readinessStub{}))

	for _, test := range []struct {
		name     string
		clientID string
		wantEcho bool
	}{
		{name: "valid client id", clientID: "trace-01HZX3K9", wantEcho: true},
		{name: "contains separator", clientID: "host/0000000001", wantEcho: false},
		{name: "too short", clientID: "short", wantEcho: false},
		{name: "too long", clientID: strings.Repeat("a", 65), wantEcho: false},
		{name: "illegal characters", clientID: "trace id with spaces", wantEcho: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
			request.Header.Set("X-Request-Id", test.clientID)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			got := response.Header().Get("X-Request-Id")
			if test.wantEcho && got != test.clientID {
				t.Fatalf("X-Request-Id = %q, want echoed %q", got, test.clientID)
			}
			if !test.wantEcho {
				if got == test.clientID {
					t.Fatalf("X-Request-Id = %q, want replaced client value", got)
				}
				if len(got) != 2*requestIDBytes {
					t.Fatalf("X-Request-Id = %q, want generated %d character id", got, 2*requestIDBytes)
				}
			}
		})
	}
}

func TestRecovererLogsPanicWithoutRequestSecrets(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	handler := requestID(recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("fixture panic")
	})))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?token=secret-query", nil)
	request.Header.Set("X-Request-Id", "panic-request-1")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-secret"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	entry := logged.String()
	for _, want := range []string{`"level":"ERROR"`, `"request_id":"panic-request-1"`, `"path":"/api/v1/tasks"`, "fixture panic", `"stack":"`} {
		if !strings.Contains(entry, want) {
			t.Fatalf("log entry %s does not contain %s", entry, want)
		}
	}
	for _, secret := range []string{"session-secret", "secret-query"} {
		if strings.Contains(entry, secret) {
			t.Fatalf("log entry leaks request data %q: %s", secret, entry)
		}
	}
}

func TestParameterBindingFailureUsesAPIError(t *testing.T) {
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: uuid.New(), Username: "admin"}}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?limit=not-a-number", nil)
	request.Header.Set("X-Request-Id", "binding-request")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var apiError ApiError
	if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if apiError.Code != "invalid_request" || apiError.RequestId != "binding-request" {
		t.Fatalf("error = %#v, want invalid_request with request id", apiError)
	}
	if strings.Contains(response.Body.String(), "not-a-number") {
		t.Fatalf("error body echoes client input: %s", response.Body.String())
	}
}

func TestUpdateConfigurationRequiresContractFields(t *testing.T) {
	userID := uuid.MustParse("10000000-0000-0000-0000-000000000007")
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: userID, Username: "admin"}}}

	for _, test := range []struct {
		name        string
		remove      []string
		wantStatus  int
		wantMissing string
	}{
		{name: "complete body", wantStatus: http.StatusOK},
		{name: "omitted events section", remove: []string{"events"}, wantStatus: http.StatusOK},
		{name: "omitted section", remove: []string{"transcode"}, wantStatus: http.StatusBadRequest, wantMissing: "transcode"},
		{name: "omitted qBittorrent url", remove: []string{"qBittorrent.url"}, wantStatus: http.StatusBadRequest, wantMissing: "qBittorrent.url"},
		{name: "omitted qBittorrent password action", remove: []string{"qBittorrent.password.action"}, wantStatus: http.StatusBadRequest, wantMissing: "qBittorrent.password.action"},
		{name: "omitted agent mode", remove: []string{"agent.subtitleVideoMatchMode"}, wantStatus: http.StatusBadRequest, wantMissing: "agent.subtitleVideoMatchMode"},
		{name: "omitted ffmpeg path", remove: []string{"paths.ffmpegPath"}, wantStatus: http.StatusBadRequest, wantMissing: "paths.ffmpegPath"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration := &runtimeConfigurationStub{configuration: domain.Configuration{Version: 9, Secrets: map[string]domain.SecretMetadata{}}}
			handler := NewHandler(NewServer(
				readinessStub{},
				WithAuthentication(authentication, false),
				WithRuntimeConfiguration(configuration),
			))
			var payload map[string]any
			if err := json.Unmarshal(rawConfigurationUpdateBody(t, nil), &payload); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			payload["events"] = map[string]any{"retentionDays": 30}
			for _, path := range test.remove {
				removeJSONField(t, payload, path)
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("encode fixture: %v", err)
			}
			request := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(encoded))
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantStatus == http.StatusOK {
				return
			}
			var apiError ApiError
			if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if apiError.Code != "invalid_request" || apiError.Details["field"] != test.wantMissing {
				t.Fatalf("error = %#v, want missing field %q", apiError, test.wantMissing)
			}
		})
	}
}

func removeJSONField(t *testing.T, payload map[string]any, path string) {
	t.Helper()
	segments := strings.Split(path, ".")
	current := payload
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			t.Fatalf("fixture has no object at %q", segment)
		}
		current = next
	}
	last := segments[len(segments)-1]
	if _, ok := current[last]; !ok {
		t.Fatalf("fixture has no field %q", path)
	}
	delete(current, last)
}
