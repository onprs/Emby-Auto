package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestOperationSchedulerRejectsInvalidRequestBeforeTransaction(t *testing.T) {
	scheduler := NewOperationScheduler(nil, nil)
	tests := []struct {
		name    string
		request ScheduleOperationRequest
		field   string
	}{
		{name: "blank idempotency key", request: ScheduleOperationRequest{Kind: appqueue.KindRSSPoll, MaxAttempts: 3, Timeout: time.Minute}, field: "idempotencyKey"},
		{name: "unknown kind", request: ScheduleOperationRequest{Kind: "unknown.job", IdempotencyKey: "key", MaxAttempts: 3, Timeout: time.Minute}, field: "kind"},
		{name: "fractional timeout", request: ScheduleOperationRequest{Kind: appqueue.KindRSSPoll, IdempotencyKey: "key", MaxAttempts: 3, Timeout: 1500 * time.Millisecond}, field: "timeout"},
		{name: "resource type without ID", request: ScheduleOperationRequest{Kind: appqueue.KindRSSPoll, IdempotencyKey: "key", MaxAttempts: 3, Timeout: time.Minute, ResourceType: "subscription"}, field: "resource"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := scheduler.Schedule(context.Background(), test.request)
			var serviceErr *Error
			if !errors.As(err, &serviceErr) || serviceErr.Code != "invalid_operation" || serviceErr.Details["field"] != test.field {
				t.Fatalf("Schedule() error = %#v, want invalid_operation for %q", err, test.field)
			}
		})
	}
}

func TestSameOperationCommandRequiresEquivalentPayloadAndExecutionPolicy(t *testing.T) {
	resourceID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	resourceType := "subscription"
	operation := db.Operation{
		Kind:           appqueue.KindRSSPoll,
		ResourceType:   &resourceType,
		ResourceID:     repository.UUIDToPG(resourceID),
		MaxAttempts:    4,
		TimeoutSeconds: 90,
		Payload:        []byte(`{"feed":"weekly","limit":10}`),
	}
	request := ScheduleOperationRequest{
		Kind:         appqueue.KindRSSPoll,
		ResourceType: "subscription",
		ResourceID:   resourceID,
		MaxAttempts:  4,
		Timeout:      90 * time.Second,
	}

	if !sameOperationCommand(operation, request, []byte(`{"limit":10,"feed":"weekly"}`)) {
		t.Fatal("sameOperationCommand() rejected semantically identical JSON payload")
	}
	if sameOperationCommand(operation, request, []byte(`{"limit":20,"feed":"weekly"}`)) {
		t.Fatal("sameOperationCommand() accepted a different payload")
	}
	request.Timeout = 2 * time.Minute
	if sameOperationCommand(operation, request, []byte(`{"limit":10,"feed":"weekly"}`)) {
		t.Fatal("sameOperationCommand() accepted a different timeout")
	}
}
