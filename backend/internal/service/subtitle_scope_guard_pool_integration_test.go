//go:build integration

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func newSubtitleScopeGuardPool(t *testing.T, maxConns int32) (context.Context, *pgxpool.Pool, *database.Transactor, *db.Queries, *MediaWorkflow, *AgentResolutionService, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	dsn, _ := testutil.NewMigratedPostgres(t)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = maxConns
	config.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	taskID, scopeID := seedSubtitleAgentEntities(t, ctx, pool)
	queries := db.New(pool)
	return ctx, pool, transactor, queries, NewMediaWorkflow(queries, transactor, scheduler),
		NewAgentResolutionService(queries, transactor, scheduler, reworkSubtitleResolver(), nil, nil), taskID, scopeID
}

func subtitleScopeGuardObservation(t *testing.T, ctx context.Context, workflow *MediaWorkflow, taskID uuid.UUID) domain.SubtitleScopeObservation {
	t.Helper()
	state, err := workflow.GetSubtitleVideoMatchSchedulingState(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return domain.SubtitleScopeObservation{TaskVersion: state.TaskVersion, TaskState: state.TaskState,
		SubtitleState: state.SubtitleState, ContentHash: state.ContentObservation}
}

func TestSubtitleScopeGuardPoolOneSequential(t *testing.T) {
	ctx, _, _, _, workflow, service, taskID, scopeID := newSubtitleScopeGuardPool(t, 1)
	observation := subtitleScopeGuardObservation(t, ctx, workflow, taskID)
	created, err := service.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: scopeID})
	if err != nil {
		t.Fatalf("CreateAutomatic() error = %v", err)
	}
	result, err := workflow.ExpireOwnerlessSubtitleVideoMatchScope(ctx, scopeID, observation)
	if err != nil {
		t.Fatalf("ExpireOwnerlessSubtitleVideoMatchScope() error = %v", err)
	}
	if result.Outcome != domain.SubtitleScopeOwnerPresent {
		t.Fatalf("guard outcome = %#v, want owner present on pool1", result)
	}
	owner, err := service.Get(ctx, created.Resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Status != domain.AgentResolutionQueued {
		t.Fatalf("owner status = %s, want queued", owner.Status)
	}
}

func TestSubtitleScopeGuardPoolTwoConcurrent(t *testing.T) {
	ctx, pool, _, _, workflow, service, taskID, scopeID := newSubtitleScopeGuardPool(t, 2)
	observation := subtitleScopeGuardObservation(t, ctx, workflow, taskID)
	start := make(chan struct{})
	var wait sync.WaitGroup
	var guardResult domain.SubtitleScopeExpiry
	var guardErr, ownerErr error
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		guardResult, guardErr = workflow.ExpireOwnerlessSubtitleVideoMatchScope(ctx, scopeID, observation)
	}()
	go func() {
		defer wait.Done()
		<-start
		_, ownerErr = service.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: scopeID})
	}()
	close(start)
	wait.Wait()
	for _, err := range []error{guardErr, ownerErr} {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("pool2 guard/owner deadlock: %v", err)
		}
	}
	if guardErr != nil {
		t.Fatalf("guard error = %v", guardErr)
	}
	if ownerErr != nil {
		// The guard may have won: the scope was legitimately expired before the
		// owner request; the caller can recreate. This is a valid serialization.
		if !errors.Is(ownerErr, ErrStateConflict) {
			t.Fatalf("owner error = %v, want state conflict after guard win", ownerErr)
		}
	}
	var scopeStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM subtitle_video_match_scopes WHERE id = $1`, repository.UUIDToPG(scopeID)).Scan(&scopeStatus); err != nil {
		t.Fatal(err)
	}
	switch guardResult.Outcome {
	case domain.SubtitleScopeExpired:
		if ownerErr == nil {
			t.Fatalf("guard expired but owner unexpectedly succeeded")
		}
		if scopeStatus != "expired" {
			t.Fatalf("scope after guard win = %s, want expired", scopeStatus)
		}
		if _, err := workflow.CreateSubtitleVideoMatchScope(ctx, taskID, subtitleMatchCandidatesForTest()); err != nil {
			t.Fatal(err)
		}
		if _, err := service.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: scopeID}); err != nil {
			t.Fatalf("post-guard recovery CreateAutomatic() error = %v", err)
		}
	case domain.SubtitleScopeOwnerPresent:
		if ownerErr != nil {
			t.Fatalf("owner present but owner CreateAutomatic failed: %v", ownerErr)
		}
		if scopeStatus != "pending" {
			t.Fatalf("scope after owner win = %s, want pending", scopeStatus)
		}
	default:
		t.Fatalf("unexpected guard outcome = %#v", guardResult)
	}
}
