package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/onprs/emby-auto/backend/internal/agentharness"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
)

// agentToolPageReserve 为页码、总数和 JSON 包装保留空间。
const agentToolPageReserve = 256

func pagedStaticAgentTool(name, description string, encoded json.RawMessage) (agentharness.Tool, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(encoded, &items); err != nil || len(items) < 2 {
		return agentharness.Tool{}, false
	}
	pageBudget := agentapi.MaxToolResultBytes - agentToolPageReserve
	pages := make(map[int]json.RawMessage)
	offsets := make([]int, 0)
	for offset := 0; offset < len(items); {
		end, used, truncated := offset, 0, false
		for end < len(items) {
			item := items[end]
			if len(item)+1 > pageBudget {
				// 单个条目超过整页预算时按字段截断并保留截断标记，
				// 否则分页无法推进且该工具调用必然被循环拒绝。
				item = clampAgentJSON(item, pageBudget-32)
				items[end] = item
				truncated = true
			}
			if used+len(item)+1 > pageBudget {
				break
			}
			used += len(item) + 1
			end++
		}
		if end == offset {
			return agentharness.Tool{}, false
		}
		data := encodeAgentToolPage(items, offset, end, len(items), truncated)
		if len(data) > agentapi.MaxToolResultBytes {
			return agentharness.Tool{}, false
		}
		pages[offset] = data
		offsets = append(offsets, offset)
		offset = end
	}
	return agentharness.Tool{
		Definition: agentapi.ToolDefinition{
			Name: name, Description: description + " 结果按页返回；先使用空参数，再用 offset 读取 nextOffset 指定的页，直到 nextOffset 不再出现。",
			Parameters: strictObject(map[string]any{"offset": map[string]any{"type": "integer", "enum": offsets}}),
		},
		MinimumReadSteps: len(pages),
		Execute: func(_ context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
			var arguments struct {
				Offset json.RawMessage `json:"offset"`
			}
			if err := strictJSON(raw, &arguments); err != nil {
				return nil, agentapi.InvalidToolArguments(err)
			}
			offset := 0
			if len(arguments.Offset) != 0 {
				var requested *int
				if err := json.Unmarshal(arguments.Offset, &requested); err != nil {
					return nil, agentapi.InvalidToolArguments(err)
				}
				if requested == nil {
					return nil, agentapi.InvalidToolArguments(fmt.Errorf("分页起点必须为整数"))
				}
				offset = *requested
			}
			page, ok := pages[offset]
			if !ok {
				return nil, agentapi.InvalidToolArguments(fmt.Errorf("分页起点超出当前列表范围"))
			}
			return page, nil
		},
	}, true
}

func encodeAgentToolPage(items []json.RawMessage, offset, end, total int, truncated bool) json.RawMessage {
	data := make(json.RawMessage, 0, agentapi.MaxToolResultBytes)
	data = append(data, `{"items":[`...)
	for index := offset; index < end; index++ {
		if index > offset {
			data = append(data, ',')
		}
		data = append(data, items[index]...)
	}
	data = append(data, `],"total":`...)
	data = strconv.AppendInt(data, int64(total), 10)
	if end < total {
		data = append(data, `,"nextOffset":`...)
		data = strconv.AppendInt(data, int64(end), 10)
	}
	if truncated {
		data = append(data, `,"truncated":true`...)
	}
	data = append(data, '}')
	return data
}

// clampAgentJSON 按字段截断 JSON 字符串值，使序列化结果不超过 budget 字节。
// 被截断的对象会带 "truncated": true 标记；数组本身没有字段可承载标记，
// 因此截断后的数组包装为带 total 与 truncated 的对象。无法在预算内表达时
// 返回稳定的截断标记，保证调用方不会收到超限或非法 JSON。
func clampAgentJSON(raw json.RawMessage, budget int) json.RawMessage {
	if budget <= 0 {
		return raw
	}
	if len(raw) <= budget {
		return raw
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	items, isArray := value.([]any)
	longest := longestAgentJSONString(value)
	if longest == 0 {
		return truncatedAgentJSONMarker()
	}
	// 二分查找能装入预算的最大截断长度，尽量保留可读内容。
	low, high, found := 0, longest-1, json.RawMessage(nil)
	for low <= high {
		limit := low + (high-low)/2
		clamped, changed := truncateAgentJSONStrings(value, limit)
		var (
			encoded []byte
			err     error
		)
		if isArray && changed {
			encoded, err = json.Marshal(map[string]any{"items": clamped, "total": len(items), "truncated": true})
		} else {
			encoded, err = json.Marshal(clamped)
		}
		if err == nil && len(encoded) <= budget {
			found = encoded
			low = limit + 1
			continue
		}
		high = limit - 1
	}
	if found != nil {
		return found
	}
	return truncatedAgentJSONMarker()
}

func truncatedAgentJSONMarker() json.RawMessage {
	marker, _ := json.Marshal(map[string]any{"truncated": true, "error": "agent_tool_result_too_large"})
	return marker
}

// longestAgentJSONString 返回载荷中最长字符串的 rune 长度。
func longestAgentJSONString(value any) int {
	switch typed := value.(type) {
	case string:
		return len([]rune(typed))
	case map[string]any:
		longest := 0
		for _, child := range typed {
			if length := longestAgentJSONString(child); length > longest {
				longest = length
			}
		}
		return longest
	case []any:
		longest := 0
		for _, child := range typed {
			if length := longestAgentJSONString(child); length > longest {
				longest = length
			}
		}
		return longest
	default:
		return 0
	}
}

// truncateAgentJSONStrings 把超过 maxRunes 的字符串截断到该长度。
// 返回值是副本，调用方可以安全地尝试多个长度而不会累积修改。
func truncateAgentJSONStrings(value any, maxRunes int) (any, bool) {
	switch typed := value.(type) {
	case string:
		runes := []rune(typed)
		if len(runes) <= maxRunes {
			return typed, false
		}
		return string(runes[:maxRunes]), true
	case map[string]any:
		cloned := make(map[string]any, len(typed)+1)
		changed := false
		for key, child := range typed {
			clamped, childChanged := truncateAgentJSONStrings(child, maxRunes)
			cloned[key] = clamped
			changed = changed || childChanged
		}
		if changed {
			cloned["truncated"] = true
		}
		return cloned, changed
	case []any:
		cloned := make([]any, len(typed))
		changed := false
		for index, child := range typed {
			clamped, childChanged := truncateAgentJSONStrings(child, maxRunes)
			cloned[index] = clamped
			changed = changed || childChanged
		}
		return cloned, changed
	default:
		return value, false
	}
}
