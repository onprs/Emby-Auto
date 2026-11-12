package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolLoopRepairsUnknownToolCallsWithinBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch calls.Add(1) {
		case 1:
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"bad-1","type":"function","function":{"name":"invented_tool","arguments":"{}"}}]}}]}`))
		case 2:
			messages := payload["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "bad-1" || last["content"] != `{"accepted":false,"error":"agent_tool_unknown"}` {
				t.Fatalf("first repair result = %#v", last)
			}
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"bad-2","type":"function","function":{"name":"another_invented_tool","arguments":"{}"}}]}}]}`))
		case 3:
			messages := payload["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "bad-2" || last["content"] != `{"accepted":false,"error":"agent_tool_unknown"}` {
				t.Fatalf("second repair result = %#v", last)
			}
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit-1","type":"function","function":{"name":"submit_result","arguments":"{\"ok\":true}"}}]}}]}`))
		default:
			t.Fatal("unexpected extra request")
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 6,
		Tools: []ToolDefinition{{Name: "read_scope", Parameters: map[string]any{"type": "object"}}, {Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
		Submit: func(name string, arguments json.RawMessage) (bool, error) {
			return name == "submit_result", nil
		},
	})
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if calls.Load() != 3 || len(result.Steps) != 3 || result.Steps[0].ErrorCode != "agent_tool_unknown" || result.Steps[1].ErrorCode != "agent_tool_unknown" || !result.Steps[2].Submitted {
		t.Fatalf("result = %#v, calls = %d", result, calls.Load())
	}
}

func TestToolLoopRepairsMissingSubmissionWithinBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"free text"}}]}`))
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		messages := payload["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		if last["role"] != "user" {
			t.Fatalf("repair prompt = %#v", last)
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit","type":"function","function":{"name":"submit_result","arguments":"{\"ok\":true}"}}]}}]}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 3,
		Tools:   []ToolDefinition{{Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		Submit:  func(string, json.RawMessage) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if len(result.Steps) != 2 || result.Steps[0].ErrorCode != "agent_submission_missing" || !result.Steps[1].Submitted {
		t.Fatalf("result = %#v", result)
	}
}

func TestToolLoopRepairsInvalidSubmissionsUntilAccepted(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		call := requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit-%d","type":"function","function":{"name":"submit_result","arguments":"{\"ok\":true}"}}]}}]}`, call)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	var submissions atomic.Int32
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 5,
		Tools: []ToolDefinition{{Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("unexpected execution")
		},
		Submit: func(string, json.RawMessage) (bool, error) {
			if submissions.Add(1) < 4 {
				return false, errors.New("invalid submission")
			}
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if requests.Load() != 4 || len(result.Steps) != 4 || result.Steps[0].ErrorCode != "agent_submission_invalid" || result.Steps[1].ErrorCode != "agent_submission_invalid" || result.Steps[2].ErrorCode != "agent_submission_invalid" || !result.Steps[3].Submitted {
		t.Fatalf("result = %#v, requests = %d", result, requests.Load())
	}
}

func TestToolLoopReturnsPartialStepsWhenSubmissionBudgetIsExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit","type":"function","function":{"name":"submit_result","arguments":"{\"ok\":true}"}}]}}]}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 3,
		Tools:   []ToolDefinition{{Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		Submit:  func(string, json.RawMessage) (bool, error) { return false, errors.New("invalid submission") },
	})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "agent_submission_exhausted" || !apiErr.Retryable {
		t.Fatalf("RunToolLoop() error = %#v, want retryable exhaustion", err)
	}
	if len(result.Steps) != 3 {
		t.Fatalf("partial steps = %d, want 3", len(result.Steps))
	}
	for _, step := range result.Steps {
		if step.ErrorCode != "agent_submission_invalid" || len(step.Arguments) == 0 || len(step.Result) == 0 {
			t.Fatalf("partial step = %#v", step)
		}
	}
}

func TestToolLoopAcceptsParallelBatchCallsWithinScopedBudget(t *testing.T) {
	const entries = 13
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			toolCalls := make([]map[string]any, 0, entries)
			for index := 0; index < entries; index++ {
				toolCalls = append(toolCalls, map[string]any{
					"id": fmt.Sprintf("analyze-%d", index), "type": "function",
					"function": map[string]any{"name": "analyze_release_title", "arguments": `{}`},
				})
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": toolCalls}}}})
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit-1","type":"function","function":{"name":"submit_result","arguments":"{\"ok\":true}"}}]}}]}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	var executions atomic.Int32
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: entries + 6,
		Tools: []ToolDefinition{{Name: "analyze_release_title", Parameters: map[string]any{"type": "object"}}, {Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			executions.Add(1)
			return json.RawMessage(`{"ok":true}`), nil
		},
		Submit: func(name string, arguments json.RawMessage) (bool, error) {
			return name == "submit_result", nil
		},
	})
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if executions.Load() != entries || len(result.Steps) != entries+1 || calls.Load() != 2 {
		t.Fatalf("executions = %d, steps = %d, calls = %d", executions.Load(), len(result.Steps), calls.Load())
	}
}

func TestToolLoopRejectsBudgetAboveHardLimit(t *testing.T) {
	client := &Client{}
	_, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		MaxSteps: 65,
		Execute:  func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		Submit:   func(string, json.RawMessage) (bool, error) { return false, nil },
	})
	if err == nil {
		t.Fatal("RunToolLoop() error = nil, want hard-limit rejection")
	}
}

func TestToolLoopRepairsInvalidToolArgumentsWithinBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch calls.Add(1) {
		case 1:
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read_scope","arguments":"{\"offset\":0,\"extra\":\"x\"}"}}]}}]}`))
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			messages := payload["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["content"] != `{"accepted":false,"error":"agent_tool_arguments_invalid"}` {
				t.Fatalf("repair result = %#v", last)
			}
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit-1","type":"function","function":{"name":"submit_result","arguments":"{\"ok\":true}"}}]}}]}`))
		default:
			t.Fatal("unexpected extra provider request")
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 6,
		Tools: []ToolDefinition{{Name: "read_scope", Parameters: map[string]any{"type": "object"}}, {Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			return nil, InvalidToolArguments(errors.New("json: unknown field \"extra\""))
		},
		Submit: func(name string, arguments json.RawMessage) (bool, error) { return name == "submit_result", nil },
	})
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if calls.Load() != 2 || len(result.Steps) != 2 || result.Steps[0].ErrorCode != "agent_tool_arguments_invalid" || !result.Steps[1].Submitted {
		t.Fatalf("calls = %d, result = %#v", calls.Load(), result)
	}
}

func TestToolLoopAbortsScopeViolationWithoutRepair(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read_scope","arguments":"{}"}}]}}]}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 6,
		Tools: []ToolDefinition{{Name: "read_scope", Parameters: map[string]any{"type": "object"}}, {Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			return nil, ToolScopeViolation(errors.New("file is outside this resolution scope"))
		},
		Submit: func(name string, arguments json.RawMessage) (bool, error) { return name == "submit_result", nil },
	})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if apiErr.Code != "agent_tool_scope_violation" || apiErr.Retryable || calls.Load() != 1 || len(result.Steps) != 1 || result.Steps[0].ErrorCode != "agent_tool_scope_violation" {
		t.Fatalf("code=%s retryable=%v calls=%d steps=%#v", apiErr.Code, apiErr.Retryable, calls.Load(), result.Steps)
	}
}

func TestToolLoopTreatsUnclassifiedExecutorErrorsAsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read_scope","arguments":"{}"}}]}}]}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 6,
		Tools: []ToolDefinition{{Name: "read_scope", Parameters: map[string]any{"type": "object"}}, {Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("list RSS pre-acquisition mapping sources: connection reset")
		},
		Submit: func(name string, arguments json.RawMessage) (bool, error) { return name == "submit_result", nil },
	})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if apiErr.Code != "agent_tool_execution_failed" || !apiErr.Retryable || len(result.Steps) != 1 || result.Steps[0].ErrorCode != "agent_tool_execution_failed" {
		t.Fatalf("code=%s retryable=%v steps=%#v", apiErr.Code, apiErr.Retryable, result.Steps)
	}
}

func TestToolLoopDegradesOversizedToolResult(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch calls.Add(1) {
		case 1:
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read_scope","arguments":"{}"}}]}}]}`))
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			messages := payload["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["content"] != `{"accepted":false,"error":"agent_tool_result_too_large"}` {
				t.Fatalf("degraded result = %#v", last)
			}
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit-1","type":"function","function":{"name":"submit_result","arguments":"{\"ok\":true}"}}]}}]}`))
		default:
			t.Fatal("unexpected extra provider request")
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	oversized, _ := json.Marshal(map[string]any{"payload": strings.Repeat("a", MaxToolResultBytes+1)})
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 4,
		Tools:   []ToolDefinition{{Name: "read_scope", Parameters: map[string]any{"type": "object"}}, {Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return oversized, nil },
		Submit:  func(name string, arguments json.RawMessage) (bool, error) { return name == "submit_result", nil },
	})
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if len(result.Steps) != 2 || result.Steps[0].ErrorCode != "agent_tool_result_too_large" || !result.Steps[1].Submitted {
		t.Fatalf("steps = %#v", result.Steps)
	}
}

func TestToolLoopStopsWhenJobBudgetIsInsufficient(t *testing.T) {
	client, err := NewClient(ClientOptions{BaseURL: "http://127.0.0.1:1", APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := client.RunToolLoop(ctx, ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 6,
		Tools:   []ToolDefinition{{Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		Submit:  func(string, json.RawMessage) (bool, error) { return true, nil },
	})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if apiErr.Code != "agent_budget_exhausted" || apiErr.Retryable || len(result.Steps) != 0 {
		t.Fatalf("code=%s retryable=%v steps=%d", apiErr.Code, apiErr.Retryable, len(result.Steps))
	}
}

func TestToolLoopEnforcesTotalToolResultBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		if call <= 33 {
			_, _ = fmt.Fprintf(writer, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-%d","type":"function","function":{"name":"read_scope","arguments":"{}"}}]}}]}`, call)
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit-1","type":"function","function":{"name":"submit_result","arguments":"{\"ok\":true}"}}]}}]}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"payload": strings.Repeat("a", MaxToolResultBytes-64)})
	result, err := client.RunToolLoop(context.Background(), ToolLoopRequest{
		SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 64,
		Tools:   []ToolDefinition{{Name: "read_scope", Parameters: map[string]any{"type": "object"}}, {Name: "submit_result", Parameters: map[string]any{"type": "object"}}},
		Execute: func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return payload, nil },
		Submit:  func(name string, arguments json.RawMessage) (bool, error) { return name == "submit_result", nil },
	})
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	budgetSteps := 0
	for _, step := range result.Steps {
		if step.ErrorCode == "agent_tool_result_budget_exhausted" {
			budgetSteps++
		}
	}
	if budgetSteps != 1 || !result.Steps[len(result.Steps)-1].Submitted {
		t.Fatalf("budget steps = %d, total steps = %d", budgetSteps, len(result.Steps))
	}
}
