//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
)

// 覆盖 cancelled 场景的真实恢复路径：video failed + subtitle ready
func TestTaskRetryCancelledVideoFailedOnlyIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskCancelled), string(domain.VideoFailed), string(domain.SubtitleASSReady), "")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)

	// 预校验 read model：安全 cancelled 应为可重试
	taskBefore, err := taskWorkflow.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !taskBefore.Actions.CanRetry {
		t.Fatalf("cancelled safe should be CanRetry true, got %#v", taskBefore.Actions)
	}

	key := "retry-cancelled-video-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.State != domain.TaskProcessing || task.VideoState != domain.VideoTranscodeQueued || task.SubtitleState != domain.SubtitleASSReady || task.Version != 2 || task.FailureStage != "" {
		t.Fatalf("cancelled video single retry task = %#v", task)
	}
	if operation.Kind != appqueue.KindTranscodeRun {
		t.Fatalf("operation kind = %q, want %q", operation.Kind, appqueue.KindTranscodeRun)
	}
	if operation.IdempotencyKey != key {
		t.Fatalf("operation key = %q, want %q", operation.IdempotencyKey, key)
	}
	if countOperationsForTask(t, fixture, taskID) != 1 {
		t.Fatalf("operations want 1, got %d", countOperationsForTask(t, fixture, taskID))
	}
	// 字幕分支未调度
	var subtitleOps int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_id = $1 AND kind = $2`, taskID, appqueue.KindSubtitlePrepare).Scan(&subtitleOps); err != nil {
		t.Fatal(err)
	}
	if subtitleOps != 0 {
		t.Fatalf("subtitle operation should not be scheduled, got %d", subtitleOps)
	}
	// 审计事件
	var topic string
	var eventData json.RawMessage
	var operationID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `SELECT topic, operation_id, data FROM events WHERE resource_type='episode_task' AND resource_id=$1 AND topic='task.retry_requested'`, taskID).Scan(&topic, &operationID, &eventData); err != nil {
		t.Fatalf("failed to load retry event: %v", err)
	}
	if topic != "task.retry_requested" || operationID != operation.ID {
		t.Fatalf("event mismatch topic %q operation %v want %v", topic, operationID, operation.ID)
	}
	// 同 key 重放无重复
	opsBefore := countOperationsForTask(t, fixture, taskID)
	eventsBefore := countEventsForTask(t, fixture, taskID)
	replayed, replayOp, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil || replayOp.ID != operation.ID || replayed.Version != 2 {
		t.Fatalf("replay = %#v / %v / %v", replayed, replayOp, err)
	}
	if countOperationsForTask(t, fixture, taskID) != opsBefore || countEventsForTask(t, fixture, taskID) != eventsBefore {
		t.Fatalf("replay changed counts")
	}
	// 不同 key 在恢复后不得再次调度
	_, _, err = workflow.Retry(ctx, taskID, 2, "retry-cancelled-video-second-"+taskID.String(), fixture.actorID)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("second different key should be state_conflict, got %v", err)
	}
}

// cancelled + video ready + subtitle failed
func TestTaskRetryCancelledSubtitleFailedOnlyIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskCancelled), string(domain.VideoReady), string(domain.SubtitleFailed), "")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)

	taskBefore, _ := taskWorkflow.GetTask(ctx, taskID)
	if !taskBefore.Actions.CanRetry {
		t.Fatalf("cancelled safe subtitle should be CanRetry")
	}
	key := "retry-cancelled-subtitle-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.State != domain.TaskProcessing || task.VideoState != domain.VideoReady || task.SubtitleState != domain.SubtitleQueued || task.Version != 2 {
		t.Fatalf("cancelled subtitle retry task = %#v", task)
	}
	if operation.Kind != appqueue.KindSubtitlePrepare {
		t.Fatalf("subtitle operation kind = %q", operation.Kind)
	}
	if countOperationsForTask(t, fixture, taskID) != 1 {
		t.Fatalf("operations want 1")
	}
}

// cancelled 双失败
func TestTaskRetryCancelledBothFailedIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskCancelled), string(domain.VideoFailed), string(domain.SubtitleFailed), "")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)

	key := "retry-cancelled-both-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.State != domain.TaskProcessing || task.VideoState != domain.VideoTranscodeQueued || task.SubtitleState != domain.SubtitleQueued || task.Version != 2 {
		t.Fatalf("cancelled both retry task = %#v", task)
	}
	if operation.Kind != appqueue.KindTranscodeRun {
		t.Fatalf("primary should be video, got %q", operation.Kind)
	}
	if countOperationsForTask(t, fixture, taskID) != 2 {
		t.Fatalf("operations want 2, got %d", countOperationsForTask(t, fixture, taskID))
	}
	derived := deriveSecondaryIdempotencyKey(key, appqueue.KindSubtitlePrepare, taskID)
	var secondaryCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1 AND kind = $2`, derived, appqueue.KindSubtitlePrepare).Scan(&secondaryCount); err != nil {
		t.Fatal(err)
	}
	if secondaryCount != 1 {
		t.Fatalf("secondary subtitle not found")
	}
}

