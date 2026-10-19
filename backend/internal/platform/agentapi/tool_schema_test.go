package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestToolLoopPreparesCompatibleSchemasWithoutChangingOptionalFields(t *testing.T) {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	stringField := map[string]any{"type": "string"}
	for _, test := range []struct {
		name   string
		schema map[string]any
		strict bool
	}{
		{name: "empty_object", schema: object(map[string]any{}), strict: true},
		{name: "required_fields", schema: object(map[string]any{"value": stringField}, "value"), strict: true},
		{name: "optional_fields", schema: object(map[string]any{"value": stringField}), strict: false},
		{name: "nested_optional", schema: object(map[string]any{"items": map[string]any{"type": "array", "items": object(map[string]any{"value": stringField})}}, "items"), strict: false},
		{name: "root_union", schema: map[string]any{"oneOf": []any{object(map[string]any{"value": stringField}, "value"), object(map[string]any{"other": stringField}, "other")}}, strict: false},
		{name: "constant", schema: object(map[string]any{"value": map[string]any{"type": "string", "const": "fixed"}}, "value"), strict: true},
		{name: "constant_with_enum", schema: object(map[string]any{"value": map[string]any{"type": "string", "const": "fixed", "enum": []any{"fixed", "other"}}}, "value"), strict: false},
		{name: "nil_required_array", schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []any(nil), "additionalProperties": false}, strict: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, err := json.Marshal(test.schema)
			if err != nil {
				t.Fatal(err)
			}
			client := agentTestClient(t, agentToolResponse([]toolCall{agentTestToolCall("submit-1", "submit_result", `{}`)}, "tool_calls"))
			client.http.Transport = agentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				var payload chatRequest
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				definition := payload.Tools[0].Function
				if definition.Strict != test.strict || definition.Parameters["type"] != "object" {
					t.Errorf("tool definition = %#v, want strict %t and object root", definition, test.strict)
				}
				if test.name == "empty_object" || test.name == "nil_required_array" {
					if required, ok := definition.Parameters["required"].([]any); !ok || len(required) != 0 {
						t.Errorf("empty object's required = %#v, want empty array", definition.Parameters["required"])
					}
				}
				if test.name == "constant" {
					property := definition.Parameters["properties"].(map[string]any)["value"].(map[string]any)
					values, ok := property["enum"].([]any)
					if property["const"] != nil || !ok || len(values) != 1 || values[0] != "fixed" {
						t.Errorf("constant constraint = %#v", property)
					}
				}
				return agentResponse(agentToolResponse([]toolCall{agentTestToolCall("submit-1", "submit_result", `{}`)}, "tool_calls")), nil
			})
			request := agentTestLoopRequest()
			request.Tools = []ToolDefinition{{Name: "submit_result", Parameters: test.schema}}
			if _, err := client.RunToolLoop(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(test.schema)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("caller schema was changed: before %s, after %s, error %v", before, after, err)
			}
		})
	}
}
