package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
)

func TestAgentStaticListToolPaginatesLongTitlesWithoutLosingEntries(t *testing.T) {
	for _, test := range []struct{ name, title string }{
		{name: "ascii", title: strings.Repeat("title", 410)},
		{name: "unicode", title: strings.Repeat("剧", 2048)},
		{name: "escaped", title: strings.Repeat("\"\\\n", 512)},
	} {
		t.Run(test.name, func(t *testing.T) {
			title := test.title
			values := make([]scopedRSSReleaseHistory, 50)
			for index := range values {
				values[index] = scopedRSSReleaseHistory{EntryID: uuid.New(), Title: title}
			}
			tool := staticAgentTool("list_release_history", "Read scoped history.", map[string]any{}, values)
			seen := make(map[uuid.UUID]bool)
			offset := 0
			for step := 0; step < 64; step++ {
				arguments := json.RawMessage(`{}`)
				if offset != 0 {
					arguments, _ = json.Marshal(map[string]int{"offset": offset})
				}
				encoded, err := tool.Execute(context.Background(), tool.Definition.Name, arguments)
				if err != nil {
					t.Fatal(err)
				}
				if len(encoded) > agentapi.MaxToolResultBytes {
					t.Fatalf("list response = %d bytes, exceeds tool result limit", len(encoded))
				}
				var page struct {
					Items      []scopedRSSReleaseHistory `json:"items"`
					Total      int                       `json:"total"`
					NextOffset *int                      `json:"nextOffset"`
				}
				if err := json.Unmarshal(encoded, &page); err != nil {
					t.Fatalf("decode page: %v", err)
				}
				if page.Total != len(values) || len(page.Items) == 0 {
					t.Fatalf("page total/items = %d/%d", page.Total, len(page.Items))
				}
				for index, item := range page.Items {
					if seen[item.EntryID] || item.EntryID != values[offset+index].EntryID || item.Title != title {
						t.Fatalf("page lost, duplicated or changed item %s", item.EntryID)
					}
					seen[item.EntryID] = true
				}
				if page.NextOffset == nil {
					break
				}
				if *page.NextOffset != offset+len(page.Items) {
					t.Fatalf("non-contiguous page offset = %d", *page.NextOffset)
				}
				offset = *page.NextOffset
			}
			if len(seen) != len(values) {
				t.Fatalf("read entries = %d, want %d", len(seen), len(values))
			}
		})
	}
}

func TestAgentStaticToolKeepsSmallListAndObjectResults(t *testing.T) {
	for _, value := range []any{[]string{"one", "two"}, map[string]any{"value": "small"}} {
		tool := staticAgentTool("read_context", "Read scoped context.", map[string]any{}, value)
		encoded, err := tool.Execute(context.Background(), tool.Definition.Name, json.RawMessage(`{}`))
		expected, _ := json.Marshal(value)
		if err != nil || string(encoded) != string(expected) {
			t.Fatalf("result = %s, error = %v, want %s", encoded, err, expected)
		}
	}
}
