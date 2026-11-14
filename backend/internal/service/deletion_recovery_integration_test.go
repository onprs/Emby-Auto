//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
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

type rssDeletionRecoveryFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	queries    *db.Queries
	transactor *database.Transactor
	lifecycle  *repository.OperationLifecycle
	scheduler  *OperationScheduler
	workflow   *RSSWorkflow
	actorID    uuid.UUID
	seriesID   uuid.UUID
}

func newRSSDeletionRecoveryFixture(t *testing.T) rssDeletionRecoveryFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)
	fixture := rssDeletionRecoveryFixture{
		ctx: ctx, pool: pool, queries: queries, transactor: transactor,
		lifecycle: repository.NewOperationLifecycle(queries, transactor),
		scheduler: scheduler, workflow: NewRSSWorkflow(queries, transactor, scheduler),
		actorID: uuid.New(), seriesID: uuid.New(),
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, fixture.actorID, "poll-state-"+fixture.actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Deletion Poll State Series')`, fixture.seriesID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture rssDeletionRecoveryFixture) createSubscription(t *testing.T, enabled bool, nextPollAt *time.Time) uuid.UUID {
	t.Helper()
	subscriptionID := uuid.New()
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, next_poll_at)
VALUES ($1, $2, 'Deletion Poll State Subscription', $3, $4, 900, 1, $5)`,
		subscriptionID, fixture.seriesID, "https://example.test/"+subscriptionID.String()+".xml", enabled, nextPollAt); err != nil {
		t.Fatal(err)
	}
	return subscriptionID
}

func (fixture rssDeletionRecoveryFixture) requestDeletion(t *testing.T, subscriptionID uuid.UUID, expectedVersion int32, key string, deleteImported bool) domain.Operation {
	t.Helper()
	operation, err := fixture.workflow.RequestSubscriptionDeletion(fixture.ctx, subscriptionID, expectedVersion, key, deleteImported, fixture.actorID)
	if err != nil {
		t.Fatalf("RequestSubscriptionDeletion() error = %v", err)
	}
	return operation
}

func (fixture rssDeletionRecoveryFixture) terminalDeletionFailure(t *testing.T, operation domain.Operation) {
	t.Helper()
	if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, operation.ID, 1, "fixture-worker"); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if err := fixture.lifecycle.FailAttempt(fixture.ctx, operation.ID, 1, "rss_delete_storage_unavailable", "fixture delete failure", false); err != nil {
		t.Fatalf("FailAttempt() error = %v", err)
	}
}

type rssDeletionSubscriptionState struct {
	enabled     bool
	deleted     bool
	completed   bool
	nextPollAt  *time.Time
	version     int32
	deletionLog bool
}

func (fixture rssDeletionRecoveryFixture) subscriptionState(t *testing.T, subscriptionID uuid.UUID) rssDeletionSubscriptionState {
	t.Helper()
	var state rssDeletionSubscriptionState
	var pollStateRestored bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT enabled, deleted_at IS NOT NULL, completed_at IS NOT NULL, next_poll_at, version
FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(
		&state.enabled, &state.deleted, &state.completed, &state.nextPollAt, &state.version,
	); err != nil {
		t.Fatalf("load subscription state: %v", err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT (data->>'pollStateRestored')::boolean
FROM events
WHERE topic = 'rss.subscription.deletion_recovery_required' AND resource_id = $1
ORDER BY event_sequence LIMIT 1`, subscriptionID).Scan(&pollStateRestored); err != nil {
		t.Fatalf("load deletion recovery event: %v", err)
	}
	state.deletionLog = pollStateRestored
	return state
}

