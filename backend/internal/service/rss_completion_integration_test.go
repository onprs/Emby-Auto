//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestFinalRSSImportDisablesAndRetainsHistoryWithOptionalSourceCleanupIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	workflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)

	nonFinalSubscriptionID, nonFinalTaskID, nonFinalImportID, nonFinalOperationID := createRSSImportingTask(t, fixture, 1, 2, true)
	if err := workflow.CompleteImport(context.Background(), domain.ImportCompletion{
		TaskID: nonFinalTaskID, ImportID: nonFinalImportID, OperationID: nonFinalOperationID,
		DestinationVideoPath:    "/library/Show/Season1/Show - S01E01.mkv",
		DestinationSubtitlePath: "/library/Show/Season1/Show - S01E01.ass",
	}); err != nil {
		t.Fatalf("CompleteImport(non-final) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, nonFinalSubscriptionID, true, false, 0)

	preservedSubscriptionID, preservedTaskID, preservedImportID, preservedOperationID := createRSSImportingTask(t, fixture, 2, 2, false)
	if err := workflow.CompleteImport(context.Background(), domain.ImportCompletion{
		TaskID: preservedTaskID, ImportID: preservedImportID, OperationID: preservedOperationID,
		DestinationVideoPath:    "/library/Preserved/Season1/Preserved - S01E02.mkv",
		DestinationSubtitlePath: "/library/Preserved/Season1/Preserved - S01E02.ass",
	}); err != nil {
		t.Fatalf("CompleteImport(final without source cleanup) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, preservedSubscriptionID, false, true, 0)
	assertRSSCompletionRecordsRetained(t, fixture, preservedSubscriptionID, preservedTaskID)

	cleanupSubscriptionID, cleanupTaskID, cleanupImportID, cleanupOperationID := createRSSImportingTask(t, fixture, 2, 2, true)
	if err := workflow.CompleteImport(context.Background(), domain.ImportCompletion{
		TaskID: cleanupTaskID, ImportID: cleanupImportID, OperationID: cleanupOperationID,
		DestinationVideoPath:    "/library/Cleanup/Season1/Cleanup - S01E02.mkv",
		DestinationSubtitlePath: "/library/Cleanup/Season1/Cleanup - S01E02.ass",
	}); err != nil {
		t.Fatalf("CompleteImport(final with source cleanup) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, cleanupSubscriptionID, false, true, 1)
	assertRSSCompletionRecordsRetained(t, fixture, cleanupSubscriptionID, cleanupTaskID)

	anchoredSubscriptionID, anchoredTaskID, anchoredImportID, anchoredOperationID := createRSSImportingTask(t, fixture, 2, 2, false, true)
	if err := workflow.CompleteImport(context.Background(), domain.ImportCompletion{
		TaskID: anchoredTaskID, ImportID: anchoredImportID, OperationID: anchoredOperationID,
		DestinationVideoPath:    "/library/Anchored/Season1/Anchored - S01E02.mkv",
		DestinationSubtitlePath: "/library/Anchored/Season1/Anchored - S01E02.ass",
	}); err != nil {
		t.Fatalf("CompleteImport(final anchored profile) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, anchoredSubscriptionID, false, true, 0)
}

func TestLegacyFinalImportDeletionKeyDoesNotBlockRetainedCompletionIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	workflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	subscriptionID, taskID, importID, importOperationID := createRSSImportingTask(t, fixture, 2, 2, false)
	legacyOperationID := uuid.New()
	legacyKey := fmt.Sprintf("rss.final-import:%s:S01E02", subscriptionID)
	if _, err := fixture.pool.Exec(context.Background(), `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, timeout_seconds, payload, started_at, finished_at
)
VALUES ($1, 'rss.subscription.delete', 'rss_subscription', $2, $3, 'succeeded',
        3, 1800, '{"command":"delete","trigger":"final_import","deleteImported":false}'::jsonb, now(), now())
`, legacyOperationID, subscriptionID, legacyKey); err != nil {
		t.Fatal(err)
	}

	if err := workflow.CompleteImport(context.Background(), domain.ImportCompletion{
		TaskID: taskID, ImportID: importID, OperationID: importOperationID,
		DestinationVideoPath:    "/library/Legacy/Season1/Legacy - S01E02.mkv",
		DestinationSubtitlePath: "/library/Legacy/Season1/Legacy - S01E02.ass",
	}); err != nil {
		t.Fatalf("CompleteImport(with legacy final-import key) error = %v", err)
	}

	var enabled, completed, deletionRequested bool
	var completionCount, legacyDeletionCount int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT
    subscription.enabled,
    subscription.completed_at IS NOT NULL,
    (SELECT count(*) FROM operations WHERE resource_type = 'rss_subscription' AND resource_id = subscription.id AND kind = 'rss.subscription.complete'),
    (SELECT count(*) FROM operations WHERE resource_type = 'rss_subscription' AND resource_id = subscription.id AND kind = 'rss.subscription.delete'),
    EXISTS (
        SELECT 1
        FROM acquisitions AS acquisition
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE entry.subscription_id = subscription.id
          AND acquisition.deletion_requested_at IS NOT NULL
    )
FROM rss_subscriptions AS subscription
WHERE subscription.id = $1
`, subscriptionID).Scan(&enabled, &completed, &completionCount, &legacyDeletionCount, &deletionRequested); err != nil {
		t.Fatal(err)
	}
	if enabled || !completed || completionCount != 0 || legacyDeletionCount != 1 || deletionRequested {
		t.Fatalf("completion with legacy key = enabled %t completed %t complete ops %d legacy delete ops %d deletion requested %t", enabled, completed, completionCount, legacyDeletionCount, deletionRequested)
	}
}

func TestRSSFinalEpisodeWaitsForEveryMappedImportIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	workflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	subscriptionID, finalTaskID, finalImportID, finalOperationID := createRSSImportingTask(t, fixture, 2, 2, false)
	if _, err := testutil.ExecFixture(context.Background(), fixture.pool, `
DELETE FROM rss_target_fulfillments AS fulfillment
USING rss_entries AS entry
WHERE fulfillment.rss_entry_id = entry.id
  AND entry.subscription_id = $1
  AND entry.source_episode = 1;
UPDATE rss_entries
SET imported_at = NULL,
    fulfillment_source = NULL
WHERE subscription_id = $1 AND source_episode = 1
`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	completion := domain.ImportCompletion{
		TaskID: finalTaskID, ImportID: finalImportID, OperationID: finalOperationID,
		DestinationVideoPath:    "/library/OutOfOrder/Season1/OutOfOrder - S01E02.mkv",
		DestinationSubtitlePath: "/library/OutOfOrder/Season1/OutOfOrder - S01E02.ass",
	}
	if err := workflow.CompleteImport(context.Background(), completion); err != nil {
		t.Fatalf("CompleteImport(final before sibling) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, subscriptionID, true, false, 0)

	if _, err := fixture.pool.Exec(context.Background(), `
WITH restored AS (
    UPDATE rss_entries
    SET imported_at = now(),
        fulfillment_source = 'managed_import'
    WHERE subscription_id = $1 AND source_episode = 1
    RETURNING id, source_season, source_episode
)
INSERT INTO rss_target_fulfillments (rss_entry_id, target_episode_id, source, verified_at)
SELECT restored.id, mapping.target_episode_id, 'managed_import', now()
FROM restored
JOIN rss_subscriptions AS subscription ON subscription.id = $1
JOIN episode_mappings AS mapping
  ON mapping.profile_id = subscription.mapping_profile_id
 AND mapping.source_season = restored.source_season
 AND mapping.source_episode = restored.source_episode
 AND mapping.mapping_status = 'mapped'
`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.transactor.WithinTx(context.Background(), pgx.TxOptions{}, func(scope database.TxScope) error {
		return workflow.scheduleRSSCompletionInTx(context.Background(), scope, completion)
	}); err != nil {
		t.Fatalf("scheduleRSSCompletionInTx(last sibling imported) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, subscriptionID, false, true, 0)

	if err := fixture.transactor.WithinTx(context.Background(), pgx.TxOptions{}, func(scope database.TxScope) error {
		return workflow.scheduleRSSCompletionInTx(context.Background(), scope, completion)
	}); err != nil {
		t.Fatalf("scheduleRSSCompletionInTx(replay) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, subscriptionID, false, true, 0)
}

func TestRSSFinalEpisodeCanBeFulfilledByComplementarySourceIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	workflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	subscriptionID, finalTaskID, finalImportID, finalOperationID := createRSSImportingTask(t, fixture, 2, 2, false, true)
	ctx := context.Background()
	var seriesID, mappingProfileID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `SELECT series_id, mapping_profile_id FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&seriesID, &mappingProfileID); err != nil {
		t.Fatal(err)
	}
	complementarySubscriptionID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, mapping_profile_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, $3, 'Complementary RSS', $4, true, 900, 1)`,
		complementarySubscriptionID, seriesID, mappingProfileID, "https://example.test/"+complementarySubscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE rss_entries
SET subscription_id = $2
WHERE subscription_id = $1 AND source_episode = 1`, subscriptionID, complementarySubscriptionID); err != nil {
		t.Fatal(err)
	}
	var secondTargetID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `
SELECT target_episode_id
FROM episode_mappings
WHERE profile_id = $1 AND source_season = 1 AND source_episode = 2`, mappingProfileID).Scan(&secondTargetID); err != nil {
		t.Fatal(err)
	}
	alternateProfileID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (
  id, series_id, name, version, source_season_lengths, active, created_by, decision_source
) VALUES ($1, $2, $3, 1, ARRAY[2]::integer[], false, $4, 'user')`,
		alternateProfileID, seriesID, "complementary-remap-"+alternateProfileID.String(), fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_mappings (
  id, profile_id, source_season, source_episode, absolute_episode,
  target_episode_id, mapping_status, match_source
) VALUES ($1, $2, 1, 1, 2, $3, 'mapped', 'explicit')`, uuid.New(), alternateProfileID, secondTargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE rss_subscriptions SET mapping_profile_id = $2 WHERE id = $1`, complementarySubscriptionID, alternateProfileID); err != nil {
		t.Fatal(err)
	}

	if err := workflow.CompleteImport(ctx, domain.ImportCompletion{
		TaskID: finalTaskID, ImportID: finalImportID, OperationID: finalOperationID,
		DestinationVideoPath:    "/library/Complementary/Season1/Complementary - S01E02.mkv",
		DestinationSubtitlePath: "/library/Complementary/Season1/Complementary - S01E02.ass",
	}); err != nil {
		t.Fatalf("CompleteImport(complementary final) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, subscriptionID, false, true, 0)
	var fulfilledTargets int
	if err := fixture.pool.QueryRow(ctx, `
SELECT count(DISTINCT fulfillment.target_episode_id)
FROM rss_target_fulfillments AS fulfillment
JOIN rss_entries AS entry ON entry.id = fulfillment.rss_entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE subscription.series_id = $1`, seriesID).Scan(&fulfilledTargets); err != nil {
		t.Fatal(err)
	}
	if fulfilledTargets != 2 {
		t.Fatalf("fulfilled target count = %d, want 2", fulfilledTargets)
	}
}

func TestRSSCompletionDoesNotReinterpretImportedTargetAfterProfileChangeIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	workflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	subscriptionID, taskID, importID, operationID := createRSSImportingTask(t, fixture, 1, 1, false)
	ctx := context.Background()
	var actualTargetID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `
SELECT mapping.target_episode_id
FROM episode_tasks AS task
JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
WHERE task.id = $1
`, taskID).Scan(&actualTargetID); err != nil {
		t.Fatal(err)
	}
	alternateProfileID, alternateTargetID := createSingleEpisodeRSSMappingProfile(t, fixture)
	if _, err := fixture.pool.Exec(ctx, `
UPDATE rss_subscriptions
SET mapping_profile_id = $2,
    version = version + 1,
    updated_at = now()
WHERE id = $1
`, subscriptionID, alternateProfileID); err != nil {
		t.Fatal(err)
	}

	if err := workflow.CompleteImport(ctx, domain.ImportCompletion{
		TaskID: taskID, ImportID: importID, OperationID: operationID,
		DestinationVideoPath:    "/library/ProfileChange/Season1/ProfileChange - S01E01.mkv",
		DestinationSubtitlePath: "/library/ProfileChange/Season1/ProfileChange - S01E01.ass",
	}); err != nil {
		t.Fatalf("CompleteImport(profile changed) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, subscriptionID, true, false, 0)
	var actualCount, reinterpretedCount int
	if err := fixture.pool.QueryRow(ctx, `
SELECT
    count(*) FILTER (WHERE target_episode_id = $2),
    count(*) FILTER (WHERE target_episode_id = $3)
FROM rss_target_fulfillments AS fulfillment
JOIN rss_entries AS entry ON entry.id = fulfillment.rss_entry_id
WHERE entry.subscription_id = $1
`, subscriptionID, actualTargetID, alternateTargetID).Scan(&actualCount, &reinterpretedCount); err != nil {
		t.Fatal(err)
	}
	if actualCount != 1 || reinterpretedCount != 0 {
		t.Fatalf("profile-changed fulfillment counts = actual %d reinterpreted %d, want 1/0", actualCount, reinterpretedCount)
	}
}

func TestRSSEmbyCatalogTargetRemapReplacesProvenanceWhenAggregateStateIsUnchangedIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	subscriptionID, taskID, _, operationID := createRSSImportingTask(t, fixture, 1, 1, false)
	ctx := context.Background()
	var entryID, originalTargetID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `
SELECT entry.id, mapping.target_episode_id
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
WHERE task.id = $1
`, taskID).Scan(&entryID, &originalTargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.ExecFixture(ctx, fixture.pool, `
UPDATE rss_entries
SET downloadable = false,
    rejection_reasons = ARRAY['target_episode_in_library']::text[],
    imported_at = now(),
    fulfillment_source = 'emby_catalog'
WHERE id = $1;
INSERT INTO rss_target_fulfillments (rss_entry_id, target_episode_id, source, verified_at)
VALUES ($1, $2, 'emby_catalog', now())
`, entryID, originalTargetID); err != nil {
		t.Fatal(err)
	}
	alternateProfileID, alternateTargetID := createSingleEpisodeRSSMappingProfile(t, fixture)
	if _, err := fixture.pool.Exec(ctx, `UPDATE rss_subscriptions SET mapping_profile_id = $2 WHERE id = $1`, subscriptionID, alternateProfileID); err != nil {
		t.Fatal(err)
	}
	var changed bool
	if err := fixture.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		var err error
		changed, err = markRSSEntryTargetOccupiedInTx(ctx, scope, entryID, operationID, rssTargetOccupancy{
			TargetEpisodeID:   alternateTargetID,
			TargetSeason:      1,
			TargetEpisode:     1,
			Reason:            rssTargetInLibraryReason,
			Fulfilled:         true,
			FulfillmentSource: rssFulfillmentEmbyCatalog,
		})
		return err
	}); err != nil {
		t.Fatalf("markRSSEntryTargetOccupiedInTx(remapped catalog) error = %v", err)
	}
	if changed {
		t.Fatal("aggregate RSS entry unexpectedly changed during target-only provenance remap")
	}
	var source string
	var imported bool
	var targets []uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `
SELECT entry.fulfillment_source,
       entry.imported_at IS NOT NULL,
       COALESCE(array_agg(fulfillment.target_episode_id ORDER BY fulfillment.target_episode_id)
           FILTER (WHERE fulfillment.source = 'emby_catalog'), ARRAY[]::uuid[])
FROM rss_entries AS entry
LEFT JOIN rss_target_fulfillments AS fulfillment ON fulfillment.rss_entry_id = entry.id
WHERE entry.id = $1
GROUP BY entry.id
`, entryID).Scan(&source, &imported, &targets); err != nil {
		t.Fatal(err)
	}
	if source != rssFulfillmentEmbyCatalog || !imported || len(targets) != 1 || targets[0] != alternateTargetID {
		t.Fatalf("remapped catalog state = source %q imported %t targets %v, want catalog/true/%s", source, imported, targets, alternateTargetID)
	}
}

func TestStaleComplementaryCatalogFulfillmentDoesNotCompleteSubscriptionIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskWorkflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	rssWorkflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	subscriptionID, finalTaskID, finalImportID, finalOperationID := createRSSImportingTask(t, fixture, 2, 2, false, true)
	ctx := context.Background()
	var seriesID, mappingProfileID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `SELECT series_id, mapping_profile_id FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&seriesID, &mappingProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM rss_entries WHERE subscription_id = $1 AND source_episode = 1`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	complementarySubscriptionID, complementaryEntryID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
  id, series_id, mapping_profile_id, name, feed_url, enabled,
  poll_interval_seconds, source_season, completed_at
) VALUES ($1, $2, $3, 'Completed complementary RSS', $4, false, 900, 1, now())`,
		complementarySubscriptionID, seriesID, mappingProfileID, "https://example.test/"+complementarySubscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (
  id, subscription_id, identity_key, title, download_uri, downloadable,
  rejection_reasons, source_season, source_episode, status, imported_at, fulfillment_source
) VALUES ($1, $2, $3, 'Catalog episode 1', 'https://example.test/catalog-1.torrent', false,
          ARRAY['target_episode_in_library']::text[], 1, 1, 'discovered', now(), 'emby_catalog')`,
		complementaryEntryID, complementarySubscriptionID, "guid:"+complementaryEntryID.String()); err != nil {
		t.Fatal(err)
	}
	var firstTargetID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `
SELECT target_episode_id
FROM episode_mappings
WHERE profile_id = $1 AND source_season = 1 AND source_episode = 1`, mappingProfileID).Scan(&firstTargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_target_fulfillments (rss_entry_id, target_episode_id, source, verified_at)
VALUES ($1, $2, 'emby_catalog', now())`, complementaryEntryID, firstTargetID); err != nil {
		t.Fatal(err)
	}
	realtimeCheckID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_target_realtime_checks (target_episode_id, check_id, present, match_source, checked_at)
VALUES ($1, $2, false, 'absent', now())`, firstTargetID, realtimeCheckID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE rss_target_fulfillments
SET invalidated_at = now(),
    updated_at = now()
WHERE target_episode_id = $1
  AND source = 'emby_catalog'
`, firstTargetID); err != nil {
		t.Fatal(err)
	}
	// VerifySubscription 已提交 absent 证据后，最终 import 可能先于 PersistPoll
	// 获得订阅锁；完成判定必须直接拒绝这条较新的缺失证据。
	if err := taskWorkflow.CompleteImport(ctx, domain.ImportCompletion{
		TaskID: finalTaskID, ImportID: finalImportID, OperationID: finalOperationID,
		DestinationVideoPath:    "/library/Complementary/Season1/Complementary - S01E02.mkv",
		DestinationSubtitlePath: "/library/Complementary/Season1/Complementary - S01E02.ass",
	}); err != nil {
		t.Fatalf("CompleteImport(before stale catalog refresh) error = %v", err)
	}
	assertRSSCompletionState(t, fixture, subscriptionID, true, false, 0)

	if err := fixture.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		return rssWorkflow.refreshRSSEmbyCatalogFulfillmentsInTx(
			ctx, scope, finalOperationID, subscriptionID, realtimeCheckID,
		)
	}); err != nil {
		t.Fatalf("refreshRSSEmbyCatalogFulfillmentsInTx() error = %v", err)
	}
	var imported bool
	var fulfillmentCount int
	if err := fixture.pool.QueryRow(ctx, `
SELECT imported_at IS NOT NULL,
       (SELECT count(*) FROM rss_target_fulfillments WHERE rss_entry_id = entry.id)
FROM rss_entries AS entry
WHERE entry.id = $1`, complementaryEntryID).Scan(&imported, &fulfillmentCount); err != nil {
		t.Fatal(err)
	}
	if imported || fulfillmentCount != 0 {
		t.Fatalf("stale catalog fulfillment = imported %t records %d, want false/0", imported, fulfillmentCount)
	}
}

