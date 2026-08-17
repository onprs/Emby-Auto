//go:build integration

package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

type rssProgressFixtureIDs struct {
	subscriptionID uuid.UUID
	downloadID     uuid.UUID
}

func seedRSSProgressFixture(
	t *testing.T,
	ctx context.Context,
	fixture recoveryFixture,
	initialDownloadProgress float64,
) rssProgressFixtureIDs {
	t.Helper()
	subscriptionID, entryID := uuid.New(), uuid.New()
	acquisitionID, downloadID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, $3, $4, true, 900, 1)
`, subscriptionID, fixture.seriesID, "Progress "+subscriptionID.String(), "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status
) VALUES ($1, $2, $3, 'Progress S01E01', 'https://example.test/episode.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued')
`, entryID, subscriptionID, "guid:"+entryID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id)
VALUES ($1, $2, 'rss', $3)
`, acquisitionID, fixture.seriesID, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, progress)
VALUES ($1, $2, 'downloading', $3)
`, downloadID, acquisitionID, initialDownloadProgress); err != nil {
		t.Fatal(err)
	}
	return rssProgressFixtureIDs{subscriptionID: subscriptionID, downloadID: downloadID}
}

func TestRSSSubscriptionProgressCommitHookIsAtomicWithBusinessStateAndEventIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	ids := seedRSSProgressFixture(t, ctx, fixture, 0)
	if reconciled, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil || reconciled != 1 {
		t.Fatalf("initial reconciliation = %d, %v", reconciled, err)
	}

	var baselineRevision int64
	if err := fixture.pool.QueryRow(ctx, `SELECT source_revision FROM rss_subscription_progress WHERE subscription_id = $1`, ids.subscriptionID).Scan(&baselineRevision); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("forced before-commit failure")
	fixture.transactor.RegisterBeforeCommitHook("test_failure", func(context.Context, database.TxScope) error {
		return wantErr
	})
	rolledBackEventID := uuid.New()
	err := fixture.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Tx.Exec(ctx, `UPDATE downloads SET progress = 0.5, updated_at = now() WHERE id = $1`, ids.downloadID); err != nil {
			return err
		}
		_, err := scope.Tx.Exec(ctx, `
INSERT INTO events (id, topic, resource_type, resource_id, data)
VALUES ($1, 'test.progress.atomicity', 'rss_subscription', $2, '{}'::jsonb)
`, rolledBackEventID, ids.subscriptionID)
		return err
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithinTx() error = %v, want %v", err, wantErr)
	}

	var downloadProgress, overallProgress float64
	var sourceRevision, calculatedRevision int64
	var dirty bool
	if err := fixture.pool.QueryRow(ctx, `SELECT progress::double precision FROM downloads WHERE id = $1`, ids.downloadID).Scan(&downloadProgress); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `
SELECT overall_progress, source_revision, calculated_revision, dirty
FROM rss_subscription_progress
WHERE subscription_id = $1
`, ids.subscriptionID).Scan(&overallProgress, &sourceRevision, &calculatedRevision, &dirty); err != nil {
		t.Fatal(err)
	}
	var rolledBackEvents int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = $1`, rolledBackEventID).Scan(&rolledBackEvents); err != nil {
		t.Fatal(err)
	}
	if downloadProgress != 0 || overallProgress != 0.02 || dirty || sourceRevision != baselineRevision || calculatedRevision != baselineRevision || rolledBackEvents != 0 {
		t.Fatalf("rollback leaked state: download %.3f progress %.3f dirty %t revisions %d/%d baseline %d events %d", downloadProgress, overallProgress, dirty, sourceRevision, calculatedRevision, baselineRevision, rolledBackEvents)
	}

	fixture.transactor.RegisterBeforeCommitHook("test_failure", func(context.Context, database.TxScope) error { return nil })
	committedEventID := uuid.New()
	if err := fixture.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Tx.Exec(ctx, `UPDATE downloads SET progress = 0.5, updated_at = now() WHERE id = $1`, ids.downloadID); err != nil {
			return err
		}
		_, err := scope.Tx.Exec(ctx, `
INSERT INTO events (id, topic, resource_type, resource_id, data)
VALUES ($1, 'test.progress.atomicity', 'rss_subscription', $2, '{}'::jsonb)
`, committedEventID, ids.subscriptionID)
		return err
	}); err != nil {
		t.Fatalf("successful WithinTx() error = %v", err)
	}
	if err := fixture.pool.QueryRow(ctx, `
