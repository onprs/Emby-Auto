package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestAgentConnectivityClosesOwnedIdleConnections(t *testing.T) {
	closed := make(chan struct{})
	var once sync.Once
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		name, arguments := "agent_connectivity_echo", `{"value":"emby-auto-agent-connectivity-v1"}`
		if payload.Messages[len(payload.Messages)-1].Role == "tool" {
			name, arguments = "submit_agent_connectivity", `{"ok":true,"echoed":"emby-auto-agent-connectivity-v1"}`
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "call-1", "type": "function", "function": map[string]any{"name": name, "arguments": arguments},
			}},
		}}}})
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			once.Do(func() { close(closed) })
		}
	}
	server.Start()
	defer server.Close()

	service := NewConnectivityService(timeoutConnectivityResolver{}, nil, nil)
	if err := service.testAgent(context.Background(), domain.Configuration{}, &domain.AgentTestConfig{
		Protocol: domain.AgentProtocolOpenAIChatCompletions, BaseURL: server.URL + "/v1", Model: "test-model",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("owned Agent transport kept an idle connection after connectivity test completed")
	}
}