func TestCompleteRSSSubscriptionCleanupRetainsLegacyArchivedHistoryIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx := context.Background()
	subscriptionID, operationID, acquisitionID := uuid.New(), uuid.New(), uuid.New()
	entryIDs := []uuid.UUID{uuid.New(), uuid.New()}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, deleted_at)
VALUES ($1, $2, 'Completed RSS history', $3, false, 900, 1, now())
`, subscriptionID, fixture.seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status)
VALUES ($1, $3, 'guid:completed-1', 'Episode 1', 'https://example.test/1.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued'),
       ($2, $3, 'guid:completed-2', 'Episode 2', 'https://example.test/2.torrent', true, ARRAY[]::text[], 1, 2, 'enqueued')
`, entryIDs[0], entryIDs[1], subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id)
VALUES ($1, $2, 'rss', $3)
`, acquisitionID, fixture.seriesID, entryIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, started_at)
VALUES ($1, 'rss.subscription.complete', 'rss_subscription', $2, $3, 'running', 3, 1800, now())
`, operationID, subscriptionID, "test-rss-complete:"+subscriptionID.String()); err != nil {
		t.Fatal(err)
	}

	workflow := NewAcquisitionDeletionWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	if err := workflow.CompleteSubscriptionCleanup(ctx, subscriptionID, operationID); err != nil {
		t.Fatalf("CompleteSubscriptionCleanup() error = %v", err)
	}

	var enabled, archived bool
	var entryCount, acquisitionCount int
	if err := fixture.pool.QueryRow(ctx, `
SELECT
    subscription.enabled,
    subscription.deleted_at IS NOT NULL,
    (SELECT count(*) FROM rss_entries WHERE subscription_id = subscription.id),
    (SELECT count(*) FROM acquisitions AS acquisition JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id WHERE entry.subscription_id = subscription.id)
FROM rss_subscriptions AS subscription
WHERE subscription.id = $1
`, subscriptionID).Scan(&enabled, &archived, &entryCount, &acquisitionCount); err != nil {
		t.Fatal(err)
	}
	if enabled || archived || entryCount != 2 || acquisitionCount != 1 {
		t.Fatalf("retained history = enabled %t archived %t entries %d acquisitions %d", enabled, archived, entryCount, acquisitionCount)
	}
	var eventTopic string
	if err := fixture.pool.QueryRow(ctx, `
SELECT topic
FROM events
WHERE resource_type = 'rss_subscription' AND resource_id = $1
ORDER BY event_sequence DESC
LIMIT 1
`, subscriptionID).Scan(&eventTopic); err != nil {
		t.Fatal(err)
	}
	if eventTopic != "rss.subscription.completion_retained" {
		t.Fatalf("completion event = %q", eventTopic)
	}
}

