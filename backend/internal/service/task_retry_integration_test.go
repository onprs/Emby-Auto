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
	// secondary exists with derived key
	derived := key + ":branch:" + appqueue.KindTranscodeRun
	var secondaryCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1 AND kind = $2`, derived, appqueue.KindTranscodeRun).Scan(&secondaryCount); err != nil {
		t.Fatal(err)
	}
	if secondaryCount != 1 {
		t.Fatalf("secondary transcode operation not found for derived key %q", derived)
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
	derived := key + ":branch:" + appqueue.KindSubtitlePrepare
	var secondaryCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1 AND kind = $2`, derived, appqueue.KindSubtitlePrepare).Scan(&secondaryCount); err != nil {
		t.Fatal(err)
	}
	if secondaryCount != 1 {
		t.Fatalf("secondary subtitle not found")
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
	derivedKey := primaryKey + ":branch:" + appqueue.KindTranscodeRun
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
	// create a task that is already processing with no failed branch, retry should map to state_conflict not 500
	_, _, _, taskID := createTaskWithMediaStates(t, fixture, string(domain.TaskFailed), string(domain.VideoReady), string(domain.SubtitleASSReady), "")
	// failure_stage empty, no retryable branch, should be invalid_state/state_conflict
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	_, _, err := workflow.Retry(ctx, taskID, 1, "retry-no-branch-"+taskID.String(), fixture.actorID)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected state_conflict for no branch, got %v", err)
	}
	var serviceErr *Error
	if !errors.As(err, &serviceErr) {
		t.Fatalf("expected service.Error, got %T", err)
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
