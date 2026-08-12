package worker

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type DownloadMaterializeStore interface {
	MaterializeDownload(context.Context, uuid.UUID, uuid.UUID) error
}

type DownloadMaterializeHandler struct {
	store DownloadMaterializeStore
}

func NewDownloadMaterializeHandler(store DownloadMaterializeStore) *DownloadMaterializeHandler {
	return &DownloadMaterializeHandler{store: store}
}

func (handler *DownloadMaterializeHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "download" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_download_operation", "download.materialize requires a download resource", nil)
	}
	if handler.store == nil {
		return permanentFailure("materialize_handler_not_configured", "download materialize storage is unavailable", nil)
	}
	if err := handler.store.MaterializeDownload(ctx, operation.ResourceID, operation.ID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("download_not_found", "the download no longer exists", err)
		}
		var workflowErr *domain.MediaWorkflowError
		if errors.As(err, &workflowErr) {
			return &Failure{Code: workflowErr.Code, Message: workflowErr.Message, Retryable: workflowErr.Retryable, Cause: err}
		}
		return retryableFailure("media_storage_unavailable", "the download could not be materialized", err)
	}
	return nil
}
