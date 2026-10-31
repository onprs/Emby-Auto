package service

import (
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

// ReadService exposes paginated, database-backed read models for the API.
type ReadService struct {
	queries    *db.Queries
	transactor *database.Transactor
}

func NewReadService(queries *db.Queries, transactor *database.Transactor) *ReadService {
	RegisterProgressCommitHook(transactor)
	return &ReadService{queries: queries, transactor: transactor}
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func pageCursor(hasMore bool, items int, idOf func(int) uuid.UUID) *uuid.UUID {
	if !hasMore || items == 0 {
		return nil
	}
	last := idOf(items - 1)
	return &last
}

func listSortDirection(order *string, defaultOrder string) int {
	value := defaultOrder
	if order != nil {
		value = *order
	}
	if value == "desc" {
		return -1
	}
	return 1
}

// fetchLimit reads one extra row to determine whether another page exists.
func fetchLimit(limit int) int32 {
	return int32(clampLimit(limit) + 1)
}

func numericToFloat(value pgtype.Numeric) float64 {
	if !value.Valid {
		return 0
	}
	float, err := value.Float64Value()
	if err != nil {
		return 0
	}
	return float.Float64
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func laterTimePointer(current *time.Time, candidate time.Time) *time.Time {
	if candidate.IsZero() || (current != nil && !candidate.After(*current)) {
		return current
	}
	value := candidate
	return &value
}

func timeValue(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

func saturatingInt(value int64) int {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(value)
}
