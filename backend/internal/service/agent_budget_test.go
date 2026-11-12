package service

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

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
)

func TestAgentJobTimeoutCoversStepBudget(t *testing.T) {
	for _, test := range []struct {
		name       string
		steps      int
		request    time.Duration
		wantBudget time.Duration
	}{
		{name: "default", steps: 6, request: 60 * time.Second, wantBudget: 390 * time.Second},
		{name: "single_step", steps: 1, request: 10 * time.Second, wantBudget: 40 * time.Second},
		{name: "paged_batch", steps: 20, request: 30 * time.Second, wantBudget: 630 * time.Second},
		{name: "capped", steps: 64, request: 120 * time.Second, wantBudget: 60 * time.Minute},
		{name: "invalid_inputs", steps: 0, request: 0, wantBudget: 90 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := agentJobTimeout(test.steps, test.request); got != test.wantBudget {
				t.Fatalf("agentJobTimeout(%d, %s) = %s, want %s", test.steps, test.request, got, test.wantBudget)
			}
		})
	}
}

func TestAutomaticAgentResolutionRetryableExcludesBudgetExhaustion(t *testing.T) {
	base := domain.AgentResolution{Trigger: "automatic", Status: domain.AgentResolutionFailed, Proposal: []byte(`{}`)}
	executionFailure := base
	executionFailure.ErrorCode = "agent_tool_execution_failed"
	if !AutomaticAgentResolutionRetryable(executionFailure) {
		t.Fatal("transient tool execution failure is not retryable")
	}
	budgetFailure := base
	budgetFailure.ErrorCode = "agent_budget_exhausted"
	if AutomaticAgentResolutionRetryable(budgetFailure) {
		t.Fatal("exhausted job budget would rerun the whole loop")
	}
	scopeFailure := base
	scopeFailure.ErrorCode = "agent_tool_scope_violation"
	if AutomaticAgentResolutionRetryable(scopeFailure) {
		t.Fatal("scope violation is retryable through the automatic path")
	}
}

