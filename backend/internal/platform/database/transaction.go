package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
)

// TxScope exposes one PostgreSQL transaction to both generated queries and
// infrastructure that must commit atomically, such as River job insertion.
type TxScope struct {
	Tx      pgx.Tx
	Queries *db.Queries
}

// Transactor owns the transaction boundary used by application services.
type Transactor struct {
	pool *pgxpool.Pool
}

func NewTransactor(pool *pgxpool.Pool) *Transactor {
	return &Transactor{pool: pool}
}

func (transactor *Transactor) WithinTx(
	ctx context.Context,
	options pgx.TxOptions,
	fn func(TxScope) error,
) error {
	if err := pgx.BeginTxFunc(ctx, transactor.pool, options, func(tx pgx.Tx) error {
		return fn(TxScope{Tx: tx, Queries: db.New(tx)})
	}); err != nil {
		return fmt.Errorf("run PostgreSQL transaction: %w", err)
	}
	return nil
}
