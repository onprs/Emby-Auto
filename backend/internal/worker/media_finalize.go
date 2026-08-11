package worker

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type MediaFinalizeStore interface {
	LoadFinalizeCommand(context.Context, uuid.UUID) (domain.FinalizeMediaCommand, error)
	CompleteFinalize(context.Context, uuid.UUID, uuid.UUID) error
}

type MediaFinalizeHandler struct {
	store MediaFinalizeStore
}

func NewMediaFinalizeHandler(store MediaFinalizeStore) *MediaFinalizeHandler {
	return &MediaFinalizeHandler{store: store}
}

func (handler *MediaFinalizeHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "episode_task" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_finalize_operation", "media.finalize requires an episode task resource", nil)
	}
	if handler.store == nil {
		return permanentFailure("finalize_handler_not_configured", "media finalization storage is unavailable", nil)
	}
	command, err := handler.store.LoadFinalizeCommand(ctx, operation.ResourceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("task_artifacts_not_found", "the episode task or its paired artifacts do not exist", err)
		}
		return mediaStoreFailure("finalize", err)
	}
	if command.TaskID != operation.ResourceID {
		return permanentFailure("finalize_resource_mismatch", "the operation does not match its episode task", nil)
	}
	switch command.State {
	case domain.TaskAwaitingReview, domain.TaskImportQueued, domain.TaskImporting, domain.TaskImported:
		return nil
	}
	if command.State != domain.TaskFinalizing {
		return permanentFailure("task_finalize_state_conflict", "the episode task is not ready for finalization", nil)
	}
	if command.Video.BaseName != command.Subtitle.BaseName {
		return permanentFailure("artifact_basename_mismatch", "video and subtitle artifacts do not share a basename", nil)
	}
	if err := verifyArtifactFile(command.Video); err != nil {
		return permanentFailure("video_artifact_invalid", "the video artifact does not match its database identity", err)
	}
	if err := verifyArtifactFile(command.Subtitle); err != nil {
		return permanentFailure("subtitle_artifact_invalid", "the subtitle artifact does not match its database identity", err)
	}
	if err := handler.store.CompleteFinalize(ctx, command.TaskID, operation.ID); err != nil {
		return mediaStoreFailure("finalize", err)
	}
	return nil
}
