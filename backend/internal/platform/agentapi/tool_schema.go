package agentapi

// prepareToolSchema 保留业务字段的可选语义，并仅对兼容的 Schema 开启严格模式。
func prepareToolSchema(schema map[string]any) (map[string]any, bool) {
	prepared := make(map[string]any, len(schema))
	strict := true
	for key, value := range schema {
		switch key {
		case "const":
			if _, hasEnum := schema["enum"]; hasEnum {
				prepared[key], strict = value, false
			} else {
				prepared["enum"] = []any{value}
			}
		case "properties":
			properties, ok := value.(map[string]any)
			if !ok {
				prepared[key], strict = value, false
				continue
			}
			normalized := make(map[string]any, len(properties))
			for name, property := range properties {
				child, ok := property.(map[string]any)
				if !ok {
					normalized[name], strict = property, false
					continue
				}
				var childStrict bool
				normalized[name], childStrict = prepareToolSchema(child)
				strict = strict && childStrict
			}
			prepared[key] = normalized
		case "items":
			child, ok := value.(map[string]any)
			if !ok {
				prepared[key], strict = value, false
				continue
			}
			var childStrict bool
			prepared[key], childStrict = prepareToolSchema(child)
			strict = strict && childStrict
		case "oneOf", "anyOf", "allOf":
			branches, ok := value.([]any)
			if !ok {
				prepared[key], strict = value, false
				continue
			}
			normalized := make([]any, 0, len(branches))
			for _, branch := range branches {
				child, ok := branch.(map[string]any)
				if !ok {
					normalized, strict = append(normalized, branch), false
					continue
				}
				preparedChild, childStrict := prepareToolSchema(child)
				normalized = append(normalized, preparedChild)
				strict = strict && childStrict
			}
			prepared[key] = normalized
			if key != "anyOf" {
				strict = false
			}
		case "type", "description", "enum", "format", "pattern", "minLength", "maxLength",
			"minimum", "maximum", "multipleOf", "minItems", "maxItems", "required", "additionalProperties":
			prepared[key] = value
		default:
			prepared[key], strict = value, false
		}
	}
	if schema["type"] == "object" {
		properties, _ := schema["properties"].(map[string]any)
		required := make(map[string]struct{}, len(properties))
		switch values := schema["required"].(type) {
		case []string:
			if values == nil {
				prepared["required"] = []string{}
			}
			for _, name := range values {
				required[name] = struct{}{}
			}
		case []any:
			if values == nil {
				prepared["required"] = []string{}
			}
			for _, value := range values {
				name, ok := value.(string)
				if !ok {
					strict = false
				}
				required[name] = struct{}{}
			}
		case nil:
			prepared["required"] = []string{}
		default:
			strict = false
		}
		if schema["additionalProperties"] != false || len(properties) != len(required) {
			strict = false
		}
		for name := range properties {
			if _, ok := required[name]; !ok {
				strict = false
			}
		}
	}
	return prepared, strict
}

func prepareToolParameters(schema map[string]any) (map[string]any, bool) {
	prepared, strict := prepareToolSchema(schema)
	if prepared["type"] != "object" {
		// 映射工具使用对象的互斥分支；API 顶层仍需声明对象类型。
		prepared["type"] = "object"
		strict = false
	}
	if _, union := prepared["anyOf"]; union {
		strict = false
	}
	return prepared, strict
}
