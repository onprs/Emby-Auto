//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestOperationSchedulerIdempotencyContractIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	jobs := &integrationJobInserter{}
	scheduler := NewOperationScheduler(database.NewTransactor(pool), jobs)
	resourceID := uuid.MustParse("73000000-0000-4000-8000-000000000001")
	request := ScheduleOperationRequest{
		Kind: appqueue.KindRSSPoll, ResourceType: "rss_subscription", ResourceID: resourceID,
		IdempotencyKey: "contract-policy-rss-poll-1", MaxAttempts: 3, Timeout: 90 * time.Second,
		Payload: map[string]any{"command": "poll", "source": "manual"},
	}

	first, err := scheduler.Schedule(ctx, request)
	if err != nil {
		t.Fatalf("first Schedule() error = %v", err)
	}
	second, err := scheduler.Schedule(ctx, request)
	if err != nil {
		t.Fatalf("idempotent Schedule() error = %v", err)
	}
	if !first.Created || second.Created || first.Operation.ID != second.Operation.ID || jobs.nextID.Load() != 1 {
		t.Fatalf("idempotent results = first(%s,%t) second(%s,%t) jobs=%d", first.Operation.ID, first.Created, second.Operation.ID, second.Created, jobs.nextID.Load())
	}

	conflict := request
	conflict.Payload = map[string]any{"command": "poll", "source": "scheduled"}
	_, err = scheduler.Schedule(ctx, conflict)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || !errors.Is(err, ErrStateConflict) || serviceErr.Code != "idempotency_conflict" {
		t.Fatalf("different payload error = %#v, want idempotency_conflict", err)
	}
	if jobs.nextID.Load() != 1 {
		t.Fatalf("conflicting replay inserted a River job: jobs=%d", jobs.nextID.Load())
	}
}
