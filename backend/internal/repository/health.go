package repository

import (
	"context"
	"time"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
)

// DatabaseHealth checks the database through a generated sqlc query.
type DatabaseHealth struct {
	queries *db.Queries
	timeout time.Duration
}

func NewDatabaseHealth(database db.DBTX, timeout time.Duration) *DatabaseHealth {
	return &DatabaseHealth{queries: db.New(database), timeout: timeout}
}

func (health *DatabaseHealth) Ping(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, health.timeout)
	defer cancel()

	_, err := health.queries.CheckDatabase(checkCtx)
	return err
}
