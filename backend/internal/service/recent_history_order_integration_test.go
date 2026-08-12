//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestRecentHistoryUsesCreationAndEventOrderAcrossCursorPagesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	reads := NewReadService(db.New(pool))
	base := time.Date(2026, time.July, 27, 0, 50, 0, 0, time.UTC)
	resourceID := uuid.MustParse("70000000-0000-4000-8000-000000000001")

	operationIDs := []uuid.UUID{
		uuid.MustParse("80000000-0000-4000-8000-000000000001"),
		uuid.MustParse("10000000-0000-4000-8000-000000000002"),
		uuid.MustParse("f0000000-0000-4000-8000-000000000003"),
	}
	searchIDs := []uuid.UUID{
		uuid.MustParse("81000000-0000-4000-8000-000000000001"),
		uuid.MustParse("11000000-0000-4000-8000-000000000002"),
		uuid.MustParse("f1000000-0000-4000-8000-000000000003"),
	}
	eventIDs := []uuid.UUID{
		uuid.MustParse("82000000-0000-4000-8000-000000000001"),
		uuid.MustParse("12000000-0000-4000-8000-000000000002"),
		uuid.MustParse("f2000000-0000-4000-8000-000000000003"),
	}

	for index := range operationIDs {
		createdAt := base.Add(time.Duration(index) * time.Minute)
		if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, created_at, updated_at)
VALUES ($1, 'history.fixture', 'history_fixture', $2, $3, 'queued', 1, 60, $4, $4)`,
			operationIDs[index], resourceID, "history-operation-"+operationIDs[index].String(), createdAt); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO search_runs (id, query, status, created_at, updated_at)
VALUES ($1, $2, 'completed', $3, $3)`,
			searchIDs[index], "History search "+searchIDs[index].String(), createdAt); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO events (id, topic, resource_type, resource_id, data, occurred_at)
VALUES ($1, 'history.updated', 'history_fixture', $2, jsonb_build_object('position', $3::integer), $4)`,
			eventIDs[index], resourceID, index+1, createdAt); err != nil {
			t.Fatal(err)
		}
	}

	operations, err := reads.ListOperations(ctx, nil, 2, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListOperations() error = %v", err)
	}
	assertOperationPageIDs(t, operations.Items, []uuid.UUID{operationIDs[2], operationIDs[1]})
	if operations.NextCursor == nil || *operations.NextCursor != operationIDs[1] {
		t.Fatalf("operation next cursor = %v, want %s", operations.NextCursor, operationIDs[1])
	}
	operations, err = reads.ListOperations(ctx, operations.NextCursor, 2, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListOperations(page 2) error = %v", err)
	}
	assertOperationPageIDs(t, operations.Items, []uuid.UUID{operationIDs[0]})

	searches, err := reads.ListSearches(ctx, nil, 2, nil, nil)
	if err != nil {
		t.Fatalf("ListSearches() error = %v", err)
	}
	assertSearchPageIDs(t, searches.Items, []uuid.UUID{searchIDs[2], searchIDs[1]})
	if searches.NextCursor == nil || *searches.NextCursor != searchIDs[1] {
		t.Fatalf("search next cursor = %v, want %s", searches.NextCursor, searchIDs[1])
	}
	searches, err = reads.ListSearches(ctx, searches.NextCursor, 2, nil, nil)
	if err != nil {
		t.Fatalf("ListSearches(page 2) error = %v", err)
	}
	assertSearchPageIDs(t, searches.Items, []uuid.UUID{searchIDs[0]})

	events, err := reads.ListResourceEvents(ctx, "history_fixture", resourceID, nil, 2)
	if err != nil {
		t.Fatalf("ListResourceEvents() error = %v", err)
	}
	assertEventPageIDs(t, events.Items, []uuid.UUID{eventIDs[2], eventIDs[1]})
	if events.NextCursor == nil || *events.NextCursor != eventIDs[1] {
		t.Fatalf("event next cursor = %v, want %s", events.NextCursor, eventIDs[1])
	}
	events, err = reads.ListResourceEvents(ctx, "history_fixture", resourceID, events.NextCursor, 2)
	if err != nil {
		t.Fatalf("ListResourceEvents(page 2) error = %v", err)
	}
	assertEventPageIDs(t, events.Items, []uuid.UUID{eventIDs[0]})
}

func assertOperationPageIDs(t *testing.T, items []domain.OperationView, expected []uuid.UUID) {
	t.Helper()
	if len(items) != len(expected) {
		t.Fatalf("operation count = %d, want %d", len(items), len(expected))
	}
	for index, id := range expected {
		if items[index].ID != id {
			t.Fatalf("operation[%d] = %s, want %s", index, items[index].ID, id)
		}
	}
}

func assertSearchPageIDs(t *testing.T, items []domain.SearchRunSummary, expected []uuid.UUID) {
	t.Helper()
	if len(items) != len(expected) {
		t.Fatalf("search count = %d, want %d", len(items), len(expected))
	}
	for index, id := range expected {
		if items[index].ID != id {
			t.Fatalf("search[%d] = %s, want %s", index, items[index].ID, id)
		}
	}
}

func assertEventPageIDs(t *testing.T, items []domain.EventRecordView, expected []uuid.UUID) {
	t.Helper()
	if len(items) != len(expected) {
		t.Fatalf("event count = %d, want %d", len(items), len(expected))
	}
	for index, id := range expected {
		if items[index].ID != id {
			t.Fatalf("event[%d] = %s, want %s", index, items[index].ID, id)
		}
	}
}
