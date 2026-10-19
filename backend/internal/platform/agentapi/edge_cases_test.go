package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type agentRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip agentRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func agentResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func agentToolResponse(calls []toolCall, finishReason string) string {
	encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"message": chatMessage{Role: "assistant", ToolCalls: calls}, "finish_reason": finishReason,
	}}})
	return string(encoded)
}

func agentTestToolCall(id, name, arguments string) toolCall {
	return toolCall{ID: id, Type: "function", Function: toolFunction{Name: name, Arguments: arguments}}
}

func agentTestClient(t testing.TB, body string) *Client {
	t.Helper()
	client, err := NewClient(ClientOptions{
		BaseURL: "http://127.0.0.1/v1", APIKey: "test-key", Model: "test-model",
		HTTPClient: &http.Client{Transport: agentRoundTripFunc(func(*http.Request) (*http.Response, error) { return agentResponse(body), nil })},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func agentTestLoopRequest() ToolLoopRequest {
	return ToolLoopRequest{
		MaxSteps: 2,
		Tools: []ToolDefinition{
			{Name: "read_context", Parameters: map[string]any{"type": "object"}},
			{Name: "submit_result", Parameters: map[string]any{"type": "object"}},
		},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
		Submit: func(name string, _ json.RawMessage) (bool, error) { return name == "submit_result", nil },
	}
}

func TestCompleteClassifiesHTTPStatusIndependentlyOfErrorBody(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		body      string
		code      string
		retryable bool
	}{
		{name: "rate_limit_html", status: 429, body: "<html>Too many requests</html>", code: "agent_rate_limited", retryable: true},
		{name: "rate_limit_empty", status: 429, code: "agent_rate_limited", retryable: true},
		{name: "gateway_html", status: 502, body: "<html>Bad gateway</html>", code: "agent_provider_unavailable", retryable: true},
		{name: "unavailable_truncated_json", status: 503, body: `{"error":`, code: "agent_provider_unavailable", retryable: true},
		{name: "internal_server_error", status: 500, body: "Internal error", code: "agent_provider_unavailable", retryable: true},
		{name: "auth_html", status: 401, body: "Unauthorized", code: "agent_authentication_failed"},
		{name: "forbidden_empty", status: 403, code: "agent_authentication_failed"},
		{name: "request_timeout", status: 408, body: "Request timeout", code: "agent_request_timeout", retryable: true},
		{name: "bad_request_html", status: 400, body: "Bad request", code: "agent_protocol_unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "test-key", Model: "test-model"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.http.CloseIdleConnections()
			_, err = client.complete(context.Background(), chatRequest{Model: "test-model"})
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != test.code || apiErr.StatusCode != test.status || apiErr.Retryable != test.retryable {
				t.Fatalf("complete() error = %#v, want status %d code %s retryable %t", err, test.status, test.code, test.retryable)
			}
		})
	}
}

func TestCompleteRequestTimeoutIncludesDNSResolution(t *testing.T) {
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	client, err := NewClient(ClientOptions{
		BaseURL: "https://agent-timeout.invalid/v1", APIKey: "test-key", Model: "test-model",
		RequestTimeout: 20 * time.Millisecond, Resolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.http.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	_, err = client.complete(ctx, chatRequest{Model: "test-model"})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "agent_request_timeout" || !apiErr.Retryable || time.Since(started) >= 500*time.Millisecond {
		t.Fatalf("complete() error = %#v after %s, configured timeout did not cover DNS", err, time.Since(started))
	}
}

func TestToolLoopRejectsMalformedCallEnvelopesBeforeExecution(t *testing.T) {
	valid := agentTestToolCall("read-1", "read_context", `{}`)
	for _, test := range []struct {
		name   string
		calls  []toolCall
		finish string
	}{
		{name: "missing_id", calls: []toolCall{agentTestToolCall("", "read_context", `{}`)}, finish: "tool_calls"},
		{name: "duplicate_id", calls: []toolCall{valid, valid}, finish: "tool_calls"},
		{name: "unsupported_type", calls: []toolCall{{ID: "read-1", Type: "custom", Function: valid.Function}}, finish: "tool_calls"},
		{name: "missing_name", calls: []toolCall{agentTestToolCall("read-1", "", `{}`)}, finish: "tool_calls"},
		{name: "truncated_completion", calls: []toolCall{valid}, finish: "length"},
		{name: "filtered_completion", calls: []toolCall{valid}, finish: "content_filter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := agentTestClient(t, agentToolResponse(test.calls, test.finish))
			request := agentTestLoopRequest()
			executions := 0
			request.Execute = func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
				executions++
				return json.RawMessage(`{}`), nil
			}
			_, err := client.RunToolLoop(context.Background(), request)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != "agent_response_invalid" || !apiErr.Retryable || executions != 0 {
				t.Fatalf("error = %#v, executions = %d, want retryable invalid response before execution", err, executions)
			}
		})
	}
}

func TestToolLoopRepairsNonObjectArgumentsWithoutInvokingCallbacks(t *testing.T) {
	for _, arguments := range []string{`null`, `[]`, `true`, `"value"`, `42`, `{"entryId":`} {
		t.Run(arguments, func(t *testing.T) {
			client := agentTestClient(t, agentToolResponse([]toolCall{agentTestToolCall("read-1", "read_context", arguments)}, "tool_calls"))
			request := agentTestLoopRequest()
			request.MaxSteps = 1
			callbacks := 0
			request.Execute = func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
				callbacks++
				return json.RawMessage(`{}`), nil
			}
			request.Submit = func(string, json.RawMessage) (bool, error) { callbacks++; return false, nil }
			result, err := client.RunToolLoop(context.Background(), request)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != "agent_tool_call_exhausted" || callbacks != 0 || len(result.Steps) != 1 || result.Steps[0].ErrorCode != "agent_tool_arguments_invalid" {
				t.Fatalf("result = %#v, error = %#v, callbacks = %d", result, err, callbacks)
			}
		})
	}
}

