//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

// TestRSSSubscriptionListSQLPaginationMatchesFullSortIntegration 验证 SQL 层
// cursor 分页与独立计算的期望排序序列完全一致：覆盖 name/series_title/
// source_season/enabled/next_poll_at 的 asc/desc、next_poll_at 的 NULL 排序
// 以及跨页无重复无遗漏。
func TestRSSSubscriptionListSQLPaginationMatchesFullSortIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	base := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)

	// 每订阅绑定独立媒体系列，title 与订阅名错开以覆盖 series_title 排序。
	names := []string{"delta", "Alpha", "Bravo", "echo", "Charlie", "Foxtrot", "golf"}
	titles := []string{"Zulu", "Alpha Show", "Mike", "Tango", "Alpha", "Sierra", "Zulu"}
	seasons := []int{2, 1, 3, 1, 2, 4, 1}
	enabled := []bool{true, false, true, true, false, true, false}
	nextPollAt := []*time.Time{nil, timePtr(base.Add(time.Hour)), nil, timePtr(base.Add(2 * time.Hour)), timePtr(base.Add(30 * time.Minute)), nil, timePtr(base.Add(-time.Hour))}

	subscriptions := make([]domain.RSSSubscription, 0, len(names))
	for index := range names {
		seriesID := uuid.New()
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, $3)`, seriesID, time.Now().UnixNano()+int64(index), titles[index]); err != nil {
			t.Fatal(err)
		}
		subscriptionID := uuid.New()
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, next_poll_at, created_at)
VALUES ($1, $2, $3, $4, $5, 900, $6, $7, $8)
`, subscriptionID, seriesID, names[index], "https://example.test/"+subscriptionID.String()+".xml", enabled[index], seasons[index], nextPollAt[index], base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		item, err := workflow.GetSubscription(ctx, subscriptionID)
		if err != nil {
			t.Fatalf("GetSubscription(%s) error = %v", subscriptionID, err)
		}
		subscriptions = append(subscriptions, item)
	}

	cases := []struct {
		field     string
		direction int
	}{
		{field: "name", direction: 1},
		{field: "name", direction: -1},
		{field: "series_title", direction: 1},
		{field: "series_title", direction: -1},
		{field: "source_season", direction: 1},
		{field: "source_season", direction: -1},
		{field: "enabled", direction: 1},
		{field: "enabled", direction: -1},
		{field: "next_poll_at", direction: 1},
		{field: "next_poll_at", direction: -1},
	}
	for _, testCase := range cases {
		t.Run(testCase.field+fmt.Sprint(testCase.direction), func(t *testing.T) {
			expected := append([]domain.RSSSubscription(nil), subscriptions...)
			sort.SliceStable(expected, func(i, j int) bool {
				left, right := expected[i], expected[j]
				comparison := 0
				switch testCase.field {
				case "series_title":
					comparison = strings.Compare(strings.ToLower(left.SeriesTitle), strings.ToLower(right.SeriesTitle))
				case "source_season":
					switch {
					case left.SourceSeason < right.SourceSeason:
						comparison = -1
					case left.SourceSeason > right.SourceSeason:
						comparison = 1
					}
				case "enabled":
					if left.Enabled != right.Enabled {
						if !left.Enabled {
							comparison = -1
						} else {
							comparison = 1
						}
					}
				case "next_poll_at":
					comparison = compareOptionalTimes(left.NextPollAt, right.NextPollAt)
				default:
					comparison = strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
				}
				if comparison == 0 {
					comparison = strings.Compare(left.ID.String(), right.ID.String())
				}
				return testCase.direction*comparison < 0
			})

			var cursor *uuid.UUID
			collected := make([]uuid.UUID, 0, len(expected))
			for {
				sortBy, sortOrder := testCase.field, "asc"
				if testCase.direction < 0 {
					sortOrder = "desc"
				}
				page, err := workflow.ListSubscriptions(ctx, cursor, 3, nil, &sortBy, &sortOrder)
				if err != nil {
					t.Fatalf("ListSubscriptions(field=%s direction=%d) error = %v", testCase.field, testCase.direction, err)
				}
				for _, item := range page.Items {
					collected = append(collected, item.ID)
				}
				cursor = page.NextCursor
				if cursor == nil {
					break
				}
				if len(collected) > len(expected) {
					t.Fatalf("pagination produced more items than the full list")
				}
			}

			if len(collected) != len(expected) {
				t.Fatalf("pagination collected %d items, want %d", len(collected), len(expected))
			}
			for index := range expected {
				if collected[index] != expected[index].ID {
					t.Fatalf("item %d = %s, want %s (full order %v, collected %v)", index, collected[index], expected[index].ID, idsOf(expected), collected)
				}
			}
		})
	}
}

