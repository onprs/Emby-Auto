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

func appendTestEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, queries *db.Queries, topic string, occurredAt time.Time) uuid.UUID {
	t.Helper()
	event, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:    UUIDToPG(uuid.New()),
		Topic: topic,
		Data:  []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("AppendEvent(%s) error = %v", topic, err)
	}
	id := UUIDFromPG(event.ID)
	if _, err := pool.Exec(ctx,
		`UPDATE events SET occurred_at = $1 WHERE id = $2`, occurredAt, event.ID,
	); err != nil {
		t.Fatalf("UPDATE occurred_at error = %v", err)
	}
	return id
}

// read model 依赖的 provenance 事件（read_models.sql / rss.sql 作为事实来源）必须被保留。
var protectedEventTopics = []string{
	"rss.entry.enqueueing",
	"task.created",
	"task.imported",
	"task.video_ready",
	"task.subtitle_ready",
	"task.awaiting_review",
	"task.reviewed",
	"acquisition.delete_completed",
}

func TestDeleteExpiredRemovesOnlyDiscardableEventsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	events := NewEvents(queries)

	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)
	var expired []uuid.UUID
	for _, topic := range []string{"operation.queued", "download.progressed", "search.created", "configuration.updated"} {
		expired = append(expired, appendTestEvent(t, ctx, pool, queries, topic, now.Add(-40*24*time.Hour)))
	}
	var kept []uuid.UUID
	for _, topic := range protectedEventTopics {
		kept = append(kept, appendTestEvent(t, ctx, pool, queries, topic, now.Add(-200*24*time.Hour)))
	}
	// 保护边界：未超保留期的可丢弃事件也不应被删除。
	keptRecent := appendTestEvent(t, ctx, pool, queries, "operation.started", now.Add(-1*24*time.Hour))
	kept = append(kept, keptRecent)

	deleted, err := events.DeleteExpired(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("DeleteExpired() error = %v", err)
	}
	if deleted != int64(len(expired)) {
		t.Fatalf("DeleteExpired() deleted = %d, want %d", deleted, len(expired))
	}
	for _, id := range expired {
		if _, err := queries.GetEvent(ctx, UUIDToPG(id)); err == nil {
			t.Fatalf("expired discardable event %s still exists after deletion", id)
		}
	}
	for _, id := range kept {
		if _, err := queries.GetEvent(ctx, UUIDToPG(id)); err != nil {
			t.Fatalf("protected or fresh event %s was deleted: %v", id, err)
		}
	}
}

func TestDeleteExpiredDeletesInBatchesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	events := NewEvents(queries)

	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)
	for range 5 {
		appendTestEvent(t, ctx, pool, queries, "operation.failed", now.Add(-60*24*time.Hour))
	}

	total := int64(0)
	for range 3 {
		deleted, err := events.DeleteExpired(ctx, cutoff, 2)
		if err != nil {
			t.Fatalf("DeleteExpired() error = %v", err)
		}
		total += deleted
	}
	if total != 5 {
		t.Fatalf("batched DeleteExpired() total = %d, want 5", total)
	}
	if remaining, err := events.DeleteExpired(ctx, cutoff, 2); err != nil || remaining != 0 {
		t.Fatalf("final DeleteExpired() = %d, %v; want 0, nil", remaining, err)
	}
}