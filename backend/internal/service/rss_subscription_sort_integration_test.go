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
				page, err := workflow.ListSubscriptions(ctx, cursor, 3, &sortBy, &sortOrder)
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

// TestRSSSubscriptionListSQLPaginationRejectsUnknownCursorIntegration
// 验证 SQL 路径对不存在的 cursor 保持 invalid_cursor 语义。
func TestRSSSubscriptionListSQLPaginationRejectsUnknownCursorIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)

	_, err := workflow.ListSubscriptions(ctx, uuidPtr(uuid.New()), 10, nil, nil)
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
