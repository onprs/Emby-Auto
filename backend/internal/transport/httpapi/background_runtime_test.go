package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type backgroundRuntimeServiceStub struct {
	runtime  domain.BackgroundRuntime
	setState domain.BackgroundRuntimeState
	err      error
}

func (stub *backgroundRuntimeServiceStub) Get(context.Context) (domain.BackgroundRuntime, error) {
	return stub.runtime, stub.err
}

func (stub *backgroundRuntimeServiceStub) Set(_ context.Context, state domain.BackgroundRuntimeState) (domain.BackgroundRuntime, error) {
	stub.setState = state
	return stub.runtime, stub.err
}

func TestBackgroundRuntimeEndpointsRequireAuthentication(t *testing.T) {
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(&authenticationStub{}, false),
		WithBackgroundRuntime(&backgroundRuntimeServiceStub{}),
	))
	for _, test := range []struct {
		method string
		body   string
	}{
		{method: http.MethodGet},
		{method: http.MethodPut, body: `{"state":"stopped"}`},
	} {
		request := httptest.NewRequest(test.method, "/api/v1/dashboard/background-runtime", strings.NewReader(test.body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, body = %s", test.method, response.Code, response.Body.String())
		}
	}
}

func TestBackgroundRuntimeEndpointsReadAndSetWorkerState(t *testing.T) {
	authentication, _ := authenticatedServer()
	stub := &backgroundRuntimeServiceStub{runtime: domain.BackgroundRuntime{State: domain.BackgroundRuntimeStopped}}
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(authentication, false),
		WithBackgroundRuntime(stub),
	))

	getRequest := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/background-runtime", nil)
	getRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", getResponse.Code, getResponse.Body.String())
	}
	var got BackgroundRuntime
	if err := json.NewDecoder(getResponse.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.State != BackgroundRuntimeStateStopped {
		t.Fatalf("GET state = %q", got.State)
	}

	stub.runtime.State = domain.BackgroundRuntimeRunning
	putRequest := httptest.NewRequest(http.MethodPut, "/api/v1/dashboard/background-runtime", strings.NewReader(`{"state":"running"}`))
	putRequest.Header.Set("Content-Type", "application/json")
	putRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	putResponse := httptest.NewRecorder()
	handler.ServeHTTP(putResponse, putRequest)
	if putResponse.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", putResponse.Code, putResponse.Body.String())
	}
	if stub.setState != domain.BackgroundRuntimeRunning {
		t.Fatalf("Set state = %q", stub.setState)
	}
}

func TestBackgroundRuntimeEndpointReturnsSafeUnavailableError(t *testing.T) {
	authentication, _ := authenticatedServer()
	stub := &backgroundRuntimeServiceStub{err: service.NewError(
		"background_runtime_unavailable",
		"background task control is unavailable",
		errors.Join(service.ErrUnavailable, errors.New("private helper detail")),
		map[string]any{"dependency": "host_control"},
	)}
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(authentication, false),
		WithBackgroundRuntime(stub),
	))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/background-runtime", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private helper detail") {
		t.Fatalf("response leaked internal helper error: %s", response.Body.String())
	}
}

var _ BackgroundRuntimeService = (*backgroundRuntimeServiceStub)(nil)
