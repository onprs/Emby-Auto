//go:build integration

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
)

func createTaskWithMediaStates(t *testing.T, fixture recoveryFixture, state, videoState, subtitleState, failureStage string) (acquisitionID, downloadID, fileID, taskID uuid.UUID) {
	t.Helper()
	acquisitionID = fixture.createAcquisition(t, `{"sourceSeason":1}`)
	downloadID = uuid.New()
	fileID = uuid.New()
	taskID = uuid.New()
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'materialized')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Series.S01E01.mkv', 1024, 'video', true, 1, 1)
`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	var failureStageValue any
	if failureStage != "" {
		failureStageValue = failureStage
	}
	var errorCode any
	var errorMessage any
	if state == string(domain.TaskFailed) {
		errorCode = "fixture_failure"
		errorMessage = "fixture failure"
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state, failure_stage, error_code, error_message)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
`, taskID, acquisitionID, fileID, fixture.profileID, state, videoState, subtitleState, failureStageValue, errorCode, errorMessage); err != nil {
		t.Fatal(err)
	}
	return acquisitionID, downloadID, fileID, taskID
}

func countOperationsForTask(t *testing.T, fixture recoveryFixture, taskID uuid.UUID) int {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE resource_id = $1 AND resource_type = 'episode_task'`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countEventsForTask(t *testing.T, fixture recoveryFixture, taskID uuid.UUID) int {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE resource_type = 'episode_task' AND resource_id = $1`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestTaskRetrySingleVideoFailedOnlyIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleASSReady), "video")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)

	key := "retry-video-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.State != domain.TaskProcessing || task.VideoState != domain.VideoTranscodeQueued || task.SubtitleState != domain.SubtitleASSReady || task.Version != 2 || task.FailureStage != "" {
		t.Fatalf("video single retry task = %#v", task)
	}
	if operation.Kind != appqueue.KindTranscodeRun || operation.MaxAttempts != 3 {
		t.Fatalf("video operation = %#v", operation)
	}
	if operation.IdempotencyKey != key {
		t.Fatalf("operation key = %q, want %q", operation.IdempotencyKey, key)
	}
	ops := countOperationsForTask(t, fixture, taskID)
	if ops != 1 {
		t.Fatalf("operations = %d, want 1", ops)
	}
	replayed, replayOp, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil || replayOp.ID != operation.ID || replayed.Version != 2 {
		t.Fatalf("replay = %#v / %#v / %v", replayed, replayOp, err)
	}
	if countOperationsForTask(t, fixture, taskID) != 1 {
		t.Fatalf("replay created extra operations")
	}
}

