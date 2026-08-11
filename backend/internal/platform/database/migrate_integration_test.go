//go:build integration

package database_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	appmigrations "github.com/onprs/emby-auto/backend/db/migrations"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestApplicationMigrationsUpgradeV8AndV9Integration(t *testing.T) {
	for _, startingVersion := range []int64{8, 9} {
		t.Run(fmt.Sprintf("v%d", startingVersion), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			databaseURL, pool := testutil.NewMigratedPostgres(t)
			downgradeApplication(t, ctx, pool, startingVersion)

			seriesID, acquisitionID, downloadID := uuid.New(), uuid.New(), uuid.New()
			if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Upgrade Fixture')`, seriesID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri) VALUES ($1, $2, 'manual', 'fixture://upgrade')`, acquisitionID, seriesID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'enqueue_pending')`, downloadID, acquisitionID); err != nil {
				t.Fatal(err)
			}

			if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
				t.Fatalf("Migrate() from v%d error = %v", startingVersion, err)
			}
			if err := database.RequireCurrentMigrations(ctx, pool); err != nil {
				t.Fatalf("RequireCurrentMigrations() after v%d upgrade error = %v", startingVersion, err)
			}
			var version int32
			var failureStage, clientState *string
			if err := pool.QueryRow(ctx, `SELECT version, failure_stage, client_state FROM downloads WHERE id = $1`, downloadID).Scan(&version, &failureStage, &clientState); err != nil {
				t.Fatalf("read upgraded v%d download: %v", startingVersion, err)
			}
			if version != 1 || failureStage != nil || clientState != nil {
				t.Fatalf("upgraded v%d download = version %d failureStage %v clientState %v", startingVersion, version, failureStage, clientState)
			}
		})
	}
}

func downgradeApplication(t *testing.T, ctx context.Context, pool *pgxpool.Pool, target int64) {
	t.Helper()
	latest, err := database.LatestApplicationMigrationVersion()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := appmigrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for version := latest; version > target; version-- {
		prefix := fmt.Sprintf("%06d_", version)
		name := ""
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".down.sql") {
				name = entry.Name()
				break
			}
		}
		if name == "" {
			t.Fatalf("down migration %d not found", version)
		}
		contents, err := appmigrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE schema_migrations SET version = $1, dirty = false`, version-1); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRSSSourceCleanupPolicyMigrationPreservesManualDeletionIntentIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	downgradeApplication(t, ctx, pool, 25)

	seriesID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'RSS policy upgrade fixture')`, seriesID); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name                       string
		cancelAutomatic            bool
		manualSubscriptionDeletion bool
		manualAcquisitionDeletion  bool
		wantDeletionRequested      bool
	}{
		{name: "active automatic completion", wantDeletionRequested: false},
		{name: "cancelled automatic completion", cancelAutomatic: true, wantDeletionRequested: true},
		{name: "manual subscription deletion", manualSubscriptionDeletion: true, wantDeletionRequested: true},
		{name: "manual acquisition deletion", manualAcquisitionDeletion: true, wantDeletionRequested: true},
	}
	type fixture struct {
		name, subscriptionID, acquisitionID string
		wantDeletionRequested               bool
	}
	fixtures := make([]fixture, 0, len(cases))
	for _, test := range cases {
		subscriptionID, entryID, acquisitionID, automaticOperationID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
    id, series_id, name, feed_url, enabled, delete_imported_on_completion,
    poll_interval_seconds, source_season, completed_at
)
VALUES ($1, $2, $3, $4, false, true, 900, 1, now())
`, subscriptionID, seriesID, test.name, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode
)
VALUES ($1, $2, $3, 'Episode 1', 'https://example.test/episode.torrent', true, ARRAY[]::text[], 1, 1)
`, entryID, subscriptionID, "guid:"+entryID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id, deletion_requested_at)
VALUES ($1, $2, 'rss', $3, now())
`, acquisitionID, seriesID, entryID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, timeout_seconds, payload, cancel_requested_at
)
VALUES ($1, 'rss.subscription.complete', 'rss_subscription', $2, $3, 'queued',
        3, 1800, '{"trigger":"final_import"}'::jsonb, CASE WHEN $4 THEN now() ELSE NULL END)
`, automaticOperationID, subscriptionID, "automatic-completion:"+automaticOperationID.String(), test.cancelAutomatic); err != nil {
			t.Fatal(err)
		}
		if test.manualSubscriptionDeletion {
			operationID := uuid.New()
			if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, timeout_seconds, payload
)
VALUES ($1, 'rss.subscription.delete', 'rss_subscription', $2, $3, 'queued',
        3, 1800, '{"trigger":"manual"}'::jsonb)
