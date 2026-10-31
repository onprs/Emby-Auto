//go:build integration

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
)

func TestStoppedTaskResumesOnlyUnfinishedStagesIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx := context.Background()
	queries := db.New(fixture.pool)
	tasks := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	commands := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, tasks)
	cases := []struct {
		name, video, subtitle, wantKind, wantState, wantVideo, wantSubtitle string
		wantOperations                                                      int
	}{
		{"双分支停止", "cancelled", "cancelled", appqueue.KindTranscodeRun, "processing", "transcode_queued", "subtitle_queued", 2},
		{"保留字幕", "cancelled", "ass_ready", appqueue.KindTranscodeRun, "processing", "transcode_queued", "ass_ready", 1},
		{"保留视频", "video_ready", "cancelled", appqueue.KindSubtitlePrepare, "processing", "video_ready", "subtitle_queued", 1},
		{"失败与停止混合", "failed", "cancelled", appqueue.KindTranscodeRun, "processing", "transcode_queued", "subtitle_queued", 2},
		{"媒体完成后停止", "video_ready", "ass_ready", appqueue.KindMediaFinalize, "finalizing", "video_ready", "ass_ready", 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, id := createTaskWithMediaStates(t, fixture, "cancelled", test.video, test.subtitle, "")
			key := "resume-" + id.String()
			view, operation, err := commands.Retry(ctx, id, 1, key, fixture.actorID)
			if err != nil {
				t.Fatal(err)
			}
			if view.State != domain.TaskState(test.wantState) || view.VideoState != domain.VideoState(test.wantVideo) || view.SubtitleState != domain.SubtitleState(test.wantSubtitle) || operation.Kind != test.wantKind {
				t.Fatalf("恢复阶段错误: %#v / %s", view, operation.Kind)
			}
			if countOperationsForTask(t, fixture, id) != test.wantOperations {
				t.Fatalf("操作数错误: %d", countOperationsForTask(t, fixture, id))
			}
			eventsBeforeReplay := countEventsForTask(t, fixture, id)
			replayed, replayOperation, err := commands.Retry(ctx, id, 1, key, fixture.actorID)
			if err != nil || replayOperation.ID != operation.ID || replayed.Version != view.Version {
				t.Fatalf("重复请求没有复用结果: %#v / %v", replayed, err)
			}
			if _, _, err := commands.Retry(ctx, id, 1, key+"-duplicate", fixture.actorID); !errors.Is(err, ErrStateConflict) {
				t.Fatalf("过期版本应冲突: %v", err)
			}
			if countOperationsForTask(t, fixture, id) != test.wantOperations || countEventsForTask(t, fixture, id) != eventsBeforeReplay {
				t.Fatal("重复请求产生了额外操作或事件")
			}
		})
	}
}

func TestStoppedTaskWaitsForCancellationAndCanBeStoppedAfterFailureIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx := context.Background()
	queries := db.New(fixture.pool)
	tasks := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	commands := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, tasks)
	_, _, _, id := createTaskWithMediaStates(t, fixture, "failed", "failed", "ass_ready", "video")
	stopped, cancellation, err := commands.Cancel(ctx, id, 1, "stop-failure", fixture.actorID)
	if err != nil || stopped.State != domain.TaskCancelled {
		t.Fatalf("失败任务无法停止: %#v / %v", stopped, err)
	}
	if _, _, err := commands.Retry(ctx, id, int32(stopped.Version), "resume-before-stop", fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("停止操作未完成时应阻止重试: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE operations SET status='succeeded', finished_at=now() WHERE id=$1`, cancellation.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commands.Retry(ctx, id, int32(stopped.Version), "resume-after-stop", fixture.actorID); err != nil {
		t.Fatal(err)
	}
}

