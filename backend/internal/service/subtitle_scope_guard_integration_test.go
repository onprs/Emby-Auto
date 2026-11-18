//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type subtitleScopeGuardFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	transactor *database.Transactor
	queries    *db.Queries
	workflow   *MediaWorkflow
	service    *AgentResolutionService
	taskID     uuid.UUID
	scopeID    uuid.UUID
}

func newSubtitleScopeGuardFixture(t *testing.T) *subtitleScopeGuardFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	taskID, scopeID := seedSubtitleAgentEntities(t, ctx, pool)
	queries := db.New(pool)
	return &subtitleScopeGuardFixture{
		ctx: ctx, pool: pool, transactor: transactor, queries: queries,
		workflow: NewMediaWorkflow(queries, transactor, scheduler),
		service:  NewAgentResolutionService(queries, transactor, scheduler, reworkSubtitleResolver(), nil, nil),
		taskID:   taskID, scopeID: scopeID,
	}
}

func (fixture *subtitleScopeGuardFixture) observation(t *testing.T) domain.SubtitleScopeObservation {
	t.Helper()
	state, err := fixture.workflow.GetSubtitleVideoMatchSchedulingState(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	return domain.SubtitleScopeObservation{
		TaskVersion: state.TaskVersion, TaskState: state.TaskState,
		SubtitleState: state.SubtitleState, ContentHash: state.ContentObservation,
	}
}

func (fixture *subtitleScopeGuardFixture) expire(t *testing.T, observed domain.SubtitleScopeObservation) domain.SubtitleScopeExpiry {
	t.Helper()
	result, err := fixture.workflow.ExpireOwnerlessSubtitleVideoMatchScope(fixture.ctx, fixture.scopeID, observed)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSubtitleScopeGuardOwnerPresent(t *testing.T) {
	fixture := newSubtitleScopeGuardFixture(t)
	observed := fixture.observation(t)
	created, err := fixture.service.CreateAutomatic(fixture.ctx, AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: fixture.scopeID})
	if err != nil {
		t.Fatal(err)
	}
	result := fixture.expire(t, observed)
	if result.Outcome != domain.SubtitleScopeOwnerPresent || result.OwnerCount != 1 {
		t.Fatalf("guard outcome = %#v, want owner present", result)
	}
	owner, err := fixture.service.Get(fixture.ctx, created.Resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Status != domain.AgentResolutionQueued || owner.OperationID != created.Operation.ID {
		t.Fatalf("owner changed: %s/%s", owner.Status, owner.OperationID)
	}
}

func TestSubtitleScopeGuardCandidateChanged(t *testing.T) {
	fixture := newSubtitleScopeGuardFixture(t)
	observed := fixture.observation(t)
	expireSubtitleVideoMatchScopeForTest(t, fixture.ctx, fixture.queries, fixture.scopeID)
	if _, err := fixture.workflow.CreateSubtitleVideoMatchScope(fixture.ctx, fixture.taskID, []domain.SubtitleMatchCandidate{
		{CandidateID: "embedded-6", Source: domain.SubtitleSourceEmbedded, StreamIndex: 6, Format: domain.SubtitleASS, Language: "chi", Title: "候选变化一"},
		{CandidateID: "embedded-7", Source: domain.SubtitleSourceEmbedded, StreamIndex: 7, Format: domain.SubtitleASS, Language: "chi", Title: "候选变化二"},
	}); err != nil {
		t.Fatal(err)
	}
	result := fixture.expire(t, observed)
	if result.Outcome != domain.SubtitleScopeChanged || result.ScopeStatus != "pending" {
		t.Fatalf("guard outcome = %#v, want changed pending", result)
	}
}

func TestSubtitleScopeGuardSameContentVersionReuse(t *testing.T) {
	fixture := newSubtitleScopeGuardFixture(t)
	observed := fixture.observation(t)
	expireSubtitleVideoMatchScopeForTest(t, fixture.ctx, fixture.queries, fixture.scopeID)
	if _, err := fixture.workflow.CreateSubtitleVideoMatchScope(fixture.ctx, fixture.taskID, subtitleMatchCandidatesForTest()); err != nil {
		t.Fatal(err)
	}
	result := fixture.expire(t, observed)
	if result.Outcome != domain.SubtitleScopeExpired {
		t.Fatalf("guard outcome = %#v, want indistinguishable expiry", result)
	}
	if _, err := fixture.workflow.CreateSubtitleVideoMatchScope(fixture.ctx, fixture.taskID, subtitleMatchCandidatesForTest()); err != nil {
		t.Fatal(err)
	}
	created, err := fixture.service.CreateAutomatic(fixture.ctx, AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: fixture.scopeID})
	if err != nil {
		t.Fatal(err)
	}
	if created.Resolution.Status != domain.AgentResolutionQueued {
		t.Fatalf("recovered owner status = %s, want queued", created.Resolution.Status)
	}
}

func TestSubtitleScopeGuardTaskVersionOrStateChanged(t *testing.T) {
	for _, change := range []string{"version", "state"} {
		t.Run(change, func(t *testing.T) {
			fixture := newSubtitleScopeGuardFixture(t)
			observed := fixture.observation(t)
			if change == "version" {
				if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE episode_tasks SET version = version + 1 WHERE id = $1`, repository.UUIDToPG(fixture.taskID)); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE episode_tasks SET state = 'cancelled', subtitle_state = 'cancelled' WHERE id = $1`, repository.UUIDToPG(fixture.taskID)); err != nil {
					t.Fatal(err)
				}
			}
			result := fixture.expire(t, observed)
			if result.Outcome != domain.SubtitleScopeChanged {
				t.Fatalf("guard outcome = %#v, want changed", result)
			}
		})
	}
}

