package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func (service *ReadService) ListOperations(ctx context.Context, cursor *uuid.UUID, limit int, resourceType *string, resourceID *uuid.UUID, status *string) (domain.OperationPage, error) {
	params := db.ListOperationsParams{RowLimit: fetchLimit(limit)}
	if cursor != nil {
		params.Cursor = repository.UUIDToPG(*cursor)
	}
	if resourceType != nil {
		params.ResourceType = resourceType
	}
	if resourceID != nil {
		params.ResourceID = repository.UUIDToPG(*resourceID)
	}
	if status != nil {
		params.Status = status
	}
	rows, err := service.queries.ListOperations(ctx, params)
	if err != nil {
		return domain.OperationPage{}, fmt.Errorf("list operations: %w", err)
	}
	views := operationViews(rows, nil)
	lim := clampLimit(limit)
	hasMore := len(views) > lim
	if hasMore {
		views = views[:lim]
	}
	return domain.OperationPage{
		Items:      views,
		NextCursor: pageCursor(hasMore, len(views), func(i int) uuid.UUID { return views[i].ID }),
	}, nil
}

func (service *ReadService) GetOperation(ctx context.Context, id uuid.UUID) (domain.OperationView, error) {
	row, err := service.queries.GetOperationByID(ctx, repository.UUIDToPG(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.OperationView{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.OperationView{}, fmt.Errorf("get operation: %w", err)
	}
	attempts, err := service.queries.ListOperationAttempts(ctx, row.ID)
	if err != nil {
		return domain.OperationView{}, fmt.Errorf("list operation attempts: %w", err)
	}
	views := operationViews([]db.Operation{row}, attempts)
	return views[0], nil
}

func operationViews(rows []db.Operation, attempts []db.OperationAttempt) []domain.OperationView {
	byOperation := make(map[uuid.UUID][]domain.OperationAttemptView, len(rows))
	for _, attempt := range attempts {
		operationID := repository.UUIDFromPG(attempt.OperationID)
		byOperation[operationID] = append(byOperation[operationID], operationAttemptView(attempt))
	}
	views := make([]domain.OperationView, 0, len(rows))
	for _, row := range rows {
		id := repository.UUIDFromPG(row.ID)
		view := domain.OperationView{
			ID:             id,
			Kind:           row.Kind,
			Status:         row.Status,
			IdempotencyKey: row.IdempotencyKey,
			MaxAttempts:    int(row.MaxAttempts),
			AttemptCount:   int(row.AttemptCount),
			CreatedAt:      row.CreatedAt.Time,
			UpdatedAt:      row.UpdatedAt.Time,
			Attempts:       byOperation[id],
		}
		if view.Attempts == nil {
			view.Attempts = []domain.OperationAttemptView{}
		}
		if row.ResourceType != nil {
			view.ResourceType = *row.ResourceType
		}
		if row.ResourceID.Valid {
			value := repository.UUIDFromPG(row.ResourceID)
			view.ResourceID = &value
			view.ResourceHref = resourceHref(view.ResourceType, value)
		}
		if row.HeartbeatAt.Valid {
			value := row.HeartbeatAt.Time
			view.HeartbeatAt = &value
		}
		if row.ErrorCode != nil {
			view.ErrorCode = *row.ErrorCode
		}
		if row.ErrorMessage != nil {
			view.ErrorMessage = *row.ErrorMessage
		}
		if row.StartedAt.Valid {
			value := row.StartedAt.Time
			view.StartedAt = &value
		}
		if row.FinishedAt.Valid {
			value := row.FinishedAt.Time
			view.FinishedAt = &value
		}
		views = append(views, view)
	}
	return views
}

func resourceHref(resourceType string, resourceID uuid.UUID) string {
	switch resourceType {
	case "agent_resolution":
		return "/agent/resolutions/" + resourceID.String()
	case "download":
		return "/downloads/" + resourceID.String()
	case "episode_task":
		return "/tasks/" + resourceID.String()
	case "acquisition":
		return "/acquisitions/" + resourceID.String()
	case "rss_subscription":
		return "/rss/" + resourceID.String()
	case "search", "search_run":
		return "/searches/" + resourceID.String()
	case "emby_scan":
		return "/emby/scans/" + resourceID.String()
	case "emby_catalog":
		return "/emby"
	default:
		return ""
	}
}

func operationAttemptView(row db.OperationAttempt) domain.OperationAttemptView {
	view := domain.OperationAttemptView{
		ID:        repository.UUIDFromPG(row.ID),
		Attempt:   int(row.Attempt),
		Status:    row.Status,
		StartedAt: row.StartedAt.Time,
	}
	if row.WorkerID != nil {
		view.WorkerID = *row.WorkerID
	}
	if row.ErrorCode != nil {
		view.ErrorCode = *row.ErrorCode
	}
	if row.ErrorMessage != nil {
		view.ErrorMessage = *row.ErrorMessage
	}
	if row.HeartbeatAt.Valid {
		value := row.HeartbeatAt.Time
		view.HeartbeatAt = &value
	}
	if row.FinishedAt.Valid {
		value := row.FinishedAt.Time
		view.FinishedAt = &value
	}
	return view
}