func TestRSSDeletionIdempotencyCannotChangeImportedResourceScopeIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	subscriptionID := uuid.New()
	if _, err := fixture.pool.Exec(context.Background(), `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Idempotency scope', $3, false, 900, 1)
`, subscriptionID, fixture.seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	key := "rss-delete-scope:" + subscriptionID.String()
	first, err := workflow.RequestSubscriptionDeletion(context.Background(), subscriptionID, 1, key, false, fixture.actorID)
	if err != nil {
		t.Fatalf("RequestSubscriptionDeletion(first) error = %v", err)
	}
	replayed, err := workflow.RequestSubscriptionDeletion(context.Background(), subscriptionID, 1, key, false, fixture.actorID)
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("RequestSubscriptionDeletion(replay) = %#v, %v", replayed, err)
	}
	_, err = workflow.RequestSubscriptionDeletion(context.Background(), subscriptionID, 1, key, true, fixture.actorID)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "idempotency_conflict" || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("RequestSubscriptionDeletion(scope change) error = %#v", err)
	}
}

func createSingleEpisodeRSSMappingProfile(t *testing.T, fixture recoveryFixture) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	profileID, seasonID, targetEpisodeID := uuid.New(), uuid.New(), uuid.New()
	var seasonNumber int
	if err := fixture.pool.QueryRow(ctx, `
SELECT COALESCE(max(season_number), 0) + 1
FROM tmdb_seasons
WHERE series_id = $1
`, fixture.seriesID).Scan(&seasonNumber); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.ExecFixture(ctx, fixture.pool, `
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count)
VALUES ($1, $2, $3, 1);
INSERT INTO media_episodes (id, season_id, episode_number, title)
VALUES ($4, $1, 1, 'Alternate target');
INSERT INTO episode_mapping_profiles (
    id, series_id, name, version, source_season_lengths, active, created_by, decision_source
)
VALUES ($5, $2, $6, 1, ARRAY[1]::integer[], false, $7, 'user');
INSERT INTO episode_mappings (
    id, profile_id, source_season, source_episode, absolute_episode,
    target_episode_id, mapping_status, match_source
)
VALUES (gen_random_uuid(), $5, 1, 1, 1, $4, 'mapped', 'explicit')
`, seasonID, fixture.seriesID, seasonNumber, targetEpisodeID, profileID, "rss-alternate-"+profileID.String(), fixture.actorID); err != nil {
		t.Fatal(err)
	}
	return profileID, targetEpisodeID
}

