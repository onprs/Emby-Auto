package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

type rssRefreshStub struct {
	calls     int
	operation domain.Operation
}

func (stub *rssRefreshStub) Handle(_ context.Context, operation domain.Operation) error {
	stub.calls++
	stub.operation = operation
	return nil
}

type rssDeletionStoreStub struct {
	ready         bool
	readyCalls    int
	listCalls     int
	cleanupCalls  int
	deletionCalls int
}

func (stub *rssDeletionStoreStub) SubscriptionDeletionReady(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	stub.readyCalls++
	return stub.ready, nil
}

func (stub *rssDeletionStoreStub) ListSubscriptionAcquisitions(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	stub.listCalls++
	return nil, nil
}

func (stub *rssDeletionStoreStub) CompleteSubscriptionCleanup(context.Context, uuid.UUID, uuid.UUID) error {
	stub.cleanupCalls++
	return nil
}

func (stub *rssDeletionStoreStub) CompleteSubscriptionDeletion(context.Context, uuid.UUID, uuid.UUID) error {
	stub.deletionCalls++
	return nil
}

func TestRSSSubscriptionDeleteWaitsForActivePollOperations(t *testing.T) {
	store := &rssDeletionStoreStub{ready: false}
	handler := NewRSSSubscriptionDeleteHandler(store, &AcquisitionDeleteHandler{})
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: uuid.New(),
	})
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) {
		t.Fatalf("Handle() error = %T %v, want JobSnoozeError", err, err)
	}
	if store.readyCalls != 1 || store.listCalls != 0 || store.cleanupCalls != 0 || store.deletionCalls != 0 {
		t.Fatalf("calls = ready %d list %d cleanup %d deletion %d", store.readyCalls, store.listCalls, store.cleanupCalls, store.deletionCalls)
	}
}

func TestRSSSubscriptionDeleteCompletesAfterSubscriptionOperationsExit(t *testing.T) {
	store := &rssDeletionStoreStub{ready: true}
	refresh := &rssRefreshStub{}
	handler := NewRSSSubscriptionDeleteHandler(store, &AcquisitionDeleteHandler{}, refresh)
	subscriptionID := uuid.New()
	if err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID,
		Payload: []byte(`{"deleteImported":true}`),
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.readyCalls != 1 || store.listCalls != 1 || store.cleanupCalls != 0 || store.deletionCalls != 1 {
		t.Fatalf("calls = ready %d list %d cleanup %d deletion %d", store.readyCalls, store.listCalls, store.cleanupCalls, store.deletionCalls)
	}
	if refresh.calls != 1 || refresh.operation.ResourceType != "emby_catalog" || refresh.operation.ResourceID != subscriptionID {
		t.Fatalf("refresh = calls %d operation %#v", refresh.calls, refresh.operation)
	}
}

func TestRSSSubscriptionCompletionRetainsSubscriptionForNewAndLegacyJobs(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		payload []byte
	}{
		{name: "new completion kind ignores legacy imported deletion policy", kind: appqueue.KindRSSSubscriptionComplete, payload: []byte(`{"trigger":"final_import","deleteImported":true}`)},
		{name: "legacy deletion kind", kind: appqueue.KindRSSSubscriptionDelete, payload: []byte(`{"trigger":"final_import"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &rssDeletionStoreStub{ready: true}
			refresh := &rssRefreshStub{}
			handler := NewRSSSubscriptionDeleteHandler(store, &AcquisitionDeleteHandler{}, refresh)
			subscriptionID := uuid.New()
			if err := handler.Handle(context.Background(), domain.Operation{
				ID: uuid.New(), Kind: test.kind, ResourceType: "rss_subscription", ResourceID: subscriptionID, Payload: test.payload,
			}); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if store.listCalls != 0 || store.cleanupCalls != 1 || store.deletionCalls != 0 {
				t.Fatalf("completion calls = list %d cleanup %d deletion %d", store.listCalls, store.cleanupCalls, store.deletionCalls)
			}
			if refresh.calls != 0 {
				t.Fatalf("completion unexpectedly refreshed Emby %d times", refresh.calls)
			}
		})
	}
}
