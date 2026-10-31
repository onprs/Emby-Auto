package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

var errListProjectionNotReady = errors.New("list projection is not ready")

// withListSnapshot 保证游标、投影与当前页详情来自同一只读快照。
// 维护写入留下的过期投影先在快照外补算，重试有明确上限。
func (service *ReadService) withListSnapshot(ctx context.Context, kind listProjectionKind, fn func(*ReadService) error) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := service.transactor.WithinReadTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(scope database.TxScope) error {
			kinds := []listProjectionKind{}
			if kind != "" {
				kinds = append(kinds, acquisitionListProjection)
			}
			if kind == rssEntryListProjection {
				kinds = append(kinds, rssEntryListProjection)
			}
			for _, requested := range kinds {
				state, err := scope.Queries.GetListProjectionReadiness(ctx, db.GetListProjectionReadinessParams{
					Kind: string(requested), TargetModelVersion: listProjectionVersion,
				})
				if err != nil {
					return fmt.Errorf("inspect list snapshot: %w", err)
				}
				if state.HasNewerModel {
					return newerListProjectionError()
				}
				if state.NeedsRefresh {
					return errListProjectionNotReady
				}
			}
			reader := *service
			reader.queries = scope.Queries
			return fn(&reader)
		})
		if !errors.Is(err, errListProjectionNotReady) {
			return err
		}
		if err := service.ReconcileListProjections(ctx); err != nil {
			return err
		}
	}
	return NewError("list_projection_busy", "列表状态正在更新，请稍后重试", ErrUnavailable, nil)
}

func listPageWindow(ids []pgtype.UUID, limit int, cursor *uuid.UUID) ([]pgtype.UUID, *uuid.UUID, error) {
	if ids == nil {
		details := map[string]any{}
		if cursor != nil {
			details["cursor"] = cursor.String()
		}
		return nil, nil, NewError("invalid_cursor", "列表游标不存在或已不属于当前筛选结果", ErrInvalidInput, details)
	}
	limit = clampLimit(limit)
	if len(ids) <= limit {
		return ids, nil, nil
	}
	ids = ids[:limit]
	next := repository.UUIDFromPG(ids[len(ids)-1])
	return ids, &next, nil
}

func listCursorValue(cursor *uuid.UUID) pgtype.UUID {
	if cursor == nil {
		return pgtype.UUID{}
	}
	return repository.UUIDToPG(*cursor)
}

func orderedListRows[T any](ids []pgtype.UUID, rows []T, idOf func(T) pgtype.UUID) ([]T, error) {
	byID := make(map[pgtype.UUID]T, len(rows))
	for _, row := range rows {
		byID[idOf(row)] = row
	}
	result := make([]T, 0, len(ids))
	for _, id := range ids {
		row, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("list snapshot detail missing for %s", repository.UUIDFromPG(id))
		}
		result = append(result, row)
	}
	return result, nil
}