SELECT overall_progress, source_revision, calculated_revision, dirty
FROM rss_subscription_progress
WHERE subscription_id = $1
`, ids.subscriptionID).Scan(&overallProgress, &sourceRevision, &calculatedRevision, &dirty); err != nil {
		t.Fatal(err)
	}
	var committedEvents int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = $1`, committedEventID).Scan(&committedEvents); err != nil {
		t.Fatal(err)
	}
	if math.Abs(overallProgress-0.16) > 1e-12 || dirty || sourceRevision != calculatedRevision || sourceRevision <= baselineRevision || committedEvents != 1 {
		t.Fatalf("commit state: progress %.12f dirty %t revisions %d/%d baseline %d events %d", overallProgress, dirty, sourceRevision, calculatedRevision, baselineRevision, committedEvents)
	}
}

func TestRSSSubscriptionProgressReconciliationRecoversCancelledExternalWriteIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	ids := seedRSSProgressFixture(t, ctx, fixture, 0)
	if _, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.pool.Exec(ctx, `UPDATE downloads SET progress = 0.75, updated_at = now() WHERE id = $1`, ids.downloadID); err != nil {
		t.Fatal(err)
	}
	var dirty bool
	var staleProgress float64
	if err := fixture.pool.QueryRow(ctx, `SELECT dirty, overall_progress FROM rss_subscription_progress WHERE subscription_id = $1`, ids.subscriptionID).Scan(&dirty, &staleProgress); err != nil {
		t.Fatal(err)
	}
	if !dirty || staleProgress != 0.02 {
		t.Fatalf("external write projection = dirty %t progress %.3f, want true/0.02", dirty, staleProgress)
	}

	cancelledCtx, cancelReconcile := context.WithCancel(ctx)
	cancelReconcile()
	if _, err := workflow.ReconcileSubscriptionProgress(cancelledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reconciliation error = %v, want context canceled", err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT dirty FROM rss_subscription_progress WHERE subscription_id = $1`, ids.subscriptionID).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("cancelled reconciliation incorrectly marked projection clean")
	}

	reconciled, err := workflow.ReconcileSubscriptionProgress(ctx)
	if err != nil || reconciled != 1 {
		t.Fatalf("recovery reconciliation = %d, %v, want 1, nil", reconciled, err)
	}
	detail, err := workflow.GetSubscription(ctx, ids.subscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(detail.OverallProgress-0.23) > 1e-12 || detail.TaskCount != 1 {
		t.Fatalf("recovered detail = %#v, want progress 0.23 and one task", detail)
	}
	if replayed, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil || replayed != 0 {
		t.Fatalf("replayed reconciliation = %d, %v, want 0, nil", replayed, err)
	}
}

func TestRSSSubscriptionProgressConcurrentWritePreservesNewDirtyRevisionIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	ids := seedRSSProgressFixture(t, ctx, fixture, 0)
	if _, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE downloads SET progress = 0.5, updated_at = now() WHERE id = $1`, ids.downloadID); err != nil {
		t.Fatal(err)
	}

	reconcileTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reconcileTx.Rollback(context.Background()) }()
	var sourceRevision int64
	if err := reconcileTx.QueryRow(ctx, `
SELECT source_revision
FROM rss_subscription_progress
WHERE subscription_id = $1
FOR UPDATE
`, ids.subscriptionID).Scan(&sourceRevision); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		close(started)
		_, writeErr := fixture.pool.Exec(ctx, `UPDATE downloads SET progress = 0.9, updated_at = now() WHERE id = $1`, ids.downloadID)
		writeDone <- writeErr
	}()
	<-started
	select {
	case writeErr := <-writeDone:
		t.Fatalf("concurrent write completed before locked reconciliation committed: %v", writeErr)
	case <-time.After(50 * time.Millisecond):
	}

	if err := recalculateRSSSubscriptionProgress(ctx, db.New(reconcileTx), []rssSubscriptionProgressCandidate{{
		subscriptionID: ids.subscriptionID,
		sourceRevision: sourceRevision,
	}}); err != nil {
		t.Fatalf("locked recalculation error = %v", err)
	}
	if err := reconcileTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if writeErr := <-writeDone; writeErr != nil {
		t.Fatalf("concurrent write error = %v", writeErr)
	}

	var dirty bool
	var calculatedRevision, latestRevision int64
	if err := fixture.pool.QueryRow(ctx, `
SELECT dirty, calculated_revision, source_revision
FROM rss_subscription_progress
WHERE subscription_id = $1
`, ids.subscriptionID).Scan(&dirty, &calculatedRevision, &latestRevision); err != nil {
		t.Fatal(err)
	}
	if !dirty || latestRevision <= calculatedRevision {
		t.Fatalf("post-concurrency revisions = dirty %t calculated/source %d/%d", dirty, calculatedRevision, latestRevision)
	}
	if reconciled, err := workflow.ReconcileSubscriptionProgress(ctx); err != nil || reconciled != 1 {
		t.Fatalf("final reconciliation = %d, %v", reconciled, err)
	}
	detail, err := workflow.GetSubscription(ctx, ids.subscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(detail.OverallProgress-0.272) > 1e-12 {
		t.Fatalf("final progress = %.12f, want 0.272", detail.OverallProgress)
	}
}
