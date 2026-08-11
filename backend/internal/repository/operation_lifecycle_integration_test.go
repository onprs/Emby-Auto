//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestDownloadOperationTerminalFailuresConvergeResourceAndAuditIntegration(t *testing.T) {
	_, pool := testutil.NewMigratedPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := db.New(pool)
	lifecycle := NewOperationLifecycle(queries, database.NewTransactor(pool))
	actorID, seriesID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "lifecycle-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Lifecycle Series')`, seriesID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		kind        string
		startStatus string
		wantStage   string
		withHash    bool
	}{
		{kind: "download.enqueue", startStatus: "enqueue_pending", wantStage: "enqueue"},
		{kind: "download.sync", startStatus: "downloading", wantStage: "sync", withHash: true},
		{kind: "download.materialize", startStatus: "selecting_files", wantStage: "materialize", withHash: true},
	}
	for index, test := range tests {
		t.Run(test.wantStage, func(t *testing.T) {
			acquisitionID, downloadID, operationID := uuid.New(), uuid.New(), uuid.New()
			if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by)
VALUES ($1, $2, 'manual', $3, $4)
`, acquisitionID, seriesID, "magnet:?xt=urn:btih:"+fmt.Sprintf("%040x", index+201), actorID); err != nil {
				t.Fatal(err)
			}
			var hash any
			if test.withHash {
				hash = fmt.Sprintf("%040x", index+301)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, torrent_hash, status) VALUES ($1, $2, $3, $4)`, downloadID, acquisitionID, hash, test.startStatus); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, attempt_count, timeout_seconds, started_at, heartbeat_at
) VALUES ($1, $2, 'download', $3, $4, 'running', 1, 1, 60, now(), now())
`, operationID, test.kind, downloadID, "terminal-"+operationID.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
INSERT INTO operation_attempts (id, operation_id, attempt, status, worker_id)
VALUES ($1, $2, 1, 'running', 'fixture-worker')
`, uuid.New(), operationID); err != nil {
				t.Fatal(err)
			}
			if err := lifecycle.FailAttempt(ctx, operationID, 1, "fixture_error", "fixture failure", true); err != nil {
				t.Fatalf("FailAttempt() error = %v", err)
			}
			var status, stage, operationStatus, attemptStatus string
			var version int
			if err := pool.QueryRow(ctx, `SELECT status, failure_stage, version FROM downloads WHERE id = $1`, downloadID).Scan(&status, &stage, &version); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, operationID).Scan(&operationStatus); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT status FROM operation_attempts WHERE operation_id = $1 AND attempt = 1`, operationID).Scan(&attemptStatus); err != nil {
				t.Fatal(err)
			}
			var resourceEvents, operationEvents int
			if err := pool.QueryRow(ctx, `
SELECT
    count(*) FILTER (WHERE topic LIKE 'download.%_failed'),
    count(*) FILTER (WHERE topic = 'operation.failed')
FROM events WHERE operation_id = $1
`, operationID).Scan(&resourceEvents, &operationEvents); err != nil {
				t.Fatal(err)
			}
			if status != "failed" || stage != test.wantStage || version != 2 || operationStatus != "failed" || attemptStatus != "failed" || resourceEvents != 1 || operationEvents != 1 {
				t.Fatalf("terminal state = download %s/%s/v%d operation %s attempt %s events %d/%d", status, stage, version, operationStatus, attemptStatus, resourceEvents, operationEvents)
			}
		})
	}
}

func TestDownloadEnqueueFailurePersistsRSSRetryabilityIntegration(t *testing.T) {
	_, pool := testutil.NewMigratedPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lifecycle := NewOperationLifecycle(db.New(pool), database.NewTransactor(pool))
	seriesID, subscriptionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'RSS Failure Retryability')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'RSS Failure Retryability', 'https://example.test/retryability.xml', true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}

	for index, test := range []struct {
		name      string
		code      string
		retryable bool
	}{
		{name: "temporary failure", code: "qbittorrent_unavailable", retryable: true},
		{name: "permanent duplicate", code: "duplicate_torrent", retryable: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			entryID, acquisitionID, downloadID, operationID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, $3, $4, $5, true, ARRAY[]::text[], 1, $6, 'enqueueing')`,
				entryID, subscriptionID, "guid:"+entryID.String(), fmt.Sprintf("Episode %d", index+1), fmt.Sprintf("https://example.test/%d.torrent", index+1), index+1); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id)
VALUES ($1, $2, 'rss', $3)`, acquisitionID, seriesID, entryID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'enqueue_pending')`, downloadID, acquisitionID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, attempt_count, timeout_seconds, started_at, heartbeat_at
) VALUES ($1, 'download.enqueue', 'download', $2, $3, 'running', 1, 1, 60, now(), now())`, operationID, downloadID, "rss-failure-"+operationID.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
INSERT INTO operation_attempts (id, operation_id, attempt, status, worker_id)
VALUES ($1, $2, 1, 'running', 'fixture-worker')`, uuid.New(), operationID); err != nil {
				t.Fatal(err)
			}

			if err := lifecycle.FailAttempt(ctx, operationID, 1, test.code, "fixture failure", test.retryable); err != nil {
				t.Fatalf("FailAttempt() error = %v", err)
			}
			var status, code string
			var retryable bool
			if err := pool.QueryRow(ctx, `SELECT status, last_error_code, last_error_retryable FROM rss_entries WHERE id = $1`, entryID).Scan(&status, &code, &retryable); err != nil {
				t.Fatal(err)
			}
			if status != "enqueue_failed" || code != test.code || retryable != test.retryable {
				t.Fatalf("RSS failure = %s/%s retryable=%t, want enqueue_failed/%s retryable=%t", status, code, retryable, test.code, test.retryable)
			}
		})
	}
}

func TestOperationCrashReplayClosesInterruptedAttemptBeforeRestartIntegration(t *testing.T) {
	_, pool := testutil.NewMigratedPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operationID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, idempotency_key, status, max_attempts, attempt_count, timeout_seconds, started_at, heartbeat_at
) VALUES ($1, 'rss.poll', $2, 'running', 3, 1, 60, now(), now())
`, operationID, "crash-"+operationID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operation_attempts (id, operation_id, attempt, status, worker_id)
VALUES ($1, $2, 1, 'running', 'crashed-worker')
`, uuid.New(), operationID); err != nil {
		t.Fatal(err)
	}
	lifecycle := NewOperationLifecycle(db.New(pool), database.NewTransactor(pool))
	operation, err := lifecycle.StartAttempt(ctx, operationID, 2, "replacement-worker")
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if operation.AttemptCount != 2 || operation.Status != "running" {
		t.Fatalf("replayed operation = %#v", operation)
	}
	var firstStatus, firstCode, secondStatus string
	if err := pool.QueryRow(ctx, `SELECT status, error_code FROM operation_attempts WHERE operation_id = $1 AND attempt = 1`, operationID).Scan(&firstStatus, &firstCode); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM operation_attempts WHERE operation_id = $1 AND attempt = 2`, operationID).Scan(&secondStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != "failed" || firstCode != "worker_interrupted" || secondStatus != "running" {
		t.Fatalf("attempt replay = first %s/%s second %s", firstStatus, firstCode, secondStatus)
	}
}
