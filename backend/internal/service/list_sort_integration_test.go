//go:build integration

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestLifecycleAndRSSColumnSortingAcrossCursorPagesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	base := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)

	seriesTitles := []string{"Zulu Show", "Alpha Show", "Mike Show"}
	seriesIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for index, id := range seriesIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, $3)`, id, time.Now().UnixNano()+int64(index), seriesTitles[index]); err != nil {
			t.Fatal(err)
		}
	}
	acquisitionIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for index, id := range acquisitionIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_at, updated_at) VALUES ($1, $2, 'manual', $3, $4, $4)`, id, seriesIDs[index], fmt.Sprintf("manual://sort/%d", index), base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status, progress, created_at, updated_at) VALUES ($1, $2, 'downloading', 0.5, $3, $3), ($4, $5, 'completed', 1, $6, $6)`, uuid.New(), acquisitionIDs[1], base.Add(time.Minute), uuid.New(), acquisitionIDs[2], base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	reads := NewReadService(queries)
	ascending, descending := "asc", "desc"

	updatedAt := "updated_at"
	page, err := reads.ListAcquisitions(ctx, nil, 2, nil, nil, nil, &updatedAt, &ascending)
	if err != nil {
		t.Fatalf("ListAcquisitions(updated_at asc) error = %v", err)
	}
	assertAcquisitionIDs(t, page, acquisitionIDs[:2])
	if page.NextCursor == nil || *page.NextCursor != acquisitionIDs[1] {
		t.Fatalf("acquisition next cursor = %v", page.NextCursor)
	}
	page, err = reads.ListAcquisitions(ctx, page.NextCursor, 2, nil, nil, nil, &updatedAt, &ascending)
	if err != nil {
		t.Fatalf("ListAcquisitions(updated_at asc page 2) error = %v", err)
	}
	assertAcquisitionIDs(t, page, acquisitionIDs[2:])

	content := "content"
	page, err = reads.ListAcquisitions(ctx, nil, 3, nil, nil, nil, &content, &ascending)
	if err != nil {
		t.Fatalf("ListAcquisitions(content asc) error = %v", err)
	}
	assertAcquisitionIDs(t, page, []uuid.UUID{acquisitionIDs[1], acquisitionIDs[2], acquisitionIDs[0]})
	progress := "progress"
	page, err = reads.ListAcquisitions(ctx, nil, 3, nil, nil, nil, &progress, &descending)
	if err != nil {
		t.Fatalf("ListAcquisitions(progress desc) error = %v", err)
	}
	assertAcquisitionIDs(t, page, []uuid.UUID{acquisitionIDs[2], acquisitionIDs[1], acquisitionIDs[0]})

	subscriptionIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	subscriptionNames := []string{"Zulu Feed", "Alpha Feed", "Mike Feed"}
	for index, id := range subscriptionIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, created_at, updated_at) VALUES ($1, $2, $3, $4, false, 900, $5, $6, $6)`, id, seriesIDs[0], subscriptionNames[index], fmt.Sprintf("https://example.test/%d.xml", index), index+1, base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	rss := NewRSSWorkflow(queries, database.NewTransactor(pool), nil)
	name := "name"
	subscriptions, err := rss.ListSubscriptions(ctx, nil, 2, nil, &name, &ascending)
	if err != nil {
		t.Fatalf("ListSubscriptions(name asc) error = %v", err)
	}
	assertSubscriptionIDs(t, subscriptions, []uuid.UUID{subscriptionIDs[1], subscriptionIDs[2]})
	subscriptions, err = rss.ListSubscriptions(ctx, subscriptions.NextCursor, 2, nil, &name, &ascending)
	if err != nil {
		t.Fatalf("ListSubscriptions(name asc page 2) error = %v", err)
	}
	assertSubscriptionIDs(t, subscriptions, []uuid.UUID{subscriptionIDs[0]})
	sourceSeason := "source_season"
	subscriptions, err = rss.ListSubscriptions(ctx, nil, 3, nil, &sourceSeason, &descending)
	if err != nil {
		t.Fatalf("ListSubscriptions(source_season desc) error = %v", err)
	}
	assertSubscriptionIDs(t, subscriptions, []uuid.UUID{subscriptionIDs[2], subscriptionIDs[1], subscriptionIDs[0]})

	entryIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	entryTitles := []string{"Zulu Entry", "Alpha Entry", "Mike Entry"}
	entryEpisodes := []int{3, 1, 2}
	entryAcquisitionIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	entryDownloadProgress := []float64{0.2, 0.8, 0.5}
	for index, id := range entryIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO rss_entries (id, subscription_id, identity_key, title, status, source_season, source_episode, discovered_at, updated_at) VALUES ($1, $2, $3, $4, 'enqueued', 1, $5, $6, $6)`, id, subscriptionIDs[0], fmt.Sprintf("entry-%d", index), entryTitles[index], entryEpisodes[index], base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id, created_at, updated_at) VALUES ($1, $2, 'rss', $3, $4, $4)`, entryAcquisitionIDs[index], seriesIDs[0], id, base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status, progress, created_at, updated_at) VALUES ($1, $2, 'downloading', $3, $4, $4)`, uuid.New(), entryAcquisitionIDs[index], entryDownloadProgress[index], base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	discoveredAt := "discovered_at"
	entries, err := reads.ListRSSEntries(ctx, subscriptionIDs[0], nil, 2, nil, nil, nil, nil, &discoveredAt, &ascending)
	if err != nil {
		t.Fatalf("ListRSSEntries(discovered_at asc) error = %v", err)
	}
	assertEntryIDs(t, entries, entryIDs[:2])
	entries, err = reads.ListRSSEntries(ctx, subscriptionIDs[0], entries.NextCursor, 2, nil, nil, nil, nil, &discoveredAt, &ascending)
	if err != nil {
		t.Fatalf("ListRSSEntries(discovered_at asc page 2) error = %v", err)
	}
	assertEntryIDs(t, entries, entryIDs[2:])
	episode := "episode"
	entries, err = reads.ListRSSEntries(ctx, subscriptionIDs[0], nil, 3, nil, nil, nil, nil, &episode, &descending)
	if err != nil {
		t.Fatalf("ListRSSEntries(episode desc) error = %v", err)
	}
	assertEntryIDs(t, entries, []uuid.UUID{entryIDs[0], entryIDs[2], entryIDs[1]})

	entries, err = reads.ListRSSEntries(ctx, subscriptionIDs[0], nil, 3, nil, nil, nil, nil, &progress, &descending)
	if err != nil {
		t.Fatalf("ListRSSEntries(progress desc) error = %v", err)
	}
	assertEntryIDs(t, entries, []uuid.UUID{entryIDs[1], entryIDs[2], entryIDs[0]})
	for index, item := range entries.Items {
		if item.AcquisitionID == nil || item.AcquisitionProgress == nil {
			t.Fatalf("RSS entry[%d] acquisition progress = %#v", index, item)
		}
		if index > 0 && entries.Items[index-1].AcquisitionProgress.OverallProgress <= item.AcquisitionProgress.OverallProgress {
			t.Fatalf("RSS progress is not descending: %#v", entries.Items)
		}
	}
}

func TestRSSSubscriptionListQueryFilterIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	base := time.Date(2026, 8, 15, 1, 0, 0, 0, time.UTC)

	frierenSeries, detectiveSeries := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, 101, 'Frieren'), ($2, 102, 'Case Files')`, frierenSeries, detectiveSeries); err != nil {
		t.Fatal(err)
	}
	frierenFeed, detectiveFeed := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, created_at, updated_at) VALUES ($1, $2, 'Frieren Feed', 'https://example.test/frieren.xml', false, 900, 1, $3, $3), ($4, $5, 'Frozen Detective', 'https://example.test/detective.xml', false, 900, 1, $3, $3)`, frierenFeed, frierenSeries, base, detectiveFeed, detectiveSeries); err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(queries, database.NewTransactor(pool), nil)

	matchName := "frieren"
	page, err := workflow.ListSubscriptions(ctx, nil, 10, &matchName, nil, nil)
	if err != nil {
		t.Fatalf("ListSubscriptions(query=name) error = %v", err)
	}
	assertSubscriptionIDs(t, page, []uuid.UUID{frierenFeed})

	matchSeriesTitle := "case files"
	page, err = workflow.ListSubscriptions(ctx, nil, 10, &matchSeriesTitle, nil, nil)
	if err != nil {
		t.Fatalf("ListSubscriptions(query=series title) error = %v", err)
	}
	assertSubscriptionIDs(t, page, []uuid.UUID{detectiveFeed})

	blank := "   "
	page, err = workflow.ListSubscriptions(ctx, nil, 10, &blank, nil, nil)
	if err != nil {
		t.Fatalf("ListSubscriptions(blank query) error = %v", err)
	}
	assertSubscriptionIDs(t, page, []uuid.UUID{frierenFeed, detectiveFeed})
}

func TestRSSEntryListQueryAndRejectReasonFilterIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	base := time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC)

	seriesID, subscriptionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, 201, 'Filter Show')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season) VALUES ($1, $2, 'Filter Show', 'https://example.test/filter.xml', true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	libraryHit, extraHit, dualHit, normalID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status, discovered_at, updated_at
) VALUES
    ($1, $5, 'guid:library', 'Filter Show S01E01', NULL, false,
     ARRAY['target_episode_in_library']::text[], 1, 1, 'discovered', $6, $6),
    ($2, $5, 'guid:extra', 'Filter Show recap', NULL, false,
     ARRAY['non_episode_extra']::text[], NULL, NULL, 'discovered', $7, $7),
    ($3, $5, 'guid:dual', 'Filter Show S01E03', NULL, false,
     ARRAY['title_excluded', 'target_episode_processing']::text[], 1, 3, 'discovered', $8, $8),
    ($4, $5, 'guid:normal', 'Filter Show S01E02', 'https://example.test/2.torrent', true,
     ARRAY[]::text[], 1, 2, 'enqueued', $9, $9)`, libraryHit, extraHit, dualHit, normalID, subscriptionID, base, base.Add(time.Minute), base.Add(2*time.Minute), base.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	reads := NewReadService(db.New(pool))

	matchTitle := "S01E02"
	page, err := reads.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, nil, &matchTitle, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListRSSEntries(query=title) error = %v", err)
	}
	assertEntryIDs(t, page, []uuid.UUID{normalID})

	rejectReason := "target_episode_in_library"
	page, err = reads.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, nil, nil, &rejectReason, nil, nil)
	if err != nil {
		t.Fatalf("ListRSSEntries(rejectReason) error = %v", err)
	}
	assertEntryIDs(t, page, []uuid.UUID{libraryHit})

	processingReason := "target_episode_processing"
	page, err = reads.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, nil, nil, &processingReason, nil, nil)
	if err != nil {
		t.Fatalf("ListRSSEntries(rejectReason=second element) error = %v", err)
	}
	assertEntryIDs(t, page, []uuid.UUID{dualHit})
	if page.Items[0].RejectReason != "title_excluded,target_episode_processing" {
		t.Fatalf("second-reason match returned rejectReason = %q, want the complete reason list", page.Items[0].RejectReason)
	}

	skipped := "skipped"
	page, err = reads.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, &skipped, nil, &rejectReason, nil, nil)
	if err != nil {
		t.Fatalf("ListRSSEntries(skipped+rejectReason) error = %v", err)
	}
	assertEntryIDs(t, page, []uuid.UUID{libraryHit})

	dualTitle, titleSort, ascending := "S01E03", "title", "asc"
	page, err = reads.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, &skipped, &dualTitle, &processingReason, &titleSort, &ascending)
	if err != nil {
		t.Fatalf("ListRSSEntries(skipped+query+rejectReason+sort) error = %v", err)
	}
	assertEntryIDs(t, page, []uuid.UUID{dualHit})
	if page.Items[0].RejectReason != "title_excluded,target_episode_processing" {
		t.Fatalf("combined filters returned rejectReason = %q, want the complete reason list", page.Items[0].RejectReason)
	}

	missingTitle := "not present"
	page, err = reads.ListRSSEntries(ctx, subscriptionID, nil, 2, nil, &skipped, &missingTitle, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListRSSEntries(no matches) error = %v", err)
	}
	if len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("no-match RSS entry page = %#v, want empty page without cursor", page)
	}

	page, err = reads.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListRSSEntries(no filters) error = %v", err)
	}
	// 默认按 discovered_at 降序；各条目发现时间递增，顺序确定
	assertEntryIDs(t, page, []uuid.UUID{normalID, dualHit, extraHit, libraryHit})
}

