//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
)

func TestImportedTaskRetriesCleanupWithoutRollingBackImportIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acquisitionID := fixture.createAcquisition(t, `{"sourceSeason":1}`)
	downloadID, fileID, taskID, cleanupID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'materialized')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Series.S01E01.mkv', 1024, 'video', true, 1, 1)
`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, 'imported', 'video_ready', 'ass_ready')
`, taskID, acquisitionID, fileID, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO cleanup_runs (id, task_id, download_id, status, error_code, error_message, completed_at)
VALUES ($1, $2, $3, 'failed', 'cleanup_failed', 'fixture cleanup failure', now())
`, cleanupID, taskID, downloadID); err != nil {
		t.Fatal(err)
	}
	queries := db.New(fixture.pool)
	taskWorkflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	workflow := NewTaskCommandWorkflow(queries, fixture.transactor, fixture.scheduler, taskWorkflow)
	key := "cleanup-retry-" + taskID.String()
	task, operation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if task.State != "imported" || task.Version != 2 || task.Cleanup == nil || task.Cleanup.Status != "queued" || operation.Kind != appqueue.KindCleanupRun {
		t.Fatalf("cleanup retry = task %#v, operation %#v", task, operation)
	}
	replayed, replayOperation, err := workflow.Retry(ctx, taskID, 1, key, fixture.actorID)
	if err != nil || replayOperation.ID != operation.ID || replayed.State != "imported" || replayed.Version != 2 {
		t.Fatalf("replayed cleanup Retry() = %#v / %#v / %v", replayed, replayOperation, err)
	}
}
