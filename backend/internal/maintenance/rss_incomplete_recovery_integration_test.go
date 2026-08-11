//go:build integration

package maintenance

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type recoveryJobInserter struct {
	nextID atomic.Int64
}

func (inserter *recoveryJobInserter) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: inserter.nextID.Add(1)}}, nil
}

func TestIncompleteRSSRecoverySchedulesOnlyMissingEntriesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	operations := service.NewOperationScheduler(transactor, &recoveryJobInserter{})
	workflow := service.NewRSSWorkflow(queries, transactor, operations)

	seriesID, profileID, subscriptionID := uuid.New(), uuid.New(), uuid.New()
	entryIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	existingAcquisitionID, existingDownloadID, existingOperationID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, title) VALUES ($1, 'Incomplete Recovery');
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active, decision_source)
VALUES ($2, $1, 'recovery-profile', 1, ARRAY[3]::integer[], true, 'legacy');
INSERT INTO rss_subscriptions (
    id, series_id, mapping_profile_id, name, feed_url, enabled,
    auto_review, poll_interval_seconds, source_season
)
VALUES ($3, $1, $2, 'Incomplete Recovery', 'https://example.test/recovery.xml', false, true, 900, 1);
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status, imported_at, fulfillment_source
) VALUES
    ($4, $3, 'guid:recovery-1', 'Recovery E01', 'https://example.test/1.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued', now(), 'managed_import'),
    ($5, $3, 'guid:recovery-2', 'Recovery E02', 'https://example.test/2.torrent', true, ARRAY[]::text[], 1, 2, 'enqueued', NULL, NULL),
    ($6, $3, 'guid:recovery-3', 'Recovery E03', 'https://example.test/3.torrent', true, ARRAY[]::text[], 1, 3, 'enqueued', NULL, NULL);
INSERT INTO acquisitions (id, series_id, mapping_profile_id, source_kind, rss_entry_id)
VALUES ($7, $1, $2, 'rss', $6);
INSERT INTO downloads (id, acquisition_id, status)
VALUES ($8, $7, 'enqueue_pending');
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, timeout_seconds
)
VALUES ($9, 'download.enqueue', 'download', $8, $10, 'queued', 5, 120);
`, seriesID, profileID, subscriptionID, entryIDs[0], entryIDs[1], entryIDs[2], existingAcquisitionID, existingDownloadID, existingOperationID, "existing-download:"+existingDownloadID.String()); err != nil {
		t.Fatal(err)
	}

	recovery := NewIncompleteRSSRecovery(queries, transactor, workflow)
	result, err := recovery.Recover(ctx, IncompleteRSSRecoveryRequest{
		SubscriptionID: subscriptionID,
		SourceEpisodes: []int32{2, 3},
	})
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if result.RequestedCount != 2 || result.ScheduledCount != 1 || result.ExistingCount != 1 {
		t.Fatalf("Recover() result = %#v", result)
	}

	var enabled, completed bool
	var firstAcquisitions, secondAcquisitions, thirdAcquisitions, secondOperations, thirdOperations int
	if err := pool.QueryRow(ctx, `
SELECT
    subscription.enabled,
    subscription.completed_at IS NOT NULL,
    (SELECT count(*) FROM acquisitions WHERE rss_entry_id = $2),
    (SELECT count(*) FROM acquisitions WHERE rss_entry_id = $3),
    (SELECT count(*) FROM acquisitions WHERE rss_entry_id = $4),
    (SELECT count(*) FROM operations AS operation JOIN downloads AS download ON download.id = operation.resource_id JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id WHERE operation.kind = 'download.enqueue' AND acquisition.rss_entry_id = $3),
    (SELECT count(*) FROM operations AS operation JOIN downloads AS download ON download.id = operation.resource_id JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id WHERE operation.kind = 'download.enqueue' AND acquisition.rss_entry_id = $4)
FROM rss_subscriptions AS subscription
WHERE subscription.id = $1
`, subscriptionID, entryIDs[0], entryIDs[1], entryIDs[2]).Scan(
		&enabled, &completed, &firstAcquisitions, &secondAcquisitions, &thirdAcquisitions, &secondOperations, &thirdOperations,
	); err != nil {
		t.Fatal(err)
	}
	if enabled || completed || firstAcquisitions != 0 || secondAcquisitions != 1 || thirdAcquisitions != 1 || secondOperations != 1 || thirdOperations != 1 {
		t.Fatalf(
			"recovery state = enabled %t completed %t acquisitions %d/%d/%d operations %d/%d",
			enabled, completed, firstAcquisitions, secondAcquisitions, thirdAcquisitions, secondOperations, thirdOperations,
		)
	}

	var eventTopic string
	if err := pool.QueryRow(ctx, `
SELECT topic
FROM events
WHERE resource_type = 'rss_subscription' AND resource_id = $1
ORDER BY event_sequence DESC
LIMIT 1
`, subscriptionID).Scan(&eventTopic); err != nil {
		t.Fatal(err)
	}
	if eventTopic != "rss.subscription.incomplete_recovery_scheduled" {
		t.Fatalf("recovery event = %q", eventTopic)
	}
}
