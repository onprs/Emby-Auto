//go:build integration

package service

import (
	"testing"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

// 已入库完成的条目（imported_at + managed_import）再次出现在 feed 时，
// 不得被媒体库占用核验改判或移入"已跳过的 RSS 更新"。
func TestRSSImportedEntrySurvivesRepeatedPollInConfirmedGroupIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	owner, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "imported-owner")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(owner.Candidates) != 1 {
		t.Fatalf("owner PersistPoll() = %#v, %v", owner, err)
	}
	entryID := owner.Candidates[0].EntryID
	if _, err := fixture.pool.Exec(fixture.ctx, `
UPDATE rss_entries
SET imported_at = now(), fulfillment_source = 'managed_import'
WHERE id = $1`, entryID); err != nil {
		t.Fatal(err)
	}
	// Emby 扫描到本系统入库产物后，媒体库出现该目标集。
	fixture.addCatalogEpisode(t, 0)

	// 同一 release 再次出现在 feed。
	if _, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "imported-owner")},
	}, domain.RSSPollPersistOptions{}); err != nil {
		t.Fatalf("repeated PersistPoll() error = %v", err)
	}

	var downloadable bool
	var reasons []string
	var source *string
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT downloadable, rejection_reasons, fulfillment_source FROM rss_entries WHERE id = $1`, entryID).Scan(&downloadable, &reasons, &source); err != nil {
		t.Fatal(err)
	}
	if !downloadable || len(reasons) != 0 || source == nil || *source != rssFulfillmentManagedImport {
		t.Fatalf("imported entry after repeated poll = downloadable %t reasons %v source %v", downloadable, reasons, source)
	}

	readService := NewReadService(db.New(fixture.pool))
	confirmedGroup, skippedGroup := "confirmed", "skipped"
	confirmed, err := readService.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, &confirmedGroup, nil, nil)
	if err != nil || len(confirmed.Items) != 1 || confirmed.Items[0].ID != entryID {
		t.Fatalf("confirmed RSS group = %#v, error = %v", confirmed.Items, err)
	}
	skipped, err := readService.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, &skippedGroup, nil, nil)
	if err != nil || len(skipped.Items) != 0 {
		t.Fatalf("skipped RSS group = %#v, error = %v", skipped.Items, err)
	}
}

// 另一个 managed import 满足目标后，回收的 enqueued 冲突条目（导入失败）
// 应留在"已跳过"，即使它带有 imported_at + managed_import 满足标记。
func TestRSSReconciledEnqueuedConflictEntryStaysInSkippedGroupIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)

	conflict, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "conflict")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(conflict.Candidates) != 1 {
		t.Fatalf("conflict PersistPoll() = %#v, %v", conflict, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, conflict.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	conflictEntryID := conflict.Candidates[0].EntryID
	var conflictAcquisitionID, conflictDownloadID, conflictMappingID uuid.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT acquisition.id, download.id
FROM acquisitions AS acquisition
JOIN downloads AS download ON download.acquisition_id = acquisition.id
WHERE acquisition.rss_entry_id = $1`, conflictEntryID).Scan(&conflictAcquisitionID, &conflictDownloadID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT id FROM episode_mappings WHERE profile_id = $1 AND source_season = 1 AND source_episode = 1`, fixture.profileID).Scan(&conflictMappingID); err != nil {
		t.Fatal(err)
	}
	// 下载器确认入队：status 变为 enqueued；随后导入因目标冲突失败。
	conflictFileID, conflictTranscodeID, conflictTaskID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
UPDATE rss_entries SET status = 'enqueued', enqueued_at = now() WHERE id = $1;
UPDATE downloads SET status = 'materialized' WHERE id = $2;
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($3, $2, 0, 'Conflict.S01E01.mkv', 1024, 'video', true, 1, 1);
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($4, $5, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1);
INSERT INTO episode_tasks (
    id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id,
    state, video_state, subtitle_state, failure_stage, error_code, error_message
) VALUES ($6, $7, $3, $8, $4, 'failed', 'video_ready', 'ass_ready', 'import', 'library_destination_conflict', 'fixture conflict');
INSERT INTO imports (id, task_id, status, error_code, error_message, completed_at)
VALUES ($9, $6, 'failed', 'library_destination_conflict', 'fixture conflict', now())`,
		conflictEntryID, conflictDownloadID, conflictFileID, conflictTranscodeID, "rss-conflict-"+conflictTranscodeID.String(), conflictTaskID, conflictAcquisitionID, conflictMappingID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	// 同一目标的另一个 release 成功导入。
	owner, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "owner")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(owner.Candidates) != 1 {
		t.Fatalf("owner PersistPoll() = %#v, %v", owner, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, owner.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	ownerEntryID := owner.Candidates[0].EntryID
	var ownerAcquisitionID, ownerDownloadID, ownerMappingID uuid.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT acquisition.id, download.id
FROM acquisitions AS acquisition
JOIN downloads AS download ON download.acquisition_id = acquisition.id
WHERE acquisition.rss_entry_id = $1`, ownerEntryID).Scan(&ownerAcquisitionID, &ownerDownloadID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT id FROM episode_mappings WHERE profile_id = $1 AND source_season = 1 AND source_episode = 1`, fixture.profileID).Scan(&ownerMappingID); err != nil {
		t.Fatal(err)
	}
	ownerFileID, ownerTranscodeID, ownerTaskID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
UPDATE downloads SET status = 'materialized' WHERE id = $1;
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($2, $1, 0, 'Owner.S01E01.mkv', 1024, 'video', true, 1, 1);
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($3, $4, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1);
INSERT INTO episode_tasks (
    id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id,
    state, video_state, subtitle_state
) VALUES ($5, $6, $2, $7, $3, 'imported', 'video_ready', 'ass_ready');
INSERT INTO imports (
    id, task_id, status, destination_video_path, destination_subtitle_path, completed_at
) VALUES ($8, $5, 'succeeded', '/library/Owner/Owner-S02E01.mkv', '/library/Owner/Owner-S02E01.ass', now());
UPDATE rss_entries SET status = 'enqueued', imported_at = now(), fulfillment_source = 'managed_import' WHERE id = $9;
INSERT INTO events (id, topic, resource_type, resource_id, data, occurred_at) VALUES
    ($10, 'task.created', 'episode_task', $5, jsonb_build_object('downloadId', $1::uuid, 'mediaType', 'episode', 'sourceSeason', 1, 'sourceEpisode', 1, 'targetSeason', 2, 'targetEpisode', 1), now()),
    ($11, 'task.imported', 'episode_task', $5, jsonb_build_object('importId', $8::uuid, 'destinationVideoPath', '/library/Owner/Owner-S02E01.mkv', 'destinationSubtitlePath', '/library/Owner/Owner-S02E01.ass'), now())`,
		ownerDownloadID, ownerFileID, ownerTranscodeID, "rss-owner-"+ownerTranscodeID.String(), ownerTaskID, ownerAcquisitionID, ownerMappingID, uuid.New(), ownerEntryID, uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}

	// 空 poll 触发入库冲突回收：冲突条目被标记满足但保留 enqueued 状态。
	if _, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{Title: "Target Occupancy"}, domain.RSSPollPersistOptions{}); err != nil {
		t.Fatalf("reconciliation PersistPoll() error = %v", err)
	}

	var conflictStatus, conflictReason string
	var conflictFulfilled bool
	var conflictSource *string
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT status, rejection_reasons[1], imported_at IS NOT NULL, fulfillment_source
FROM rss_entries WHERE id = $1`, conflictEntryID).Scan(&conflictStatus, &conflictReason, &conflictFulfilled, &conflictSource); err != nil {
		t.Fatal(err)
	}
	if conflictStatus != "enqueued" || conflictReason != rssTargetImportedReason || !conflictFulfilled || conflictSource == nil || *conflictSource != rssFulfillmentManagedImport {
		t.Fatalf("reconciled conflict entry = status %q reason %q fulfilled %t source %v", conflictStatus, conflictReason, conflictFulfilled, conflictSource)
	}

	readService := NewReadService(db.New(fixture.pool))
	confirmedGroup, skippedGroup := "confirmed", "skipped"
	confirmed, err := readService.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, &confirmedGroup, nil, nil)
	if err != nil || len(confirmed.Items) != 1 || confirmed.Items[0].ID != ownerEntryID {
		t.Fatalf("confirmed RSS group = %#v, error = %v", confirmed.Items, err)
	}
	skipped, err := readService.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, &skippedGroup, nil, nil)
	if err != nil || len(skipped.Items) != 1 || skipped.Items[0].ID != conflictEntryID {
		t.Fatalf("skipped RSS group = %#v, error = %v", skipped.Items, err)
	}
	if skipped.Items[0].RejectReason != rssTargetImportedReason {
		t.Fatalf("skipped entry reject reason = %q, want %q", skipped.Items[0].RejectReason, rssTargetImportedReason)
	}
}

