package service

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// 恢复按订阅、目标剧集、获取记录的顺序加锁，与 RSS 入队及删除使用相同的资源顺序。
func lockWorkflowForRetry(ctx context.Context, scope database.TxScope, acquisitionID uuid.UUID, taskID pgtype.UUID, reclaimTargets, requireIdle bool) error {
	if _, err := scope.Queries.LockRSSSubscriptionForMappingAcquisition(ctx, repository.UUIDToPG(acquisitionID)); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock retry RSS subscription: %w", err)
	}
	params := db.ListWorkflowRetryTargetsParams{AcquisitionID: repository.UUIDToPG(acquisitionID), TaskID: taskID}
	targets, err := scope.Queries.ListWorkflowRetryTargets(ctx, params)
	if err != nil {
		return fmt.Errorf("load retry targets: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, repository.UUIDFromPG(target))
	}
	if err := LockRSSTargetEpisodes(ctx, scope, ids); err != nil {
		return err
	}
	acquisition, err := scope.Queries.LockAcquisitionForDeletion(ctx, repository.UUIDToPG(acquisitionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock retry acquisition: %w", err)
	}
	if acquisition.DeletionRequestedAt.Valid {
		return NewError("deletion_in_progress", "the content is being deleted", ErrStateConflict, map[string]any{})
	}
	currentTargets, err := scope.Queries.ListWorkflowRetryTargets(ctx, params)
	if err != nil {
		return fmt.Errorf("reload retry targets: %w", err)
	}
	if !slices.Equal(targets, currentTargets) {
		return NewError("state_conflict", "the episode mapping changed before retry", ErrStateConflict, map[string]any{})
	}
	sourceDeleted, err := scope.Queries.WorkflowRetrySourceDeleted(ctx, repository.UUIDToPG(acquisitionID))
	if err != nil {
		return fmt.Errorf("check retry source: %w", err)
	}
	if sourceDeleted {
		return NewError("deletion_in_progress", "the RSS subscription is being deleted", ErrStateConflict, map[string]any{})
	}
	pending, err := scope.Queries.WorkflowRetryHasPendingCancellation(ctx, db.WorkflowRetryHasPendingCancellationParams{
		AcquisitionID: repository.UUIDToPG(acquisitionID), TaskID: taskID, RequireIdle: requireIdle,
	})
	if err != nil {
		return fmt.Errorf("check pending cancellation: %w", err)
	}
	if pending {
		return NewError("operation_in_progress", "the previous operation is still stopping; retry after it has stopped", ErrStateConflict, map[string]any{})
	}
	if reclaimTargets {
		for _, target := range targets {
			conflict, err := scope.Queries.GetEpisodeMappingTargetOccupancy(ctx, db.GetEpisodeMappingTargetOccupancyParams{
				TargetEpisodeID: target, SeriesID: acquisition.SeriesID, ExcludedAcquisitionID: acquisition.ID,
			})
			if err != nil {
				return fmt.Errorf("check retry target occupancy: %w", err)
			}
			if conflict.ProcessingPresent || conflict.ManagedImportPresent || conflict.CatalogPresent {
				return NewError("target_episode_processing", "the episode is already handled by another task", ErrStateConflict, map[string]any{})
			}
		}
	}
	return nil
}
