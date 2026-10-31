package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func (service *ReadService) ListSearches(ctx context.Context, cursor *uuid.UUID, limit int, status *string, query *string) (domain.SearchRunSummaryPage, error) {
	params := db.ListSearchRunsParams{RowLimit: fetchLimit(limit)}
	if cursor != nil {
		params.Cursor = repository.UUIDToPG(*cursor)
	}
	if status != nil {
		params.Status = status
	}
	if query != nil && *query != "" {
		params.Query = query
	}
	rows, err := service.queries.ListSearchRuns(ctx, params)
	if err != nil {
		return domain.SearchRunSummaryPage{}, fmt.Errorf("list search runs: %w", err)
	}
	views := make([]domain.SearchRunSummary, 0, len(rows))
	for _, row := range rows {
		view := domain.SearchRunSummary{
			ID:        repository.UUIDFromPG(row.ID),
			Query:     row.Query,
			Status:    row.Status,
			CreatedAt: row.CreatedAt.Time,
			UpdatedAt: row.UpdatedAt.Time,
		}
		if row.ErrorCode != nil {
			view.ErrorCode = *row.ErrorCode
		}
		if row.ErrorMessage != nil {
			view.ErrorMessage = *row.ErrorMessage
		}
		views = append(views, view)
	}
	lim := clampLimit(limit)
	hasMore := len(views) > lim
	if hasMore {
		views = views[:lim]
	}
	return domain.SearchRunSummaryPage{
		Items:      views,
		NextCursor: pageCursor(hasMore, len(views), func(i int) uuid.UUID { return views[i].ID }),
	}, nil
}
