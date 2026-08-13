package systemmetrics

import (
	"context"
	"strings"
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

func TestCollectorSkipsDiskRateWhenDeviceSetChanges(t *testing.T) {
	changes := []struct {
		name    string
		before  string
		after   string
		settled string
	}{
		{name: "added", before: "nvme0n1", after: "nvme0n1,sda", settled: "nvme0n1,sda"},
		{name: "removed", before: "nvme0n1,sda", after: "nvme0n1", settled: "nvme0n1"},
		{name: "replaced", before: "nvme0n1", after: "sda", settled: "sda"},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			base := time.Date(2026, time.July, 26, 4, 2, 0, 0, time.UTC)
			source := &sequenceSource{readings: []HostReading{
				completeReadingWithDiskSet(10, 100, 100, 1_000, 2_000, change.before),
				completeReadingWithDiskSet(20, 200, 200, 51_000, 62_000, change.after),
				completeReadingWithDiskSet(30, 300, 300, 51_400, 62_600, change.settled),
			}}
			collector := NewCollector(source, Options{Interval: 2 * time.Second, MaxSamples: 60})

			collector.collectAt(context.Background(), base)
			collector.collectAt(context.Background(), base.Add(2*time.Second))
			collector.collectAt(context.Background(), base.Add(4*time.Second))

			samples := collector.Snapshot().Samples
			if samples[1].DiskReadBytesPerSecond != nil || samples[1].DiskWriteBytesPerSecond != nil {
				t.Fatalf("changed device set exposed rate: %#v", samples[1])
			}
			assertMetric(t, "settled disk read", samples[2].DiskReadBytesPerSecond, 200)
			assertMetric(t, "settled disk write", samples[2].DiskWriteBytesPerSecond, 300)
		})
	}
}

func TestCollectorTreatsReorderedDiskDeviceSetAsStable(t *testing.T) {
	base := time.Date(2026, time.July, 26, 4, 4, 0, 0, time.UTC)
	source := &sequenceSource{readings: []HostReading{
		completeReadingWithDiskSet(10, 100, 100, 1_000, 2_000, "sda,nvme0n1"),
		completeReadingWithDiskSet(20, 200, 200, 1_400, 2_600, "NVME0N1,SDA"),
	}}
	collector := NewCollector(source, Options{Interval: 2 * time.Second, MaxSamples: 60})

	collector.collectAt(context.Background(), base)
	collector.collectAt(context.Background(), base.Add(2*time.Second))

	latest := collector.Snapshot().Samples[1]
	assertMetric(t, "reordered disk read", latest.DiskReadBytesPerSecond, 200)
	assertMetric(t, "reordered disk write", latest.DiskWriteBytesPerSecond, 300)
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
	snapshot.Disks[0].PhysicalDevices[0] = "mutated"
	fresh := collector.Snapshot()
	assertMetric(t, "snapshot copy", fresh.Samples[0].CPUUsedPercent, 20)
	if fresh.Disks[0].PhysicalDevices[0] != "D:" {
		t.Fatalf("snapshot physical devices were mutated: %#v", fresh.Disks[0].PhysicalDevices)
	}
}

func TestCollectorCopiesDiskPhysicalDevicesFromSource(t *testing.T) {
	at := time.Date(2026, time.July, 26, 4, 8, 0, 0, time.UTC)
	reading := completeReading(10, 100, 100, 1_000, 2_000)
	source := &sequenceSource{readings: []HostReading{reading}}
	collector := NewCollector(source, Options{Interval: time.Second, MaxSamples: 2})

	collector.collectAt(context.Background(), at)
	reading.Disks[0].PhysicalDevices[0] = "source-mutated"

	snapshot := collector.Snapshot()
	if snapshot.Disks[0].PhysicalDevices[0] != "D:" {
		t.Fatalf("source mutated collector snapshot: %#v", snapshot.Disks[0].PhysicalDevices)
	}
	snapshot.Disks[0].PhysicalDevices[0] = "caller-mutated"
	if collector.Snapshot().Disks[0].PhysicalDevices[0] != "D:" {
		t.Fatal("caller mutated collector snapshot")
	}
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
	return completeReadingWithDiskSet(cpu, received, sent, read, written, "d:")
}

func completeReadingWithDiskSet(cpu float64, received, sent, read, written uint64, devices string) HostReading {
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
		DiskDevices:       strings.Split(devices, ","),
		DisksAvailable:    true,
		Disks: []domain.SystemDiskUsage{{
			Device: "D:", PhysicalDevices: []string{"D:"}, Path: "D:", TotalBytes: 100_000, UsedBytes: 60_000, UsedPercent: 60,
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
