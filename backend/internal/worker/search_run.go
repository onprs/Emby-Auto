package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type SearchClient interface {
	Search(context.Context, string) (domain.SearchProviderResult, error)
}

type SearchClientFactory func(context.Context) (SearchClient, error)

type SearchRunStore interface {
	BeginSearch(context.Context, uuid.UUID, uuid.UUID) (domain.SearchCommand, error)
	CompleteSearch(context.Context, uuid.UUID, uuid.UUID, domain.SearchProviderResult) error
}

type SearchRunHandler struct {
	client    SearchClient
	newClient SearchClientFactory
	store     SearchRunStore
}

func NewSearchRunHandler(client SearchClient, store SearchRunStore) *SearchRunHandler {
	return &SearchRunHandler{client: client, store: store}
}

func NewConfiguredSearchRunHandler(newClient SearchClientFactory, store SearchRunStore) *SearchRunHandler {
	return &SearchRunHandler{newClient: newClient, store: store}
}

func (handler *SearchRunHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "search_run" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_search_operation", "search.run requires a search run resource", nil)
	}
	if (handler.client == nil && handler.newClient == nil) || handler.store == nil {
		return permanentFailure("search_handler_not_configured", "search handler dependencies are unavailable", nil)
	}
	command, err := handler.store.BeginSearch(ctx, operation.ResourceID, operation.ID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("search_not_found", "the search run no longer exists", err)
		}
		return retryableFailure("search_storage_unavailable", "search storage is unavailable", err)
	}
	if command.ID != operation.ResourceID {
		return permanentFailure("search_resource_mismatch", "the operation does not match its search run", nil)
	}
	switch command.Status {
	case domain.SearchCompleted:
		return nil
	case domain.SearchFailed, domain.SearchCancelled:
		return permanentFailure("search_state_conflict", fmt.Sprintf("search cannot run from status %q", command.Status), nil)
	case domain.SearchQueued, domain.SearchRunning:
	default:
		return permanentFailure("search_state_conflict", fmt.Sprintf("search has unknown status %q", command.Status), nil)
	}

	client := handler.client
	if handler.newClient != nil {
		client, err = handler.newClient(ctx)
		if err != nil {
			return retryableFailure("search_client_unavailable", "the search client could not be configured", err)
		}
	}
	result, err := client.Search(ctx, command.Query)
	if err != nil {
		return retryableFailure("search_providers_unavailable", "all configured search providers are unavailable", err)
	}
	if err := handler.store.CompleteSearch(ctx, command.ID, operation.ID, result); err != nil {
		return retryableFailure("search_storage_unavailable", "search candidates could not be persisted", err)
	}
	return nil
}
