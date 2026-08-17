//go:build integration

package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
)

func TestRSSSubscriptionProgressSQLCursorPaginationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)

	ids := []uuid.UUID{
		uuid.MustParse("31000000-0000-0000-0000-000000000001"),
		uuid.MustParse("31000000-0000-0000-0000-000000000002"),
		uuid.MustParse("31000000-0000-0000-0000-000000000003"),
		uuid.MustParse("31000000-0000-0000-0000-000000000004"),
		uuid.MustParse("31000000-0000-0000-0000-000000000005"),
		uuid.MustParse("31000000-0000-0000-0000-000000000006"),
		uuid.MustParse("31000000-0000-0000-0000-000000000007"),
	}
	downloadProgress := []*float64{
		nil,
		nil,
		float64Ptr(0.25),
		float64Ptr(0.5),
		float64Ptr(0.5),
		float64Ptr(0.75),
		nil,
	}
	wantProgress := map[uuid.UUID]float64{
		ids[0]: 0,
		ids[1]: 0.02,
		ids[2]: 0.09,
		ids[3]: 0.16,
		ids[4]: 0.16,
		ids[5]: 0.23,
		ids[6]: 1,
	}

	for index, subscriptionID := range ids {
		completed := index == len(ids)-1
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
    id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, completed_at
) VALUES ($1, $2, $3, $4, $5, 900, 1, CASE WHEN $6 THEN now() ELSE NULL END)
`, subscriptionID, fixture.seriesID, "Progress order "+subscriptionID.String(), "https://example.test/"+subscriptionID.String()+".xml", !completed, completed); err != nil {
			t.Fatal(err)
		}
		if index == 0 || completed {
			continue
		}
		entryID, acquisitionID := uuid.New(), uuid.New()
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, $3, $4, 'https://example.test/episode.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued')
`, entryID, subscriptionID, "guid:"+entryID.String(), "Progress S01E01 "+subscriptionID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id)
VALUES ($1, $2, 'rss', $3)
`, acquisitionID, fixture.seriesID, entryID); err != nil {
			t.Fatal(err)
		}
		if downloadProgress[index] != nil {
			if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, progress)
VALUES ($1, $2, 'downloading', $3)
`, uuid.New(), acquisitionID, *downloadProgress[index]); err != nil {
				t.Fatal(err)
			}
		}
	}

	reconciled, err := workflow.ReconcileSubscriptionProgress(ctx)
	if err != nil || reconciled != len(ids) {
		t.Fatalf("ReconcileSubscriptionProgress() = %d, %v, want %d, nil", reconciled, err, len(ids))
	}

	cases := []struct {
		name     string
		order    string
		expected []uuid.UUID
	}{
		{name: "ascending", order: "asc", expected: append([]uuid.UUID(nil), ids...)},
		{name: "descending", order: "desc", expected: reverseUUIDs(ids)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			sortBy := "progress"
			var cursor *uuid.UUID
			collected := make([]uuid.UUID, 0, len(ids))
			seen := make(map[uuid.UUID]struct{}, len(ids))
			for {
				page, err := workflow.ListSubscriptions(ctx, cursor, 2, nil, &sortBy, &test.order)
				if err != nil {
					t.Fatalf("ListSubscriptions(cursor=%v) error = %v", cursor, err)
				}
				for _, item := range page.Items {
					if _, duplicate := seen[item.ID]; duplicate {
						t.Fatalf("duplicate subscription %s across progress pages", item.ID)
					}
					seen[item.ID] = struct{}{}
					collected = append(collected, item.ID)
					if math.Abs(item.OverallProgress-wantProgress[item.ID]) > 1e-12 {
						t.Fatalf("subscription %s progress = %.12f, want %.12f", item.ID, item.OverallProgress, wantProgress[item.ID])
					}
					if item.RetryableTaskCount != 0 {
						t.Fatalf("subscription %s retryable task count = %d, want 0", item.ID, item.RetryableTaskCount)
					}
					detail, err := workflow.GetSubscription(ctx, item.ID)
					if err != nil {
						t.Fatal(err)
					}
					if detail.OverallProgress != item.OverallProgress || detail.TaskCount != item.TaskCount ||
						detail.CompletedTaskCount != item.CompletedTaskCount || detail.AttentionTaskCount != item.AttentionTaskCount {
						t.Fatalf("list/detail progress mismatch for %s: list %#v detail %#v", item.ID, item, detail)
					}
				}
				cursor = page.NextCursor
				if cursor == nil {
					break
				}
				if len(collected) > len(ids) {
					t.Fatal("progress pagination returned more rows than fixtures")
				}
			}
			if len(collected) != len(test.expected) {
				t.Fatalf("collected %d subscriptions, want %d", len(collected), len(test.expected))
			}
			for index, want := range test.expected {
				if collected[index] != want {
					t.Fatalf("subscription %d = %s, want %s; order %v", index, collected[index], want, collected)
				}
			}
		})
	}

	sortBy, ascending := "progress", "asc"
	missingQuery := "no matching progress subscription"
	empty, err := workflow.ListSubscriptions(ctx, nil, 2, &missingQuery, &sortBy, &ascending)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatalf("empty progress page = %#v", empty)
	}

	_, err = workflow.ListSubscriptions(ctx, uuidPtr(uuid.New()), 2, nil, &sortBy, &ascending)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "invalid_cursor" {
		t.Fatalf("unknown progress cursor error = %#v, want invalid_cursor", err)
	}
}

