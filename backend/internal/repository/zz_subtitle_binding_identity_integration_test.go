//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/subtitlebinding"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestSubtitleBindingRejectsWrongOperationAndCASUsesCapturedPhase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	operationA, operationB := uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO operations (id, kind, idempotency_key, max_attempts, timeout_seconds, payload)
VALUES ($1, 'subtitle.prepare', $3, 3, 60, '{}'), ($2, 'subtitle.prepare', $4, 3, 60, '{}')
`, UUIDToPG(operationA), UUIDToPG(operationB), "binding-a-"+operationA.String(), "binding-b-"+operationB.String()); err != nil {
		t.Fatal(err)
	}
	observation, err := subtitlebinding.ObservationFromSnapshot(subtitlebinding.Snapshot{
		TaskVersion: 1, TaskState: "processing", SubtitleState: "subtitle_queued", VideoState: "transcoding",
	})
	if err != nil {
		t.Fatal(err)
	}
	attemptA, attemptB := uuid.New(), uuid.New()
	handleA := subtitlebinding.AttemptHandle{
		OperationID: operationA, JobAttempt: 1, AttemptID: attemptA, OperationAttemptCount: 1,
		ResourceType: "episode_task", ResourceID: uuid.New(),
	}
	handleB := handleA
	handleB.OperationID = operationB
	handleB.AttemptID = attemptB
	bindingA := subtitlebinding.NewStartBinding(handleA, observation)
	bindingB := subtitlebinding.NewStartBinding(handleB, observation)
	rawA, err := subtitlebinding.Encode(bindingA)
	if err != nil {
		t.Fatal(err)
	}
	rawB, err := subtitlebinding.Encode(bindingB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO operation_subtitle_bindings (operation_id, binding_version, phase, attempt, attempt_id, operation_attempt_count, binding)
VALUES ($1, 1, 'started', 1, $2, 1, $3)
`, UUIDToPG(operationA), UUIDToPG(attemptB), string(rawB)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadSubtitleBinding(ctx, queries, operationA); !errors.Is(err, subtitlebinding.ErrUnsupported) {
		t.Fatalf("wrong-operation binding read error = %v, want ErrUnsupported", err)
	}
	if _, _, err := ReadSubtitleBinding(ctx, queries, operationB); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("operation B binding read error = %v, want pgx.ErrNoRows", err)
	}
	if _, err := queries.AdvanceOperationSubtitleBinding(ctx, db.AdvanceOperationSubtitleBindingParams{
		NextPhase: "begun", Binding: rawA, OperationID: UUIDToPG(operationA), BindingVersion: 1,
		ExpectedPhase: "begun", Attempt: 1, AttemptID: UUIDToPG(attemptA), OperationAttemptCount: 1,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong-phase advance error = %v, want pgx.ErrNoRows", err)
	}
	row, err := queries.GetOperationSubtitleBinding(ctx, UUIDToPG(operationA))
	if err != nil {
		t.Fatal(err)
	}
	if row.Phase != "started" {
		t.Fatalf("wrong-phase advance changed phase to %s", row.Phase)
	}
	rowBinding, err := subtitlebinding.Decode(row.Binding, row.BindingVersion, row.Phase, row.Attempt, UUIDFromPG(row.AttemptID), row.OperationAttemptCount)
	if err != nil {
		t.Fatalf("decode unchanged row: %v", err)
	}
	if rowBinding.OperationID != operationB {
		t.Fatal("wrong-phase advance changed the captured binding identity")
	}
}