func (fixture rssDeletionRecoveryFixture) countPollOperations(t *testing.T, subscriptionID uuid.UUID) int {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT count(*) FROM operations
WHERE kind = 'rss.poll' AND resource_type = 'rss_subscription' AND resource_id = $1`, subscriptionID).Scan(&count); err != nil {
		t.Fatalf("count RSS poll operations: %v", err)
	}
	return count
}

// TestFailedRSSDeletionRestoresPollIntentIntegration 验证终态失败恢复删除前的
// 启用意图与轮询计划，且正式 reconciliation 只补建一次轮询。
func TestFailedRSSDeletionRestoresPollIntentIntegration(t *testing.T) {
	fixture := newRSSDeletionRecoveryFixture(t)
	dueAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	subscriptionID := fixture.createSubscription(t, true, &dueAt)

	operation := fixture.requestDeletion(t, subscriptionID, 1, "poll-state-delete-"+subscriptionID.String(), false)
	var captured bool
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT payload->'pollState' IS NOT NULL FROM operations WHERE id = $1`, operation.ID).Scan(&captured); err != nil {
		t.Fatal(err)
	}
	if !captured {
		t.Fatal("deletion operation payload is missing the captured poll state")
	}
	fixture.terminalDeletionFailure(t, operation)

	state := fixture.subscriptionState(t, subscriptionID)
	if state.deleted || !state.enabled || state.version != 3 {
		t.Fatalf("restored subscription = %#v, want enabled, not archived, version 3", state)
	}
	if state.nextPollAt == nil || !state.nextPollAt.UTC().Equal(dueAt) {
		t.Fatalf("restored next poll = %v, want %v", state.nextPollAt, dueAt)
	}
	if !state.deletionLog {
		t.Fatal("recovery event should report that the poll state was restored")
	}

	reconciled, err := fixture.workflow.ReconcileDuePolls(fixture.ctx)
	if err != nil {
		t.Fatalf("ReconcileDuePolls() error = %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("ReconcileDuePolls() = %d, want 1", reconciled)
	}
	if count := fixture.countPollOperations(t, subscriptionID); count != 1 {
		t.Fatalf("RSS poll operations after reconcile = %d, want 1", count)
	}
	reconciled, err = fixture.workflow.ReconcileDuePolls(fixture.ctx)
	if err != nil {
		t.Fatalf("ReconcileDuePolls() replay error = %v", err)
	}
	if reconciled != 0 {
		t.Fatalf("ReconcileDuePolls() replay = %d, want 0", reconciled)
	}
	if count := fixture.countPollOperations(t, subscriptionID); count != 1 {
		t.Fatalf("RSS poll operations after reconcile replay = %d, want 1", count)
	}
}

// TestFailedRSSDeletionKeepsPausedSubscriptionIntegration 验证删除前已停用的订阅
// 不会被失败恢复意外启用。
func TestFailedRSSDeletionKeepsPausedSubscriptionIntegration(t *testing.T) {
	fixture := newRSSDeletionRecoveryFixture(t)
	subscriptionID := fixture.createSubscription(t, false, nil)
	operation := fixture.requestDeletion(t, subscriptionID, 1, "poll-state-paused-"+subscriptionID.String(), false)
	fixture.terminalDeletionFailure(t, operation)

	state := fixture.subscriptionState(t, subscriptionID)
	if state.deleted || state.enabled || state.version != 3 {
		t.Fatalf("restored paused subscription = %#v, want disabled, not archived, version 3", state)
	}
	if state.nextPollAt != nil {
		t.Fatalf("restored paused subscription next poll = %v, want nil", state.nextPollAt)
	}
	if !state.deletionLog {
		t.Fatal("recovery event should report that the paused poll state was restored")
	}
	reconciled, err := fixture.workflow.ReconcileDuePolls(fixture.ctx)
	if err != nil {
		t.Fatalf("ReconcileDuePolls() error = %v", err)
	}
	if reconciled != 0 || fixture.countPollOperations(t, subscriptionID) != 0 {
		t.Fatalf("paused subscription reconciled = %d, want 0 polls", reconciled)
	}
}

