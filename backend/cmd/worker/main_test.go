package main

import (
	"testing"
	"time"

	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
)

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