`, operationID, subscriptionID, "manual-subscription-deletion:"+operationID.String()); err != nil {
				t.Fatal(err)
			}
		}
		if test.manualAcquisitionDeletion {
			operationID := uuid.New()
			if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, timeout_seconds, payload
)
VALUES ($1, 'acquisition.delete', 'acquisition', $2, $3, 'queued', 5, 1800, '{}'::jsonb)
`, operationID, acquisitionID, "manual-acquisition-deletion:"+operationID.String()); err != nil {
				t.Fatal(err)
			}
		}
		fixtures = append(fixtures, fixture{
			name: test.name, subscriptionID: subscriptionID.String(), acquisitionID: acquisitionID.String(),
			wantDeletionRequested: test.wantDeletionRequested,
		})
	}

	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("Migrate() from v25 error = %v", err)
	}
	for _, item := range fixtures {
		t.Run(item.name, func(t *testing.T) {
			var cleanupSource, deletionRequested bool
			if err := pool.QueryRow(ctx, `
SELECT subscription.cleanup_source_on_completion, acquisition.deletion_requested_at IS NOT NULL
FROM rss_subscriptions AS subscription
JOIN rss_entries AS entry ON entry.subscription_id = subscription.id
JOIN acquisitions AS acquisition ON acquisition.rss_entry_id = entry.id
WHERE subscription.id = $1 AND acquisition.id = $2
`, item.subscriptionID, item.acquisitionID).Scan(&cleanupSource, &deletionRequested); err != nil {
				t.Fatal(err)
			}
			if !cleanupSource || deletionRequested != item.wantDeletionRequested {
				t.Fatalf("upgraded policy = cleanup source %t deletion requested %t", cleanupSource, deletionRequested)
			}
		})
	}
}

func TestMigrationStatusRejectsBehindAndDirtyDatabasesIntegration(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		if err := database.RequireCurrentMigrations(ctx, pool); err != nil {
			t.Fatalf("RequireCurrentMigrations() error = %v", err)
		}
	})

	t.Run("application behind", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		latest, err := database.LatestApplicationMigrationVersion()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET version = $1`, latest-1); err != nil {
			t.Fatal(err)
		}
		if err := database.RequireCurrentMigrations(ctx, pool); err == nil || !strings.Contains(err.Error(), "application migrations are behind") {
			t.Fatalf("RequireCurrentMigrations() error = %v", err)
		}
	})

	t.Run("application dirty", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET dirty = true`); err != nil {
			t.Fatal(err)
		}
		if err := database.RequireCurrentMigrations(ctx, pool); err == nil || !strings.Contains(err.Error(), "application schema is dirty") {
			t.Fatalf("RequireCurrentMigrations() error = %v", err)
		}
	})

	t.Run("River behind", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		if _, err := pool.Exec(ctx, `DELETE FROM river_migration WHERE line = 'main' AND version = (SELECT max(version) FROM river_migration WHERE line = 'main')`); err != nil {
			t.Fatal(err)
		}
		if err := database.RequireCurrentMigrations(ctx, pool); err == nil || !strings.Contains(err.Error(), "river migrations are behind") {
			t.Fatalf("RequireCurrentMigrations() error = %v", err)
		}
	})
}
