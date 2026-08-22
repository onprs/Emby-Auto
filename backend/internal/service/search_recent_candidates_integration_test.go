//go:build integration

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestListRecentReleaseCandidatesOrderingAndLimitIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	workflow := NewSearchWorkflow(db.New(pool), nil, nil)
	queries := db.New(pool)

	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	runOld := uuid.MustParse("a0000000-0000-4000-8000-000000000001")
	runMid := uuid.MustParse("b0000000-0000-4000-8000-000000000002")
	runNew := uuid.MustParse("c0000000-0000-4000-8000-000000000003")

	runs := []struct {
		id        uuid.UUID
		createdAt time.Time
		query     string
	}{
		{runOld, base, "old query"},
		{runMid, base.Add(10 * time.Minute), "mid query"},
		{runNew, base.Add(20 * time.Minute), "new query"},
	}
	for _, run := range runs {
		if _, err := pool.Exec(ctx, `INSERT INTO search_runs (id, query, status, created_at, updated_at) VALUES ($1, $2, 'completed', $3, $3)`, run.id, run.query, run.createdAt); err != nil {
			t.Fatalf("insert search run %s: %v", run.id, err)
		}
	}

	candidates := []struct {
		id        uuid.UUID
		searchID  uuid.UUID
		title     string
		createdAt time.Time
		payload   map[string]any
	}{
		{uuid.MustParse("a0000000-0000-4000-8000-000000000011"), runOld, "old-01", base.Add(1 * time.Second), map[string]any{"upstream": "old-01-secret", "provider_raw": "raw-old-01"}},
		{uuid.MustParse("a0000000-0000-4000-8000-000000000012"), runOld, "old-02", base.Add(2 * time.Second), map[string]any{"upstream": "old-02-secret"}},
		{uuid.MustParse("b0000000-0000-4000-8000-000000000021"), runMid, "mid-01", base.Add(10*time.Minute + 1*time.Second), map[string]any{"upstream": "mid-01-secret"}},
		{uuid.MustParse("b0000000-0000-4000-8000-000000000022"), runMid, "mid-02", base.Add(10*time.Minute + 2*time.Second), map[string]any{"upstream": "mid-02-secret"}},
		{uuid.MustParse("b0000000-0000-4000-8000-000000000023"), runMid, "mid-03", base.Add(10*time.Minute + 3*time.Second), map[string]any{"upstream": "mid-03-secret"}},
		{uuid.MustParse("c0000000-0000-4000-8000-000000000031"), runNew, "new-01", base.Add(20*time.Minute + 1*time.Second), map[string]any{"upstream": "new-01-secret"}},
		{uuid.MustParse("c0000000-0000-4000-8000-000000000032"), runNew, "new-02", base.Add(20*time.Minute + 2*time.Second), map[string]any{"upstream": "new-02-secret"}},
		{uuid.MustParse("c0000000-0000-4000-8000-000000000033"), runNew, "new-03", base.Add(20*time.Minute + 3*time.Second), map[string]any{"upstream": "new-03-secret"}},
	}

	for _, c := range candidates {
		payloadJSON, err := json.Marshal(c.payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO release_candidates (id, search_run_id, provider, identity_key, title, download_uri, size_bytes, upstream_payload, created_at) VALUES ($1, $2, 'dmhy', $3, $4, 'magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01', 12345, $5, $6)`, c.id, c.searchID, "key-"+c.id.String(), c.title, payloadJSON, c.createdAt); err != nil {
			t.Fatalf("insert candidate %s: %v", c.id, err)
		}
	}

	// Service layer must return exactly 5 in stable order: newest run first, then persistence order within run.
	result, err := workflow.ListRecentCandidates(ctx, 5)
	if err != nil {
		t.Fatalf("ListRecentCandidates() error = %v", err)
	}
	if len(result) != 5 {
		t.Fatalf("recent count = %d, want 5", len(result))
	}
	expectedOrder := []uuid.UUID{
		uuid.MustParse("c0000000-0000-4000-8000-000000000031"),
		uuid.MustParse("c0000000-0000-4000-8000-000000000032"),
		uuid.MustParse("c0000000-0000-4000-8000-000000000033"),
		uuid.MustParse("b0000000-0000-4000-8000-000000000021"),
		uuid.MustParse("b0000000-0000-4000-8000-000000000022"),
	}
	for index, expectedID := range expectedOrder {
		if result[index].ID != expectedID {
			t.Fatalf("recent[%d] = %s, want %s (titles %v)", index, result[index].ID, expectedID, expectedOrder)
		}
	}
	// Verify that the 6th candidate (b-03) and older run candidates are correctly truncated.
	for _, item := range result {
		if item.ID == uuid.MustParse("b0000000-0000-4000-8000-000000000023") {
			t.Fatalf("recent should not contain 6th item b-03 when limit=5")
		}
		if item.ID == uuid.MustParse("a0000000-0000-4000-8000-000000000011") || item.ID == uuid.MustParse("a0000000-0000-4000-8000-000000000012") {
			t.Fatalf("recent should not contain oldest run candidates when newer runs already fill limit")
		}
		if len(item.UpstreamPayload) == 0 {
			t.Fatalf("service should still load upstream payload internally, got empty for %s", item.ID)
		}
	}

	// SQL ordering directly also respects the contract.
	rows, err := queries.ListRecentReleaseCandidates(ctx, 5)
	if err != nil {
		t.Fatalf("ListRecentReleaseCandidates sqlc error = %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("sqlc recent count = %d, want 5", len(rows))
	}
	for index, expectedID := range expectedOrder {
		if repository.UUIDFromPG(rows[index].ID) != expectedID {
			t.Fatalf("sqlc recent[%d] = %s, want %s", index, repository.UUIDFromPG(rows[index].ID), expectedID)
		}
		if len(rows[index].UpstreamPayload) == 0 {
			t.Fatalf("sqlc row should retain upstream payload")
		}
	}
}
