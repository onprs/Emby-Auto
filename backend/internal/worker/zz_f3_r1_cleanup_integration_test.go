//go:build integration

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestZZR1CleanupTrustedScope(t *testing.T) {
	t.Run("same_content_owned_scope_can_expire", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
		scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-cleanup-same-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		started, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-cleanup-same")
		if err != nil || started.SubtitleAttempt == nil {
			t.Fatalf("StartAttempt = %+v/%v", started, err)
		}
		if _, err := workflow.BeginSubtitleForOperation(ctx, *started.SubtitleAttempt, taskID); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		state, err := workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := workflow.ExpireOwnerlessSubtitleVideoMatchScopeForOperation(ctx, *started.SubtitleAttempt, state.ScopeID, domain.SubtitleScopeObservation{
			TaskVersion: state.TaskVersion, TaskState: state.TaskState, SubtitleState: state.SubtitleState, ContentHash: state.ContentObservation,
		})
		if err != nil {
			t.Fatalf("cleanup same content: %v", err)
		}
		if result.Outcome != domain.SubtitleScopeExpired {
			t.Fatalf("same-content cleanup outcome = %s, want expired", result.Outcome)
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM subtitle_video_match_scopes WHERE id = $1`, repository.UUIDToPG(scopeID)).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "expired" {
			t.Fatalf("same-content scope status = %s, want expired", status)
		}
	})

	t.Run("external_different_content_is_not_expired", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
		scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-cleanup-external-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		started, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-cleanup-external")
		if err != nil || started.SubtitleAttempt == nil {
			t.Fatalf("StartAttempt = %+v/%v", started, err)
		}
		if _, err := workflow.BeginSubtitleForOperation(ctx, *started.SubtitleAttempt, taskID); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := queries.ExpireSubtitleVideoMatchScope(ctx, repository.UUIDToPG(scopeID)); err != nil {
			t.Fatal(err)
		}
		reused, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, zzR1ExternalCandidates())
		if err != nil || reused != scopeID {
			t.Fatalf("external scope = %s/%v", reused, err)
		}
		state, err := workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := workflow.ExpireOwnerlessSubtitleVideoMatchScopeForOperation(ctx, *started.SubtitleAttempt, state.ScopeID, domain.SubtitleScopeObservation{
			TaskVersion: state.TaskVersion, TaskState: state.TaskState, SubtitleState: state.SubtitleState, ContentHash: state.ContentObservation,
		})
		if err != nil {
			t.Fatalf("cleanup external: %v", err)
		}
		if result.Outcome != domain.SubtitleScopeChanged {
			t.Fatalf("external cleanup outcome = %s, want scope_changed", result.Outcome)
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM subtitle_video_match_scopes WHERE id = $1`, repository.UUIDToPG(scopeID)).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "pending" {
			t.Fatalf("external scope status = %s, want pending", status)
		}
		candidateIDs := zzR1CandidateIDs(t, ctx, pool, scopeID)
		if len(candidateIDs) != 2 || candidateIDs[0] != "embedded-6" || candidateIDs[1] != "embedded-7" {
			t.Fatalf("external candidates changed: %v", candidateIDs)
		}
	})
}
