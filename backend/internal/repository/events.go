package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type eventQueries interface {
	GetEvent(context.Context, pgtype.UUID) (db.Event, error)
	GetEventStats(context.Context) (db.GetEventStatsRow, error)
	ListEvents(context.Context, int32) ([]db.Event, error)
	ListEventsAfter(context.Context, db.ListEventsAfterParams) ([]db.Event, error)
	DeleteExpiredEvents(context.Context, db.DeleteExpiredEventsParams) (int64, error)
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

func (repository *Events) Stats(ctx context.Context) (domain.EventStats, error) {
	row, err := repository.queries.GetEventStats(ctx)
	if err != nil {
		return domain.EventStats{}, fmt.Errorf("get event stats: %w", err)
	}
	stats := domain.EventStats{Count: row.EventCount}
	if row.EarliestOccurredAt.Valid {
		earliest := row.EarliestOccurredAt.Time.UTC()
		stats.EarliestOccurredAt = &earliest
	}
	return stats, nil
}

// DeleteExpired 分批删除早于 before 且在 fail-closed allowlist 中的事件，
// 结构化 provenance 事实与未知事件由独立存储/SQL 默认保留；每批最多删除 maxRows 行。
func (repository *Events) DeleteExpired(ctx context.Context, before time.Time, maxRows int32) (int64, error) {
	if maxRows <= 0 {
		return 0, fmt.Errorf("event deletion batch size must be positive")
	}
	deleted, err := repository.queries.DeleteExpiredEvents(ctx, db.DeleteExpiredEventsParams{
		Before:  pgtype.Timestamptz{Time: before, Valid: true},
		MaxRows: maxRows,
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired events: %w", err)
	}
	return deleted, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
