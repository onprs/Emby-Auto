//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestDashboardAttentionUsesAcquisitionsAndIgnoresHistoricalOperationFailuresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	read := NewReadService(db.New(pool))

	failedOperationID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count, timeout_seconds, error_code, error_message, finished_at)
VALUES ($1, 'download.enqueue', 'download', $2, $3, 'failed', 3, 3, 120, 'duplicate_torrent', 'the torrent has already been downloaded', now())`,
		failedOperationID, uuid.New(), "dashboard-history-"+failedOperationID.String()); err != nil {
		t.Fatal(err)
	}

	summary, err := read.DashboardSummary(ctx)
	if err != nil {
		t.Fatalf("DashboardSummary() historical failure error = %v", err)
	}
	if summary.Counts.Attention != 0 || len(summary.AttentionItems) != 0 {
		t.Fatalf("historical operation became attention: counts=%#v items=%#v", summary.Counts, summary.AttentionItems)
	}
	if len(summary.RecentOperations) != 1 || summary.RecentOperations[0].ID != failedOperationID {
		t.Fatalf("historical operation missing from recent activity: %#v", summary.RecentOperations)
	}

	seriesID, acquisitionID, downloadID, fileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Dashboard Mapping Fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri, source_payload) VALUES ($1, $2, 'manual', 'manual://dashboard-mapping', '{"sourceSeason":1,"sourceEpisode":7}')`, acquisitionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status, progress, completed_at) VALUES ($1, $2, 'completed', 1, now())`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'episode-07.mkv', 1024, 'video', true, 1, 7)`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}

	summary, err = read.DashboardSummary(ctx)
	if err != nil {
		t.Fatalf("DashboardSummary() mapping attention error = %v", err)
	}
	if summary.Counts.Attention != 1 || len(summary.AttentionItems) != 1 {
		t.Fatalf("mapping attention = count %d items %#v", summary.Counts.Attention, summary.AttentionItems)
	}
	item := summary.AttentionItems[0]
	if item.Acquisition.ID != acquisitionID || item.Acquisition.SeriesTitle != "Dashboard Mapping Fixture" || item.Reason != "mapping_required" {
		t.Fatalf("mapping attention item = %#v", item)
	}
	if len(summary.RecentOperations) != 1 || summary.RecentOperations[0].ID != failedOperationID {
		t.Fatalf("recent operations changed after attention item: %#v", summary.RecentOperations)
	}
}

func TestDashboardTreatsCompletedTransferMappingFailureAsMappingPendingIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	read := NewReadService(db.New(pool))

	seriesID, acquisitionID, downloadID, fileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Completed Transfer Mapping Fixture')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, source_uri, source_payload) VALUES ($1, $2, 'manual', 'manual://completed-transfer-mapping', '{"sourceSeason":1,"sourceEpisode":6}')`, acquisitionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, progress, client_state, failure_stage, error_code, error_message, completed_at)
VALUES ($1, $2, 'failed', 1, 'uploading', 'materialize', 'mapping_profile_required', 'mapping is required', now())`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'episode-06.mkv', 1024, 'video', true, 1, 6)`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}

	view, err := read.GetAcquisition(ctx, acquisitionID)
	if err != nil {
		t.Fatalf("GetAcquisition() error = %v", err)
	}
	if view.AggregateStatus != "mapping_pending" || view.CurrentStage != "mapping" || view.Download == nil || view.Download.Status != "failed" {
		t.Fatalf("acquisition lifecycle = %#v", view)
	}
	if acquisitionStageKeyStatus(view.Stages, "download") != stageCompleted || acquisitionStageKeyStatus(view.Stages, "mapping") != stageWaiting {
		t.Fatalf("acquisition stages = %#v", view.Stages)
	}

	downloadView, err := read.GetDownload(ctx, downloadID)
	if err != nil {
		t.Fatalf("GetDownload() error = %v", err)
	}
	if downloadView.Actions.CanRetry {
		t.Fatalf("mapping-blocked download unexpectedly allows direct retry: %#v", downloadView.Actions)
	}
	completedPhase := "completed"
	completedDownloads, err := read.ListDownloads(ctx, nil, 100, nil, &completedPhase, nil, nil, nil)
	if err != nil || len(completedDownloads.Items) != 1 || completedDownloads.Items[0].ID != downloadID {
		t.Fatalf("completed transfer downloads = %#v, error = %v", completedDownloads.Items, err)
	}
	failedPhase := "failed"
	failedDownloads, err := read.ListDownloads(ctx, nil, 100, nil, &failedPhase, nil, nil, nil)
	if err != nil || len(failedDownloads.Items) != 0 {
		t.Fatalf("transfer-failed downloads = %#v, error = %v", failedDownloads.Items, err)
	}

	phase := "mapping_pending"
	page, err := read.ListAcquisitions(ctx, nil, 100, nil, nil, &phase, nil, nil)
	if err != nil {
		t.Fatalf("ListAcquisitions(mapping_pending) error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != acquisitionID {
		t.Fatalf("mapping pending page = %#v", page.Items)
	}

	summary, err := read.DashboardSummary(ctx)
	if err != nil {
		t.Fatalf("DashboardSummary() error = %v", err)
	}
	if summary.Counts.MappingPending != 1 || summary.Counts.Failed != 0 || summary.Counts.Attention != 1 {
		t.Fatalf("dashboard counts = %#v", summary.Counts)
	}
	if len(summary.AttentionItems) != 1 || summary.AttentionItems[0].Reason != "mapping_required" {
		t.Fatalf("dashboard attention = %#v", summary.AttentionItems)
	}
}
