//go:build integration

package repository

import (
	"context"
	"strings"
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

// read model 依赖的 provenance 事件必须被保留。
var provenanceEventTopics = []string{
	"rss.entry.enqueueing",
	"task.created",
	"task.imported",
	"task.video_ready",
	"task.subtitle_ready",
	"task.awaiting_review",
	"task.reviewed",
	"acquisition.delete_completed",
}

// 这些固定 topic 的当前状态均由业务表持久化，事件只承担限期通知与审计。
var discardableEventTopics = []string{
	"configuration.updated",
	"operation.queued",
	"operation.started",
	"operation.succeeded",
	"operation.retry_scheduled",
	"operation.failed",
	"operation.cancel_requested",
	"operation.cancelled",
	"operation.recovered",
	"download.enqueue_failed",
	"download.sync_failed",
	"download.materialize_failed",
	"download.manifest_persisted",
	"download.enqueued",
	"download.selection_applied",
	"download.progressed",
	"download.completed",
	"download.materialized",
	"download.removed",
	"download.retry_requested",
	"download.cancel_requested",
	"download.removal_requested",
	"download.file_resolution_saved",
	"download.file_selection_saved",
	"download.mapping_recovered",
	"search.created",
	"search.started",
	"search.completed",
	"search.failed",
	"search.cancelled",
	"acquisition.created",
	"acquisition.delete_requested",
	"task.finalizing",
	"task.import_queued",
	"task.cleanup_completed",
	"task.retry_requested",
	"task.cancel_requested",
	"task.media_failed",
	"task.import_failed",
	"task.cleanup_failed",
	"task.cleanup_cancelled",
	"task.import_cancelled",
	"task.media_cancelled",
	"agent.resolution_queued",
	"agent.resolution_failed",
	"agent.resolution_cancelled",
	"rss.adjudication_applied",
	"rss.coordinate_resolved",
	"subtitle.video_match_saved",
	"tmdb.series_synchronized",
	"mapping.profile_saved",
	"rss.mapping_profile_applied",
	"emby.scan_completed",
	"emby.scan_failed",
	"emby.scan_cancelled",
	"rss.entry.ignored",
	"rss.entry.target_occupied",
	"rss.entry.fulfillment_expired",
	"rss.entry.enqueue_failed",
	"rss.mapping_discovery_recorded",
	"rss.polled",
	"rss.poll_failed",
	"rss.poll_completed",
	"rss.subscription.fulfilled",
	"rss.subscription.final_imported",
	"rss.subscription.created",
	"rss.subscription.updated",
	"rss.subscription.archived",
	"rss.subscription.delete_requested",
	"rss.subscription.delete_completed",
	"rss.subscription.delete_partial",
	"rss.subscription.completion_retained",
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
	for _, topic := range discardableEventTopics {
		expired = append(expired, appendTestEvent(t, ctx, pool, queries, topic, now.Add(-40*24*time.Hour)))
	}
	var kept []uuid.UUID
	for _, topic := range provenanceEventTopics {
		kept = append(kept, appendTestEvent(t, ctx, pool, queries, topic, now.Add(-200*24*time.Hour)))
	}
	for _, topic := range []string{
		"future.read_model.provenance",
		"rss.subscription.incomplete_recovery_future",
		"legacy.custom.history",
	} {
		kept = append(kept, appendTestEvent(t, ctx, pool, queries, topic, now.Add(-200*24*time.Hour)))
	}
	// 未超保留期的可丢弃事件也不应被删除。
	kept = append(kept, appendTestEvent(t, ctx, pool, queries, "operation.started", now.Add(-24*time.Hour)))

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

func TestDeleteExpiredUsesOccurredAtAndSequenceTieBreakIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	events := NewEvents(queries)

	occurredAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := appendTestEvent(t, ctx, pool, queries, "operation.failed", occurredAt)
	second := appendTestEvent(t, ctx, pool, queries, "operation.failed", occurredAt)
	third := appendTestEvent(t, ctx, pool, queries, "operation.failed", occurredAt)

	deleted, err := events.DeleteExpired(ctx, occurredAt.Add(time.Hour), 2)
	if err != nil {
		t.Fatalf("DeleteExpired() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("DeleteExpired() deleted = %d, want 2", deleted)
	}
	for _, id := range []uuid.UUID{first, second} {
		if _, err := queries.GetEvent(ctx, UUIDToPG(id)); err == nil {
			t.Fatalf("earlier event %s still exists after tie-broken batch", id)
		}
	}
	if _, err := queries.GetEvent(ctx, UUIDToPG(third)); err != nil {
		t.Fatalf("last event in tied timestamp was deleted: %v", err)
	}
}

func TestDeleteExpiredSkipsLargeProtectedBacklogIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	events := NewEvents(queries)

	occurredAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO events (topic, data, occurred_at)
SELECT
    (ARRAY[
        'rss.entry.enqueueing',
        'task.created',
        'task.imported',
        'task.video_ready',
        'task.subtitle_ready',
        'task.awaiting_review',
        'task.reviewed',
        'acquisition.delete_completed'
    ]::text[])[((candidate - 1) % 8) + 1],
    '{}'::jsonb,
    $1
FROM generate_series(1, 5000) AS candidates(candidate)
`, occurredAt); err != nil {
		t.Fatalf("insert protected backlog: %v", err)
	}
	for range 3 {
		appendTestEvent(t, ctx, pool, queries, "download.progressed", occurredAt)
	}

	deleted, err := events.DeleteExpired(ctx, occurredAt.Add(time.Hour), 1000)
	if err != nil {
		t.Fatalf("DeleteExpired() error = %v", err)
	}
	if deleted != 3 {
		t.Fatalf("DeleteExpired() deleted = %d, want three discardable events", deleted)
	}
	var protectedCount int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE occurred_at = $1 AND topic = ANY($2::text[])`,
		occurredAt,
		provenanceEventTopics,
	).Scan(&protectedCount); err != nil {
		t.Fatalf("count protected backlog: %v", err)
	}
	if protectedCount != 5000 {
		t.Fatalf("protected backlog count = %d, want 5000", protectedCount)
	}
}

