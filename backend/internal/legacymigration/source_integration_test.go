//go:build integration

package legacymigration

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestPostgresSourceReadsStructuredTasksAndMergesWatchQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	schema := "legacy_source_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		var exists bool
		if err := pool.QueryRow(cleanupCtx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&exists); err != nil {
			t.Errorf("verify source cleanup: %v", err)
		} else if exists {
			t.Errorf("source cleanup left schema %s", schema)
		}
	})
	setup := fmt.Sprintf(`
CREATE SCHEMA %s;
CREATE TABLE %s.tasks (
    task_id text PRIMARY KEY,
    canonical_series text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'queued',
    source jsonb NOT NULL DEFAULT '{}',
    paths jsonb NOT NULL DEFAULT '{}',
    artifacts jsonb NOT NULL DEFAULT '{}',
    created_at text NOT NULL DEFAULT '',
    updated_at text NOT NULL DEFAULT ''
);
CREATE TABLE %s.task_events (
    id serial PRIMARY KEY,
    task_id text NOT NULL,
    time text NOT NULL,
    type text NOT NULL,
    message text NOT NULL DEFAULT '',
    extra jsonb NOT NULL DEFAULT '{}'
);
CREATE TABLE %s.rss_drafts (
    draft_id text PRIMARY KEY,
    payload jsonb NOT NULL DEFAULT '{}',
    created_at text NOT NULL DEFAULT '',
    updated_at text NOT NULL DEFAULT ''
);
CREATE TABLE %s.watch_queue_items (
    item_id text PRIMARY KEY,
    payload jsonb NOT NULL DEFAULT '{}',
    created_at text NOT NULL DEFAULT '',
    updated_at text NOT NULL DEFAULT ''
);
CREATE TABLE %s.intake_drafts (
    draft_id text PRIMARY KEY,
    payload jsonb NOT NULL DEFAULT '{}',
    created_at text NOT NULL DEFAULT '',
    updated_at text NOT NULL DEFAULT ''
);
CREATE TABLE %s.deleted_task_archive (task_id text PRIMARY KEY);
`, schema, schema, schema, schema, schema, schema, schema)
	if _, err := pool.Exec(ctx, setup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE")
	})
	insert := fmt.Sprintf(`
INSERT INTO %s.tasks (task_id, canonical_series, created_at, updated_at)
VALUES ('task-1', 'Structured Series', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z');
INSERT INTO %s.task_events (task_id, time, type, message)
VALUES ('task-1', '2026-01-01T01:02:03Z', 'created', 'created');
INSERT INTO %s.watch_queue_items (item_id, payload)
VALUES
    ('queue-duplicate', '{"task_id":"task-1","canonical_series":"Duplicate"}'::jsonb),
    ('queue-only', '{"task_id":"task-2","canonical_series":"Queue Series"}'::jsonb);
INSERT INTO %s.rss_drafts (draft_id, payload)
VALUES ('rss-1', '{"canonical_series":"RSS Series","source_season":2,"feed_url":"https://example.test/feed.xml"}'::jsonb);
INSERT INTO %s.intake_drafts (draft_id, payload) VALUES (
    'intake-1',
    '{"runtime_task":{"task_id":"task-1"},"tmdb":{"tmdb_id":42,"episodes":[{"episode_number":1,"name":"One"}]}}'::jsonb
);
INSERT INTO %s.deleted_task_archive (task_id) VALUES ('deleted-1');
`, schema, schema, schema, schema, schema, schema)
	if _, err := pool.Exec(ctx, insert); err != nil {
		t.Fatal(err)
	}

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	snapshot, err := (PostgresSource{DatabaseURL: parsed.String()}).Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if snapshot.DatabaseIdentity == "" {
		t.Fatal("PostgreSQL source did not record its database identity")
	}
	if snapshot.Inventory["tasks"] != 1 || snapshot.Inventory["watchQueue"] != 2 || snapshot.Inventory["rssDrafts"] != 1 || snapshot.Inventory["intakeDrafts"] != 1 || snapshot.Inventory["deletedTasks"] != 1 {
		t.Fatalf("inventory = %#v", snapshot.Inventory)
	}
	if len(snapshot.Records) != 2 || len(snapshot.RSSDrafts) != 1 {
		t.Fatalf("snapshot counts = tasks %d RSS %d", len(snapshot.Records), len(snapshot.RSSDrafts))
	}
	var structured, queueOnly Record
	for _, record := range snapshot.Records {
		switch record.LegacyID {
		case "task-1":
			structured = record
		case "task-2":
			queueOnly = record
		}
	}
	if textFrom(structured.Payload, "canonical_series") != "Structured Series" || len(structured.History) != 1 || textFrom(structured.History[0], "time") != "2026-01-01T01:02:03Z" || positiveInt(objectValue(structured.Payload["tmdb"])["tmdb_id"]) != 42 {
		t.Fatalf("structured task = %#v", structured)
	}
	if textFrom(queueOnly.Payload, "canonical_series") != "Queue Series" {
		t.Fatalf("queue-only task = %#v", queueOnly)
	}
}
