package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

// eventsRetentionBatchSize 每批删除的最大事件数，分批避免长事务。
const eventsRetentionBatchSize int32 = 1000

// EventsRetentionConfiguration 读取运行时配置以获取事件保留期。
type EventsRetentionConfiguration interface {
	Load(context.Context) (domain.Configuration, error)
}

// EventsRetentionStore 删除早于截止时间的可安全丢弃事件，返回实际删除行数。
type EventsRetentionStore interface {
	DeleteExpired(context.Context, time.Time, int32) (int64, error)
}

// EventsRetentionWorker 由 River 周期任务触发，按保留期分批清理过期事件。
// 保留期为 0 时跳过清理，允许保留全部事件历史；
// 仅清理流式/操作审计类事件，read model 依赖的 provenance 事件始终保留。
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
	for {
		deleted, err := worker.store.DeleteExpired(ctx, cutoff, eventsRetentionBatchSize)
		if err != nil {
			return fmt.Errorf("delete expired events: %w", err)
		}
		if deleted < int64(eventsRetentionBatchSize) {
			return nil
		}
	}
}
