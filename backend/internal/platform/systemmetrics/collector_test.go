package systemmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

type sequenceSource struct {
	readings []HostReading
	index    int
	paths    [][]string
}

func (source *sequenceSource) Read(_ context.Context, paths []string) HostReading {
	source.paths = append(source.paths, append([]string(nil), paths...))
	reading := source.readings[source.index]
	if source.index < len(source.readings)-1 {
		source.index++
	}
	return reading
}

func TestCollectorCalculatesRatesFromAdjacentCounters(t *testing.T) {
	base := time.Date(2026, time.July, 26, 4, 0, 0, 0, time.UTC)
	source := &sequenceSource{readings: []HostReading{
		completeReading(12.5, 1_000, 2_000, 4_000, 8_000),
		completeReading(25, 1_400, 2_600, 5_000, 8_400),
	}}
	collector := NewCollector(source, Options{Interval: 2 * time.Second, MaxSamples: 60})
	collector.SetDiskPaths([]string{"D:/media/work", "D:/media/library"})

	collector.collectAt(context.Background(), base)
	collector.collectAt(context.Background(), base.Add(2*time.Second))

	snapshot := collector.Snapshot()
	if len(snapshot.Samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(snapshot.Samples))
	}
	first, second := snapshot.Samples[0], snapshot.Samples[1]
	if first.NetworkReceiveBytesPerSecond != nil || first.DiskReadBytesPerSecond != nil {
		t.Fatalf("first sample rates = %#v, want unavailable until a second counter exists", first)
	}
	assertMetric(t, "network receive", second.NetworkReceiveBytesPerSecond, 200)
	assertMetric(t, "network send", second.NetworkSendBytesPerSecond, 300)
	assertMetric(t, "disk read", second.DiskReadBytesPerSecond, 500)
	assertMetric(t, "disk write", second.DiskWriteBytesPerSecond, 200)
	assertMetric(t, "CPU", second.CPUUsedPercent, 25)
	assertMetric(t, "memory", second.MemoryUsedPercent, 50)
	if !snapshot.Availability.CPU || !snapshot.Availability.Memory || !snapshot.Availability.Network || !snapshot.Availability.DiskIO || !snapshot.Availability.DiskCapacity {
		t.Fatalf("availability = %#v, want all metrics available", snapshot.Availability)
	}
	if snapshot.Memory == nil || snapshot.Memory.UsedBytes != 8_000 || snapshot.Memory.TotalBytes != 16_000 {
		t.Fatalf("memory = %#v", snapshot.Memory)
	}
	if len(snapshot.Disks) != 1 || snapshot.Disks[0].Path != "D:" || snapshot.Disks[0].UsedPercent != 60 {
		t.Fatalf("disks = %#v", snapshot.Disks)
	}
	if len(source.paths) != 2 || len(source.paths[1]) != 2 || source.paths[1][0] != "D:/media/work" {
		t.Fatalf("source paths = %#v", source.paths)
	}
}

func TestCollectorClampsCounterResetsAndTrimsOldSamples(t *testing.T) {
	base := time.Date(2026, time.July, 26, 4, 5, 0, 0, time.UTC)
	source := &sequenceSource{readings: []HostReading{
		completeReading(10, 100, 100, 100, 100),
		completeReading(20, 300, 300, 300, 300),
		completeReading(30, 5, 7, 11, 13),
	}}
	collector := NewCollector(source, Options{Interval: time.Second, MaxSamples: 2})

	collector.collectAt(context.Background(), base)
	collector.collectAt(context.Background(), base.Add(time.Second))
	collector.collectAt(context.Background(), base.Add(2*time.Second))

	snapshot := collector.Snapshot()
	if len(snapshot.Samples) != 2 || !snapshot.Samples[0].SampledAt.Equal(base.Add(time.Second)) {
		t.Fatalf("sample window = %#v, want the newest two points", snapshot.Samples)
	}
	latest := snapshot.Samples[1]
	assertMetric(t, "reset network receive", latest.NetworkReceiveBytesPerSecond, 0)
	assertMetric(t, "reset network send", latest.NetworkSendBytesPerSecond, 0)
	assertMetric(t, "reset disk read", latest.DiskReadBytesPerSecond, 0)
	assertMetric(t, "reset disk write", latest.DiskWriteBytesPerSecond, 0)
	if snapshot.HistoryWindowSeconds != 2 || snapshot.SampleIntervalSeconds != 1 {
		t.Fatalf("window/interval = %d/%d, want 2/1", snapshot.HistoryWindowSeconds, snapshot.SampleIntervalSeconds)
	}

	snapshot.Samples[0].CPUUsedPercent = metric(99)
	fresh := collector.Snapshot()
	assertMetric(t, "snapshot copy", fresh.Samples[0].CPUUsedPercent, 20)
}

func TestCollectorKeepsPartialMetricsExplicitlyUnavailable(t *testing.T) {
	at := time.Date(2026, time.July, 26, 4, 10, 0, 0, time.UTC)
	source := &sequenceSource{readings: []HostReading{{
		CPUAvailable:     true,
		CPUUsedPercent:   37.5,
		MemoryAvailable:  false,
		NetworkAvailable: false,
		DiskIOAvailable:  false,
		DisksAvailable:   false,
	}}}
	collector := NewCollector(source, Options{Interval: 2 * time.Second, MaxSamples: 60})
	collector.collectAt(context.Background(), at)

	snapshot := collector.Snapshot()
	if len(snapshot.Samples) != 1 || snapshot.Samples[0].CPUUsedPercent == nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Samples[0].MemoryUsedPercent != nil || snapshot.Samples[0].NetworkReceiveBytesPerSecond != nil || snapshot.Samples[0].DiskReadBytesPerSecond != nil {
		t.Fatalf("unavailable values were exposed as samples: %#v", snapshot.Samples[0])
	}
	if snapshot.Memory != nil || len(snapshot.Disks) != 0 || snapshot.Availability.Memory || snapshot.Availability.Network || snapshot.Availability.DiskIO || snapshot.Availability.DiskCapacity {
		t.Fatalf("partial availability = %#v memory=%#v disks=%#v", snapshot.Availability, snapshot.Memory, snapshot.Disks)
	}
}

func completeReading(cpu float64, received, sent, read, written uint64) HostReading {
	return HostReading{
		CPUAvailable:      true,
		CPUUsedPercent:    cpu,
		MemoryAvailable:   true,
		MemoryTotalBytes:  16_000,
		MemoryUsedBytes:   8_000,
		MemoryUsedPercent: 50,
		NetworkAvailable:  true,
		NetworkBytesRecv:  received,
		NetworkBytesSent:  sent,
		DiskIOAvailable:   true,
		DiskReadBytes:     read,
		DiskWrittenBytes:  written,
		DisksAvailable:    true,
		Disks: []domain.SystemDiskUsage{{
			Device: "D:", Path: "D:", TotalBytes: 100_000, UsedBytes: 60_000, UsedPercent: 60,
		}},
	}
}

func metric(value float64) *float64 {
	return &value
}

func assertMetric(t *testing.T, name string, value *float64, want float64) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("%s = %v, want %v", name, value, want)
	}
}
