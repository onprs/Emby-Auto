package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
)

func FuzzAgentStaticListPages(f *testing.F) {
	for _, seed := range []struct {
		title string
		count uint8
	}{
		{title: "", count: 0},
		{title: "title", count: 50},
		{title: strings.Repeat("剧", 2048), count: 120},
		{title: strings.Repeat("\"\\\n", 512), count: 50},
		{title: string([]byte{0xff, 0xfe, 0x00}), count: 50},
	} {
		f.Add(seed.title, seed.count)
	}
	f.Fuzz(func(t *testing.T, rawTitle string, count uint8) {
		title := boundedAgentText(rawTitle, 2048)
		values := make([]scopedEpisode, int(count))
		for index := range values {
			id := uuid.UUID{1}
			id[15] = byte(index + 1)
			values[index] = scopedEpisode{ID: id, Season: 1, Episode: index + 1, Title: title}
		}
		tool := staticAgentTool("list_tmdb_episodes", "Read scoped episodes.", map[string]any{}, values)
		offset, observed := 0, 0
		for range 64 {
			arguments := json.RawMessage(`{}`)
			if offset != 0 {
				arguments, _ = json.Marshal(map[string]int{"offset": offset})
			}
			encoded, err := tool.Execute(context.Background(), tool.Definition.Name, arguments)
			if err != nil || len(encoded) > agentapi.MaxToolResultBytes || !json.Valid(encoded) {
				t.Fatalf("invalid tool page: error %v, bytes %d", err, len(encoded))
			}
			var page struct {
				Items      []scopedEpisode `json:"items"`
				Total      int             `json:"total"`
				NextOffset *int            `json:"nextOffset"`
			}
			if encoded[0] == '[' {
				err = json.Unmarshal(encoded, &page.Items)
				page.Total = len(values)
			} else {
				err = json.Unmarshal(encoded, &page)
			}
			if err != nil || page.Total != len(values) || observed+len(page.Items) > len(values) {
				t.Fatalf("invalid page metadata: error %v, total %d, observed %d, items %d", err, page.Total, observed, len(page.Items))
			}
			for _, item := range page.Items {
				expected := values[observed]
				expected.Title = string([]rune(expected.Title))
				if item != expected {
					t.Fatalf("page changed or reordered item %d", observed)
				}
				observed++
			}
			if page.NextOffset == nil {
				break
			}
			if *page.NextOffset != observed || *page.NextOffset <= offset {
				t.Fatalf("invalid continuation offset %d", *page.NextOffset)
			}
			offset = *page.NextOffset
		}
		if observed != len(values) {
			t.Fatalf("observed %d entries, want %d", observed, len(values))
		}
	})
}
