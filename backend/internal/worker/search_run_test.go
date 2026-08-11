package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type searchClientStub struct {
	query  string
	result domain.SearchProviderResult
	err    error
}

func (stub *searchClientStub) Search(_ context.Context, query string) (domain.SearchProviderResult, error) {
	stub.query = query
	return stub.result, stub.err
}

type searchRunStoreStub struct {
	command       domain.SearchCommand
	beginErr      error
	completedID   uuid.UUID
	completedOpID uuid.UUID
	completed     domain.SearchProviderResult
	completeErr   error
}

func (stub *searchRunStoreStub) BeginSearch(context.Context, uuid.UUID, uuid.UUID) (domain.SearchCommand, error) {
	return stub.command, stub.beginErr
}

func (stub *searchRunStoreStub) CompleteSearch(_ context.Context, id uuid.UUID, operationID uuid.UUID, result domain.SearchProviderResult) error {
	stub.completedID = id
	stub.completedOpID = operationID
	stub.completed = result
	return stub.completeErr
}

func TestSearchRunHandlerPersistsCandidatesAndPartialProviderFailures(t *testing.T) {
	searchID := uuid.MustParse("72000000-0000-0000-0000-000000000001")
	operationID := uuid.MustParse("72000000-0000-0000-0000-000000000002")
	result := domain.SearchProviderResult{
		Candidates: []domain.ReleaseCandidate{{Provider: "dmhy", Title: "Canonical Show", IdentityKey: "title:one"}},
		Failures:   []domain.SearchProviderFailure{{Provider: "mikan", Code: "search_provider_failed", Message: "HTTP 503"}},
	}
	client := &searchClientStub{result: result}
	store := &searchRunStoreStub{command: domain.SearchCommand{ID: searchID, Query: "Canonical Show", Status: domain.SearchRunning}}
	handler := NewSearchRunHandler(client, store)

	err := handler.Handle(context.Background(), domain.Operation{
		ID:           operationID,
		ResourceType: "search_run",
		ResourceID:   searchID,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if client.query != "Canonical Show" || store.completedID != searchID || store.completedOpID != operationID {
		t.Fatalf("calls = query %q search %s operation %s", client.query, store.completedID, store.completedOpID)
	}
	if len(store.completed.Candidates) != 1 || len(store.completed.Failures) != 1 {
		t.Fatalf("completed result = %#v", store.completed)
	}
}

func TestConfiguredSearchRunHandlerCreatesClientForEachRunnableOperation(t *testing.T) {
	searchID := uuid.MustParse("72000000-0000-0000-0000-000000000007")
	client := &searchClientStub{result: domain.SearchProviderResult{}}
	factoryCalls := 0
	store := &searchRunStoreStub{command: domain.SearchCommand{ID: searchID, Query: "Show", Status: domain.SearchRunning}}
	handler := NewConfiguredSearchRunHandler(func(context.Context) (SearchClient, error) {
		factoryCalls++
		return client, nil
	}, store)

	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "search_run", ResourceID: searchID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if factoryCalls != 1 || client.query != "Show" {
		t.Fatalf("factory calls = %d, query = %q", factoryCalls, client.query)
	}
}

func TestSearchRunHandlerRetriesWhenAllProvidersFail(t *testing.T) {
	searchID := uuid.MustParse("72000000-0000-0000-0000-000000000003")
	client := &searchClientStub{err: errors.New("providers unavailable")}
	store := &searchRunStoreStub{command: domain.SearchCommand{ID: searchID, Query: "Show", Status: domain.SearchRunning}}
	err := NewSearchRunHandler(client, store).Handle(context.Background(), domain.Operation{
		ID:           uuid.MustParse("72000000-0000-0000-0000-000000000004"),
		ResourceType: "search_run",
		ResourceID:   searchID,
	})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable || failure.Code != "search_providers_unavailable" {
		t.Fatalf("Handle() error = %#v", err)
	}
	if store.completedID != uuid.Nil {
		t.Fatalf("CompleteSearch() called for %s", store.completedID)
	}
}

func TestSearchRunHandlerTreatsCompletedRunAsIdempotentReplay(t *testing.T) {
	searchID := uuid.MustParse("72000000-0000-0000-0000-000000000005")
	client := &searchClientStub{}
	store := &searchRunStoreStub{command: domain.SearchCommand{ID: searchID, Query: "Show", Status: domain.SearchCompleted}}
	err := NewSearchRunHandler(client, store).Handle(context.Background(), domain.Operation{
		ID:           uuid.MustParse("72000000-0000-0000-0000-000000000006"),
		ResourceType: "search_run",
		ResourceID:   searchID,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if client.query != "" {
		t.Fatalf("Search() query = %q, want no call", client.query)
	}
}
