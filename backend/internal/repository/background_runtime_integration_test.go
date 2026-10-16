//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestBackgroundRuntimeLockDoesNotExhaustSingleConnectionPoolIntegration(t *testing.T) {
	dsn, _ := testutil.NewMigratedPostgres(t)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	unlock, err := NewBackgroundRuntimeLock(pool).Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var value int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&value); err != nil || value != 1 {
		t.Fatalf("持锁后无法使用业务连接池：%v", err)
	}
}

func TestBackgroundRuntimeLockSerializesProcessesAndReleasesAfterCancellationIntegration(t *testing.T) {
	_, pool := testutil.NewMigratedPostgres(t)
	first := NewBackgroundRuntimeLock(pool)
	second := NewBackgroundRuntimeLock(pool)
	ctx, cancel := context.WithCancel(context.Background())
	unlock, err := first.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err = second.Lock(waitCtx)
	waitCancel()
	if err == nil {
		unlock()
		t.Fatal("两个控制入口同时持有后台转换锁")
	}
	cancel()
	unlock()
	followCtx, followCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer followCancel()
	followUnlock, err := second.Lock(followCtx)
	if err != nil {
		t.Fatalf("取消后遗留了后台转换锁：%v", err)
	}
	followUnlock()
	var stillLocked bool
	if err := pool.QueryRow(followCtx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))").Scan(&stillLocked); err != nil || stillLocked {
		t.Fatalf("维护锁未释放：%t，%v", stillLocked, err)
	}
}