// TestFailedRSSDeletionDoesNotReenableCompletedSubscriptionIntegration 验证已完成
// 订阅恢复后保持完成状态且不产生新轮询。
func TestFailedRSSDeletionDoesNotReenableCompletedSubscriptionIntegration(t *testing.T) {
	fixture := newRSSDeletionRecoveryFixture(t)
	dueAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	subscriptionID := fixture.createSubscription(t, true, &dueAt)
	if _, err := fixture.pool.Exec(fixture.ctx, `
UPDATE rss_subscriptions SET enabled = false, next_poll_at = NULL, completed_at = now() WHERE id = $1`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	operation := fixture.requestDeletion(t, subscriptionID, 1, "poll-state-completed-"+subscriptionID.String(), false)
	fixture.terminalDeletionFailure(t, operation)

	state := fixture.subscriptionState(t, subscriptionID)
	if state.deleted || state.enabled || !state.completed || state.version != 3 {
		t.Fatalf("restored completed subscription = %#v, want disabled and still completed", state)
	}
	if state.nextPollAt != nil {
		t.Fatalf("restored completed subscription next poll = %v, want nil", state.nextPollAt)
	}
	reconciled, err := fixture.workflow.ReconcileDuePolls(fixture.ctx)
	if err != nil {
		t.Fatalf("ReconcileDuePolls() error = %v", err)
	}
	if reconciled != 0 || fixture.countPollOperations(t, subscriptionID) != 0 {
		t.Fatalf("completed subscription reconciled = %d, want 0 polls", reconciled)
	}
}

// TestFailedRSSDeletionWithoutPollStateSnapshotKeepsSubscriptionPausedIntegration
// 验证缺少快照的旧在途操作沿用既有恢复行为，不猜测原 enabled。
func TestFailedRSSDeletionWithoutPollStateSnapshotKeepsSubscriptionPausedIntegration(t *testing.T) {
	fixture := newRSSDeletionRecoveryFixture(t)
	dueAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	subscriptionID := fixture.createSubscription(t, true, &dueAt)
	operation := fixture.requestDeletion(t, subscriptionID, 1, "poll-state-legacy-"+subscriptionID.String(), false)
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operations SET payload = payload - 'pollState' WHERE id = $1`, operation.ID); err != nil {
		t.Fatal(err)
	}
	fixture.terminalDeletionFailure(t, operation)

	state := fixture.subscriptionState(t, subscriptionID)
	if state.deleted || state.enabled || state.version != 3 {
		t.Fatalf("legacy restore = %#v, want disabled, not archived, version 3", state)
	}
	if state.nextPollAt != nil {
		t.Fatalf("legacy restore next poll = %v, want nil", state.nextPollAt)
	}
	if state.deletionLog {
		t.Fatal("legacy recovery event must not claim a restored poll state")
	}
	reconciled, err := fixture.workflow.ReconcileDuePolls(fixture.ctx)
	if err != nil {
		t.Fatalf("ReconcileDuePolls() error = %v", err)
	}
	if reconciled != 0 {
		t.Fatalf("legacy restore reconciled = %d, want 0", reconciled)
	}
}

// TestFailedRSSDeletionRestoreRespectsGuardsIntegration 验证恢复的版本守卫和
// 新一代删除守卫都不会撤销更新的删除请求。
func TestFailedRSSDeletionRestoreRespectsGuardsIntegration(t *testing.T) {
	fixture := newRSSDeletionRecoveryFixture(t)
	dueAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	subscriptionID := fixture.createSubscription(t, true, &dueAt)
	first := fixture.requestDeletion(t, subscriptionID, 1, "poll-state-guard-first-"+subscriptionID.String(), false)
	fixture.terminalDeletionFailure(t, first)
	second := fixture.requestDeletion(t, subscriptionID, 3, "poll-state-guard-second-"+subscriptionID.String(), false)

	staleRestore, err := fixture.queries.RestoreRSSSubscriptionAfterFailedDeletionWithPollState(fixture.ctx, db.RestoreRSSSubscriptionAfterFailedDeletionWithPollStateParams{
		Enabled: true, NextPollAt: pgtype.Timestamptz{Time: dueAt, Valid: true},
		ID: repository.UUIDToPG(subscriptionID), ArchivedVersion: 2, OperationID: repository.UUIDToPG(first.ID),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale restore = %#v, %v; want no rows", staleRestore, err)
	}
	newerGenerationRestore, err := fixture.queries.RestoreRSSSubscriptionAfterFailedDeletionWithPollState(fixture.ctx, db.RestoreRSSSubscriptionAfterFailedDeletionWithPollStateParams{
		Enabled: true, NextPollAt: pgtype.Timestamptz{Time: dueAt, Valid: true},
		ID: repository.UUIDToPG(subscriptionID), ArchivedVersion: 4, OperationID: repository.UUIDToPG(first.ID),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("new generation restore = %#v, %v; want no rows", newerGenerationRestore, err)
	}
	state := fixture.subscriptionState(t, subscriptionID)
	if !state.deleted || state.enabled || state.version != 4 {
		t.Fatalf("guarded restoration changed subscription = %#v, want archived version 4", state)
	}
	var secondStatus string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT status FROM operations WHERE id = $1`, second.ID).Scan(&secondStatus); err != nil {
		t.Fatal(err)
	}
	if secondStatus != "queued" {
		t.Fatalf("newer deletion operation status = %q, want queued", secondStatus)
	}
}