func TestRetryCannotRestoreDeletingResourcesIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx := context.Background()
	queries := db.New(fixture.pool)
	tasks := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	commands := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, tasks)
	acquisitionID, _, _, taskID := createTaskWithMediaStates(t, fixture, "cancelled", "cancelled", "ass_ready", "")
	if _, err := fixture.pool.Exec(ctx, `UPDATE acquisitions SET deletion_requested_at=now() WHERE id=$1`, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commands.Retry(ctx, taskID, 1, "retry-deleting-task", fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("删除中的任务不应恢复: %v", err)
	}
	if countOperationsForTask(t, fixture, taskID) != 0 {
		t.Fatal("删除中的任务产生了恢复操作")
	}
	acquisitionID = fixture.createAcquisition(t, `{"sourceSeason":1}`)
	downloadID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO downloads(id,acquisition_id,status,deletion_requested_at) VALUES($1,$2,'cancelled',now())`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler).Retry(ctx, downloadID, 1, "retry-deleting-download", fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("删除中的下载不应恢复: %v", err)
	}
}

func TestStoppedDownloadPreservesVerifiedManifestAndRestartsOperationsIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx := context.Background()
	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1,"sourceEpisode":1,"singleEpisode":true}`)
	downloadID, fileID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO downloads(id,acquisition_id,status,torrent_hash,file_resolution_source,save_path,client_state,last_synced_at) VALUES($1,$2,'downloading','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','deterministic','/downloads/old','downloading',now())`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO download_files(id,download_id,file_index,relative_path,size_bytes,media_kind,selected,source_season,source_episode) VALUES($1,$2,0,'Series.S01E01.mkv',1024,'video',true,1,1)`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	commands := NewDownloadCommandWorkflow(fixture.transactor, fixture.scheduler)
	stopped, cancellation, err := commands.Cancel(ctx, downloadID, 1, "stop-download", fixture.actorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := commands.Retry(ctx, downloadID, int32(stopped.Version), "resume-pending-download", fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("取消尚未完成时不应启动下载: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE operations SET status='succeeded',finished_at=now() WHERE id=$1`, cancellation.ID); err != nil {
		t.Fatal(err)
	}
	view, operation, err := commands.Retry(ctx, downloadID, int32(stopped.Version), "resume-download", fixture.actorID)
	if err != nil || view.Status != "enqueue_pending" || view.TorrentHash != "" || len(view.Files) != 1 || view.Files[0].ID != fileID || operation.Kind != appqueue.KindDownloadEnqueue {
		t.Fatalf("下载未正确恢复: %#v / %#v / %v", view, operation, err)
	}
	if _, replay, err := commands.Retry(ctx, downloadID, int32(stopped.Version), "resume-download", fixture.actorID); err != nil || replay.ID != operation.ID {
		t.Fatalf("下载重试不幂等: %v", err)
	}
	var resolution, savePath, clientState *string
	if err := fixture.pool.QueryRow(ctx, `SELECT file_resolution_source,save_path,client_state FROM downloads WHERE id=$1`, downloadID).Scan(&resolution, &savePath, &clientState); err != nil || resolution == nil || *resolution != "deterministic" || savePath != nil || clientState != nil {
		t.Fatalf("下载残留旧执行信息: %v / %v / %v / %v", resolution, savePath, clientState, err)
	}
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for kind, key := range map[string]string{
		appqueue.KindDownloadSelectionApply: "download.selection.apply:" + downloadID.String() + ":" + hash,
		appqueue.KindDownloadSync:           "download.sync:" + downloadID.String() + ":" + hash,
		appqueue.KindDownloadMaterialize:    "download.materialize:" + downloadID.String(),
	} {
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO operations(id,kind,resource_type,resource_id,idempotency_key,status,max_attempts,timeout_seconds,finished_at) VALUES($1,$2,'download',$3,$4,'cancelled',3,60,now())`, uuid.New(), kind, downloadID, key); err != nil {
			t.Fatal(err)
		}
	}
	freshOperation := func(kind string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		var count int
		if err := fixture.pool.QueryRow(ctx, `SELECT id,(SELECT count(*) FROM operations WHERE resource_id=$1 AND kind=$2 AND status='queued') FROM operations WHERE resource_id=$1 AND kind=$2 AND status='queued' LIMIT 1`, downloadID, kind).Scan(&id, &count); err != nil || count != 1 {
			t.Fatalf("%s 没有创建新的幂等阶段: %d / %v", kind, count, err)
		}
		return id
	}
	workflow := NewDownloadWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	command, err := workflow.LoadEnqueueCommand(ctx, downloadID)
	if err != nil || !command.HasResolvedManifest {
		t.Fatalf("恢复没有识别已确认文件清单: %#v / %v", command, err)
	}
	if err := workflow.CompleteEnqueue(ctx, domain.DownloadEnqueueCompletion{
		OperationID: operation.ID, DownloadID: downloadID, TorrentHash: hash, SavePath: "/downloads/retry", Outcome: domain.DownloadManifestResolved,
		Files: []domain.ClassifiedDownloadFile{{DownloadFile: domain.DownloadFile{Index: 0, RelativePath: "Other.S01E02.mkv", SizeBytes: 1024}, Kind: "video", SourceSeason: 1, SourceEpisode: 2, Selected: true}},
	}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("改变文件清单后不应复用旧映射: %v", err)
	}
	if err := workflow.CompleteEnqueue(ctx, domain.DownloadEnqueueCompletion{
		OperationID: operation.ID, DownloadID: downloadID, TorrentHash: hash, SavePath: "/downloads/retry", Outcome: domain.DownloadManifestUnresolved,
		Files: []domain.ClassifiedDownloadFile{{DownloadFile: domain.DownloadFile{Index: 0, RelativePath: "Series.S01E01.mkv", SizeBytes: 1024}, Kind: "video", SourceSeason: 2, SourceEpisode: 9, Selected: false}},
	}); err != nil {
		t.Fatal(err)
	}
	var persistedID uuid.UUID
	var season, episode int
	var selected bool
	if err := fixture.pool.QueryRow(ctx, `SELECT id,source_season,source_episode,selected FROM download_files WHERE download_id=$1`, downloadID).Scan(&persistedID, &season, &episode, &selected); err != nil || persistedID != fileID || season != 1 || episode != 1 || !selected {
		t.Fatalf("恢复覆盖了已确认的文件选择或坐标: %s / %d/%d / %v / %v", persistedID, season, episode, selected, err)
	}
	selectionOperation := freshOperation(appqueue.KindDownloadSelectionApply)
	if err := workflow.CompleteSelectionApply(ctx, downloadID, selectionOperation); err != nil {
		t.Fatal(err)
	}
	syncOperation := freshOperation(appqueue.KindDownloadSync)
	if err := workflow.CompleteDownload(ctx, downloadID, syncOperation, "uploading"); err != nil {
		t.Fatal(err)
	}
	freshOperation(appqueue.KindDownloadMaterialize)
}

func TestDashboardRecentTasksExcludesOperationsAndDeletingVideosIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx := context.Background()
	_, _, _, id := createTaskWithMediaStates(t, fixture, "processing", "transcoding", "ass_ready", "")
	deletingAcquisitionID, _, _, _ := createTaskWithMediaStates(t, fixture, "cancelled", "cancelled", "cancelled", "")
	if _, err := fixture.pool.Exec(ctx, `UPDATE acquisitions SET deletion_requested_at=now() WHERE id=$1`, deletingAcquisitionID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{appqueue.KindRSSPoll, appqueue.KindDownloadSync, appqueue.KindTaskCancel} {
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO operations(id,kind,resource_type,resource_id,idempotency_key,status,max_attempts,timeout_seconds) VALUES($1,$2,'episode_task',$3,$4,'queued',3,60)`, uuid.New(), kind, id, uuid.NewString()); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := NewReadService(db.New(fixture.pool), database.NewTransactor(fixture.pool)).DashboardSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.RecentTasks) != 1 || summary.RecentTasks[0].ID != id {
		t.Fatalf("最近视频混入了操作或删除中的内容: %#v", summary.RecentTasks)
	}
}
