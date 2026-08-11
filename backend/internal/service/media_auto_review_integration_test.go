//go:build integration

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestRSSAutoReviewRecordsApprovalAndQueuesImportAtomicallyIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createFinalizingRSSTask(t, fixture, true)
	workflow := NewMediaWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)

	if err := workflow.CompleteFinalize(context.Background(), taskID, uuid.Nil); err != nil {
		t.Fatalf("CompleteFinalize() error = %v", err)
	}
	if err := workflow.CompleteFinalize(context.Background(), taskID, uuid.Nil); err != nil {
		t.Fatalf("CompleteFinalize(replay) error = %v", err)
	}

	var state, decision, notes, operationKind string
	var version, expectedVersion int32
	var reviewedByIsNull bool
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT task.state, task.version, review.decision, review.notes,
       review.reviewed_by IS NULL, review.expected_task_version
FROM episode_tasks AS task
JOIN reviews AS review ON review.task_id = task.id
WHERE task.id = $1
`, taskID).Scan(&state, &version, &decision, &notes, &reviewedByIsNull, &expectedVersion); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.TaskImportQueued) || version != 4 || decision != string(domain.TaskApproved) ||
		!reviewedByIsNull || expectedVersion != 2 || !strings.Contains(notes, "RSS subscription policy") {
		t.Fatalf("automatic review = state %s/v%d decision %s system=%t expected=v%d notes=%q", state, version, decision, reviewedByIsNull, expectedVersion, notes)
	}

	var reviews, imports, operations, riverJobs, awaitingEvents, reviewedEvents, queuedEvents int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT
    (SELECT count(*) FROM reviews WHERE task_id = $1),
    (SELECT count(*) FROM imports WHERE task_id = $1 AND status = 'queued'),
    (SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'emby.import'),
    (SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'emby.import' AND river_job_id IS NOT NULL),
    (SELECT count(*) FROM events WHERE resource_type = 'episode_task' AND resource_id = $1 AND topic = 'task.awaiting_review'),
    (SELECT count(*) FROM events WHERE resource_type = 'episode_task' AND resource_id = $1 AND topic = 'task.reviewed' AND (data->>'automatic')::boolean),
    (SELECT count(*) FROM events WHERE resource_type = 'episode_task' AND resource_id = $1 AND topic = 'task.import_queued' AND (data->>'automatic')::boolean)
`, taskID).Scan(&reviews, &imports, &operations, &riverJobs, &awaitingEvents, &reviewedEvents, &queuedEvents); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT kind FROM operations
WHERE resource_type = 'episode_task' AND resource_id = $1 AND kind = 'emby.import'
`, taskID).Scan(&operationKind); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 || imports != 1 || operations != 1 || riverJobs != 1 || awaitingEvents != 1 || reviewedEvents != 1 || queuedEvents != 1 || operationKind != "emby.import" {
		t.Fatalf("review/import/operation/job/events = %d/%d/%d/%d/%d/%d/%d kind=%s", reviews, imports, operations, riverJobs, awaitingEvents, reviewedEvents, queuedEvents, operationKind)
	}
}

func TestRSSFinalizeWithoutAutoReviewWaitsForHumanReviewIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createFinalizingRSSTask(t, fixture, false)
	workflow := NewMediaWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)

	if err := workflow.CompleteFinalize(context.Background(), taskID, uuid.Nil); err != nil {
		t.Fatalf("CompleteFinalize() error = %v", err)
	}
	var state string
	var version, artifactSets, reviews, imports, operations int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT task.state, task.version,
       (SELECT count(*) FROM artifact_sets WHERE task_id = task.id),
       (SELECT count(*) FROM reviews WHERE task_id = task.id),
       (SELECT count(*) FROM imports WHERE task_id = task.id),
       (SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = task.id AND kind = 'emby.import')
FROM episode_tasks AS task
WHERE task.id = $1
`, taskID).Scan(&state, &version, &artifactSets, &reviews, &imports, &operations); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.TaskAwaitingReview) || version != 2 || artifactSets != 1 || reviews != 0 || imports != 0 || operations != 0 {
		t.Fatalf("manual review task = %s/v%d artifacts=%d reviews=%d imports=%d operations=%d", state, version, artifactSets, reviews, imports, operations)
	}
}

