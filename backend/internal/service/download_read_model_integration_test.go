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
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestListDownloadsFiltersSearchesAndSortsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	service := NewReadService(db.New(pool))

	seriesID := uuid.New()
	acquisitionID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Needle Series')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', 'magnet:?xt=urn:btih:navigation-test')`, acquisitionID, seriesID); err != nil {
		t.Fatal(err)
	}

	type fixture struct {
		name         string
		status       string
		clientState  *string
		failureStage *string
	}
	state := func(value string) *string { return &value }
	fixtures := []fixture{
		{name: "enqueue", status: "enqueue_pending", clientState: state("added")},
		{name: "queued", status: "downloading", clientState: state("queuedDL")},
		{name: "downloading", status: "downloading", clientState: state("downloading")},
		{name: "paused", status: "downloading", clientState: state("pausedDL")},
		{name: "stopped", status: "downloading", clientState: state("stoppedDL")},
		{name: "completed", status: "completed", clientState: state("uploading")},
		{name: "selecting", status: "selecting_files", clientState: state("uploading")},
		{name: "materialized", status: "materialized", clientState: state("uploading")},
		{name: "failed", status: "failed", clientState: state("error"), failureStage: state("sync")},
		{name: "cancelled", status: "cancelled", clientState: nil},
	}
	ids := make(map[string]uuid.UUID, len(fixtures))
	base := time.Date(2026, time.July, 25, 10, 0, 0, 0, time.UTC)
	for index, item := range fixtures {
		id := uuid.New()
		ids[item.name] = id
		if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, attempt, status, client_state, failure_stage, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, id, acquisitionID, index+1, item.status, item.clientState, item.failureStage, base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatalf("insert %s: %v", item.name, err)
		}
	}

	phaseCases := []struct {
		phase string
		want  []string
	}{
		{phase: "active", want: []string{"enqueue", "queued", "downloading", "paused", "stopped"}},
		{phase: "waiting", want: []string{"enqueue", "queued"}},
		{phase: "downloading", want: []string{"downloading"}},
		{phase: "paused", want: []string{"paused", "stopped"}},
		{phase: "completed", want: []string{"completed", "selecting", "materialized"}},
		{phase: "failed", want: []string{"failed"}},
	}
	sortBy := "updated_at"
	descending := "desc"
	for _, testCase := range phaseCases {
		t.Run(testCase.phase, func(t *testing.T) {
			page, err := service.ListDownloads(ctx, nil, 100, nil, &testCase.phase, nil, &sortBy, &descending)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != len(testCase.want) {
				t.Fatalf("items = %d, want %d", len(page.Items), len(testCase.want))
			}
			seen := make(map[uuid.UUID]bool, len(page.Items))
			for _, item := range page.Items {
				seen[item.ID] = true
			}
			for _, name := range testCase.want {
				if !seen[ids[name]] {
					t.Errorf("missing %s", name)
				}
			}
		})
	}

	query := "Needle Series"
	searchPage, err := service.ListDownloads(ctx, nil, 100, nil, nil, &query, &sortBy, &descending)
	if err != nil {
		t.Fatal(err)
	}
	if len(searchPage.Items) != len(fixtures) {
		t.Fatalf("title search returned %d items, want %d", len(searchPage.Items), len(fixtures))
	}

	first, err := service.ListDownloads(ctx, nil, 2, nil, nil, nil, &sortBy, &descending)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page = %#v", first)
	}
	if first.Items[0].ID != ids["cancelled"] || first.Items[1].ID != ids["failed"] {
		t.Fatalf("newest order = %s, %s", first.Items[0].ID, first.Items[1].ID)
	}
	second, err := service.ListDownloads(ctx, first.NextCursor, 2, nil, nil, nil, &sortBy, &descending)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 || second.Items[0].ID != ids["materialized"] || second.Items[1].ID != ids["selecting"] {
		t.Fatalf("second page IDs = %s", formatDownloadIDs(second.Items))
	}

	ascending := "asc"
	oldestPage, err := service.ListDownloads(ctx, nil, 2, nil, nil, nil, &sortBy, &ascending)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldestPage.Items) != 2 || oldestPage.Items[0].ID != ids["enqueue"] || oldestPage.Items[1].ID != ids["queued"] {
		t.Fatalf("oldest order = %s", formatDownloadIDs(oldestPage.Items))
	}
}

func formatDownloadIDs(items []domain.DownloadView) string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.ID.String())
	}
	return fmt.Sprint(values)
}
