//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func appendTestEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, queries *db.Queries, occurredAt time.Time) uuid.UUID {
	t.Helper()
	event, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:    UUIDToPG(uuid.New()),
		Topic: "events.retention.integration_test",
		Data:  []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	id := UUIDFromPG(event.ID)
	if _, err := pool.Exec(ctx,
		`UPDATE events SET occurred_at = $1 WHERE id = $2`, occurredAt, event.ID,
	); err != nil {
		t.Fatalf("UPDATE occurred_at error = %v", err)
	}
	return id
}

func TestDeleteEventsBeforeRemovesOnlyExpiredEventsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	events := NewEvents(queries)

	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)
	expired := appendTestEvent(t, ctx, pool, queries, now.Add(-40*24*time.Hour))
	keptRecent := appendTestEvent(t, ctx, pool, queries, now.Add(-29*24*time.Hour))
	keptNew := appendTestEvent(t, ctx, pool, queries, now)

	deleted, err := events.DeleteBefore(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("DeleteBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteBefore() deleted = %d, want 1", deleted)
	}
	for _, id := range []uuid.UUID{expired} {
		if _, err := queries.GetEvent(ctx, UUIDToPG(id)); err == nil {
			t.Fatalf("expired event %s still exists after deletion", id)
		}
	}
	for _, id := range []uuid.UUID{keptRecent, keptNew} {
		if _, err := queries.GetEvent(ctx, UUIDToPG(id)); err != nil {
			t.Fatalf("kept event %s was deleted: %v", id, err)
		}
	}
}

func TestDeleteEventsBeforeDeletesInBatchesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	events := NewEvents(queries)

	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)
	for range 5 {
		appendTestEvent(t, ctx, pool, queries, now.Add(-60*24*time.Hour))
	}

	total := int64(0)
	for range 3 {
		deleted, err := events.DeleteBefore(ctx, cutoff, 2)
		if err != nil {
			t.Fatalf("DeleteBefore() error = %v", err)
		}
		total += deleted
	}
	if total != 5 {
		t.Fatalf("batched DeleteBefore() total = %d, want 5", total)
	}
	if remaining, err := events.DeleteBefore(ctx, cutoff, 2); err != nil || remaining != 0 {
		t.Fatalf("final DeleteBefore() = %d, %v; want 0, nil", remaining, err)
	}
}