func assertAcquisitionIDs(t *testing.T, page domain.AcquisitionPage, expected []uuid.UUID) {
	t.Helper()
	if len(page.Items) != len(expected) {
		t.Fatalf("acquisition count = %d, want %d", len(page.Items), len(expected))
	}
	for index, id := range expected {
		if page.Items[index].ID != id {
			t.Fatalf("acquisition[%d] = %s, want %s", index, page.Items[index].ID, id)
		}
	}
}

func assertSubscriptionIDs(t *testing.T, page domain.RSSSubscriptionPage, expected []uuid.UUID) {
	t.Helper()
	if len(page.Items) != len(expected) {
		t.Fatalf("subscription count = %d, want %d", len(page.Items), len(expected))
	}
	for index, id := range expected {
		if page.Items[index].ID != id {
			t.Fatalf("subscription[%d] = %s, want %s", index, page.Items[index].ID, id)
		}
	}
}

func assertEntryIDs(t *testing.T, page domain.RSSEntryPage, expected []uuid.UUID) {
	t.Helper()
	if len(page.Items) != len(expected) {
		t.Fatalf("entry count = %d, want %d", len(page.Items), len(expected))
	}
	for index, id := range expected {
		if page.Items[index].ID != id {
			t.Fatalf("entry[%d] = %s, want %s", index, page.Items[index].ID, id)
		}
	}
}
