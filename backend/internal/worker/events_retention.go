package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

const (
	// eventsRetentionBatchSize 限制单次事务删除的事件数。
	eventsRetentionBatchSize int32 = 1000
	// eventsRetentionMaxBatches 限制单个小时任务的总工作量；积压由后续任务继续清理。
	eventsRetentionMaxBatches = 10
)

// EventsRetentionConfiguration 读取运行时配置以获取事件保留期。
type EventsRetentionConfiguration interface {
	Load(context.Context) (domain.Configuration, error)
}

// EventsRetentionStore 删除早于截止时间的可安全丢弃事件，返回实际删除行数。
type EventsRetentionStore interface {
	DeleteExpired(context.Context, time.Time, int32) (int64, error)
}

// EventsRetentionWorker 由 River 周期任务触发，按保留期分批清理过期事件。
// 保留期为 0 时跳过清理，允许保留全部事件历史；每个任务最多删除 10 批，积压由后续小时任务继续清理；
// 仅清理 fail-closed allowlist 中可由业务表恢复的事件，provenance 与未知事件始终保留。
type EventsRetentionWorker struct {
	river.WorkerDefaults[appqueue.EventsRetentionCleanupArgs]
	configuration EventsRetentionConfiguration
	store         EventsRetentionStore
	now           func() time.Time
}

func NewEventsRetentionWorker(
	configuration EventsRetentionConfiguration,
	store EventsRetentionStore,
) *EventsRetentionWorker {
	return &EventsRetentionWorker{
		configuration: configuration,
		store:         store,
		now:           time.Now,
	}
}

func (worker *EventsRetentionWorker) Work(
	ctx context.Context,
	_ *river.Job[appqueue.EventsRetentionCleanupArgs],
) error {
	if worker.configuration == nil || worker.store == nil {
		return fmt.Errorf("events retention worker dependencies are unavailable")
	}
	configuration, err := worker.configuration.Load(ctx)
	if err != nil {
		return fmt.Errorf("load configuration for events retention: %w", err)
	}
	retentionDays := configuration.Settings.Events.RetentionDays
	if retentionDays <= 0 {
		// 保留清理已禁用，保留全部事件历史。
		return nil
	}
	cutoff := worker.now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	for range eventsRetentionMaxBatches {
		deleted, err := worker.store.DeleteExpired(ctx, cutoff, eventsRetentionBatchSize)
		if err != nil {
			return fmt.Errorf("delete expired events: %w", err)
		}
		if deleted < int64(eventsRetentionBatchSize) {
			return nil
		}
	}
	return nil
}
