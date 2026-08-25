//go:build integration

package service

import (
	"testing"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func requireTargetProcessingOccupancy(t *testing.T, fixture rssTargetFixture, targetIndex int) {
	t.Helper()
	generic, err := fixture.queries.GetEpisodeMappingTargetOccupancy(fixture.ctx, db.GetEpisodeMappingTargetOccupancyParams{
		ExcludedAcquisitionID: repository.UUIDToPG(uuid.New()),
		TargetEpisodeID:       repository.UUIDToPG(fixture.targetEpisodeIDs[targetIndex]),
		SeriesID:              repository.UUIDToPG(fixture.seriesID),
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(
		fixture.ctx,
		fixture.queries,
		fixture.subscriptionID,
		1,
		targetIndex+1,
		uuid.Nil,
		uuid.Nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !generic.ProcessingPresent || mapped.Reason != rssTargetProcessingReason {
		t.Fatalf("target %d occupancy = generic %#v mapped %#v, want processing", targetIndex+1, generic, mapped)
	}
}

func TestRSSOccupancyFindsManualManagedImportWithNullExcludedEntryIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	acquisitionID, downloadID := uuid.New(), uuid.New()
	fileID, transcodeID, taskID := uuid.New(), uuid.New(), uuid.New()
	var mappingID uuid.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT id
FROM episode_mappings
WHERE profile_id = $1 AND source_season = 1 AND source_episode = 1`, fixture.profileID).Scan(&mappingID); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
INSERT INTO acquisitions (
    id, series_id, mapping_profile_id, source_kind, source_uri,
    source_payload, created_by
) VALUES ($1, $2, $3, 'manual', $4, '{"sourceSeason":1,"sourceEpisode":1,"singleEpisode":true}', $5);
INSERT INTO downloads (id, acquisition_id, attempt, status)
VALUES ($6, $1, 1, 'materialized');
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind,
    selected, source_season, source_episode
) VALUES ($7, $6, 0, 'Manual.S01E01.mkv', 1024, 'video', true, 1, 1);
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($8, $9, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1);
INSERT INTO episode_tasks (
    id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id,
    state, video_state, subtitle_state
) VALUES ($10, $1, $7, $11, $8, 'imported', 'video_ready', 'ass_ready');
INSERT INTO imports (
    id, task_id, status, destination_video_path, destination_subtitle_path, completed_at
) VALUES ($12, $10, 'succeeded', '/library/Manual/Manual-S02E01.mkv', '/library/Manual/Manual-S02E01.ass', now())`,
		acquisitionID,
		fixture.seriesID,
		fixture.profileID,
		"https://example.test/manual-managed-import.torrent",
		fixture.actorID,
		downloadID,
		fileID,
		transcodeID,
		"manual-managed-import-"+transcodeID.String(),
		taskID,
		mappingID,
		uuid.New(),
	); err != nil {
		t.Fatal(err)
	}

	occupancy, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(
		fixture.ctx,
		fixture.queries,
		fixture.subscriptionID,
		1,
		1,
		uuid.Nil,
		uuid.Nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if occupancy.Reason != rssTargetImportedReason || !occupancy.Fulfilled || occupancy.FulfillmentSource != rssFulfillmentManagedImport {
		t.Fatalf("manual managed-import occupancy = %#v", occupancy)
	}
}

func TestTargetOccupancyUsesCanonicalDownloadAttemptsIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)

	preManifestAcquisitionID := uuid.New()
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
INSERT INTO acquisitions (
    id, series_id, mapping_profile_id, source_kind, source_uri,
    source_payload, created_by
) VALUES ($1, $2, $3, 'manual', $4, '{"sourceSeason":1,"sourceEpisode":1,"singleEpisode":true}', $5);
INSERT INTO downloads (id, acquisition_id, attempt, status, failure_stage)
VALUES
    ($6, $1, 1, 'enqueue_pending', NULL),
    ($7, $1, 2, 'cancelled', NULL),
    ($8, $1, 3, 'failed', 'enqueue');
UPDATE downloads SET deleted_at = now() WHERE id = $8`,
		preManifestAcquisitionID,
		fixture.seriesID,
		fixture.profileID,
		"https://example.test/pre-manifest-canonical.torrent",
		fixture.actorID,
		uuid.New(),
		uuid.New(),
		uuid.New(),
	); err != nil {
		t.Fatal(err)
	}
	requireTargetProcessingOccupancy(t, fixture, 0)

	manifestAcquisitionID, staleDownloadID, canonicalDownloadID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
INSERT INTO acquisitions (
    id, series_id, mapping_profile_id, source_kind, source_uri,
    source_payload, created_by
) VALUES ($1, $2, $3, 'manual', $4, '{"sourceSeason":1,"sourceEpisode":0,"singleEpisode":false}', $5);
INSERT INTO downloads (id, acquisition_id, attempt, status, failure_stage)
VALUES
    ($6, $1, 1, 'completed', NULL),
    ($7, $1, 2, 'completed', NULL),
    ($8, $1, 3, 'cancelled', NULL),
    ($9, $1, 4, 'failed', 'enqueue');
UPDATE downloads SET deleted_at = now() WHERE id = $9;
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind,
    selected, source_season, source_episode
) VALUES
    ($10, $6, 0, 'SeasonPack.Stale.S01E01.mkv', 1024, 'video', true, 1, 1),
    ($11, $7, 0, 'SeasonPack.Canonical.S01E02.mkv', 1024, 'video', true, 1, 2)`,
		manifestAcquisitionID,
		fixture.seriesID,
		fixture.profileID,
		"https://example.test/manifest-canonical.torrent",
		fixture.actorID,
		staleDownloadID,
		canonicalDownloadID,
		uuid.New(),
		uuid.New(),
		uuid.New(),
		uuid.New(),
	); err != nil {
		t.Fatal(err)
	}
	requireTargetProcessingOccupancy(t, fixture, 1)
}