// TestRSSSubscriptionListSQLQueryPaginationIntegration 验证 query 在 SQL
// cursor 与 limit 之前生效，并覆盖跨页、稳定并列键、作品名匹配、空结果、
// LIKE 元字符的字面量匹配，以及 progress 特殊排序路径。
func TestRSSSubscriptionListSQLQueryPaginationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	base := time.Date(2026, 8, 16, 3, 0, 0, 0, time.UTC)

	type subscriptionFixture struct {
		id           uuid.UUID
		seriesID     uuid.UUID
		tmdbSeriesID int64
		name         string
		seriesTitle  string
	}
	fixtures := []subscriptionFixture{
		{id: uuid.MustParse("21000000-0000-0000-0000-000000000001"), seriesID: uuid.MustParse("22000000-0000-0000-0000-000000000001"), tmdbSeriesID: 9301, name: "Needle Feed", seriesTitle: "Other One"},
		{id: uuid.MustParse("21000000-0000-0000-0000-000000000002"), seriesID: uuid.MustParse("22000000-0000-0000-0000-000000000002"), tmdbSeriesID: 9302, name: "needle feed", seriesTitle: "Other Two"},
		{id: uuid.MustParse("21000000-0000-0000-0000-000000000003"), seriesID: uuid.MustParse("22000000-0000-0000-0000-000000000003"), tmdbSeriesID: 9303, name: "Alpha Needle", seriesTitle: "Other Three"},
		{id: uuid.MustParse("21000000-0000-0000-0000-000000000004"), seriesID: uuid.MustParse("22000000-0000-0000-0000-000000000004"), tmdbSeriesID: 9304, name: "Beta Feed", seriesTitle: "Needle Series"},
		{id: uuid.MustParse("21000000-0000-0000-0000-000000000005"), seriesID: uuid.MustParse("22000000-0000-0000-0000-000000000005"), tmdbSeriesID: 9305, name: "Delta Feed", seriesTitle: "Other Four"},
		{id: uuid.MustParse("21000000-0000-0000-0000-000000000006"), seriesID: uuid.MustParse("22000000-0000-0000-0000-000000000006"), tmdbSeriesID: 9306, name: "Omega Needle", seriesTitle: "Other Five"},
		{id: uuid.MustParse("21000000-0000-0000-0000-000000000007"), seriesID: uuid.MustParse("22000000-0000-0000-0000-000000000007"), tmdbSeriesID: 9307, name: "Percent Needle %", seriesTitle: "Other Six"},
	}
	for index, item := range fixtures {
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, $3)`, item.seriesID, item.tmdbSeriesID, item.seriesTitle); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, created_at, updated_at)
VALUES ($1, $2, $3, $4, true, 900, 1, $5, $5)
`, item.id, item.seriesID, item.name, "https://example.test/query-"+item.id.String()+".xml", base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	query, sortBy, sortOrder := "nEeDlE", "name", "asc"
	expected := []uuid.UUID{fixtures[2].id, fixtures[3].id, fixtures[0].id, fixtures[1].id, fixtures[5].id, fixtures[6].id}
	var cursor *uuid.UUID
	collected := make([]uuid.UUID, 0, len(expected))
	seen := make(map[uuid.UUID]struct{}, len(expected))
	pageSizes := []int{2, 2, 2}
	for pageIndex := 0; ; pageIndex++ {
		page, err := workflow.ListSubscriptions(ctx, cursor, 2, &query, &sortBy, &sortOrder)
		if err != nil {
			t.Fatalf("ListSubscriptions(query page %d) error = %v", pageIndex+1, err)
		}
		if pageIndex >= len(pageSizes) || len(page.Items) != pageSizes[pageIndex] {
			t.Fatalf("query page %d size = %d, want page sizes %v", pageIndex+1, len(page.Items), pageSizes)
		}
		for _, item := range page.Items {
			if _, duplicate := seen[item.ID]; duplicate {
				t.Fatalf("query pagination returned duplicate subscription %s", item.ID)
			}
			seen[item.ID] = struct{}{}
			collected = append(collected, item.ID)
		}
		cursor = page.NextCursor
		if cursor == nil {
			if pageIndex+1 != len(pageSizes) {
				t.Fatalf("query pagination ended after %d pages, want %d", pageIndex+1, len(pageSizes))
			}
			break
		}
	}
	if len(collected) != len(expected) {
		t.Fatalf("query pagination returned %d subscriptions, want %d: %v", len(collected), len(expected), collected)
	}
	for index, want := range expected {
		if collected[index] != want {
			t.Fatalf("query result %d = %s, want %s; full result %v", index, collected[index], want, collected)
		}
	}

	missing := "does-not-exist"
	empty, err := workflow.ListSubscriptions(ctx, nil, 2, &missing, &sortBy, &sortOrder)
	if err != nil {
		t.Fatalf("ListSubscriptions(no matches) error = %v", err)
	}
	if len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatalf("no-match page = %#v, want empty page without cursor", empty)
	}

	literalPercent := "%"
	literal, err := workflow.ListSubscriptions(ctx, nil, 2, &literalPercent, &sortBy, &sortOrder)
	if err != nil {
		t.Fatalf("ListSubscriptions(literal percent) error = %v", err)
	}
	if len(literal.Items) != 1 || literal.Items[0].ID != fixtures[6].id || literal.NextCursor != nil {
		t.Fatalf("literal percent page = %#v, want only %s", literal, fixtures[6].id)
	}

	progressSort := "progress"
	progressPage, err := workflow.ListSubscriptions(ctx, nil, 10, &query, &progressSort, &sortOrder)
	if err != nil {
		t.Fatalf("ListSubscriptions(query+progress) error = %v", err)
	}
	progressExpected := []uuid.UUID{fixtures[0].id, fixtures[1].id, fixtures[2].id, fixtures[3].id, fixtures[5].id, fixtures[6].id}
	if len(progressPage.Items) != len(progressExpected) || progressPage.NextCursor != nil {
		t.Fatalf("query+progress page = %#v, want %d items without cursor", progressPage, len(progressExpected))
	}
	for index, want := range progressExpected {
		if progressPage.Items[index].ID != want {
			t.Fatalf("query+progress item %d = %s, want %s", index, progressPage.Items[index].ID, want)
		}
	}
}

// TestRSSSubscriptionListSQLPaginationRejectsUnknownCursorIntegration
// 验证 SQL 路径对不存在的 cursor 保持 invalid_cursor 语义。
func TestRSSSubscriptionListSQLPaginationRejectsUnknownCursorIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)

	_, err := workflow.ListSubscriptions(ctx, uuidPtr(uuid.New()), 10, nil, nil, nil)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "invalid_cursor" {
		t.Fatalf("ListSubscriptions(unknown cursor) error = %#v, want invalid_cursor", err)
	}
}

func compareOptionalTimes(left, right *time.Time) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return 1
	case right == nil:
		return -1
	default:
		switch {
		case left.Before(*right):
			return -1
		case left.After(*right):
			return 1
		default:
			return 0
		}
	}
}

func idsOf(items []domain.RSSSubscription) []uuid.UUID {
	ids := make([]uuid.UUID, len(items))
	for index, item := range items {
		ids[index] = item.ID
	}
	return ids
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func uuidPtr(value uuid.UUID) *uuid.UUID {
	return &value
}