// 目标同时被本系统成功导入且媒体库已存在时，重复 release 仍应报告
// managed_import 占用（而非 emby_catalog），并归入"已跳过"。
func TestRSSDuplicateReleaseKeepsManagedImportFulfillmentWithCatalogPresentIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	owner, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "managed-owner")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(owner.Candidates) != 1 {
		t.Fatalf("owner PersistPoll() = %#v, %v", owner, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, owner.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	var acquisitionID, downloadID, mappingID uuid.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT acquisition.id, download.id
FROM acquisitions AS acquisition
JOIN downloads AS download ON download.acquisition_id = acquisition.id
WHERE acquisition.rss_entry_id = $1`, owner.Candidates[0].EntryID).Scan(&acquisitionID, &downloadID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT id FROM episode_mappings WHERE profile_id = $1 AND source_season = 1 AND source_episode = 1`, fixture.profileID).Scan(&mappingID); err != nil {
		t.Fatal(err)
	}
	fileID, transcodeID, taskID := uuid.New(), uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
UPDATE downloads SET status = 'materialized' WHERE id = $1;
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($2, $1, 0, 'Managed.S01E01.mkv', 1024, 'video', true, 1, 1);
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($3, $4, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1);
INSERT INTO episode_tasks (
    id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id,
    state, video_state, subtitle_state
) VALUES ($5, $6, $2, $7, $3, 'imported', 'video_ready', 'ass_ready');
INSERT INTO imports (
    id, task_id, status, destination_video_path, destination_subtitle_path, completed_at
) VALUES ($8, $5, 'succeeded', '/library/Managed/Managed-S02E01.mkv', '/library/Managed/Managed-S02E01.ass', now());
UPDATE rss_entries SET imported_at = now(), fulfillment_source = 'managed_import' WHERE id = $9`,
		downloadID, fileID, transcodeID, "managed-owner-"+transcodeID.String(), taskID, acquisitionID, mappingID, uuid.New(), owner.Candidates[0].EntryID); err != nil {
		t.Fatal(err)
	}
	// 媒体库同时存在该目标集。
	fixture.addCatalogEpisode(t, 0)

	blocked, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "managed-alternate")},
	}, domain.RSSPollPersistOptions{})
	if err != nil {
		t.Fatalf("managed occupancy PersistPoll() error = %v", err)
	}
	if len(blocked.Candidates) != 0 {
		t.Fatalf("managed occupancy candidates = %v, want none", blocked.Candidates)
	}
	var reason, source string
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT rejection_reasons[1], fulfillment_source
FROM rss_entries
WHERE subscription_id = $1 AND id <> $2
ORDER BY discovered_at DESC LIMIT 1`, fixture.subscriptionID, owner.Candidates[0].EntryID).Scan(&reason, &source); err != nil {
		t.Fatal(err)
	}
	if reason != rssTargetImportedReason || source != rssFulfillmentManagedImport {
		t.Fatalf("managed occupancy with catalog present = reason %q source %q", reason, source)
	}

	readService := NewReadService(db.New(fixture.pool))
	skippedGroup := "skipped"
	skipped, err := readService.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, &skippedGroup, nil, nil)
	if err != nil || len(skipped.Items) != 1 {
		t.Fatalf("skipped RSS group = %#v, error = %v", skipped.Items, err)
	}
	if skipped.Items[0].RejectReason != rssTargetImportedReason {
		t.Fatalf("skipped entry reject reason = %q, want %q", skipped.Items[0].RejectReason, rssTargetImportedReason)
	}
}

