package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const backgroundRuntimeLockKey int64 = 0x454d425952554e

// BackgroundRuntimeLock 使 API 与维护命令共用同一个后台状态转换锁。
type BackgroundRuntimeLock struct {
	connectionConfig *pgx.ConnConfig
}

func NewBackgroundRuntimeLock(pool *pgxpool.Pool) *BackgroundRuntimeLock {
	lock := &BackgroundRuntimeLock{}
	if pool != nil {
		lock.connectionConfig = pool.Config().ConnConfig.Copy()
	}
	return lock
}

func (lock *BackgroundRuntimeLock) Lock(ctx context.Context) (func(), error) {
	if lock == nil || lock.connectionConfig == nil {
		return nil, errors.New("后台运行锁未配置")
	}
	// 独立会话持有锁，业务连接池只有一个连接时仍可读取配置和执行事务。
	connection, err := pgx.ConnectConfig(ctx, lock.connectionConfig.Copy())
	if err != nil {
		return nil, err
	}
	closeConnection := func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = connection.Close(closeCtx)
	}
	if _, err := connection.Exec(ctx, "SELECT pg_advisory_lock($1)", backgroundRuntimeLockKey); err != nil {
		// 取消可能发生在服务端取得锁之后，关闭连接可确保没有遗留会话锁。
		closeConnection()
		return nil, err
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var unlocked bool
		_ = connection.QueryRow(unlockCtx, "SELECT pg_advisory_unlock($1)", backgroundRuntimeLockKey).Scan(&unlocked)
		closeConnection()
	}, nil
}