func TestTaskRetrySingleSubtitleFailedOnlyIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoReady), string(domain.SubtitleFailed), "subtitle")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key := "retry-subtitle-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.State != domain.TaskProcessing || task.VideoState != domain.VideoReady || task.SubtitleState != domain.SubtitleQueued || task.Version != 2 {
		t.Fatalf("subtitle single retry task = %#v", task)
	}
	if operation.Kind != appqueue.KindSubtitlePrepare {
		t.Fatalf("subtitle operation kind = %q", operation.Kind)
	}
	if countOperationsForTask(t, fixture, taskID) != 1 {
		t.Fatalf("operations want 1")
	}
	// finalize should not be scheduled
	var finalizeOps int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_id = $1 AND kind = $2`, taskID, appqueue.KindMediaFinalize).Scan(&finalizeOps); err != nil {
		t.Fatal(err)
	}
	if finalizeOps != 0 {
		t.Fatalf("unexpected finalize ops %d", finalizeOps)
	}
}

func TestTaskRetryBothFailedPrimarySubtitleIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleFailed), "subtitle")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key := "retry-both-subtitle-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.State != domain.TaskProcessing || task.VideoState != domain.VideoTranscodeQueued || task.SubtitleState != domain.SubtitleQueued || task.Version != 2 {
		t.Fatalf("both subtitle primary task = %#v", task)
	}
	if operation.Kind != appqueue.KindSubtitlePrepare {
		t.Fatalf("primary should be subtitle, got %q", operation.Kind)
	}
	if operation.IdempotencyKey != key {
		t.Fatalf("primary key mismatch %q", operation.IdempotencyKey)
	}
	// secondary exists with bounded derived key
	derived := deriveSecondaryIdempotencyKey(key, appqueue.KindTranscodeRun, taskID)
	var secondaryCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1 AND kind = $2`, derived, appqueue.KindTranscodeRun).Scan(&secondaryCount); err != nil {
		t.Fatal(err)
	}
	if secondaryCount != 1 {
		t.Fatalf("secondary transcode operation not found for derived key %q", derived)
	}
	// 明确格式断言：不依赖函数自身生成的预期，使用独立计算验证有界命名空间
	digest := sha256.Sum256([]byte(key))
	expectedHex := hex.EncodeToString(digest[:])
	expectedDerived := "task-retry:" + taskID.String() + ":" + appqueue.KindTranscodeRun + ":" + expectedHex
	if derived != expectedDerived {
		t.Fatalf("derived key format mismatch: got %q want %q", derived, expectedDerived)
	}
	if len(derived) > 256 || strings.Contains(derived, key) && len(key) > 10 {
		t.Fatalf("derived key must be bounded and not contain raw primary, got %q", derived)
	}
	if countOperationsForTask(t, fixture, taskID) != 2 {
		t.Fatalf("operations want 2")
	}
	// replay with same key must not increase version/ops/events
	opsBefore := countOperationsForTask(t, fixture, taskID)
	eventsBefore := countEventsForTask(t, fixture, taskID)
	replayed, replayOp, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil || replayOp.ID != operation.ID || replayed.Version != 2 {
		t.Fatalf("replay both = %#v / %#v / %v", replayed, replayOp, err)
	}
	if countOperationsForTask(t, fixture, taskID) != opsBefore || countEventsForTask(t, fixture, taskID) != eventsBefore {
		t.Fatalf("replay changed counts")
	}
}

func TestTaskRetryBothFailedPrimaryVideoIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleFailed), "video")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key := "retry-both-video-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.VideoState != domain.VideoTranscodeQueued || task.SubtitleState != domain.SubtitleQueued {
		t.Fatalf("both video primary task = %#v", task)
	}
	if operation.Kind != appqueue.KindTranscodeRun {
		t.Fatalf("primary should be video, got %q", operation.Kind)
	}
	derived := deriveSecondaryIdempotencyKey(key, appqueue.KindSubtitlePrepare, taskID)
	var secondaryCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1 AND kind = $2`, derived, appqueue.KindSubtitlePrepare).Scan(&secondaryCount); err != nil {
		t.Fatal(err)
	}
	if secondaryCount != 1 {
		t.Fatalf("secondary subtitle not found")
	}
	digest := sha256.Sum256([]byte(key))
	expectedHex := hex.EncodeToString(digest[:])
	expectedDerived := "task-retry:" + taskID.String() + ":" + appqueue.KindSubtitlePrepare + ":" + expectedHex
	if derived != expectedDerived {
		t.Fatalf("derived key format mismatch: got %q want %q", derived, expectedDerived)
	}
}

func TestTaskRetryProcessingStuckVideoIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskProcessing), string(domain.VideoFailed), string(domain.SubtitleASSReady), "")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)

	// verify CanRetry true via read model
	taskBefore, err := taskWorkflow.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !taskBefore.Actions.CanRetry {
		t.Fatalf("processing stuck should be CanRetry true, got %#v", taskBefore.Actions)
	}
	if taskBefore.VideoState != domain.VideoFailed {
		t.Fatalf("setup videoState wrong")
	}
	key := "retry-processing-video-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.State != domain.TaskProcessing || task.VideoState != domain.VideoTranscodeQueued || task.SubtitleState != domain.SubtitleASSReady || task.Version != 2 {
		t.Fatalf("processing retry task = %#v", task)
	}
	if operation.Kind != appqueue.KindTranscodeRun {
		t.Fatalf("processing retry operation kind = %q", operation.Kind)
	}
	// verify that subtitle branch not touched
	var ops int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_id = $1 AND kind = $2`, taskID, appqueue.KindSubtitlePrepare).Scan(&ops); err != nil {
		t.Fatal(err)
	}
	if ops != 0 {
		t.Fatalf("should not have subtitle operation, got %d", ops)
	}
}

