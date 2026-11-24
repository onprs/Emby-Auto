//go:build integration

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestZZR1OldParentActiveDoesNotProtectNewExtractionFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	queries := db.New(pool)
	taskID := seedSubtitleGenerationTask(t, ctx, pool)
	workflow := service.NewMediaWorkflow(queries, transactor, nil)
	scopeID, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, zzR1Candidates())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
	oldParent, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
		Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
		IdempotencyKey: "zz-r1-parent-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
		Payload: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := repository.NewOperationLifecycle(queries, transactor)
	parent, err := lifecycle.StartAttempt(ctx, oldParent.Operation.ID, 1, "zz-r1-parent")
	if err != nil || parent.SubtitleAttempt == nil {
		t.Fatalf("parent StartAttempt = %+v/%v", parent, err)
	}
	// Reproduce the formal Apply DB effects: applied selection + a queued
	// subtitle.prepare with the Agent key. The Agent operation itself is not
	// invoked here; this is the Apply's exact scope/schedule shape.
	resolutionID, resolutionOperationID := uuid.New(), uuid.New()
	selectedID := "embedded-2"
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, attempt_count, timeout_seconds, started_at, heartbeat_at, finished_at
) VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'succeeded', 3, 1, 60, now(), now(), now());
INSERT INTO agent_resolutions (
    id, operation_id, capability, resource_type, resource_id, trigger, status,
    input_fingerprint, configuration_version, protocol, provider_origin, model,
    prompt_version, toolset_version, started_at, completed_at, applied_at
) VALUES ($2, $1, 'subtitle_video_match', 'episode_task', $4, 'automatic', 'applied',
    $5, 1, 'openai_chat_completions', 'https://agent.example', 'test-model',
    'subtitle-video-match-v1', 'agent-tools-v1', now(), now(), now());
`, repository.UUIDToPG(resolutionOperationID), repository.UUIDToPG(resolutionID), "zz-r1-res-"+resolutionID.String(), repository.UUIDToPG(scopeID), make([]byte, 32)); err != nil {
		t.Fatalf("insert applied resolution: %v", err)
	}
	if _, err := queries.ApplySubtitleVideoMatchSelection(ctx, db.ApplySubtitleVideoMatchSelectionParams{
		ID: repository.UUIDToPG(scopeID), SelectedCandidateID: &selectedID, AgentResolutionID: repository.UUIDToPG(resolutionID),
	}); err != nil {
		t.Fatalf("apply selection: %v", err)
	}
	newExtraction, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
		Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
		IdempotencyKey: "subtitle.prepare:agent:" + resolutionID.String(), MaxAttempts: 5, Timeout: 30 * time.Second,
		Payload: map[string]any{"agentResolutionId": resolutionID},
	})
	if err != nil {
		t.Fatalf("schedule Agent extraction: %v", err)
	}
	extraction, err := lifecycle.StartAttempt(ctx, newExtraction.Operation.ID, 1, "zz-r1-extraction")
	if err != nil || extraction.SubtitleAttempt == nil {
		t.Fatalf("extraction StartAttempt = %+v/%v", extraction, err)
	}
	if err := lifecycle.FailSubtitleAttempt(ctx, *extraction.SubtitleAttempt, "extraction_failed", "new extraction failed", false); err != nil {
		t.Fatalf("FailSubtitleAttempt: %v", err)
	}
	var taskState, subtitleState string
	if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState); err != nil {
		t.Fatal(err)
	}
	if taskState != "failed" || subtitleState != "failed" {
		t.Fatalf("new extraction failure left task %s/%s, want failed/failed", taskState, subtitleState)
	}
	var parentStatus, extractionStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, repository.UUIDToPG(oldParent.Operation.ID)).Scan(&parentStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, repository.UUIDToPG(newExtraction.Operation.ID)).Scan(&extractionStatus); err != nil {
		t.Fatal(err)
	}
	if parentStatus != "running" || extractionStatus != "failed" {
		t.Fatalf("window status parent=%s extraction=%s, want running/failed", parentStatus, extractionStatus)
	}
	if err := lifecycle.SucceedSubtitleAttempt(ctx, *parent.SubtitleAttempt); err != nil {
		t.Fatalf("parent SucceedSubtitleAttempt: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, repository.UUIDToPG(oldParent.Operation.ID)).Scan(&parentStatus); err != nil {
		t.Fatal(err)
	}
	if parentStatus != "succeeded" {
		t.Fatalf("parent status = %s, want succeeded", parentStatus)
	}
	if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState); err != nil {
		t.Fatal(err)
	}
	if taskState != "failed" || subtitleState != "failed" {
		t.Fatalf("task after parent success = %s/%s, want failed/failed", taskState, subtitleState)
	}
}
