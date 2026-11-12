//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func TestFailedAcquisitionDeletionRestoresRecoveryEntryIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	lifecycle := repository.NewOperationLifecycle(queries, transactor)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)
	workflow := NewAcquisitionDeletionWorkflow(queries, transactor, scheduler)

	actorID, seriesID, acquisitionID, downloadID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "recovery-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Deletion Recovery Series')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', 'magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb')`, acquisitionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, attempt, status) VALUES ($1, $2, 1, 'file_resolution_pending')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}

	operation, err := workflow.RequestDeletion(ctx, acquisitionID, "recovery-delete-"+acquisitionID.String(), actorID)
	if err != nil {
		t.Fatalf("RequestDeletion() error = %v", err)
	}
	var cancelledStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM downloads WHERE id = $1`, downloadID).Scan(&cancelledStatus); err != nil {
		t.Fatal(err)
	}
	if cancelledStatus != "cancelled" {
		t.Fatalf("file_resolution_pending download status = %q, want cancelled", cancelledStatus)
	}

	if _, err := lifecycle.StartAttempt(ctx, operation.ID, 1, "fixture-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := lifecycle.FailAttempt(ctx, operation.ID, 1, "acquisition_delete_file_failed", "fixture delete failure", false); err != nil {
		t.Fatalf("FailAttempt() error = %v", err)
	}

	var restoredMarker bool
	if err := pool.QueryRow(ctx, `SELECT deletion_requested_at IS NOT NULL FROM acquisitions WHERE id = $1`, acquisitionID).Scan(&restoredMarker); err != nil {
		t.Fatal(err)
	}
	if restoredMarker {
		t.Fatal("acquisition recovery marker should be cleared after terminal failure")
	}
	var recoveryEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = 'acquisition.deletion_recovery_required' AND resource_id = $1`, acquisitionID).Scan(&recoveryEvents); err != nil {
		t.Fatal(err)
	}
	if recoveryEvents != 1 {
		t.Fatalf("deletion recovery event count = %d, want 1", recoveryEvents)
	}
	var restoredStatus, restoredCode string
	if err := pool.QueryRow(ctx, `SELECT status, error_code FROM downloads WHERE id = $1`, downloadID).Scan(&restoredStatus, &restoredCode); err != nil {
		t.Fatal(err)
	}
	if restoredStatus != "cancelled" || restoredCode != "deletion_failed" {
		t.Fatalf("restored download = %s/%s, want cancelled/deletion_failed", restoredStatus, restoredCode)
	}
	readService := NewReadService(queries, transactor)
	view, err := readService.GetDownload(ctx, downloadID)
	if err != nil {
		t.Fatalf("GetDownload() error = %v", err)
	}
	if view.Actions.CanRetry || view.Actions.CanCancel || !view.Actions.CanDelete {
		t.Fatalf("recovered download actions = %#v, want delete only", view.Actions)
	}
	page, err := readService.ListDownloads(ctx, nil, 100, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListDownloads() error = %v", err)
	}
	listed := false
	for _, item := range page.Items {
		if item.ID == downloadID {
			listed = true
			break
		}
	}
	if !listed {
		t.Fatal("recovered download is missing from the downloads list")
	}

	retry, err := workflow.RequestDeletion(ctx, acquisitionID, "recovery-delete-retry-"+acquisitionID.String(), actorID)
	if err != nil {
		t.Fatalf("retry RequestDeletion() error = %v", err)
	}
	if retry.ID == operation.ID || retry.Status != "queued" {
		t.Fatalf("retry operation = %#v", retry)
	}
}

func TestFailedRSSDeletionRestoresSubscriptionIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	lifecycle := repository.NewOperationLifecycle(queries, transactor)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)
	workflow := NewRSSWorkflow(queries, transactor, scheduler)

	actorID, seriesID, subscriptionID, entryID, acquisitionID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-recovery-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'RSS Recovery Series')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Recovery Subscription', $3, true, 900, 1)`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_entries (id, subscription_id, identity_key, title, status) VALUES ($1, $2, 'recovery-entry', 'Recovery Episode', 'enqueued')`, entryID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, acquisitionID, seriesID, entryID); err != nil {
		t.Fatal(err)
	}

	operation, err := workflow.RequestSubscriptionDeletion(ctx, subscriptionID, 1, "rss-recovery-delete-"+subscriptionID.String(), false, actorID)
	if err != nil {
		t.Fatalf("RequestSubscriptionDeletion() error = %v", err)
	}
	if _, err := lifecycle.StartAttempt(ctx, operation.ID, 1, "fixture-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := lifecycle.FailAttempt(ctx, operation.ID, 1, "rss_delete_storage_unavailable", "fixture delete failure", false); err != nil {
		t.Fatalf("FailAttempt() error = %v", err)
	}

	var archived bool
	var restoredVersion int
	if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL, version FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&archived, &restoredVersion); err != nil {
		t.Fatal(err)
	}
	if archived || restoredVersion != 3 {
		t.Fatalf("subscription archived = %t version = %d, want restored version 3", archived, restoredVersion)
	}
	var acquisitionMarker bool
	if err := pool.QueryRow(ctx, `SELECT deletion_requested_at IS NOT NULL FROM acquisitions WHERE id = $1`, acquisitionID).Scan(&acquisitionMarker); err != nil {
		t.Fatal(err)
	}
	if acquisitionMarker {
		t.Fatal("subscription acquisition deletion marker should be cleared after terminal failure")
	}

	retry, err := workflow.RequestSubscriptionDeletion(ctx, subscriptionID, 3, "rss-recovery-delete-retry-"+subscriptionID.String(), false, actorID)
	if err != nil {
		t.Fatalf("retry RequestSubscriptionDeletion() error = %v", err)
	}
	if retry.ID == operation.ID || retry.Status != "queued" {
		t.Fatalf("retry operation = %#v", retry)
	}
}