func TestToolLoopStopsAfterCancellationBeforeSubmission(t *testing.T) {
	for _, phase := range []string{"execute", "execute_empty_result", "submit"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := agentTestClient(t, agentToolResponse([]toolCall{
				agentTestToolCall("read-1", "read_context", `{}`), agentTestToolCall("submit-1", "submit_result", `{}`),
			}, "tool_calls"))
			request := agentTestLoopRequest()
			request.Execute = func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
				if phase != "submit" {
					cancel()
				}
				if phase == "execute_empty_result" {
					return nil, nil
				}
				return json.RawMessage(`{}`), nil
			}
			request.Submit = func(name string, _ json.RawMessage) (bool, error) {
				if name == "submit_result" {
					cancel()
					return true, nil
				}
				return false, nil
			}
			result, err := client.RunToolLoop(ctx, request)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != "agent_request_cancelled" || apiErr.Retryable || !errors.Is(err, context.Canceled) || len(result.Submission) != 0 {
				t.Fatalf("error = %#v, submission = %s, want non-retryable cancellation", err, result.Submission)
			}
		})
	}
}

func TestToolLoopPreservesExecutorCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			client := agentTestClient(t, agentToolResponse([]toolCall{agentTestToolCall("read-1", "read_context", `{}`)}, "tool_calls"))
			request := agentTestLoopRequest()
			request.Execute = func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, cause }
			_, err := client.RunToolLoop(context.Background(), request)
			var apiErr *Error
			code := "agent_request_cancelled"
			if errors.Is(cause, context.DeadlineExceeded) {
				code = "agent_request_timeout"
			}
			if !errors.As(err, &apiErr) || apiErr.Code != code || !errors.Is(err, cause) {
				t.Fatalf("RunToolLoop() error = %#v, want %s", err, code)
			}
		})
	}
}

func TestConnectivityRequiresReceivingEchoResultBeforeSubmission(t *testing.T) {
	client := agentTestClient(t, agentToolResponse([]toolCall{
		agentTestToolCall("echo-1", "agent_connectivity_echo", `{"value":"emby-auto-agent-connectivity-v1"}`),
		agentTestToolCall("submit-1", "submit_agent_connectivity", `{"ok":true,"echoed":"emby-auto-agent-connectivity-v1"}`),
	}, "tool_calls"))
	if err := client.ConnectivityTest(context.Background()); err == nil {
		t.Fatal("connectivity succeeded without a round trip carrying the echo result")
	}
}