func TestSubtitleScopeGuardAlreadyAppliedOrExpired(t *testing.T) {
	fixture := newSubtitleScopeGuardFixture(t)
	observed := fixture.observation(t)
	created, err := fixture.service.CreateAutomatic(fixture.ctx, AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: fixture.scopeID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
UPDATE subtitle_video_match_scopes
SET status = 'applied', selected_candidate_id = 'embedded-2', agent_resolution_id = $1
WHERE id = $2
`, repository.UUIDToPG(created.Resolution.ID), repository.UUIDToPG(fixture.scopeID)); err != nil {
		t.Fatal(err)
	}
	result := fixture.expire(t, observed)
	if result.Outcome != domain.SubtitleScopeAlreadyApplied {
		t.Fatalf("guard outcome = %#v, want applied", result)
	}
}

func TestSubtitleScopeGuardAlreadyExpired(t *testing.T) {
	fixture := newSubtitleScopeGuardFixture(t)
	observed := fixture.observation(t)
	expireSubtitleVideoMatchScopeForTest(t, fixture.ctx, fixture.queries, fixture.scopeID)
	result := fixture.expire(t, observed)
	if result.Outcome != domain.SubtitleScopeAlreadyExpired || result.ScopeStatus != "expired" {
		t.Fatalf("guard outcome = %#v, want already expired", result)
	}
}

func TestSubtitleScopeGuardMultipleOwners(t *testing.T) {
	fixture := newSubtitleScopeGuardFixture(t)
	observed := fixture.observation(t)
	created, err := fixture.service.CreateAutomatic(fixture.ctx, AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: fixture.scopeID})
	if err != nil {
		t.Fatal(err)
	}
	secondOperationID, secondResolutionID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, payload)
VALUES ($1, 'agent.resolve', 'agent_resolution', $2::uuid, $3, 'queued', 3, 390, jsonb_build_object('resolutionId', $2::uuid))
`, repository.UUIDToPG(secondOperationID), repository.UUIDToPG(secondResolutionID), "guard-second-"+secondResolutionID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO agent_resolutions (
    id, operation_id, resource_version, capability, resource_type, resource_id, trigger, status,
    input_fingerprint, configuration_version, protocol, provider_origin, model, prompt_version, toolset_version
)
SELECT $1, $2, resource_version, capability, resource_type, resource_id, trigger, 'queued',
       decode(repeat('00', 32), 'hex'), configuration_version, protocol, provider_origin, model, prompt_version, toolset_version
FROM agent_resolutions WHERE id = $3
`, repository.UUIDToPG(secondResolutionID), repository.UUIDToPG(secondOperationID), repository.UUIDToPG(created.Resolution.ID)); err != nil {
		t.Fatal(err)
	}
	result := fixture.expire(t, observed)
	if result.Outcome != domain.SubtitleScopeMultipleOwners || result.OwnerCount != 2 {
		t.Fatalf("guard outcome = %#v, want multiple owners", result)
	}
}
