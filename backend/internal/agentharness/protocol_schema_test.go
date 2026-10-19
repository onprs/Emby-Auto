package agentharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
)

func TestRunnerSendsProviderCompatibleSchemasForEveryCapability(t *testing.T) {
	for capability := range promptVersions {
		t.Run(string(capability), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var payload struct {
					Tools []struct {
						Function struct {
							Parameters map[string]any `json:"parameters"`
							Strict     bool           `json:"strict"`
						} `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				for _, tool := range payload.Tools {
					if tool.Function.Parameters["type"] != "object" {
						t.Error("function parameters do not declare an object root")
						writer.WriteHeader(http.StatusBadRequest)
						return
					}
					if err := validateProviderToolSchema(tool.Function.Parameters, tool.Function.Strict); err != nil {
						t.Error(err)
						writer.WriteHeader(http.StatusBadRequest)
						return
					}
				}
				// 返回限流以在实际工具执行前结束，验证请求已通过协议约束。
				writer.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			httpClient := server.Client()
			defer httpClient.CloseIdleConnections()
			client, err := agentapi.NewClient(agentapi.ClientOptions{
				BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "test-model", HTTPClient: httpClient,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = (Runner{}).Run(context.Background(), client, Context{
				Capability: capability, Resource: json.RawMessage(`{}`),
			})
			var apiErr *agentapi.Error
			if !errors.As(err, &apiErr) || apiErr.Code != "agent_rate_limited" {
				t.Fatalf("Run(%s) = %#v, expected request to pass schema validation", capability, err)
			}
		})
	}
}

func validateProviderToolSchema(schema map[string]any, strict bool) error {
	if schema["type"] == "object" {
		properties, _ := schema["properties"].(map[string]any)
		requiredValue, present := schema["required"]
		required, ok := requiredValue.([]any)
		if present && !ok {
			return fmt.Errorf("object required must be an array")
		}
		if strict && (schema["additionalProperties"] != false || len(required) != len(properties)) {
			return fmt.Errorf("strict objects must forbid extra properties and require every field")
		}
		for name, property := range properties {
			if strict {
				found := false
				for _, requiredName := range required {
					found = found || requiredName == name
				}
				if !found {
					return fmt.Errorf("strict field %s is optional", name)
				}
			}
			if err := validateProviderToolSchema(property.(map[string]any), strict); err != nil {
				return err
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if err := validateProviderToolSchema(items, strict); err != nil {
			return err
		}
	}
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		if branches, ok := schema[keyword].([]any); ok {
			if strict && keyword != "anyOf" {
				return fmt.Errorf("strict schema does not support %s", keyword)
			}
			for _, branch := range branches {
				if err := validateProviderToolSchema(branch.(map[string]any), strict); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