// TestFailedRSSDeletionRequestReplayKeepsCapturedPollStateIntegration 验证相同
// 幂等键重放返回既有操作且不重算快照。
func TestFailedRSSDeletionRequestReplayKeepsCapturedPollStateIntegration(t *testing.T) {
	fixture := newRSSDeletionRecoveryFixture(t)
	dueAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	subscriptionID := fixture.createSubscription(t, true, &dueAt)
	key := "poll-state-replay-" + subscriptionID.String()
	first := fixture.requestDeletion(t, subscriptionID, 1, key, false)

	var payloadBefore string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT payload::text FROM operations WHERE id = $1`, first.ID).Scan(&payloadBefore); err != nil {
		t.Fatal(err)
	}
	replayed := fixture.requestDeletion(t, subscriptionID, 1, key, false)
	var payloadAfter string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT payload::text FROM operations WHERE id = $1`, replayed.ID).Scan(&payloadAfter); err != nil {
		t.Fatal(err)
	}
	if replayed.ID != first.ID {
		t.Fatalf("replayed operation = %s, want %s", replayed.ID, first.ID)
	}
	if payloadAfter != payloadBefore {
		t.Fatalf("replayed operation payload drifted:\nbefore=%s\nafter=%s", payloadBefore, payloadAfter)
	}

	_, err := fixture.workflow.RequestSubscriptionDeletion(fixture.ctx, subscriptionID, 1, key, true, fixture.actorID)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "idempotency_conflict" || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("different delete scope error = %#v, want idempotency_conflict", err)
	}
}

// TestFailedRSSDeletionKeepsFuturePollScheduleIntegration 验证恢复后尚未到期的
// 订阅保留原计划，不会立即补建轮询。
func TestFailedRSSDeletionKeepsFuturePollScheduleIntegration(t *testing.T) {
	fixture := newRSSDeletionRecoveryFixture(t)
	futureAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	subscriptionID := fixture.createSubscription(t, true, &futureAt)
	operation := fixture.requestDeletion(t, subscriptionID, 1, "poll-state-future-"+subscriptionID.String(), false)
	fixture.terminalDeletionFailure(t, operation)

	state := fixture.subscriptionState(t, subscriptionID)
	if state.deleted || !state.enabled || state.version != 3 {
		t.Fatalf("restored future subscription = %#v, want enabled, not archived, version 3", state)
	}
	if state.nextPollAt == nil || !state.nextPollAt.UTC().Equal(futureAt) {
		t.Fatalf("restored future next poll = %v, want %v", state.nextPollAt, futureAt)
	}
	reconciled, err := fixture.workflow.ReconcileDuePolls(fixture.ctx)
	if err != nil {
		t.Fatalf("ReconcileDuePolls() error = %v", err)
	}
	if reconciled != 0 || fixture.countPollOperations(t, subscriptionID) != 0 {
		t.Fatalf("future subscription reconciled = %d, want 0 polls before the schedule is due", reconciled)
	}
}
