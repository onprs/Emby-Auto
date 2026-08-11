//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestSyntheticReadModelCapacityBaselineIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	reads := NewReadService(queries)

	seriesID, acquisitionID, downloadID, subscriptionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	resourceID := uuid.New()
	seed := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO media_series (id, title) VALUES ($1, 'Capacity Fixture')`, []any{seriesID}},
		{`INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', 'fixture://capacity')`, []any{acquisitionID, seriesID}},
		{`INSERT INTO downloads (id, acquisition_id, status, progress) VALUES ($1, $2, 'completed', 1)`, []any{downloadID, acquisitionID}},
		{`INSERT INTO rss_subscriptions (id, series_id, name, feed_url) VALUES ($1, $2, 'Capacity Feed', 'https://fixture.test/capacity.xml')`, []any{subscriptionID, seriesID}},
		{`
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
SELECT md5('capacity-file-' || item)::uuid, $1, item, 'Season/episode-' || item || '.mkv', 1048576 + item, 'video', item = 1
FROM generate_series(1, 1500) AS item`, []any{downloadID}},
		{`
INSERT INTO rss_entries (id, subscription_id, identity_key, title, discovered_at)
SELECT md5('capacity-rss-' || item)::uuid, $1, 'capacity-rss-' || item, 'Capacity RSS ' || item,
       now() - item * interval '1 second'
FROM generate_series(1, 2000) AS item`, []any{subscriptionID}},
		{`
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, created_at)
SELECT md5('capacity-operation-' || item)::uuid, 'capacity.read', 'capacity_fixture', $1,
       'capacity-operation-' || item, 'queued', 1, 60, now() - item * interval '1 second'
FROM generate_series(1, 2000) AS item`, []any{resourceID}},
		{`
INSERT INTO events (id, topic, resource_type, resource_id, data, occurred_at)
SELECT md5('capacity-event-' || item)::uuid, 'capacity.updated', 'capacity_fixture', $1,
       jsonb_build_object('sequence', item), now() + item * interval '1 millisecond'
FROM generate_series(1, 1500) AS item`, []any{resourceID}},
	}
	for _, statement := range seed {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	measureCapacityQuery(t, "download detail with 1500 files", func() error {
		view, err := reads.GetDownload(ctx, downloadID)
		if err == nil && len(view.Files) != 1500 {
			t.Fatalf("download file count = %d, want 1500", len(view.Files))
		}
		return err
	})
	measureCapacityQuery(t, "RSS first page from 2000 entries", func() error {
		page, err := reads.ListRSSEntries(ctx, subscriptionID, nil, 100, nil, nil, nil, nil)
		if err == nil && (len(page.Items) != 100 || page.NextCursor == nil) {
			t.Fatalf("RSS page = %d items, cursor %v", len(page.Items), page.NextCursor)
		}
		return err
	})
	measureCapacityQuery(t, "operation first page from 2000 rows", func() error {
		page, err := reads.ListOperations(ctx, nil, 100, nil, nil, nil)
		if err == nil && (len(page.Items) != 100 || page.NextCursor == nil) {
			t.Fatalf("operation page = %d items, cursor %v", len(page.Items), page.NextCursor)
		}
		return err
	})
	measureCapacityQuery(t, "resource history first page from 1500 events", func() error {
		page, err := reads.ListResourceEvents(ctx, "capacity_fixture", resourceID, nil, 100)
		if err == nil && (len(page.Items) != 100 || page.NextCursor == nil) {
			t.Fatalf("event page = %d items, cursor %v", len(page.Items), page.NextCursor)
		}
		return err
	})

	events := repository.NewEvents(queries)
	first, err := events.List(ctx, nil, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("load SSE cursor: count=%d error=%v", len(first), err)
	}
	measureCapacityQuery(t, "SSE backlog of 1000 from 1500 events", func() error {
		backlog, listErr := events.List(ctx, &first[0].ID, 1000)
		if listErr == nil && len(backlog) != 1000 {
			t.Fatalf("SSE backlog count = %d, want 1000", len(backlog))
		}
		return listErr
	})
}

func measureCapacityQuery(t *testing.T, name string, query func() error) {
	t.Helper()
	started := time.Now()
	if err := query(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	elapsed := time.Since(started)
	t.Logf("%s: %s", name, elapsed)
	if elapsed > 5*time.Second {
		t.Fatalf("%s exceeded 5s regression ceiling: %s", name, elapsed)
	}
}
