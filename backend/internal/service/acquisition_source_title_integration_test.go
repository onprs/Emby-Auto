//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestAcquisitionReadModelsKeepPerSourceTitlesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	read := NewReadService(db.New(pool))

	seriesID := uuid.New()
	searchID, candidateID, searchAcquisitionID := uuid.New(), uuid.New(), uuid.New()
	subscriptionID, entryID, rssAcquisitionID := uuid.New(), uuid.New(), uuid.New()
	manualAcquisitionID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Canonical Series Title')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO search_runs (id, query, status) VALUES ($1, 'fixture query', 'completed')`, searchID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO release_candidates (id, search_run_id, provider, identity_key, title)
VALUES ($1, $2, 'fixture', $3, 'Original selected season pack title')`, candidateID, searchID, candidateID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Source title fixture', 'https://example.test/source-title.xml', false, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status)
VALUES ($1, $2, 'guid:source-title', 'Original RSS episode title', 'https://example.test/source-title.torrent', true, ARRAY[]::text[], 1, 2, 'enqueued')`, entryID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, release_candidate_id, rss_entry_id, source_uri, source_payload)
VALUES ($1, $4, 'search', $5, NULL, NULL, '{"sourceSeason":1,"sourceEpisode":0,"singleEpisode":false}'),
       ($2, $4, 'rss', NULL, $6, NULL, '{"sourceSeason":1,"sourceEpisode":2,"singleEpisode":true}'),
       ($3, $4, 'manual', NULL, NULL, 'manual://source-title-fixture', '{}')`, searchAcquisitionID, rssAcquisitionID, manualAcquisitionID, seriesID, candidateID, entryID); err != nil {
		t.Fatal(err)
	}

	expectedTitles := map[uuid.UUID]string{
		searchAcquisitionID: "Original selected season pack title",
		rssAcquisitionID:    "Original RSS episode title",
		manualAcquisitionID: "",
	}
	for _, acquisitionID := range []uuid.UUID{searchAcquisitionID, rssAcquisitionID, manualAcquisitionID} {
		view, err := read.GetAcquisition(ctx, acquisitionID)
		if err != nil {
			t.Fatalf("GetAcquisition(%s) error = %v", acquisitionID, err)
		}
		if view.SeriesTitle != "Canonical Series Title" || view.SourceTitle != expectedTitles[acquisitionID] {
			t.Fatalf("acquisition %s titles = canonical %q source %q, want %q/%q", acquisitionID, view.SeriesTitle, view.SourceTitle, "Canonical Series Title", expectedTitles[acquisitionID])
		}
	}

	page, err := read.ListAcquisitions(ctx, nil, 10, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListAcquisitions() error = %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("acquisition list count = %d, want 3", len(page.Items))
	}
	for _, item := range page.Items {
		wantTitle, ok := expectedTitles[item.ID]
		if !ok {
			t.Fatalf("acquisition list contains unexpected item %s", item.ID)
		}
		if item.SourceTitle != wantTitle {
			t.Fatalf("acquisition list item %s source title = %q, want %q", item.ID, item.SourceTitle, wantTitle)
		}
	}
}