func TestEnablingRSSAutoReviewImmediatelyApprovesPendingTasksIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createFinalizingRSSTask(t, fixture, false)
	mediaWorkflow := NewMediaWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	if err := mediaWorkflow.CompleteFinalize(context.Background(), taskID, uuid.Nil); err != nil {
		t.Fatalf("CompleteFinalize() error = %v", err)
	}

	rssWorkflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	updated, err := enableTaskSubscriptionAutoReview(t, fixture, rssWorkflow, taskID)
	if err != nil {
		t.Fatalf("UpdateSubscription() error = %v", err)
	}
	if !updated.AutoReview {
		t.Fatal("updated subscription auto review = false")
	}
	assertAutomaticallyQueuedTask(t, fixture, taskID)
}

func TestRSSAutoReviewStartupReconciliationApprovesLegacyPendingTasksIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createFinalizingRSSTask(t, fixture, false)
	mediaWorkflow := NewMediaWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	if err := mediaWorkflow.CompleteFinalize(context.Background(), taskID, uuid.Nil); err != nil {
		t.Fatalf("CompleteFinalize() error = %v", err)
	}
	if _, err := fixture.pool.Exec(context.Background(), `
UPDATE rss_subscriptions AS subscription
SET auto_review = true
FROM rss_entries AS entry
JOIN acquisitions AS acquisition ON acquisition.rss_entry_id = entry.id
JOIN episode_tasks AS task ON task.acquisition_id = acquisition.id
WHERE task.id = $1 AND subscription.id = entry.subscription_id
`, taskID); err != nil {
		t.Fatal(err)
	}

	rssWorkflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	reviewed, err := rssWorkflow.ReconcileAutoReviews(context.Background())
	if err != nil {
		t.Fatalf("ReconcileAutoReviews() error = %v", err)
	}
	if reviewed != 1 {
		t.Fatalf("reviewed tasks = %d, want 1", reviewed)
	}
	replayed, err := rssWorkflow.ReconcileAutoReviews(context.Background())
	if err != nil || replayed != 0 {
		t.Fatalf("ReconcileAutoReviews(replay) = %d, %v", replayed, err)
	}
	assertAutomaticallyQueuedTask(t, fixture, taskID)
}

func TestEnablingRSSAutoReviewRollsBackWhenImportJobCannotBeCreatedIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createFinalizingRSSTask(t, fixture, false)
	mediaWorkflow := NewMediaWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	if err := mediaWorkflow.CompleteFinalize(context.Background(), taskID, uuid.Nil); err != nil {
		t.Fatalf("CompleteFinalize() error = %v", err)
	}
	rssWorkflow := NewRSSWorkflow(
		db.New(fixture.pool),
		fixture.transactor,
		NewOperationScheduler(fixture.transactor, &failingReviewJobInserter{}),
	)
	if _, err := enableTaskSubscriptionAutoReview(t, fixture, rssWorkflow, taskID); err == nil {
		t.Fatal("UpdateSubscription() error = nil")
	}

	var autoReview bool
	var state string
	var reviews, imports, operations int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT subscription.auto_review, task.state,
       (SELECT count(*) FROM reviews WHERE task_id = task.id),
       (SELECT count(*) FROM imports WHERE task_id = task.id),
       (SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = task.id AND kind = 'emby.import')
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE task.id = $1
`, taskID).Scan(&autoReview, &state, &reviews, &imports, &operations); err != nil {
		t.Fatal(err)
	}
	if autoReview || state != string(domain.TaskAwaitingReview) || reviews != 0 || imports != 0 || operations != 0 {
		t.Fatalf("rolled back enable = auto=%t state=%s reviews=%d imports=%d operations=%d", autoReview, state, reviews, imports, operations)
	}
}

func TestRSSAutoReviewRollsBackWhenImportJobCannotBeCreatedIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createFinalizingRSSTask(t, fixture, true)
	scheduler := NewOperationScheduler(fixture.transactor, &failingReviewJobInserter{})
	workflow := NewMediaWorkflow(db.New(fixture.pool), fixture.transactor, scheduler)

	if err := workflow.CompleteFinalize(context.Background(), taskID, uuid.Nil); err == nil {
		t.Fatal("CompleteFinalize() error = nil")
	}
	var state string
	var version, artifactSets, reviews, imports, operations, events int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT task.state, task.version,
       (SELECT count(*) FROM artifact_sets WHERE task_id = task.id),
       (SELECT count(*) FROM reviews WHERE task_id = task.id),
       (SELECT count(*) FROM imports WHERE task_id = task.id),
       (SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = task.id AND kind = 'emby.import'),
       (SELECT count(*) FROM events WHERE resource_type = 'episode_task' AND resource_id = task.id AND topic IN ('task.awaiting_review', 'task.reviewed', 'task.import_queued'))
FROM episode_tasks AS task
WHERE task.id = $1
`, taskID).Scan(&state, &version, &artifactSets, &reviews, &imports, &operations, &events); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.TaskFinalizing) || version != 1 || artifactSets != 0 || reviews != 0 || imports != 0 || operations != 0 || events != 0 {
		t.Fatalf("rolled back auto review = %s/v%d artifacts=%d reviews=%d imports=%d operations=%d events=%d", state, version, artifactSets, reviews, imports, operations, events)
	}
}