func createRSSImportingTask(t *testing.T, fixture recoveryFixture, sourceEpisode, seasonLength int, cleanupSource bool, anchoredProfile ...bool) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	subscriptionID, entryID, acquisitionID := uuid.New(), uuid.New(), uuid.New()
	mappingProfileID, downloadID, sourceFileID := uuid.New(), uuid.New(), uuid.New()
	taskMappingID := uuid.New()
	taskID, importID, operationID := uuid.New(), uuid.New(), uuid.New()
	videoArtifactID, subtitleArtifactID := uuid.New(), uuid.New()
	targetSeasonID := uuid.New()
	var targetSeasonNumber int
	if err := fixture.pool.QueryRow(ctx, `
SELECT COALESCE(max(season_number), 0) + 1
FROM tmdb_seasons
WHERE series_id = $1
`, fixture.seriesID).Scan(&targetSeasonNumber); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count)
VALUES ($1, $2, $3, $4)
`, targetSeasonID, fixture.seriesID, targetSeasonNumber, seasonLength); err != nil {
		t.Fatal(err)
	}
	targetEpisodeIDs := make([]uuid.UUID, seasonLength)
	for episode := 1; episode <= seasonLength; episode++ {
		targetEpisodeIDs[episode-1] = uuid.New()
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO media_episodes (id, season_id, episode_number, title)
VALUES ($1, $2, $3, $4)
`, targetEpisodeIDs[episode-1], targetSeasonID, episode, fmt.Sprintf("Episode %d", episode)); err != nil {
			t.Fatal(err)
		}
	}
	anchored := len(anchoredProfile) > 0 && anchoredProfile[0]
	if anchored {
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (
    id, series_id, name, version, source_season_lengths, active, created_by,
    anchor_source_season, anchor_source_episode, anchor_target_episode_id, target_episode_offset,
    decision_source
)
VALUES ($1, $2, $3, 1, NULL, true, $4, 1, 1, $5, 0, 'user')
`, mappingProfileID, fixture.seriesID, "rss-completion-anchor-"+mappingProfileID.String(), fixture.actorID, targetEpisodeIDs[0]); err != nil {
			t.Fatal(err)
		}
	} else if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active, created_by, decision_source)
VALUES ($1, $2, $3, 1, ARRAY[$4]::integer[], true, $5, 'user')
`, mappingProfileID, fixture.seriesID, "rss-completion-"+mappingProfileID.String(), seasonLength, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	for episode := 1; episode <= seasonLength; episode++ {
		mappingID := uuid.New()
		if episode == sourceEpisode {
			mappingID = taskMappingID
		}
		matchSource := "explicit"
		if anchored {
			matchSource = "anchor"
		}
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_mappings (
    id, profile_id, source_season, source_episode, absolute_episode,
    target_episode_id, mapping_status, match_source
)
VALUES ($1, $2, 1, $3, $3, $4, 'mapped', $5)
`, mappingID, mappingProfileID, episode, targetEpisodeIDs[episode-1], matchSource); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, mapping_profile_id, name, feed_url, enabled, cleanup_source_on_completion, poll_interval_seconds, source_season)
VALUES ($1, $2, $3, $4, $5, true, $6, 900, 1)
`, subscriptionID, fixture.seriesID, mappingProfileID, "RSS completion", "https://example.test/"+subscriptionID.String()+".xml", cleanupSource); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status)
VALUES ($1, $2, $3, $4, $5, true, ARRAY[]::text[], 1, $6, 'enqueued')
`, entryID, subscriptionID, "guid:"+entryID.String(), "Episode", "https://example.test/episode.torrent", sourceEpisode); err != nil {
		t.Fatal(err)
	}
	for episode := 1; episode < sourceEpisode; episode++ {
		priorEntryID := uuid.New()
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status, imported_at, fulfillment_source
)
VALUES ($1, $2, $3, $4, $5, true, ARRAY[]::text[], 1, $6, 'enqueued', now(), 'managed_import')
`, priorEntryID, subscriptionID, "guid:"+priorEntryID.String(), fmt.Sprintf("Episode %d", episode), "https://example.test/prior.torrent", episode); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_target_fulfillments (rss_entry_id, target_episode_id, source, verified_at)
VALUES ($1, $2, 'managed_import', now())
`, priorEntryID, targetEpisodeIDs[episode-1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, mapping_profile_id, source_kind, rss_entry_id)
VALUES ($1, $2, $3, 'rss', $4)
`, acquisitionID, fixture.seriesID, mappingProfileID, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status, progress, save_path)
VALUES ($1, $2, $3, 'materialized', 1, $4)
`, downloadID, acquisitionID, strings.ReplaceAll(downloadID.String(), "-", "")+downloadID.String()[:8], "/downloads/"+downloadID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'episode.mkv', 1024, 'video', true, 1, $3)
`, sourceFileID, downloadID, sourceEpisode); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_tasks (
  id, acquisition_id, source_video_file_id, mapping_id,
  transcode_profile_id, state, video_state, subtitle_state
)
VALUES ($1, $2, $3, $4, $5, 'importing', 'video_ready', 'ass_ready')
`, taskID, acquisitionID, sourceFileID, taskMappingID, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO media_artifacts (id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $3, $4, $5, 'video', 'Show - S01E01', $6, 'matroska', 10, decode(repeat('01', 32), 'hex')),
       ($2, $3, $4, NULL, 'subtitle', 'Show - S01E01', $7, 'ass', 10, decode(repeat('02', 32), 'hex'))
`, videoArtifactID, subtitleArtifactID, taskID, sourceFileID, fixture.profileID, "/staging/"+taskID.String()+"/episode.mkv", "/staging/"+taskID.String()+"/episode.ass"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO artifact_sets (id, task_id, transcode_profile_id, basename, video_artifact_id, subtitle_artifact_id)
VALUES ($1, $2, $3, 'Show - S01E01', $4, $5)
`, uuid.New(), taskID, fixture.profileID, videoArtifactID, subtitleArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO imports (id, task_id, attempt, status, started_at) VALUES ($1, $2, 1, 'running', now())`, importID, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, started_at)
VALUES ($1, 'emby.import', 'episode_task', $2, $3, 'running', 5, 1800, now())
`, operationID, taskID, "rss-import-"+operationID.String()); err != nil {
		t.Fatal(err)
	}
	return subscriptionID, taskID, importID, operationID
}

func assertRSSCompletionState(
	t *testing.T,
	fixture recoveryFixture,
	subscriptionID uuid.UUID,
	wantEnabled bool,
	wantCompleted bool,
	cleanupOperations int,
) {
	t.Helper()
	var isArchived, enabled, completed bool
	var completionCount, deletionCount, cleanupCount int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT
    subscription.deleted_at IS NOT NULL,
    subscription.enabled,
    subscription.completed_at IS NOT NULL,
    (SELECT count(*) FROM operations WHERE resource_type = 'rss_subscription' AND resource_id = subscription.id AND kind = 'rss.subscription.complete'),
    (SELECT count(*) FROM operations WHERE resource_type = 'rss_subscription' AND resource_id = subscription.id AND kind = 'rss.subscription.delete'),
    (
        SELECT count(*)
        FROM operations AS operation
        JOIN episode_tasks AS task ON operation.resource_type = 'episode_task' AND operation.resource_id = task.id
        JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE entry.subscription_id = subscription.id
          AND operation.kind = 'cleanup.run'
    )
FROM rss_subscriptions AS subscription
WHERE subscription.id = $1
`, subscriptionID).Scan(&isArchived, &enabled, &completed, &completionCount, &deletionCount, &cleanupCount); err != nil {
		t.Fatal(err)
	}
	if isArchived || enabled != wantEnabled || completed != wantCompleted || completionCount != 0 || deletionCount != 0 || cleanupCount != cleanupOperations {
		t.Fatalf(
			"subscription state = archived %t enabled %t completed %t completion operations %d deletion operations %d cleanup operations %d",
			isArchived, enabled, completed, completionCount, deletionCount, cleanupCount,
		)
	}
}

func assertRSSCompletionRecordsRetained(t *testing.T, fixture recoveryFixture, subscriptionID, taskID uuid.UUID) {
	t.Helper()
	var acquisitionCount, taskCount, downloadCount int
	var taskState string
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT
    (SELECT count(*) FROM acquisitions AS acquisition JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id WHERE entry.subscription_id = $1),
    (SELECT count(*) FROM episode_tasks WHERE id = $2),
    (
        SELECT count(*)
        FROM downloads AS download
        JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE entry.subscription_id = $1
    ),
    (SELECT state FROM episode_tasks WHERE id = $2)
`, subscriptionID, taskID).Scan(&acquisitionCount, &taskCount, &downloadCount, &taskState); err != nil {
		t.Fatal(err)
	}
	if acquisitionCount != 1 || taskCount != 1 || downloadCount != 1 || taskState != "imported" {
		t.Fatalf("retained records = acquisitions %d tasks %d downloads %d task state %q", acquisitionCount, taskCount, downloadCount, taskState)
	}
}
