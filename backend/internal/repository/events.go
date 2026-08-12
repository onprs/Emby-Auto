package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type eventQueries interface {
	GetEvent(context.Context, pgtype.UUID) (db.Event, error)
	ListEvents(context.Context, int32) ([]db.Event, error)
	ListEventsAfter(context.Context, db.ListEventsAfterParams) ([]db.Event, error)
}

type Events struct {
	queries eventQueries
}

func NewEvents(queries eventQueries) *Events {
	return &Events{queries: queries}
}

func (repository *Events) List(
	ctx context.Context,
	cursor *uuid.UUID,
	limit int32,
) ([]domain.Event, error) {
	var (
		rows []db.Event
		err  error
	)
	if cursor == nil {
		rows, err = repository.queries.ListEvents(ctx, limit)
	} else {
		if _, getErr := repository.queries.GetEvent(ctx, UUIDToPG(*cursor)); errors.Is(getErr, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		} else if getErr != nil {
			return nil, fmt.Errorf("validate event cursor: %w", getErr)
		}
		rows, err = repository.queries.ListEventsAfter(ctx, db.ListEventsAfterParams{
			CursorID: UUIDToPG(*cursor),
			PageSize: limit,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}

	events := make([]domain.Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, domain.Event{
			ID:           UUIDFromPG(row.ID),
			Topic:        row.Topic,
			ResourceType: valueOrEmpty(row.ResourceType),
			ResourceID:   UUIDFromPG(row.ResourceID),
			OperationID:  UUIDFromPG(row.OperationID),
			Data:         row.Data,
			OccurredAt:   row.OccurredAt.Time,
		})
	}
	return events, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
