package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectivityTestRequiresEchoThenTypedSubmission(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-secret" {
			t.Fatal("authorization header was not set")
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "test-model" || payload["temperature"] != float64(0) {
			t.Fatalf("request payload = %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch calls.Add(1) {
		case 1:
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"echo-1","type":"function","function":{"name":"agent_connectivity_echo","arguments":"{\"value\":\"emby-auto-agent-connectivity-v1\"}"}}]}}]}`))
		case 2:
			messages := payload["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "echo-1" {
				t.Fatalf("tool result = %#v", last)
			}
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit-1","type":"function","function":{"name":"submit_agent_connectivity","arguments":"{\"ok\":true,\"echoed\":\"emby-auto-agent-connectivity-v1\"}"}}]}}]}`))
		default:
			t.Fatal("unexpected extra request")
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL + "/v1", APIKey: "test-secret", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ConnectivityTest(context.Background()); err != nil {
		t.Fatalf("ConnectivityTest() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("request count = %d, want 2", calls.Load())
	}
}

func TestConnectivityTestRejectsUnknownAndOutOfOrderTools(t *testing.T) {
	tests := []struct {
		name     string
		response string
		code     string
	}{
		{
			name:     "unknown tool",
			response: `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"run_shell","arguments":"{}"}}]}}]}`,
			code:     "agent_tool_call_invalid",
		},
		{
			name:     "submission before echo",
			response: `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"submit_agent_connectivity","arguments":"{\"ok\":true,\"echoed\":\"emby-auto-agent-connectivity-v1\"}"}}]}}]}`,
			code:     "agent_tool_call_invalid",
		},
		{
			name:     "free text",
			response: `{"choices":[{"message":{"role":"assistant","content":"connected"}}]}`,
			code:     "agent_submission_missing",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(test.response))
			}))
			defer server.Close()
			client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
			if err != nil {
				t.Fatal(err)
			}
			err = client.ConnectivityTest(context.Background())
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != test.code {
				t.Fatalf("ConnectivityTest() error = %#v, want %s", err, test.code)
			}
		})
	}
}

func TestClientClassifiesInvalidProviderOutputAsRetryable(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "invalid JSON", body: `not-json`, code: "agent_response_invalid"},
		{name: "oversized response", body: strings.Repeat("x", maxResponseBytes+1), code: "agent_response_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.complete(context.Background(), chatRequest{Model: "model"})
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != test.code || !apiErr.Retryable {
				t.Fatalf("complete() error = %#v, want retryable %s", err, test.code)
			}
		})
	}
}

func TestClientClassifiesProviderFailuresWithoutExposingSecretOrBody(t *testing.T) {
	tests := []struct {
		status    int
		code      string
		retryable bool
	}{
		{status: http.StatusUnauthorized, code: "agent_authentication_failed"},
		{status: http.StatusForbidden, code: "agent_authentication_failed"},
		{status: http.StatusNotFound, code: "agent_model_unavailable"},
		{status: http.StatusTooManyRequests, code: "agent_rate_limited", retryable: true},
		{status: http.StatusBadGateway, code: "agent_provider_unavailable", retryable: true},
		{status: http.StatusServiceUnavailable, code: "agent_provider_unavailable", retryable: true},
	}
	for _, test := range tests {
		t.Run(test.code+http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{"error":{"message":"body-sensitive-value"}}`))
			}))
			defer server.Close()
			client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret-sensitive-value", Model: "model"})
			if err != nil {
				t.Fatal(err)
			}
			err = client.ConnectivityTest(context.Background())
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != test.code || apiErr.Retryable != test.retryable {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(err.Error(), "sensitive-value") {
				t.Fatalf("error exposed sensitive data: %v", err)
			}
		})
	}
}

func TestClientRejectsUnsafeEndpointShapeAndOversizedResponse(t *testing.T) {
	for _, rawURL := range []string{
		"https://user:password@example.test/v1",
		"https://example.test/v1?token=value",
		"https://example.test/v1#fragment",
		"ftp://example.test/v1",
	} {
		if _, err := NewClient(ClientOptions{BaseURL: rawURL, APIKey: "secret", Model: "model"}); err == nil {
			t.Fatalf("NewClient(%q) error = nil", rawURL)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	err = client.ConnectivityTest(context.Background())
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "agent_response_too_large" {
		t.Fatalf("error = %#v, want agent_response_too_large", err)
	}
}

func TestClientHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = client.ConnectivityTest(ctx)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "agent_request_timeout" {
		t.Fatalf("error = %#v, want cancelled request classification", err)
	}
}
