package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func (service *ReadService) ListResourceEvents(ctx context.Context, resourceType string, resourceID uuid.UUID, cursor *uuid.UUID, limit int) (domain.EventRecordPage, error) {
	params := db.ListResourceEventsParams{
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(resourceID),
		RowLimit:     fetchLimit(limit),
	}
	if cursor != nil {
		params.Cursor = repository.UUIDToPG(*cursor)
	}
	rows, err := service.queries.ListResourceEvents(ctx, params)
	if err != nil {
		return domain.EventRecordPage{}, fmt.Errorf("list resource events: %w", err)
	}
	views := make([]domain.EventRecordView, 0, len(rows))
	for _, row := range rows {
		view := domain.EventRecordView{
			ID:         repository.UUIDFromPG(row.ID),
			Topic:      row.Topic,
			Data:       row.Data,
			OccurredAt: row.OccurredAt.Time,
		}
		if row.ResourceType != nil {
			view.ResourceType = *row.ResourceType
		}
		if row.ResourceID.Valid {
			value := repository.UUIDFromPG(row.ResourceID)
			view.ResourceID = &value
		}
		if row.OperationID.Valid {
			value := repository.UUIDFromPG(row.OperationID)
			view.OperationID = &value
		}
		views = append(views, view)
	}
	lim := clampLimit(limit)
	hasMore := len(views) > lim
	if hasMore {
		views = views[:lim]
	}
	return domain.EventRecordPage{
		Items:      views,
		NextCursor: pageCursor(hasMore, len(views), func(i int) uuid.UUID { return views[i].ID }),
	}, nil
}
