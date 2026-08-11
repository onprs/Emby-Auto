//go:build integration

package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func TestRSSDeletionRemovesSubscriptionWorkflowAndExternalResourcesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := service.NewOperationScheduler(transactor, riverClient)
	rss := service.NewRSSWorkflow(queries, transactor, scheduler)
	deletions := service.NewAcquisitionDeletionWorkflow(queries, transactor, scheduler)

	root := t.TempDir()
	downloadRoot := filepath.Join(root, "downloads")
	stagingRoot := filepath.Join(root, "staging")
	libraryFile := filepath.Join(root, "library", "preserved.mkv")
	downloadPath := filepath.Join(downloadRoot, "rss-source")
	for path, content := range map[string]string{filepath.Join(downloadPath, "episode.mkv"): "source", libraryFile: "library"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	actorID, seriesID, subscriptionID := uuid.New(), uuid.New(), uuid.New()
	entryID, acquisitionID, downloadID, queuedPollID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	archivedSubscriptionID, archivedEntryID := uuid.New(), uuid.New()
	archivedAcquisitionID, archivedDownloadID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-delete-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'RSS Delete Fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season) VALUES ($1, $2, 'Delete Me', $3, false, 900, 1)`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, deleted_at) VALUES ($1, $2, 'Archived Owner', $3, false, 900, 1, now())`, archivedSubscriptionID, seriesID, "https://example.test/"+archivedSubscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_entries (id, subscription_id, identity_key, title, status) VALUES ($1, $2, 'entry', 'Episode', 'enqueued'), ($3, $4, 'archived-entry', 'Archived Episode', 'enqueued')`, entryID, subscriptionID, archivedEntryID, archivedSubscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3), ($4, $2, 'rss', $5)`, acquisitionID, seriesID, entryID, archivedAcquisitionID, archivedEntryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, torrent_hash, status, save_path) VALUES ($1, $2, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'materialized', $3), ($4, $5, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'cancelled', $3)`, downloadID, acquisitionID, downloadPath, archivedDownloadID, archivedAcquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds) VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'queued', 5, 30)`, queuedPollID, subscriptionID, "queued-poll-"+queuedPollID.String()); err != nil {
		t.Fatal(err)
	}

	operation, err := rss.RequestSubscriptionDeletion(ctx, subscriptionID, 1, "rss-delete-"+subscriptionID.String(), false, actorID)
	if err != nil {
		t.Fatalf("RequestSubscriptionDeletion() error = %v", err)
	}
	var pollCancellationRequested bool
	if err := pool.QueryRow(ctx, `SELECT cancel_requested_at IS NOT NULL FROM operations WHERE id = $1`, queuedPollID).Scan(&pollCancellationRequested); err != nil || !pollCancellationRequested {
		t.Fatalf("queued poll cancellation = %t, %v", pollCancellationRequested, err)
	}
	configuration := &acquisitionDeleteConfigurationStub{secret: "password", configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb.test", Username: "user"},
		Paths:       domain.PathSettings{DownloadRoot: downloadRoot, StagingRoot: stagingRoot},
	}}}
	torrent := &acquisitionDeleteTorrentStub{}
	acquisitionHandler := NewAcquisitionDeleteHandler(configuration, deletions, func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error) { return torrent, nil })
	handler := NewRSSSubscriptionDeleteHandler(deletions, acquisitionHandler)
	if err := handler.Handle(ctx, operation); err != nil {
		t.Fatalf("RSS Handle() error = %v", err)
	}
	if _, err := os.Stat(downloadPath); !os.IsNotExist(err) {
		t.Fatalf("RSS download path remains: %v", err)
	}
	if _, err := os.Stat(libraryFile); err != nil {
		t.Fatalf("library file was removed: %v", err)
	}
	if len(torrent.hashes) != 1 || torrent.deleteFiles[0] {
		t.Fatalf("torrent deletion = hashes %#v deleteFiles %#v", torrent.hashes, torrent.deleteFiles)
	}
	for table, id := range map[string]uuid.UUID{"rss_subscriptions": subscriptionID, "rss_entries": entryID, "acquisitions": acquisitionID, "downloads": downloadID} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s row count = %d, %v", table, count, err)
		}
	}
	var operationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id IN ($1, $2)`, operation.ID, queuedPollID).Scan(&operationCount); err != nil || operationCount != 2 {
		t.Fatalf("RSS deletion audit operation count = %d, %v", operationCount, err)
	}
}