// 普通 cancelled、active 分支、双 ready、非法组合、版本冲突均拒绝且无副作用
func TestTaskRetryCancelledRejectedIntegration(t *testing.T) {
	tests := []struct {
		name          string
		state         string
		videoState    string
		subtitleState string
		failureStage  string
		expectedCode  string
	}{
		{"cancelled with cancelled branch", string(domain.TaskCancelled), string(domain.VideoCancelled), string(domain.SubtitleASSReady), "", "state_conflict"},
		{"cancelled with active video", string(domain.TaskCancelled), string(domain.VideoTranscoding), string(domain.SubtitleASSReady), "", "state_conflict"},
		{"cancelled with active subtitle", string(domain.TaskCancelled), string(domain.VideoReady), string(domain.SubtitleExtractingConverting), "", "state_conflict"},
		{"cancelled both ready no failed", string(domain.TaskCancelled), string(domain.VideoReady), string(domain.SubtitleASSReady), "", "state_conflict"},
		{"cancelled both queued", string(domain.TaskCancelled), string(domain.VideoTranscodeQueued), string(domain.SubtitleQueued), "", "state_conflict"},
		{"cancelled subtitle cancelled with video failed but subtitle cancelled not allowed", string(domain.TaskCancelled), string(domain.VideoFailed), string(domain.SubtitleCancelled), "", "state_conflict"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _, _, taskID := createTaskWithMediaStates(t, fixture, tc.state, tc.videoState, tc.subtitleState, tc.failureStage)
			queries := db.New(fixture.pool)
			taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
			workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
			_, _, err := workflow.Retry(ctx, taskID, 1, "retry-reject-"+taskID.String(), fixture.actorID)
			if !errors.Is(err, ErrStateConflict) {
				t.Fatalf("expected state_conflict, got %v", err)
			}
			var version int32
			var state, videoState, subtitleState string
			if err := fixture.pool.QueryRow(ctx, `SELECT version, state, video_state, subtitle_state FROM episode_tasks WHERE id = $1`, taskID).Scan(&version, &state, &videoState, &subtitleState); err != nil {
				t.Fatal(err)
			}
			if version != 1 || state != tc.state || videoState != tc.videoState || subtitleState != tc.subtitleState {
				t.Fatalf("side effect leaked: version %d state %q video %q subtitle %q", version, state, videoState, subtitleState)
			}
			if countOperationsForTask(t, fixture, taskID) != 0 || countEventsForTask(t, fixture, taskID) != 0 {
				t.Fatalf("should have no side effect")
			}
			taskBefore, _ := taskWorkflow.GetTask(ctx, taskID)
			if taskBefore.Actions.CanRetry {
				t.Fatalf("rejected case should not be CanRetry, got %#v", taskBefore.Actions)
			}
		})
	}
}

func TestTaskRetryCancelledVersionConflictNoSideEffectIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskCancelled), string(domain.VideoFailed), string(domain.SubtitleASSReady), "")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	_, _, err := workflow.Retry(ctx, taskID, 99, "retry-cancelled-version-"+taskID.String(), fixture.actorID)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	var version int32
	if err := fixture.pool.QueryRow(ctx, `SELECT version FROM episode_tasks WHERE id = $1`, taskID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version leaked %d", version)
	}
	if countOperationsForTask(t, fixture, taskID) != 0 || countEventsForTask(t, fixture, taskID) != 0 {
		t.Fatalf("operation/event leaked")
	}
}

// read model：完整 Task 与 Acquisition 摘要对各类状态返回准确能力
func TestTaskCancelledReadModelIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := db.New(fixture.pool)
	readService := NewReadService(queries)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)

	cases := []struct {
		name          string
		state         string
		videoState    string
		subtitleState string
		failureStage  string
		wantCanRetry  bool
	}{
		{"failed with video failed", string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleASSReady), "video", true},
		{"processing stuck", string(domain.TaskProcessing), string(domain.VideoFailed), string(domain.SubtitleASSReady), "", true},
		{"cancelled safe video failed", string(domain.TaskCancelled), string(domain.VideoFailed), string(domain.SubtitleASSReady), "", true},
		{"cancelled safe subtitle failed", string(domain.TaskCancelled), string(domain.VideoReady), string(domain.SubtitleFailed), "", true},
		{"cancelled safe both failed", string(domain.TaskCancelled), string(domain.VideoFailed), string(domain.SubtitleFailed), "", true},
		{"cancelled ordinary both cancelled", string(domain.TaskCancelled), string(domain.VideoCancelled), string(domain.SubtitleCancelled), "", false},
		{"cancelled with active branch", string(domain.TaskCancelled), string(domain.VideoTranscoding), string(domain.SubtitleASSReady), "", false},
		{"cancelled both ready", string(domain.TaskCancelled), string(domain.VideoReady), string(domain.SubtitleASSReady), "", false},
		{"processing both ready not retry", string(domain.TaskProcessing), string(domain.VideoReady), string(domain.SubtitleASSReady), "", false},
		{"failed no stage no branch not retry", string(domain.TaskFailed), string(domain.VideoReady), string(domain.SubtitleASSReady), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, taskID := createTaskWithMediaStates(t, fixture, tc.state, tc.videoState, tc.subtitleState, tc.failureStage)
			task, err := taskWorkflow.GetTask(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Actions.CanRetry != tc.wantCanRetry {
				t.Fatalf("GetTask CanRetry = %v, want %v for case %q task=%#v", task.Actions.CanRetry, tc.wantCanRetry, tc.name, task)
			}
			// Acquisition 摘要同样使用共享判定
			acquisitionID := task.AcquisitionID
			acquisition, err := readService.GetAcquisition(ctx, acquisitionID)
			if err != nil {
				t.Fatalf("GetAcquisition error = %v", err)
			}
			if len(acquisition.Tasks) != 1 {
				t.Fatalf("tasks length = %d, want 1", len(acquisition.Tasks))
			}
			if acquisition.Tasks[0].CanRetry != tc.wantCanRetry {
				t.Fatalf("Acquisition summary CanRetry = %v, want %v for case %q summary=%#v", acquisition.Tasks[0].CanRetry, tc.wantCanRetry, tc.name, acquisition.Tasks[0])
			}
		})
	}
}
