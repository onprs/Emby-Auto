package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func BenchmarkToolLoop(b *testing.B) {
	for _, test := range []struct {
		steps       int
		resultBytes int
	}{
		{steps: 4, resultBytes: 1024},
		{steps: 16, resultBytes: 4096},
		{steps: 64, resultBytes: 4096},
		{steps: 64, resultBytes: MaxToolResultBytes},
	} {
		b.Run(fmt.Sprintf("steps_%d/result_%d", test.steps, test.resultBytes), func(b *testing.B) {
			readBody := agentToolResponse([]toolCall{agentTestToolCall("read-1", "read_context", `{}`)}, "tool_calls")
			submitBody := agentToolResponse([]toolCall{agentTestToolCall("submit-1", "submit_result", `{}`)}, "tool_calls")
			toolResult := json.RawMessage(`{"value":"` + strings.Repeat("x", test.resultBytes-12) + `"}`)
			calls := 0
			var requestBytes int64
			client, err := NewClient(ClientOptions{
				BaseURL: "http://127.0.0.1/v1", APIKey: "test-key", Model: "test-model",
				HTTPClient: &http.Client{Transport: agentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					count, err := io.Copy(io.Discard, request.Body)
					if err != nil {
						return nil, err
					}
					requestBytes += count
					calls++
					if calls%test.steps == 0 {
						return agentResponse(submitBody), nil
					}
					return agentResponse(readBody), nil
				})},
			})
			if err != nil {
				b.Fatal(err)
			}
			request := agentTestLoopRequest()
			request.MaxSteps = test.steps
			request.UserPrompt = strings.Repeat("r", 1024)
			request.Execute = func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return toolResult, nil }
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := client.RunToolLoop(context.Background(), request); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(requestBytes)/float64(b.N), "request-B/op")
		})
	}
}

func BenchmarkToolLoopRejectOversizedResult(b *testing.B) {
	body := agentToolResponse([]toolCall{agentTestToolCall("read-1", "read_context", `{}`)}, "tool_calls")
	client := agentTestClient(b, body)
	toolResult := json.RawMessage(`{"value":"` + strings.Repeat("x", 1<<20) + `"}`)
	request := agentTestLoopRequest()
	request.Execute = func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return toolResult, nil }
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := client.RunToolLoop(context.Background(), request); err == nil {
			b.Fatal("oversized tool result was accepted")
		}
	}
}
