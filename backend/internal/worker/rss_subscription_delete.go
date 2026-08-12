package worker

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

// RSSDeletionStore resolves all acquisitions owned by one subscription, then
// records a retained completion or physically removes an archived subscription.
type RSSDeletionStore interface {
	SubscriptionDeletionReady(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	ListSubscriptionAcquisitions(context.Context, uuid.UUID) ([]uuid.UUID, error)
	CompleteSubscriptionCleanup(context.Context, uuid.UUID, uuid.UUID) error
	CompleteSubscriptionDeletion(context.Context, uuid.UUID, uuid.UUID) error
}

// RSSSubscriptionDeleteHandler keeps legacy completion jobs harmless while
// manual subscription deletion continues to own torrent, filesystem, and
// database removal.
type RSSSubscriptionDeleteHandler struct {
	store    RSSDeletionStore
	deletion *AcquisitionDeleteHandler
	refresh  Handler
}

func NewRSSSubscriptionDeleteHandler(store RSSDeletionStore, deletion *AcquisitionDeleteHandler, refresh ...Handler) *RSSSubscriptionDeleteHandler {
	handler := &RSSSubscriptionDeleteHandler{store: store, deletion: deletion}
	if len(refresh) > 0 {
		handler.refresh = refresh[0]
	}
	return handler
}

func (handler *RSSSubscriptionDeleteHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "rss_subscription" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_rss_cleanup_operation", "RSS subscription cleanup requires an RSS subscription resource", nil)
	}
	if handler.store == nil || handler.deletion == nil {
		return permanentFailure("rss_cleanup_handler_not_configured", "RSS subscription cleanup dependencies are unavailable", nil)
	}
	var payload struct {
		SubscriptionID uuid.UUID `json:"subscriptionId"`
		DeleteImported bool      `json:"deleteImported"`
		Trigger        string    `json:"trigger"`
	}
	if len(operation.Payload) > 0 {
		_ = json.Unmarshal(operation.Payload, &payload)
	}
	if payload.SubscriptionID == uuid.Nil {
		payload.SubscriptionID = operation.ResourceID
	}
	ready, err := handler.store.SubscriptionDeletionReady(ctx, payload.SubscriptionID, operation.ID)
	if err != nil {
		return retryableFailure("rss_delete_storage_unavailable", "RSS deletion readiness could not be checked", err)
	}
	if !ready {
		return river.JobSnooze(acquisitionDeletionGuardInterval)
	}
	completion := operation.Kind == appqueue.KindRSSSubscriptionComplete || payload.Trigger == "final_import"
	if completion {
		if err := handler.store.CompleteSubscriptionCleanup(ctx, payload.SubscriptionID, operation.ID); err != nil {
			return retryableFailure("rss_cleanup_storage_unavailable", "completed RSS subscription retention could not be recorded", err)
		}
		return nil
	}
	acquisitionIDs, err := handler.store.ListSubscriptionAcquisitions(ctx, payload.SubscriptionID)
	if err != nil {
		return retryableFailure("rss_delete_storage_unavailable", "RSS task deletion resources could not be loaded", err)
	}
	for _, acquisitionID := range acquisitionIDs {
		if err := handler.deletion.deleteAcquisition(ctx, acquisitionID, operation.ID, payload.DeleteImported); err != nil {
			return err
		}
	}
	if err := handler.store.CompleteSubscriptionDeletion(ctx, payload.SubscriptionID, operation.ID); err != nil {
		return retryableFailure("rss_delete_storage_unavailable", "RSS subscription records could not be deleted", err)
	}
	if handler.refresh != nil && payload.DeleteImported {
		if err := handler.refresh.Handle(ctx, domain.Operation{ID: operation.ID, ResourceType: "emby_catalog", ResourceID: payload.SubscriptionID}); err != nil {
			return err
		}
	}
	return nil
}
