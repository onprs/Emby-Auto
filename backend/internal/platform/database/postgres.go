package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open creates a PostgreSQL pool. Connectivity is checked separately so the
// API can remain live while reporting not-ready during a database outage.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return OpenWithConnectTimeout(ctx, databaseURL, 0)
}

// OpenWithConnectTimeout 创建连接池，并在 connectTimeout 大于 0 时限制单条连接
// 建立过程（TCP 拨号、TLS 与认证握手）的总时长。建立连接不再仅依赖操作系统
// 的超时，不可达或静默目标会在限定时间内返回错误。
func OpenWithConnectTimeout(ctx context.Context, databaseURL string, connectTimeout time.Duration) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: invalid PostgreSQL connection string")
	}
	if connectTimeout > 0 {
		config.ConnConfig.ConnectTimeout = connectTimeout
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	return pool, nil
}
