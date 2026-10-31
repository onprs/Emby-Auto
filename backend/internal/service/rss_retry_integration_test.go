//go:build integration

package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestRSSStoppedDownloadShowsStoppedAndSkippedEntryCanTakeOverIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	initial, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "恢复订阅", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "owner")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(initial.Candidates) != 1 {
		t.Fatalf("准备主来源失败: %#v / %v", initial, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, initial.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	relations, err := fixture.queries.GetRSSEntryRelations(fixture.ctx, repository.UUIDToPG(initial.Candidates[0].EntryID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "恢复订阅", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "alternative")},
	}, domain.RSSPollPersistOptions{}); err != nil {
		t.Fatal(err)
	}
	reads := NewReadService(fixture.queries, database.NewTransactor(fixture.pool))
	group := "skipped"
	page, err := reads.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, &group, nil, nil, nil, nil)
	if err != nil || len(page.Items) != 1 || !page.Items[0].CanRetry || page.Items[0].RejectReason != rssTargetProcessingReason {
		t.Fatalf("占用跳过项应允许重新核查: %#v / %v", page, err)
	}
	skipped := page.Items[0]
	transactor := database.NewTransactor(fixture.pool)
	commands := NewDownloadCommandWorkflow(transactor, fixture.workflow.operations)
	downloadID := repository.UUIDFromPG(relations.DownloadID)
	stopped, _, err := commands.Cancel(fixture.ctx, downloadID, 1, "stop-owner", fixture.actorID)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟取消处理器已经退出，旧下载操作也已收到取消。
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operations SET status=CASE WHEN kind='download.cancel' THEN 'succeeded' ELSE 'cancelled' END,finished_at=now() WHERE resource_type='download' AND resource_id=$1`, downloadID); err != nil {
		t.Fatal(err)
	}
	confirmed := "confirmed"
	page, err = reads.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, &confirmed, nil, nil, nil, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].AcquisitionProgress == nil || page.Items[0].AcquisitionProgress.AggregateStatus != "cancelled" || !page.Items[0].CanRetry {
		t.Fatalf("已确认列表没有反映停止状态: %#v / %v", page, err)
	}
	operation, err := fixture.workflow.RetryEntry(fixture.ctx, fixture.subscriptionID, skipped.ID, int32(skipped.Version), "retry-skipped", fixture.actorID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.workflow.RetryEntry(fixture.ctx, fixture.subscriptionID, skipped.ID, int32(skipped.Version), "retry-skipped", fixture.actorID)
	if err != nil || replay.ID != operation.ID {
		t.Fatalf("RSS 重试未保持幂等: %v", err)
	}
	if _, err := fixture.workflow.RetryEntry(fixture.ctx, fixture.subscriptionID, skipped.ID, int32(skipped.Version), "retry-skipped-stale", fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("RSS 重试应拒绝旧版本: %v", err)
	}
	entry, err := fixture.queries.LockRSSEntryForEnqueue(fixture.ctx, repository.UUIDToPG(skipped.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, domain.RSSEnqueueCandidate{
		EntryID: skipped.ID, ExpectedVersion: int32(rssEntryVersion(entry.EnqueueAttempts)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.queries.GetRSSEntryRelations(fixture.ctx, entry.ID); err != nil {
		t.Fatalf("停止释放占用后没有创建新的来源: %v", err)
	}
	if _, _, err := commands.Retry(fixture.ctx, downloadID, int32(stopped.Version), "retry-displaced-owner", fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("原下载不得与接手来源重复启动: %v", err)
	}
	var pending int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM operations WHERE resource_type='download' AND resource_id=$1 AND status='queued'`, downloadID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("被接手的下载产生了新操作: %d / %v", pending, err)
	}
}

func TestRSSCancelledMediaFailureReleasesTargetOccupancyIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	initial, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "停止后恢复", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "initial")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(initial.Candidates) != 1 {
		t.Fatalf("准备来源失败: %#v / %v", initial, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, initial.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	relations, err := fixture.queries.GetRSSEntryRelations(fixture.ctx, repository.UUIDToPG(initial.Candidates[0].EntryID))
	if err != nil {
		t.Fatal(err)
	}
	profileID, fileID, taskID := uuid.New(), uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO transcode_profiles(id,name,version,active,is_default,video_codec,encoder,container,file_extension,quality_mode,quality_value,audio_policy,preset,pixel_format,thread_count,max_concurrency) VALUES($1,'停止恢复测试',1,true,false,'h264','libx264','matroska','mkv','crf',20,'copy','medium','yuv420p',0,1)`, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO download_files(id,download_id,file_index,relative_path,size_bytes,media_kind,selected,source_season,source_episode) VALUES($1,$2,0,'Series.S01E01.mkv',1024,'video',true,1,1)`, fileID, relations.DownloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO episode_tasks(id,acquisition_id,source_video_file_id,mapping_id,transcode_profile_id,state,video_state,subtitle_state) SELECT $1,$2,$3,id,$4,'cancelled','failed','ass_ready' FROM episode_mappings WHERE profile_id=$5 AND source_episode=1`, taskID, relations.AcquisitionID, fileID, profileID, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE downloads SET status='materialized' WHERE id=$1`, relations.DownloadID); err != nil {
		t.Fatal(err)
	}
	occupancy, err := fixture.queries.GetEpisodeMappingTargetOccupancy(fixture.ctx, db.GetEpisodeMappingTargetOccupancyParams{
		TargetEpisodeID: repository.UUIDToPG(fixture.targetEpisodeIDs[0]), SeriesID: repository.UUIDToPG(fixture.seriesID), ExcludedAcquisitionID: repository.UUIDToPG(uuid.New()),
	})
	if err != nil || occupancy.ProcessingPresent {
		t.Fatalf("已停止的失败分支仍占用映射目标: %#v / %v", occupancy, err)
	}
	poll, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "停止后恢复", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "replacement-source")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(poll.Candidates) != 1 {
		t.Fatalf("已停止媒体任务仍占用 RSS 目标: %#v / %v", poll, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, poll.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operations SET status='succeeded',finished_at=now() WHERE resource_type='download' AND resource_id=$1`, relations.DownloadID); err != nil {
		t.Fatal(err)
	}
	transactor := database.NewTransactor(fixture.pool)
	tasks := NewTaskWorkflow(fixture.queries, transactor, fixture.workflow.operations)
	commands := NewTaskCommandWorkflow(fixture.queries, transactor, fixture.workflow.operations, tasks)
	_, _, err = commands.Retry(fixture.ctx, taskID, 1, "retry-displaced-video", fixture.actorID)
	var conflict *Error
	if !errors.As(err, &conflict) || conflict.Code != "target_episode_processing" {
		t.Fatalf("被其它来源接手的视频任务不应恢复: %v", err)
	}
}