func TestRSSSubscriptionProgressListReconcilesBeforeReturningDirtySnapshotIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	ids := seedRSSProgressFixture(t, ctx, fixture, 0)
	if _, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE downloads SET progress = 0.5 WHERE id = $1`, ids.downloadID); err != nil {
		t.Fatal(err)
	}

	sortBy, ascending := "progress", "asc"
	if _, err := workflow.listSubscriptionsByProgressSnapshot(ctx, nil, 10, nil, 1); !errors.Is(err, errRSSSubscriptionProgressSnapshotNotReady) {
		t.Fatalf("dirty snapshot error = %v, want snapshot-not-ready", err)
	}
	page, err := workflow.ListSubscriptions(ctx, nil, 10, nil, &sortBy, &ascending)
	if err != nil {
		t.Fatalf("ListSubscriptions() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != ids.subscriptionID || math.Abs(page.Items[0].OverallProgress-0.16) > 1e-12 {
		t.Fatalf("reconciled page = %#v, want one subscription at progress 0.16", page)
	}
}

func TestRSSSubscriptionProgressRejectsNewerPersistedModelIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	ids := seedRSSProgressFixture(t, ctx, fixture, 0)
	if _, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE rss_subscription_progress
SET model_version = $2
WHERE subscription_id = $1
`, ids.subscriptionID, rssSubscriptionProgressModelVersion+1); err != nil {
		t.Fatal(err)
	}

	if _, err := workflow.ReconcileSubscriptionProgress(ctx); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("ReconcileSubscriptionProgress() error = %v, want newer model rejection", err)
	}
	sortBy, ascending := "progress", "asc"
	if _, err := workflow.ListSubscriptions(ctx, nil, 10, nil, &sortBy, &ascending); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("ListSubscriptions() error = %v, want newer model rejection", err)
	}
}

func TestRSSSubscriptionProgressSortIndexSupportsBothDirectionsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, setting := range []string{
		`SET LOCAL enable_seqscan = off`,
		`SET LOCAL enable_bitmapscan = off`,
		`SET LOCAL enable_sort = off`,
	} {
		if _, err := tx.Exec(ctx, setting); err != nil {
			t.Fatal(err)
		}
	}

	plans := map[string]string{
		"ascending": `
EXPLAIN (COSTS OFF)
SELECT subscription.id
FROM rss_subscription_progress AS progress
JOIN rss_subscriptions AS subscription ON subscription.id = progress.subscription_id
JOIN media_series AS series ON series.id = subscription.series_id
WHERE subscription.deleted_at IS NULL
  AND NOT progress.dirty
  AND progress.model_version = $1
  AND (progress.overall_progress, progress.subscription_id) >
      (0.5::double precision, '00000000-0000-0000-0000-000000000000'::uuid)
ORDER BY progress.overall_progress ASC, progress.subscription_id ASC
LIMIT 101
`,
		"descending": `
EXPLAIN (COSTS OFF)
SELECT subscription.id
FROM rss_subscription_progress AS progress
JOIN rss_subscriptions AS subscription ON subscription.id = progress.subscription_id
JOIN media_series AS series ON series.id = subscription.series_id
WHERE subscription.deleted_at IS NULL
  AND NOT progress.dirty
  AND progress.model_version = $1
  AND (progress.overall_progress, progress.subscription_id) <
      (0.5::double precision, 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid)
ORDER BY progress.overall_progress DESC, progress.subscription_id DESC
LIMIT 101
`,
	}
	for name, statement := range plans {
		t.Run(name, func(t *testing.T) {
			rows, err := tx.Query(ctx, statement, rssSubscriptionProgressModelVersion)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			lines := make([]string, 0)
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(lines, "\n")
			if !strings.Contains(plan, "rss_subscription_progress_sort_idx") {
				t.Fatalf("query plan does not use progress sort index:\n%s", plan)
			}
		})
	}
}

func reverseUUIDs(values []uuid.UUID) []uuid.UUID {
	result := append([]uuid.UUID(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func float64Ptr(value float64) *float64 {
	return &value
}