func enableTaskSubscriptionAutoReview(
	t *testing.T,
	fixture recoveryFixture,
	workflow *RSSWorkflow,
	taskID uuid.UUID,
) (domain.RSSSubscription, error) {
	t.Helper()
	var subscriptionID uuid.UUID
	var name, feedURL string
	var version int32
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT subscription.id, subscription.name, subscription.feed_url, subscription.version
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE task.id = $1
`, taskID).Scan(&subscriptionID, &name, &feedURL, &version); err != nil {
		t.Fatal(err)
	}
	return workflow.UpdateSubscription(context.Background(), domain.UpdateRSSSubscription{
		ID: subscriptionID, ExpectedVersion: version, Name: name, FeedURL: feedURL,
		Enabled: false, AutoReview: true, SourceSeason: 1, PollInterval: 15 * time.Minute,
		ActorUserID: fixture.actorID,
	})
}

func assertAutomaticallyQueuedTask(t *testing.T, fixture recoveryFixture, taskID uuid.UUID) {
	t.Helper()
	var state, decision string
	var imports, operations int
	if err := fixture.pool.QueryRow(context.Background(), `
SELECT task.state, review.decision,
       (SELECT count(*) FROM imports WHERE task_id = task.id AND status = 'queued'),
       (SELECT count(*) FROM operations WHERE resource_type = 'episode_task' AND resource_id = task.id AND kind = 'emby.import')
FROM episode_tasks AS task
JOIN reviews AS review ON review.task_id = task.id
WHERE task.id = $1
`, taskID).Scan(&state, &decision, &imports, &operations); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.TaskImportQueued) || decision != string(domain.TaskApproved) || imports != 1 || operations != 1 {
		t.Fatalf("automatic task = state %s decision %s imports=%d operations=%d", state, decision, imports, operations)
	}
}

func createFinalizingRSSTask(t *testing.T, fixture recoveryFixture, autoReview bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	subscriptionID, entryID, acquisitionID := uuid.New(), uuid.New(), uuid.New()
	downloadID, sourceFileID, taskID := uuid.New(), uuid.New(), uuid.New()
	videoArtifactID, subtitleArtifactID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, auto_review, poll_interval_seconds, source_season)
VALUES ($1, $2, $3, $4, false, $5, 900, 1)
`, subscriptionID, fixture.seriesID, "Auto Review "+subscriptionID.String(), "https://example.test/"+subscriptionID.String()+".xml", autoReview); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status)
VALUES ($1, $2, $3, 'Auto Review S01E01', 'https://example.test/episode.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued')
`, entryID, subscriptionID, "guid:"+entryID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id, source_payload)
VALUES ($1, $2, 'rss', $3, '{"sourceSeason":1,"sourceEpisode":1}')
`, acquisitionID, fixture.seriesID, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, progress, save_path)
VALUES ($1, $2, 'materialized', 1, '/downloads/rss-auto-review')
`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Auto.Review.S01E01.mkv', 1024, 'video', true, 1, 1)
`, sourceFileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id, state, video_state, subtitle_state)
VALUES ($1, $2, $3, $4, 'finalizing', 'video_ready', 'ass_ready')
`, taskID, acquisitionID, sourceFileID, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO media_artifacts (id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $2, $3, $4, 'video', 'Auto Review - S01E01 - Pilot', '/staging/auto-review.mkv', 'matroska', 10, decode(repeat('01', 32), 'hex'))
`, videoArtifactID, taskID, sourceFileID, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO media_artifacts (id, task_id, source_file_id, kind, basename, file_path, format, size_bytes, checksum_sha256)
VALUES ($1, $2, $3, 'subtitle', 'Auto Review - S01E01 - Pilot', '/staging/auto-review.ass', 'ass', 10, decode(repeat('02', 32), 'hex'))
`, subtitleArtifactID, taskID, sourceFileID); err != nil {
		t.Fatal(err)
	}
	return taskID
}
