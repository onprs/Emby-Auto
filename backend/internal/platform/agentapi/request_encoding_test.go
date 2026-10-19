package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestToolLoopRequestHistoryMatchesStandardJSONEncoding(t *testing.T) {
	client := agentTestClient(t, "")
	calls := 0
	client.http.Transport = agentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload chatRequest
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		standard, err := json.Marshal(payload)
		if err != nil || !bytes.Equal(body, standard) {
			t.Fatalf("request differs from standard JSON: error %v", err)
		}
		calls++
		name := "read_context"
		if calls == 3 {
			name = "submit_result"
		}
		return agentResponse(agentToolResponse([]toolCall{agentTestToolCall(name, name, `{}`)}, "tool_calls")), nil
	})
	request := agentTestLoopRequest()
	request.MaxSteps = 4
	request.SystemPrompt = "系统 <>&\"\\\n"
	request.UserPrompt = "资源 \u2028\u2029 中文"
	result, _ := json.Marshal(map[string]string{"title": "资源 <>&\"\\\n"})
	request.Execute = func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return result, nil }
	if _, err := client.RunToolLoop(context.Background(), request); err != nil || calls != 3 {
		t.Fatalf("RunToolLoop() = %v, calls = %d", err, calls)
	}
}

func TestToolLoopBoundsRequestHistoryBeforeSending(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"` + strings.Repeat("<", maxResponseBytes-256) + `"}}]}`
	client := agentTestClient(t, body)
	calls, largestRequest := 0, int64(0)
	client.http.Transport = agentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		count, err := io.Copy(io.Discard, request.Body)
		if err != nil {
			return nil, err
		}
		calls++
		largestRequest = max(largestRequest, count)
		return agentResponse(body), nil
	})
	request := agentTestLoopRequest()
	request.MaxSteps = 8
	result, err := client.RunToolLoop(context.Background(), request)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "agent_context_too_large" || apiErr.Retryable ||
		!errors.Is(err, errChatRequestTooLarge) || calls >= request.MaxSteps || largestRequest > maxChatRequestBytes || len(result.Submission) != 0 {
		t.Fatalf("error = %#v, requests = %d, largest request = %d, submission bytes = %d", err, calls, largestRequest, len(result.Submission))
	}
}