func TestAgentStaticToolClampsSingleOversizedArrayItem(t *testing.T) {
	oversized := strings.Repeat("长", agentapi.MaxToolResultBytes)
	value := []map[string]any{{"relativePath": oversized, "sizeBytes": 1024}}
	tool := staticAgentTool("analyze_download_manifest", "Read the download manifest.", map[string]any{}, value)
	result, err := tool.Execute(context.Background(), tool.Definition.Name, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result) > agentapi.MaxToolResultBytes || !json.Valid(result) {
		t.Fatalf("clamped result bytes = %d, valid = %v", len(result), json.Valid(result))
	}
	var page struct {
		Items []struct {
			RelativePath string `json:"relativePath"`
			Truncated    bool   `json:"truncated"`
		} `json:"items"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(result, &page); err != nil {
		t.Fatalf("clamped result = %s, error = %v", result, err)
	}
	if page.Total != 1 || len(page.Items) != 1 || !page.Truncated || !page.Items[0].Truncated {
		t.Fatalf("clamped page = %#v", page)
	}
	if !strings.HasPrefix(page.Items[0].RelativePath, "长") {
		t.Fatalf("clamped path lost its leading content")
	}
}

func TestAgentPagedToolClampsOversizedItemAndKeepsPagesReadable(t *testing.T) {
	values := []map[string]any{
		{"relativePath": strings.Repeat("剧", agentapi.MaxToolResultBytes), "sizeBytes": 2048},
		{"relativePath": "Show.S01E02.mkv", "sizeBytes": 1024},
	}
	tool := staticAgentTool("analyze_download_manifest", "Read the download manifest.", map[string]any{}, values)
	if tool.MinimumReadSteps < 2 {
		t.Fatalf("MinimumReadSteps = %d, want at least the clamped item page and the remaining item", tool.MinimumReadSteps)
	}
	var total int
	offset := 0
	for pages := 0; pages < tool.MinimumReadSteps; pages++ {
		arguments, _ := json.Marshal(map[string]any{"offset": offset})
		result, err := tool.Execute(context.Background(), tool.Definition.Name, json.RawMessage(arguments))
		if err != nil {
			t.Fatalf("Execute(offset=%d) error = %v", offset, err)
		}
		if len(result) > agentapi.MaxToolResultBytes || !json.Valid(result) {
			t.Fatalf("page offset=%d bytes = %d, valid = %v", offset, len(result), json.Valid(result))
		}
		var page struct {
			Items      []json.RawMessage `json:"items"`
			Total      int               `json:"total"`
			NextOffset *int              `json:"nextOffset"`
			Truncated  bool              `json:"truncated"`
		}
		if err := json.Unmarshal(result, &page); err != nil {
			t.Fatalf("page offset=%d error = %v", offset, err)
		}
		if offset == 0 && !page.Truncated {
			t.Fatalf("oversized first page is not marked as truncated")
		}
		total += len(page.Items)
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if total != len(values) {
		t.Fatalf("read items = %d, want %d", total, len(values))
	}
}

func TestAgentStaticToolClampsOversizedObjectPayload(t *testing.T) {
	payload := map[string]any{
		"taskId": uuid.New(), "videoPath": strings.Repeat("a", agentapi.MaxToolResultBytes), "candidateId": "embedded-1",
	}
	tool := staticAgentTool("inspect_candidate", "Read the scoped candidate.", map[string]any{"candidateId": map[string]any{"type": "string"}}, payload)
	result, err := tool.Execute(context.Background(), tool.Definition.Name, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result) > agentapi.MaxToolResultBytes || !json.Valid(result) {
		t.Fatalf("clamped result bytes = %d, valid = %v", len(result), json.Valid(result))
	}
	var object map[string]any
	if err := json.Unmarshal(result, &object); err != nil {
		t.Fatal(err)
	}
	if object["truncated"] != true || object["candidateId"] != "embedded-1" {
		t.Fatalf("clamped object = %#v", object)
	}
}

type agentCatalogStub struct {
	preview func(context.Context, domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error)
}

func (agentCatalogStub) AutomaticEpisodeMappingEnabled(context.Context, uuid.UUID) (bool, error) {
	return true, nil
}

func (agentCatalogStub) TryDeterministicEpisodeMapping(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}

func (stub agentCatalogStub) PreviewEpisodeMapping(ctx context.Context, input domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error) {
	if stub.preview == nil {
		return domain.EpisodeMappingPreview{}, nil
	}
	return stub.preview(ctx, input)
}

func (agentCatalogStub) ApplyAgentEpisodeMapping(context.Context, domain.AgentResolution, domain.AgentEpisodeMappingProposal, domain.AgentProposalValidation) (domain.SavedEpisodeMapping, error) {
	return domain.SavedEpisodeMapping{}, nil
}

func TestAgentPreviewToolClassifiesExecutorFailures(t *testing.T) {
	acquisitionID, fileID := uuid.New(), uuid.New()
	files := map[uuid.UUID]scopedFile{fileID: {ID: fileID, RelativePath: "Show.S01E01.mkv"}}
	for _, test := range []struct {
		name          string
		previewErr    error
		wantLoopError string
		wantRetryable bool
		wantRepair    bool
	}{
		{name: "transient_dependency", previewErr: errors.New("连接被重置"), wantLoopError: "agent_tool_execution_failed", wantRetryable: true},
		{name: "recoverable_plan", previewErr: NewError("mapping_incomplete", "the anchor does not cover every file", ErrStateConflict, nil), wantRepair: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					arguments, _ := json.Marshal(map[string]any{
						"mode": "anchor", "anchor": map[string]any{"sourceFileId": fileID, "targetSeason": 1, "targetEpisode": 1},
					})
					call, _ := json.Marshal(map[string]any{"id": "preview-1", "type": "function", "function": map[string]any{"name": "preview_episode_mapping", "arguments": string(arguments)}})
					response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{json.RawMessage(call)}}}}})
					_, _ = writer.Write(response)
					return
				}
				if test.wantRepair {
					var payload struct {
						Messages []struct {
							Role    string `json:"role"`
							Content string `json:"content"`
						} `json:"messages"`
					}
					if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					last := payload.Messages[len(payload.Messages)-1]
					if last.Role != "tool" || last.Content != `{"accepted":false,"error":"agent_tool_arguments_invalid"}` {
						t.Errorf("repair result = %#v", last)
					}
				}
				_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"submit-1","type":"function","function":{"name":"submit_episode_mapping","arguments":"{\"mode\":\"anchor\"}"}}]}}]}`))
			}))
			defer server.Close()
			service := &AgentResolutionService{catalog: agentCatalogStub{preview: func(context.Context, domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error) {
				return domain.EpisodeMappingPreview{}, test.previewErr
			}}}
			preview := service.previewMappingTool(acquisitionID, files)
			client, err := agentapi.NewClient(agentapi.ClientOptions{BaseURL: server.URL, APIKey: "secret", Model: "model"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.RunToolLoop(context.Background(), agentapi.ToolLoopRequest{
				SystemPrompt: "system", UserPrompt: "resource", MaxSteps: 4,
				Tools: []agentapi.ToolDefinition{
					preview.Definition,
					{Name: "submit_episode_mapping", Parameters: map[string]any{"type": "object"}},
				},
				Execute: func(callCtx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
					return preview.Execute(callCtx, preview.Definition.Name, raw)
				},
				Submit: func(name string, arguments json.RawMessage) (bool, error) {
					return name == "submit_episode_mapping", nil
				},
			})
			if test.wantRepair {
				if err != nil {
					t.Fatalf("RunToolLoop() error = %v", err)
				}
				if len(result.Steps) != 2 || result.Steps[0].ErrorCode != "agent_tool_arguments_invalid" || !result.Steps[1].Submitted {
					t.Fatalf("steps = %#v", result.Steps)
				}
				return
			}
			var apiErr *agentapi.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("RunToolLoop() error = %v", err)
			}
			if apiErr.Code != test.wantLoopError || apiErr.Retryable != test.wantRetryable {
				t.Fatalf("code = %s retryable = %v", apiErr.Code, apiErr.Retryable)
			}
		})
	}
}