func TestTaskRetryProcessingNoFailedRejectsIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskProcessing), string(domain.VideoReady), string(domain.SubtitleASSReady), "")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	taskBefore, _ := taskWorkflow.GetTask(ctx, taskID)
	if taskBefore.Actions.CanRetry {
		t.Fatalf("processing without failed branch should not be CanRetry")
	}
	_, _, err := workflow.Retry(ctx, taskID, 1, "retry-processing-no-failed-"+taskID.String(), fixture.actorID)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected state_conflict, got %v", err)
	}
	if countOperationsForTask(t, fixture, taskID) != 0 {
		t.Fatalf("should not create operations on reject")
	}
	var version int32
	if err := fixture.pool.QueryRow(ctx, `SELECT version FROM episode_tasks WHERE id = $1`, taskID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version should stay 1, got %d", version)
	}
}

func TestTaskRetryFinalizeRegressionIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoReady), string(domain.SubtitleASSReady), "finalize")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key := "retry-finalize-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry finalize error = %v", err)
	}
	if task.State != domain.TaskFinalizing {
		t.Fatalf("finalize retry state = %q", task.State)
	}
	if operation.Kind != appqueue.KindMediaFinalize {
		t.Fatalf("finalize operation kind = %q", operation.Kind)
	}
}

func TestTaskRetryImportRegressionIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acquisitionID, downloadID, fileID, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoReady), string(domain.SubtitleASSReady), "import")
	// need failed import record
	importID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO imports (id, task_id, attempt, status, error_code, error_message) VALUES ($1, $2, 1, 'failed', 'import_failed', 'fixture')`, importID, taskID); err != nil {
		t.Fatal(err)
	}
	// also need artifact_set to satisfy foreign key? RequeueTaskImportBranch checks exists import failed but doesn't need artifact? But GetTaskView maybe needs artifact? But retry should work without artifact_set? It checks task state and existence of failed import, not artifact. So fine.
	_ = acquisitionID
	_ = downloadID
	_ = fileID
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key := "retry-import-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry import error = %v", err)
	}
	if task.State != domain.TaskImportQueued {
		t.Fatalf("import retry state = %q", task.State)
	}
	if operation.Kind != appqueue.KindEmbyImport {
		t.Fatalf("import operation kind = %q", operation.Kind)
	}
	var payload map[string]any
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["importId"] == nil {
		t.Fatalf("import payload missing importId")
	}
}

func TestTaskRetryVersionConflictNoSideEffectIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleASSReady), "video")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	wrongKey := "retry-version-conflict-" + taskID.String()
	_, _, err := workflow.Retry(ctx, taskID, 99, wrongKey, fixture.actorID)
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
	if countOperationsForTask(t, fixture, taskID) != 0 {
		t.Fatalf("operation leaked on version conflict")
	}
	if countEventsForTask(t, fixture, taskID) != 0 {
		t.Fatalf("event leaked on version conflict")
	}
}

func TestTaskRetryDerivedKeyConflictRollbackIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleFailed), "subtitle")
	// pre-create an operation that will collide with derived secondary key but with different kind/resource to cause conflict
	primaryKey := "retry-derived-conflict-" + taskID.String()
	derivedKey := deriveSecondaryIdempotencyKey(primaryKey, appqueue.KindTranscodeRun, taskID)
	// 额外验证新派生 key 的有界格式：包含 taskID、kind、hex digest，且不包含完整 primary，且长度远低于 256
	digest := sha256.Sum256([]byte(primaryKey))
	expectedHex := hex.EncodeToString(digest[:])
	expectedDerived := "task-retry:" + taskID.String() + ":" + appqueue.KindTranscodeRun + ":" + expectedHex
	if derivedKey != expectedDerived {
		t.Fatalf("derived key must use bounded namespace, got %q want %q", derivedKey, expectedDerived)
	}
	if len(derivedKey) >= 256 {
		t.Fatalf("derived key must be well below 256, got length %d", len(derivedKey))
	}
	if strings.Contains(derivedKey, primaryKey) {
		t.Fatalf("derived key must not contain raw primary, got %q", derivedKey)
	}
	// create conflicting operation with same derivedKey but different kind (e.g., cleanup) and same resource type but different kind
	conflictID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, payload)
VALUES ($1, $2, 'episode_task', $3, $4, 'queued', 3, 60, '{}')
`, conflictID, appqueue.KindCleanupRun, taskID, derivedKey); err != nil {
		t.Fatal(err)
	}
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	_, _, err := workflow.Retry(ctx, taskID, 1, primaryKey, fixture.actorID)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	// verify no partial side effects: task version unchanged, no new operations beyond the pre-created one, no state change
	var version int32
	var state, videoState, subtitleState string
	if err := fixture.pool.QueryRow(ctx, `SELECT version, state, video_state, subtitle_state FROM episode_tasks WHERE id = $1`, taskID).Scan(&version, &state, &videoState, &subtitleState); err != nil {
		t.Fatal(err)
	}
	if version != 1 || state != string(domain.TaskFailed) || videoState != string(domain.VideoFailed) || subtitleState != string(domain.SubtitleFailed) {
		t.Fatalf("partial side effect leaked: version %d state %q video %q subtitle %q", version, state, videoState, subtitleState)
	}
	// operations count should still be 1 (the conflicting one), not 2 or 3
	if countOperationsForTask(t, fixture, taskID) != 1 {
		t.Fatalf("expected only conflicting operation, got %d", countOperationsForTask(t, fixture, taskID))
	}
	// ensure no new operation with primaryKey was created
	var primaryExists int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1`, primaryKey).Scan(&primaryExists); err != nil {
		t.Fatal(err)
	}
	if primaryExists != 0 {
		t.Fatalf("primary operation leaked despite rollback")
	}
}

func TestTaskRetryNoRowsMapsToStateConflictIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// 合法约束：state=failed 必须有 failure_stage，使用 finalize 但让 video/subtitle 不满足 ready 守卫，触发 RequeueTaskFinalizeBranch 的 no-row 守卫
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoTranscodeQueued), string(domain.SubtitleQueued), "finalize")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key := "retry-no-branch-" + taskID.String()
	_, _, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected state_conflict for no branch, got %v", err)
	}
	var serviceErr *Error
	if !errors.As(err, &serviceErr) {
		t.Fatalf("expected service.Error, got %T", err)
	}
	// 断言无副作用：task version/state/operation/event 均未变更
	var version int32
	var state, videoState, subtitleState string
	var failureStage *string
	if err := fixture.pool.QueryRow(ctx, `SELECT version, state, video_state, subtitle_state, failure_stage FROM episode_tasks WHERE id = $1`, taskID).Scan(&version, &state, &videoState, &subtitleState, &failureStage); err != nil {
		t.Fatal(err)
	}
	if version != 1 || state != string(domain.TaskFailed) || videoState != string(domain.VideoTranscodeQueued) || subtitleState != string(domain.SubtitleQueued) || failureStage == nil || *failureStage != "finalize" {
		t.Fatalf("side effect leaked: version %d state %q video %q subtitle %q failureStage %v", version, state, videoState, subtitleState, failureStage)
	}
	if countOperationsForTask(t, fixture, taskID) != 0 {
		t.Fatalf("should not create operations on finalize no-row")
	}
	if countEventsForTask(t, fixture, taskID) != 0 {
		t.Fatalf("should not create events on finalize no-row")
	}
}

// Verify GetTask CanRetry for stuck processing returns true and frontend would show.
func TestTaskStuckProcessingCanRetryReadModelIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskProcessing), string(domain.VideoFailed), string(domain.SubtitleASSReady), "")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, nil)
	task, err := taskWorkflow.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !task.Actions.CanRetry {
		t.Fatalf("stuck processing should be canRetry")
	}
	// also check that taskFailureInfo equivalent logic would pick video
	if task.VideoState != domain.VideoFailed {
		t.Fatalf("videoState not failed")
	}
}

func TestTaskRetryBothFailedIdempotentDifferentKeyIsNewCommandIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleFailed), "video")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key1 := "retry-both-newcmd-1-" + taskID.String()
	key2 := "retry-both-newcmd-2-" + taskID.String()
	_, op1, err := workflow.Retry(ctx, taskID, 1, key1, fixture.actorID)
	if err != nil {
		t.Fatalf("first retry error = %v", err)
	}
	if op1.Kind != appqueue.KindTranscodeRun {
		t.Fatalf("first primary kind %q", op1.Kind)
	}
	// second retry with different key should fail because task is now processing not failed (version is now 2, but we pass expectedVersion 2)
	_, _, err = workflow.Retry(ctx, taskID, 2, key2, fixture.actorID)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("second retry with different key on now-processing task should be state_conflict, got %v", err)
	}
	// first key replay should still return original operation even though version is now 2
	replayed, replayOp, err := workflow.Retry(ctx, taskID, 1, key1, fixture.actorID)
	if err != nil || replayOp.ID != op1.ID || replayed.Version != 2 {
		t.Fatalf("replay after processing should return original op, got %#v / %v / %v", replayed, replayOp, err)
	}
}

func TestTaskRetryIdempotencyKeyBoundariesIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleASSReady), "video")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	// 256 允许：精确构造 256 字符的 idempotency key
	key256 := strings.Repeat("a", 256)
	_, _, err := workflow.Retry(ctx, taskID, 1, key256, fixture.actorID)
	if err != nil {
		t.Fatalf("256-char key should be allowed, got %v", err)
	}
	// 验证已产生 operation 且 key 被 trim 后存储
	var storedKey string
	if err := fixture.pool.QueryRow(ctx, `SELECT idempotency_key FROM operations WHERE resource_id = $1 AND kind = $2 ORDER BY created_at LIMIT 1`, taskID, appqueue.KindTranscodeRun).Scan(&storedKey); err != nil {
		t.Fatal(err)
	}
	if storedKey != key256 {
		t.Fatalf("stored key mismatch, got %q want %q", storedKey, key256)
	}
}

func TestTaskRetryIdempotencyKeyTooLongRejectedIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleASSReady), "video")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key257 := strings.Repeat("a", 257)
	_, _, err := workflow.Retry(ctx, taskID, 1, key257, fixture.actorID)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("257-char key should be ErrInvalidInput, got %v", err)
	}
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Details["field"] != "idempotencyKey" {
		t.Fatalf("expected invalid_task_command field idempotencyKey, got %v", err)
	}
	// 无副作用：version 未变，无 operation/event
	var version int32
	if err := fixture.pool.QueryRow(ctx, `SELECT version FROM episode_tasks WHERE id = $1`, taskID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version should stay 1 on invalid input, got %d", version)
	}
	if countOperationsForTask(t, fixture, taskID) != 0 {
		t.Fatalf("operation leaked on invalid input")
	}
	if countEventsForTask(t, fixture, taskID) != 0 {
		t.Fatalf("event leaked on invalid input")
	}
}

func TestTaskRetryDerivedKeyPropertiesIntegration(t *testing.T) {
	// 验证派生 key 的有界、稳定、域隔离，且不依赖截断碰撞
	taskID1 := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	taskID2 := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	primary := "primary-key-" + strings.Repeat("x", 100)
	kindVideo := appqueue.KindTranscodeRun
	kindSubtitle := appqueue.KindSubtitlePrepare
	key1 := deriveSecondaryIdempotencyKey(primary, kindVideo, taskID1)
	key1Again := deriveSecondaryIdempotencyKey(primary, kindVideo, taskID1)
	keyDifferentTask := deriveSecondaryIdempotencyKey(primary, kindVideo, taskID2)
	keyDifferentKind := deriveSecondaryIdempotencyKey(primary, kindSubtitle, taskID1)
	keyDifferentPrimary := deriveSecondaryIdempotencyKey(primary+"diff", kindVideo, taskID1)
	// 稳定性
	if key1 != key1Again {
		t.Fatalf("derived key must be stable, got %q vs %q", key1, key1Again)
	}
	// 域隔离：同 primary 不同 task/kind 生成不同 key
	if key1 == keyDifferentTask {
		t.Fatalf("different task should produce different derived key")
	}
	if key1 == keyDifferentKind {
		t.Fatalf("different kind should produce different derived key")
	}
	if key1 == keyDifferentPrimary {
		t.Fatalf("different primary should produce different digest")
	}
	// 有界且不包含完整 primary
	if len(key1) >= 256 {
		t.Fatalf("derived key must be well below 256, got %d", len(key1))
	}
	if strings.Contains(key1, primary) {
		t.Fatalf("derived key must not contain raw primary")
	}
	// 固定前缀 + task UUID + kind + lowercase hex digest 的明确字符串格式断言（独立计算预期，不调用函数）
	digest := sha256.Sum256([]byte(primary))
	expectedHex := hex.EncodeToString(digest[:])
	expected := "task-retry:" + taskID1.String() + ":" + kindVideo + ":" + expectedHex
	if key1 != expected {
		t.Fatalf("derived key format mismatch: got %q want %q", key1, expected)
	}
	// hex 必须为小写
	if strings.ToLower(expectedHex) != expectedHex {
		t.Fatalf("hex digest must be lowercase")
	}
	if len(expectedHex) != 64 {
		t.Fatalf("hex digest must be 64 chars")
	}
}

func TestTaskRetryDualBranchAuditEventIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleFailed), "video")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key := "retry-audit-dual-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.Version != 2 {
		t.Fatalf("version want 2 got %d", task.Version)
	}
	// 解析唯一的 task.retry_requested 事件，断言包含兼容 failureStage、确定顺序的 failureStages 与分支 operation 关联
	var eventData json.RawMessage
	var operationID uuid.UUID
	var topic string
	if err := fixture.pool.QueryRow(ctx, `SELECT topic, operation_id, data FROM events WHERE resource_type = 'episode_task' AND resource_id = $1 AND topic = 'task.retry_requested' ORDER BY occurred_at`, taskID).Scan(&topic, &operationID, &eventData); err != nil {
		t.Fatalf("failed to load task retry event: %v", err)
	}
	if topic != "task.retry_requested" {
		t.Fatalf("topic mismatch %q", topic)
	}
	if operationID != operation.ID {
		t.Fatalf("event should link primary operation, got %v want %v", operationID, operation.ID)
	}
	var data map[string]any
	if err := json.Unmarshal(eventData, &data); err != nil {
		t.Fatalf("unmarshal event data: %v", err)
	}
	// 兼容字段
	if data["failureStage"] != "video" {
		t.Fatalf("failureStage want video, got %v", data["failureStage"])
	}
	// 确定顺序的全部 failureStages
	stagesRaw, ok := data["failureStages"]
	if !ok {
		t.Fatalf("missing failureStages")
	}
	stages, ok := stagesRaw.([]any)
	if !ok || len(stages) != 2 {
		t.Fatalf("failureStages must be array of 2, got %v", stagesRaw)
	}
	sorted := []string{stages[0].(string), stages[1].(string)}
	sort.Strings(sorted)
	// 期望为有序的 [subtitle, video]
	if sorted[0] != "subtitle" || sorted[1] != "video" {
		t.Fatalf("failureStages unexpected sorted %v", sorted)
	}
	// 校验 stages 本身已是确定顺序（实现排序后相等）
	if stages[0].(string) != sorted[0] || stages[1].(string) != sorted[1] {
		t.Fatalf("failureStages must be deterministically sorted, got %v want %v", stages, sorted)
	}
	// 分支 operation 关联：不含敏感值，仅含 kind/operationId
	branchesRaw, ok := data["branches"]
	if !ok {
		t.Fatalf("missing branches")
	}
	branches, ok := branchesRaw.([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("branches must be array of 2, got %v", branchesRaw)
	}
	// 收集每个分支的 kind 与 operationId，验证与实际 operation 对应且无敏感 key
	branchMap := map[string]string{}
	for _, b := range branches {
		m, ok := b.(map[string]any)
		if !ok {
			t.Fatalf("branch not object %v", b)
		}
		stage, _ := m["stage"].(string)
		kind, _ := m["kind"].(string)
		opID, _ := m["operationId"].(string)
		if stage == "" || kind == "" || opID == "" {
			t.Fatalf("branch missing fields %v", m)
		}
		// 不得包含敏感值
		if _, hasKey := m["idempotencyKey"]; hasKey {
			t.Fatalf("branch must not contain idempotencyKey")
		}
		if _, hasDigest := m["digest"]; hasDigest {
			t.Fatalf("branch must not contain digest")
		}
		branchMap[stage] = kind + ":" + opID
		if kind != appqueue.KindTranscodeRun && kind != appqueue.KindSubtitlePrepare {
			t.Fatalf("unexpected branch kind %q", kind)
		}
		if _, err := uuid.Parse(opID); err != nil {
			t.Fatalf("invalid operationId %q", opID)
		}
		// 确保 operationId 对应真实 operation
		var exists int
		if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id = $1 AND kind = $2`, uuid.MustParse(opID), kind).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 1 {
			t.Fatalf("branch operation not found for %v", m)
		}
	}
	// 主 operation 必须在分支中
	foundPrimary := false
	for _, b := range branches {
		m := b.(map[string]any)
		if m["operationId"].(string) == operation.ID.String() {
			foundPrimary = true
		}
	}
	if !foundPrimary {
		t.Fatalf("primary operation must be in branches")
	}
	// 同 key 重放不应增加事件
	eventsBefore := countEventsForTask(t, fixture, taskID)
	_, replayOp, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil || replayOp.ID != operation.ID {
		t.Fatalf("replay failed %v / %v", replayOp, err)
	}
	if countEventsForTask(t, fixture, taskID) != eventsBefore {
		t.Fatalf("replay should not increase events")
	}
	// 验证无重复 command 级事件：仍只有 1 个 task.retry_requested
	var retryEventCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE resource_type='episode_task' AND resource_id=$1 AND topic='task.retry_requested'`, taskID).Scan(&retryEventCount); err != nil {
		t.Fatal(err)
	}
	if retryEventCount != 1 {
		t.Fatalf("should have exactly 1 retry event, got %d", retryEventCount)
	}
}

func TestTaskRetrySingleBranchAuditEventIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleASSReady), "video")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key := "retry-audit-single-" + taskID.String()
	_, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	var data json.RawMessage
	if err := fixture.pool.QueryRow(ctx, `SELECT data FROM events WHERE resource_type='episode_task' AND resource_id=$1 AND topic='task.retry_requested'`, taskID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["failureStage"] != "video" {
		t.Fatalf("single failureStage want video, got %v", parsed["failureStage"])
	}
	stages, ok := parsed["failureStages"].([]any)
	if !ok || len(stages) != 1 || stages[0].(string) != "video" {
		t.Fatalf("single failureStages want [video], got %v", parsed["failureStages"])
	}
	branches, ok := parsed["branches"].([]any)
	if !ok || len(branches) != 1 {
		t.Fatalf("single branches want 1, got %v", parsed["branches"])
	}
	branch := branches[0].(map[string]any)
	if branch["stage"] != "video" || branch["kind"] != appqueue.KindTranscodeRun || branch["operationId"] != operation.ID.String() {
		t.Fatalf("single branch mismatch %v", branch)
	}
}

func TestTaskRetryInvalidUtf8AndTrimIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleASSReady), "video")
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	// 无效 UTF-8
	invalid := string([]byte{0xff, 0xfe, 0xfd})
	_, _, err := workflow.Retry(ctx, taskID, 1, invalid, fixture.actorID)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid utf8 should be ErrInvalidInput, got %v", err)
	}
	// 空白 trim 后为空
	_, _, err = workflow.Retry(ctx, taskID, 1, "   ", fixture.actorID)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank key should be ErrInvalidInput, got %v", err)
	}
	// 前后空白应被 trim 后接受（使用另一任务避免状态污染）
	_, _, _, taskID2 := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoFailed), string(domain.SubtitleASSReady), "video")
	keyWithSpaces := "  retry-trim-" + taskID2.String() + "  "
	_, _, err = workflow.Retry(ctx, taskID2, 1, keyWithSpaces, fixture.actorID)
	if err != nil {
		t.Fatalf("trimmed key should be accepted, got %v", err)
	}
	var stored string
	if err := fixture.pool.QueryRow(ctx, `SELECT idempotency_key FROM operations WHERE resource_id = $1`, taskID2).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != strings.TrimSpace(keyWithSpaces) {
		t.Fatalf("stored key should be trimmed, got %q", stored)
	}
	// 无副作用验证：第一个任务仍无 operation
	if countOperationsForTask(t, fixture, taskID) != 0 {
		t.Fatalf("invalid input should not create operations")
	}
}
