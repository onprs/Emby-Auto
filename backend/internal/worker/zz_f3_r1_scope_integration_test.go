//go:build integration

package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func zzR1ExternalCandidates() []domain.SubtitleMatchCandidate {
	return []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-6", Source: domain.SubtitleSourceEmbedded, StreamIndex: 6, Format: domain.SubtitleASS, Language: "chi", Title: "简中"},
		{CandidateID: "embedded-7", Source: domain.SubtitleSourceEmbedded, StreamIndex: 7, Format: domain.SubtitleASS, Language: "chi", Title: "繁中"},
	}
}

func TestZZR1ScopeObservationBoundaries(t *testing.T) {
	t.Run("begin_rejects_scope_content_changed_after_start", func(t *testing.T) {
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
			IdempotencyKey: "zz-r1-begin-scope-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		started, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-begin-scope")
		if err != nil || started.SubtitleAttempt == nil {
			t.Fatalf("StartAttempt = %+v/%v", started, err)
		}
		if _, err := queries.ExpireSubtitleVideoMatchScope(ctx, repository.UUIDToPG(scopeID)); err != nil {
			t.Fatalf("expire scope: %v", err)
		}
		reused, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, zzR1ExternalCandidates())
		if err != nil {
			t.Fatalf("reuse expired scope: %v", err)
		}
		if reused != scopeID {
			t.Fatalf("reused scope = %s, want %s", reused, scopeID)
		}
		if _, err := workflow.BeginSubtitleForOperation(ctx, *started.SubtitleAttempt, taskID); err == nil {
			t.Fatal("Begin accepted a scope whose content changed after Start")
		}
		var taskState, subtitleState string
		if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState); err != nil {
			t.Fatal(err)
		}
		if taskState != "processing" || subtitleState != "subtitle_queued" {
			t.Fatalf("task changed after rejected Begin: %s/%s", taskState, subtitleState)
		}
		var scopeStatus string
		if err := pool.QueryRow(ctx, `SELECT status FROM subtitle_video_match_scopes WHERE id = $1`, repository.UUIDToPG(scopeID)).Scan(&scopeStatus); err != nil {
			t.Fatal(err)
		}
		candidateIDs := zzR1CandidateIDs(t, ctx, pool, scopeID)
		if scopeStatus != "pending" || len(candidateIDs) != 2 || candidateIDs[0] != "embedded-6" || candidateIDs[1] != "embedded-7" {
			t.Fatalf("external scope was adopted: status=%s candidates=%v", scopeStatus, candidateIDs)
		}
	})

	t.Run("create_does_not_adopt_external_scope_after_begin", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, pool := testutil.NewMigratedPostgres(t)
		transactor := database.NewTransactor(pool)
		queries := db.New(pool)
		taskID := seedSubtitleGenerationTask(t, ctx, pool)
		workflow := service.NewMediaWorkflow(queries, transactor, nil)
		scheduler := service.NewOperationScheduler(transactor, &zzR1JobInserter{})
		scheduled, err := scheduler.Schedule(ctx, service.ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: taskID,
			IdempotencyKey: "zz-r1-create-scope-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
			Payload: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		lifecycle := repository.NewOperationLifecycle(queries, transactor)
		started, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-create-scope")
		if err != nil || started.SubtitleAttempt == nil {
			t.Fatalf("StartAttempt = %+v/%v", started, err)
		}
		if _, err := workflow.BeginSubtitleForOperation(ctx, *started.SubtitleAttempt, taskID); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		externalScopeID, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, zzR1ExternalCandidates())
		if err != nil {
			t.Fatalf("external scope: %v", err)
		}
		if _, err := workflow.CreateSubtitleVideoMatchScopeForOperation(ctx, *started.SubtitleAttempt, taskID, zzR1Candidates()); err == nil {
			t.Fatal("Create adopted an external scope")
		} else {
			var mediaErr *domain.MediaWorkflowError
			if !errors.As(err, &mediaErr) || mediaErr.Code != "subtitle_scope_external" {
				t.Fatalf("Create error = %v, want subtitle_scope_external", err)
			}
		}
		candidateIDs := zzR1CandidateIDs(t, ctx, pool, externalScopeID)
		if len(candidateIDs) != 2 || candidateIDs[0] != "embedded-6" || candidateIDs[1] != "embedded-7" {
			t.Fatalf("external candidates were overwritten: %v", candidateIDs)
		}
		if err := lifecycle.FailSubtitleAttempt(ctx, *started.SubtitleAttempt, "subtitle_scope_external", "external scope", false); err != nil {
			t.Fatalf("FailSubtitleAttempt: %v", err)
		}
		var taskState, subtitleState string
		if err := pool.QueryRow(ctx, `SELECT state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskState, &subtitleState); err != nil {
			t.Fatal(err)
		}
		if taskState != "failed" || subtitleState != "failed" {
			t.Fatalf("task after safe terminal = %s/%s, want failed/failed", taskState, subtitleState)
		}
		var operationStatus string
		if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, repository.UUIDToPG(scheduled.Operation.ID)).Scan(&operationStatus); err != nil {
			t.Fatal(err)
		}
		if operationStatus != "failed" {
			t.Fatalf("operation = %s, want failed", operationStatus)
		}
	})
}

func TestZZR1BeginWaitsForScopeLockAndRejectsExternalContent(t *testing.T) {
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
		IdempotencyKey: "zz-r1-begin-barrier-" + uuid.NewString(), MaxAttempts: 3, Timeout: 30 * time.Minute,
		Payload: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := repository.NewOperationLifecycle(queries, transactor)
	started, err := lifecycle.StartAttempt(ctx, scheduled.Operation.ID, 1, "zz-r1-begin-barrier")
	if err != nil || started.SubtitleAttempt == nil {
		t.Fatalf("StartAttempt = %+v/%v", started, err)
	}
	var taskVersionBefore int32
	if err := pool.QueryRow(ctx, `SELECT version FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskVersionBefore); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE subtitle_video_match_scopes SET status = 'expired' WHERE id = $1`, repository.UUIDToPG(scopeID)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM subtitle_video_match_candidates WHERE scope_id = $1`, repository.UUIDToPG(scopeID)); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range zzR1ExternalCandidates() {
		if _, err := tx.Exec(ctx, `
INSERT INTO subtitle_video_match_candidates (scope_id, candidate_id, source, stream_index, format, language, title)
VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			repository.UUIDToPG(scopeID), candidate.CandidateID, string(candidate.Source), int32(candidate.StreamIndex),
			string(candidate.Format), candidate.Language, candidate.Title); err != nil {
			t.Fatal(err)
		}
	}
	beginErr := make(chan error, 1)
	go func() {
		_, err := workflow.BeginSubtitleForOperation(ctx, *started.SubtitleAttempt, taskID)
		beginErr <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-beginErr:
		if err == nil {
			t.Fatal("Begin accepted a scope content committed while it was waiting")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Begin did not return after the scope lock released")
	}
	var taskVersionAfter int32
	var state, subtitle string
	if err := pool.QueryRow(ctx, `SELECT version, state, subtitle_state FROM episode_tasks WHERE id = $1`, repository.UUIDToPG(taskID)).Scan(&taskVersionAfter, &state, &subtitle); err != nil {
		t.Fatal(err)
	}
	if taskVersionAfter != taskVersionBefore || state != "processing" || subtitle != "subtitle_queued" {
		t.Fatalf("rejected Begin changed task to version=%d %s/%s", taskVersionAfter, state, subtitle)
	}
	candidateIDs := zzR1CandidateIDs(t, ctx, pool, scopeID)
	if len(candidateIDs) != 2 || candidateIDs[0] != "embedded-6" || candidateIDs[1] != "embedded-7" {
		t.Fatalf("barrier candidates = %v, want external content preserved", candidateIDs)
	}
}

func zzR1CandidateIDs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scopeID uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT candidate_id FROM subtitle_video_match_candidates WHERE scope_id = $1 ORDER BY candidate_id`, repository.UUIDToPG(scopeID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}
