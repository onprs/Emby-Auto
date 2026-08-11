//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type failingReviewJobInserter struct{}

func (*failingReviewJobInserter) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, errors.New("fixture River insert failed")
}

func TestApprovedReviewQueuesImportAtomicallyIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createReviewableTask(t, fixture)
	workflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	input := domain.ReviewTask{
		TaskID: taskID, ExpectedVersion: 1, Decision: domain.TaskApproved, Notes: "video and subtitle checked",
		IdempotencyKey: "approved-review", ActorUserID: fixture.actorID,
	}

	task, err := workflow.ReviewTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ReviewTask(approved) error = %v", err)
	}
	if task.State != domain.TaskImportQueued || task.Version != 3 || task.Review == nil || task.Review.Decision != domain.TaskApproved || task.Import == nil || task.Import.Status != "queued" {
		t.Fatalf("approved task = %#v", task)
	}
	if task.Actions.CanImport || len(task.Operations) != 1 || task.Operations[0].Kind != "emby.import" || task.Operations[0].Status != "queued" {
		t.Fatalf("approved task actions/operations = %#v / %#v", task.Actions, task.Operations)
	}
	var acquisitionID uuid.UUID
	if err := fixture.pool.QueryRow(context.Background(), `SELECT acquisition_id FROM episode_tasks WHERE id = $1`, taskID).Scan(&acquisitionID); err != nil {
		t.Fatal(err)
	}
	acquisition, err := NewReadService(db.New(fixture.pool)).GetAcquisition(context.Background(), acquisitionID)
	if err != nil {
		t.Fatalf("GetAcquisition(approved) error = %v", err)
	}
	if len(acquisition.Stages) != 9 || len(acquisition.Tasks) != 1 || acquisition.Tasks[0].State != "import_queued" || acquisition.Tasks[0].ReviewDecision != "approved" || acquisition.Tasks[0].ImportStatus != "queued" {
		t.Fatalf("approved acquisition = %#v", acquisition)
	}

	replayed, err := workflow.ReviewTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ReviewTask(replay) error = %v", err)
	}
	if replayed.State != domain.TaskImportQueued || replayed.Import == nil || replayed.Import.ID != task.Import.ID {
		t.Fatalf("replayed task = %#v", replayed)
	}
	var reviewCount, importCount, operationCount, reviewedEvents, queuedEvents int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT
    (SELECT count(*) FROM reviews WHERE task_id = $1),
    (SELECT count(*) FROM imports WHERE task_id = $1),
    (SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'emby.import'),
    (SELECT count(*) FROM events WHERE resource_type = 'episode_task' AND resource_id = $1 AND topic = 'task.reviewed'),
    (SELECT count(*) FROM events WHERE resource_type = 'episode_task' AND resource_id = $1 AND topic = 'task.import_queued')
`, taskID).Scan(&reviewCount, &importCount, &operationCount, &reviewedEvents, &queuedEvents); err != nil {
		t.Fatal(err)
	}
	if reviewCount != 1 || importCount != 1 || operationCount != 1 || reviewedEvents != 1 || queuedEvents != 1 {
		t.Fatalf("review/import/operation/events = %d/%d/%d/%d/%d, want 1/1/1/1/1", reviewCount, importCount, operationCount, reviewedEvents, queuedEvents)
	}
}

func TestRejectedReviewDoesNotQueueImportIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createReviewableTask(t, fixture)
	workflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)

	task, err := workflow.ReviewTask(context.Background(), domain.ReviewTask{
		TaskID: taskID, ExpectedVersion: 1, Decision: domain.TaskRejected, Notes: "subtitle timing is wrong",
		IdempotencyKey: "rejected-review", ActorUserID: fixture.actorID,
	})
	if err != nil {
		t.Fatalf("ReviewTask(rejected) error = %v", err)
	}
	if task.State != domain.TaskRejected || task.Review == nil || task.Review.Decision != domain.TaskRejected || task.Import != nil {
		t.Fatalf("rejected task = %#v", task)
	}
	var imports, operations int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT
    (SELECT count(*) FROM imports WHERE task_id = $1),
    (SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'emby.import')
`, taskID).Scan(&imports, &operations); err != nil {
		t.Fatal(err)
	}
	if imports != 0 || operations != 0 {
		t.Fatalf("rejected import/operation counts = %d/%d, want 0/0", imports, operations)
	}
}

func TestApprovedReviewRollsBackWhenImportJobCannotBeCreatedIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createReviewableTask(t, fixture)
	scheduler := NewOperationScheduler(fixture.transactor, &failingReviewJobInserter{})
	workflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, scheduler)

	_, err := workflow.ReviewTask(context.Background(), domain.ReviewTask{
		TaskID: taskID, ExpectedVersion: 1, Decision: domain.TaskApproved,
		IdempotencyKey: "rollback-review", ActorUserID: fixture.actorID,
	})
	if err == nil {
		t.Fatal("ReviewTask(scheduler failure) error = nil")
	}
	var state string
	var version, reviews, imports, operations int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT
    task.state,
    task.version,
    (SELECT count(*) FROM reviews WHERE task_id = task.id),
    (SELECT count(*) FROM imports WHERE task_id = task.id),
    (SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = task.id AND kind = 'emby.import')
FROM episode_tasks AS task
WHERE task.id = $1
`, taskID).Scan(&state, &version, &reviews, &imports, &operations); err != nil {
		t.Fatal(err)
	}
	if state != "awaiting_review" || version != 1 || reviews != 0 || imports != 0 || operations != 0 {
		t.Fatalf("rolled back task = %s/v%d reviews=%d imports=%d operations=%d", state, version, reviews, imports, operations)
	}
}

func createReviewableTask(t *testing.T, fixture recoveryFixture) uuid.UUID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acquisitionID := fixture.createAcquisition(t, `{}`)
	downloadID, sourceFileID, taskID := uuid.New(), uuid.New(), uuid.New()
	videoArtifactID, subtitleArtifactID := uuid.New(), uuid.New()
	batch := &pgx.Batch{}
	batch.Queue(`
INSERT INTO downloads (id, acquisition_id, status, progress, save_path)
VALUES ($1, $2, 'materialized', 1, '/downloads/review-fixture')
`, downloadID, acquisitionID)
	batch.Queue(`
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Review.Show.S01E01.mkv', 1024, 'video', true, 1, 1)
`, sourceFileID, downloadID)
	batch.Queue(`
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, 'awaiting_review', 'video_ready', 'ass_ready')
`, taskID, acquisitionID, sourceFileID, fixture.profileID)
	batch.Queue(`
INSERT INTO media_artifacts (id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $2, $3, $4, 'video', 'Review Show - S01E01 - Pilot', '/staging/review-video.mkv', 'matroska', 10, decode(repeat('01', 32), 'hex'))
`, videoArtifactID, taskID, sourceFileID, fixture.profileID)
	batch.Queue(`
INSERT INTO media_artifacts (id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $2, $3, NULL, 'subtitle', 'Review Show - S01E01 - Pilot', '/staging/review-subtitle.ass', 'ass', 10, decode(repeat('02', 32), 'hex'))
`, subtitleArtifactID, taskID, sourceFileID)
	batch.Queue(`
INSERT INTO artifact_sets (id, task_id, transcode_profile_id, basename, video_artifact_id, subtitle_artifact_id)
VALUES ($1, $2, $3, 'Review Show - S01E01 - Pilot', $4, $5)
`, uuid.New(), taskID, fixture.profileID, videoArtifactID, subtitleArtifactID)
	if err := fixture.pool.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}
	return taskID
}
