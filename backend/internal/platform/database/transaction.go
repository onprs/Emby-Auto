package database

import (
	"context"
	"fmt"
	"sync"

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

// BeforeCommitHook 在业务函数成功后、事务提交前运行。
// 返回错误会回滚同一事务。
type BeforeCommitHook func(context.Context, TxScope) error

type namedBeforeCommitHook struct {
	name string
	hook BeforeCommitHook
}

// Transactor owns the transaction boundary used by application services.
type Transactor struct {
	pool *pgxpool.Pool

	hooksMu sync.RWMutex
	hooks   []namedBeforeCommitHook
}

func NewTransactor(pool *pgxpool.Pool) *Transactor {
	return &Transactor{pool: pool}
}

// RegisterBeforeCommitHook 按名称注册唯一钩子。重复注册会原位替换，
// 从而让共享 Transactor 的多个 workflow 构造保持幂等。
func (transactor *Transactor) RegisterBeforeCommitHook(name string, hook BeforeCommitHook) {
	if transactor == nil || name == "" || hook == nil {
		return
	}
	transactor.hooksMu.Lock()
	defer transactor.hooksMu.Unlock()
	for index := range transactor.hooks {
		if transactor.hooks[index].name == name {
			transactor.hooks[index].hook = hook
			return
		}
	}
	transactor.hooks = append(transactor.hooks, namedBeforeCommitHook{name: name, hook: hook})
}

func (transactor *Transactor) beforeCommitHooks() []namedBeforeCommitHook {
	transactor.hooksMu.RLock()
	defer transactor.hooksMu.RUnlock()
	return append([]namedBeforeCommitHook(nil), transactor.hooks...)
}

func (transactor *Transactor) WithinTx(
	ctx context.Context,
	options pgx.TxOptions,
	fn func(TxScope) error,
) error {
	return transactor.withinTx(ctx, options, true, fn)
}

// WithinReadTx 在只读事务中执行一致性读取，不运行提交前写入钩子。
func (transactor *Transactor) WithinReadTx(
	ctx context.Context,
	options pgx.TxOptions,
	fn func(TxScope) error,
) error {
	options.AccessMode = pgx.ReadOnly
	return transactor.withinTx(ctx, options, false, fn)
}

func (transactor *Transactor) withinTx(
	ctx context.Context,
	options pgx.TxOptions,
	runBeforeCommitHooks bool,
	fn func(TxScope) error,
) error {
	if err := pgx.BeginTxFunc(ctx, transactor.pool, options, func(tx pgx.Tx) error {
		scope := TxScope{Tx: tx, Queries: db.New(tx)}
		if err := fn(scope); err != nil {
			return err
		}
		if !runBeforeCommitHooks {
			return nil
		}
		for _, registered := range transactor.beforeCommitHooks() {
			if err := registered.hook(ctx, scope); err != nil {
				return fmt.Errorf("run %s before commit hook: %w", registered.name, err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("run PostgreSQL transaction: %w", err)
	}
	return nil
}
