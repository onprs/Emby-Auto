package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/platform/config"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
)

func TestRSSSubscriptionProgressReconciliationScheduleAndUniquenessAreMinutely(t *testing.T) {
	args, options := rssSubscriptionProgressReconciliationJob()
	if args.Kind() != appqueue.KindRSSSubscriptionProgressReconcile {
		t.Fatalf("job kind = %q, want %q", args.Kind(), appqueue.KindRSSSubscriptionProgressReconcile)
	}
	if options.MaxAttempts != 3 || options.Queue != appqueue.QueueGeneral {
		t.Fatalf("insert options = %#v, want three attempts on the general queue", options)
	}
	if !options.UniqueOpts.ByArgs || !options.UniqueOpts.ByQueue ||
		options.UniqueOpts.ByPeriod != rssSubscriptionProgressReconciliationPeriod {
		t.Fatalf("unique options = %#v, want args/queue uniqueness in one-minute periods", options.UniqueOpts)
	}
	if newRSSSubscriptionProgressReconciliationPeriodicJob() == nil {
		t.Fatal("newRSSSubscriptionProgressReconciliationPeriodicJob() = nil")
	}
}

func TestRSSPollReconciliationScheduleAndUniquenessAreMinutely(t *testing.T) {
	args, options := rssPollReconciliationJob()
	if args.Kind() != appqueue.KindRSSPollReconcile {
		t.Fatalf("job kind = %q, want %q", args.Kind(), appqueue.KindRSSPollReconcile)
	}
	if options.MaxAttempts != 3 || options.Queue != appqueue.QueueGeneral {
		t.Fatalf("insert options = %#v, want three attempts on the general queue", options)
	}
	if !options.UniqueOpts.ByArgs || !options.UniqueOpts.ByQueue ||
		options.UniqueOpts.ByPeriod != rssPollReconciliationPeriod {
		t.Fatalf("unique options = %#v, want args/queue uniqueness in one-minute periods", options.UniqueOpts)
	}
	if newRSSPollReconciliationPeriodicJob() == nil {
		t.Fatal("newRSSPollReconciliationPeriodicJob() = nil")
	}
}

func TestEventsRetentionCleanupScheduleAndUniquenessAreHourly(t *testing.T) {
	startedAt := time.Date(2026, 8, 16, 12, 15, 0, 0, time.UTC)
	if next := eventsRetentionCleanupSchedule().Next(startedAt); !next.Equal(startedAt.Add(time.Hour)) {
		t.Fatalf("Next() = %v, want %v", next, startedAt.Add(time.Hour))
	}

	args, options := eventsRetentionCleanupJob()
	if args.Kind() != appqueue.KindEventsRetentionCleanup {
		t.Fatalf("job kind = %q, want %q", args.Kind(), appqueue.KindEventsRetentionCleanup)
	}
	if options.MaxAttempts != 3 || options.Queue != appqueue.QueueGeneral {
		t.Fatalf("insert options = %#v, want three attempts on the general queue", options)
	}
	if !options.UniqueOpts.ByArgs || !options.UniqueOpts.ByQueue || options.UniqueOpts.ByPeriod != time.Hour {
		t.Fatalf("unique options = %#v, want args/queue uniqueness in one-hour periods", options.UniqueOpts)
	}

	firstBucket := startedAt.Truncate(options.UniqueOpts.ByPeriod)
	if sameHour := startedAt.Add(30 * time.Minute).Truncate(options.UniqueOpts.ByPeriod); !sameHour.Equal(firstBucket) {
		t.Fatalf("same-hour insert bucket = %v, want %v", sameHour, firstBucket)
	}
	if nextHour := startedAt.Add(time.Hour).Truncate(options.UniqueOpts.ByPeriod); nextHour.Equal(firstBucket) {
		t.Fatalf("next-hour insert bucket = %v, must differ from %v", nextHour, firstBucket)
	}

	if newEventsRetentionPeriodicJob() == nil {
		t.Fatal("newEventsRetentionPeriodicJob() = nil")
	}
}

// 停止信号属于正常停机：waitForInstalledRuntime 返回的 context 取消错误
// 必须与 API 的停止路径一致，让 Worker 干净退出而不是以失败退出码结束。
func TestWorkerStopSignalExitsCleanly(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := waitForInstalledRuntime(parent, config.Config{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForInstalledRuntime() error = %v, want context.Canceled", err)
	}
	if workerStopExitCode(err) != 0 {
		t.Fatalf("workerStopExitCode(context.Canceled) = %d, want 0", workerStopExitCode(err))
	}
	if workerStopExitCode(errors.New("transcode failed")) != 1 {
		t.Fatalf("workerStopExitCode(ordinary error) != 1")
	}
}
