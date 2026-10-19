package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/agentharness"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
)

func TestAgentReadsPagedEpisodeContextBeforeSubmitting(t *testing.T) {
	values := make([]scopedEpisode, 120)
	for index := range values {
		values[index] = scopedEpisode{ID: uuid.New(), Season: 1, Episode: index + 1, Title: strings.Repeat("剧", 2048)}
	}
	tool := staticAgentTool("list_tmdb_episodes", "Read scoped episodes.", map[string]any{}, values)
	budget := agentToolStepBudget(domain.AgentCapabilityEpisodeMapping, agentContextSnapshot{Tools: []agentharness.Tool{tool}})
	if tool.MinimumReadSteps <= 6 || budget < tool.MinimumReadSteps+1 || budget > 64 {
		t.Fatalf("read steps/budget = %d/%d", tool.MinimumReadSteps, budget)
	}
	acquisitionID, fileID := uuid.New(), uuid.New()
	proposal, _ := json.Marshal(map[string]any{
		"acquisitionId": acquisitionID, "mode": "anchor", "decision": "resolved", "evidenceCodes": []string{"catalog_alignment"},
		"anchor": map[string]any{"sourceFileId": fileID, "targetSeason": 1, "targetEpisode": 1},
	})
	var calls atomic.Int32
	var mutex sync.Mutex
	seen := make(map[uuid.UUID]bool)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		sequence := calls.Add(1)
		name, arguments := tool.Definition.Name, `{}`
		last := payload.Messages[len(payload.Messages)-1]
		if last.Role == "tool" {
			var page struct {
				Items      []scopedEpisode `json:"items"`
				Total      int             `json:"total"`
				NextOffset *int            `json:"nextOffset"`
			}
			if err := json.Unmarshal([]byte(last.Content), &page); err != nil {
				t.Error(err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			mutex.Lock()
			for _, item := range page.Items {
				if seen[item.ID] || item.Title != values[0].Title {
					t.Errorf("duplicate or changed episode %s", item.ID)
				}
				seen[item.ID] = true
			}
			mutex.Unlock()
			if page.Total != len(values) {
				t.Errorf("page total = %d", page.Total)
			}
			if page.NextOffset != nil {
				encoded, _ := json.Marshal(map[string]int{"offset": *page.NextOffset})
				arguments = string(encoded)
			} else {
				name, arguments = "submit_episode_mapping", string(proposal)
			}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{
			"finish_reason": "tool_calls", "message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{
					"id": fmt.Sprintf("call-%d", sequence), "type": "function", "function": map[string]any{"name": name, "arguments": arguments},
				}},
			},
		}}})
	}))
	defer server.Close()
	httpClient := server.Client()
	defer httpClient.CloseIdleConnections()
	client, err := agentapi.NewClient(agentapi.ClientOptions{BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resource, _ := json.Marshal(map[string]any{"acquisitionId": acquisitionID, "episodeCount": len(values)})
	result, err := (agentharness.Runner{}).Run(ctx, client, agentharness.Context{
		Capability: domain.AgentCapabilityEpisodeMapping, Resource: resource, Tools: []agentharness.Tool{tool}, MaxSteps: budget,
		ValidateSubmission: func(json.RawMessage) error {
			mutex.Lock()
			defer mutex.Unlock()
			if len(seen) != len(values) {
				return fmt.Errorf("未读取完整集数上下文")
			}
			return nil
		},
	})
	if err != nil || len(result.Proposal) == 0 || int(calls.Load()) != tool.MinimumReadSteps+1 {
		t.Fatalf("Run() = %v, calls = %d, proposal bytes = %d", err, calls.Load(), len(result.Proposal))
	}
}

func TestAgentPagedStaticToolRejectsInvalidPageOffsets(t *testing.T) {
	values := make([]string, 50)
	for index := range values {
		values[index] = strings.Repeat("title", 410)
	}
	tool := staticAgentTool("read_context", "Read scoped context.", map[string]any{}, values)
	for _, arguments := range []string{`{"offset":-1}`, `{"offset":1}`, `{"offset":50}`, `{"offset":"0"}`, `{"offset":null}`, `{"offset":true}`, `{"offset":1.5}`, `{"unexpected":true}`} {
		if _, err := tool.Execute(context.Background(), tool.Definition.Name, json.RawMessage(arguments)); err == nil {
			t.Fatalf("invalid page arguments accepted: %s", arguments)
		}
	}
}

func TestAgentPagedToolBudgetIsCapped(t *testing.T) {
	if got := agentToolStepBudget(domain.AgentCapabilityEpisodeMapping, agentContextSnapshot{Tools: []agentharness.Tool{{MinimumReadSteps: 1000}}}); got != 64 {
		t.Fatalf("large context budget = %d, want 64", got)
	}
}