func TestEventDiscardabilityFunctionAndPartialIndexIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)

	for _, topic := range discardableEventTopics {
		var discardable bool
		if err := pool.QueryRow(ctx, `SELECT event_is_discardable($1)`, topic).Scan(&discardable); err != nil {
			t.Fatalf("event_is_discardable(%q): %v", topic, err)
		}
		if !discardable {
			t.Fatalf("event_is_discardable(%q) = false, want true", topic)
		}
	}
	protectedTopics := append([]string{}, provenanceEventTopics...)
	protectedTopics = append(protectedTopics,
		"future.read_model.provenance",
		"rss.subscription.incomplete_recovery_future",
		"legacy.custom.history",
	)
	for _, topic := range protectedTopics {
		var discardable bool
		if err := pool.QueryRow(ctx, `SELECT event_is_discardable($1)`, topic).Scan(&discardable); err != nil {
			t.Fatalf("event_is_discardable(%q): %v", topic, err)
		}
		if discardable {
			t.Fatalf("event_is_discardable(%q) = true, want fail-closed false", topic)
		}
	}

	var volatility string
	var strict bool
	if err := pool.QueryRow(ctx, `
SELECT provolatile::text, proisstrict
FROM pg_proc
WHERE proname = 'event_is_discardable'
  AND pg_function_is_visible(oid)
`).Scan(&volatility, &strict); err != nil {
		t.Fatalf("read event_is_discardable metadata: %v", err)
	}
	if volatility != "i" || !strict {
		t.Fatalf("event_is_discardable metadata = volatility %q strict %t, want immutable and strict", volatility, strict)
	}

	var indexDefinition string
	if err := pool.QueryRow(ctx, `
SELECT indexdef
FROM pg_indexes
WHERE schemaname = current_schema()
  AND indexname = 'events_discardable_occurred_at_sequence_idx'
`).Scan(&indexDefinition); err != nil {
		t.Fatalf("read retention index definition: %v", err)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(indexDefinition), " "))
	if !strings.Contains(normalized, "(occurred_at, event_sequence)") ||
		!strings.Contains(normalized, "event_is_discardable(topic)") {
		t.Fatalf("retention index definition = %q, want ordered partial index", indexDefinition)
	}
}
