package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/onprs/emby-auto/backend/internal/agentharness"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
)

func pagedStaticAgentTool(name, description string, encoded json.RawMessage) (agentharness.Tool, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(encoded, &items); err != nil || len(items) < 2 {
		return agentharness.Tool{}, false
	}
	pages := make(map[int]json.RawMessage)
	offsets := make([]int, 0)
	for offset := 0; offset < len(items); {
		end, used := offset, 0
		// 为页码、总数和 JSON 包装保留空间。
		for end < len(items) && used+len(items[end])+1 <= agentapi.MaxToolResultBytes-256 {
			used += len(items[end]) + 1
			end++
		}
		if end == offset {
			return agentharness.Tool{}, false
		}
		// 条目已由 JSON 解析器验证，保留原始编码并缓存完整分页结果。
		data := make(json.RawMessage, 0, used+128)
		data = append(data, `{"items":[`...)
		for index := offset; index < end; index++ {
			if index > offset {
				data = append(data, ',')
			}
			data = append(data, items[index]...)
		}
		data = append(data, `],"total":`...)
		data = strconv.AppendInt(data, int64(len(items)), 10)
		if end < len(items) {
			data = append(data, `,"nextOffset":`...)
			data = strconv.AppendInt(data, int64(end), 10)
		}
		data = append(data, '}')
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
				return nil, err
			}
			offset := 0
			if len(arguments.Offset) != 0 {
				var requested *int
				if err := json.Unmarshal(arguments.Offset, &requested); err != nil {
					return nil, err
				}
				if requested == nil {
					return nil, fmt.Errorf("分页起点必须为整数")
				}
				offset = *requested
			}
			page, ok := pages[offset]
			if !ok {
				return nil, fmt.Errorf("分页起点超出当前列表范围")
			}
			return page, nil
		},
	}, true
}