func TestQueuedRSSPollCancellationReleasesDeletionGuardIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)
	workflow := NewRSSWorkflow(queries, transactor, scheduler)
	deletions := NewAcquisitionDeletionWorkflow(queries, transactor, scheduler)

	actorID, seriesID, subscriptionID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-poll-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'RSS Poll Cancellation')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Long Poll Subscription', $3, true, 86400, 1)`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	poll, err := scheduler.Schedule(ctx, ScheduleOperationRequest{
		Kind: appqueue.KindRSSPoll, ResourceType: "rss_subscription", ResourceID: subscriptionID,
		IdempotencyKey: "rss.poll.continuous:" + subscriptionID.String(),
		MaxAttempts:    5, Timeout: 30 * time.Second,
		Payload: map[string]any{"continuous": true, "subscriptionId": subscriptionID, "subscriptionVersion": 1},
	})
	if err != nil {
		t.Fatalf("schedule RSS poll: %v", err)
	}
	if poll.Operation.RiverJobID == 0 {
		t.Fatal("scheduled RSS poll has no River job ID")
	}
	// 模拟 River snooze：作业计划在轮询间隔之后，操作回到 queued 且不占用尝试次数。
	if _, err := pool.Exec(ctx, `UPDATE river_job SET state = 'scheduled', scheduled_at = now() + interval '1 day' WHERE id = $1`, poll.Operation.RiverJobID); err != nil {
		t.Fatal(err)
	}

	deletion, err := workflow.RequestSubscriptionDeletion(ctx, subscriptionID, 1, "rss-poll-delete-"+subscriptionID.String(), false, actorID)
	if err != nil {
		t.Fatalf("RequestSubscriptionDeletion() error = %v", err)
	}
	var pollStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, poll.Operation.ID).Scan(&pollStatus); err != nil {
		t.Fatal(err)
	}
	var jobState string
	if err := pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id = $1`, poll.Operation.RiverJobID).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if pollStatus != "cancelled" || jobState != "cancelled" {
		t.Fatalf("queued RSS poll after deletion = operation %q job %q, want cancelled/cancelled", pollStatus, jobState)
	}
	ready, err := deletions.SubscriptionDeletionReady(ctx, subscriptionID, deletion.ID)
	if err != nil || !ready {
		t.Fatalf("SubscriptionDeletionReady() = %t, %v; want ready", ready, err)
	}
}

func TestQueuedRSSCancellationHandlesMissingRiverJobIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)
	workflow := NewRSSWorkflow(queries, transactor, scheduler)
	deletions := NewAcquisitionDeletionWorkflow(queries, transactor, scheduler)

	actorID, seriesID, subscriptionID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-lost-job-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'RSS Lost Job Cancellation')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Lost Job Subscription', $3, true, 86400, 1)`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	poll, err := scheduler.Schedule(ctx, ScheduleOperationRequest{
		Kind: appqueue.KindRSSPoll, ResourceType: "rss_subscription", ResourceID: subscriptionID,
		IdempotencyKey: "rss.poll.lost:" + subscriptionID.String(),
		MaxAttempts:    5, Timeout: 30 * time.Second,
		Payload: map[string]any{"continuous": true, "subscriptionId": subscriptionID, "subscriptionVersion": 1},
	})
	if err != nil {
		t.Fatalf("schedule RSS poll: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM river_job WHERE id = $1`, poll.Operation.RiverJobID); err != nil {
		t.Fatal(err)
	}

	deletion, err := workflow.RequestSubscriptionDeletion(ctx, subscriptionID, 1, "rss-lost-delete-"+subscriptionID.String(), false, actorID)
	if err != nil {
		t.Fatalf("RequestSubscriptionDeletion() error = %v", err)
	}
	var pollStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, poll.Operation.ID).Scan(&pollStatus); err != nil {
		t.Fatal(err)
	}
	if pollStatus != "cancelled" {
		t.Fatalf("queued RSS poll with lost River job status = %q, want cancelled", pollStatus)
	}
	ready, err := deletions.SubscriptionDeletionReady(ctx, subscriptionID, deletion.ID)
	if err != nil || !ready {
		t.Fatalf("SubscriptionDeletionReady() = %t, %v; want ready", ready, err)
	}
}
