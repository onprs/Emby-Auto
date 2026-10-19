package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func FuzzToolLoopProviderResponses(f *testing.F) {
	for _, body := range []string{
		`{}`, `null`, `{"choices":null}`, `{"choices":[{"message":{"role":"assistant","content":"text"}}]}`,
		agentToolResponse([]toolCall{agentTestToolCall("submit-1", "submit_result", `{}`)}, "tool_calls"),
		agentToolResponse([]toolCall{agentTestToolCall("read-1", "read_context", `null`)}, "tool_calls"),
		agentToolResponse([]toolCall{agentTestToolCall("unknown-1", "unknown", `{}`)}, "tool_calls"),
		agentToolResponse([]toolCall{agentTestToolCall("", "read_context", `{}`)}, "tool_calls"),
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		client := agentTestClient(t, "")
		requests := 0
		client.http.Transport = agentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			encoded, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			var payload chatRequest
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatalf("invalid request JSON: %v", err)
			}
			standard, err := json.Marshal(payload)
			if err != nil || !bytes.Equal(encoded, standard) {
				t.Fatalf("request history differs from standard JSON: %v", err)
			}
			return agentResponse(string(body)), nil
		})
		request := agentTestLoopRequest()
		request.Execute = func(_ context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
			if name != "read_context" {
				t.Fatalf("unregistered executor called: %s", name)
			}
			trimmed := bytes.TrimSpace(arguments)
			if !json.Valid(trimmed) || len(trimmed) == 0 || trimmed[0] != '{' {
				t.Fatalf("executor received non-object arguments: %q", arguments)
			}
			return json.RawMessage(`{}`), nil
		}
		result, err := client.RunToolLoop(context.Background(), request)
		if requests > request.MaxSteps || len(result.Steps) > request.MaxSteps {
			t.Fatalf("tool loop exceeded budget: requests %d, steps %d", requests, len(result.Steps))
		}
		if err == nil && (len(result.Submission) == 0 || !json.Valid(result.Submission) || bytes.TrimSpace(result.Submission)[0] != '{') {
			t.Fatalf("successful tool loop returned invalid submission: %q", result.Submission)
		}
	})
}
