package systemmetrics

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

const (
	defaultSampleInterval = 2 * time.Second
	defaultMaxSamples     = 60
)

type Source interface {
	Read(context.Context, []string) HostReading
}

type HostReading struct {
	CPUAvailable      bool
	CPUUsedPercent    float64
	MemoryAvailable   bool
	MemoryTotalBytes  uint64
	MemoryUsedBytes   uint64
	MemoryUsedPercent float64

	NetworkAvailable bool
	NetworkBytesRecv uint64
	NetworkBytesSent uint64

	DiskIOAvailable  bool
	DiskReadBytes    uint64
	DiskWrittenBytes uint64

	DisksAvailable bool
	Disks          []domain.SystemDiskUsage
}

type Options struct {
	Interval   time.Duration
	MaxSamples int
	Now        func() time.Time
}

type counterState struct {
	sampledAt       time.Time
	networkReady    bool
	networkReceived uint64
	networkSent     uint64
	diskReady       bool
	diskRead        uint64
	diskWritten     uint64
}

// Collector keeps a bounded process-local history and derives rates from host counters.
type Collector struct {
	source Source
	now    func() time.Time

	collectMu sync.Mutex
	mu        sync.RWMutex
	interval  time.Duration
	max       int
	paths     []string
	previous  counterState
	snapshot  domain.SystemMetricsSnapshot
}

func NewCollector(source Source, options Options) *Collector {
	interval := options.Interval
	if interval <= 0 {
		interval = defaultSampleInterval
	}
	maxSamples := options.MaxSamples
	if maxSamples <= 0 {
		maxSamples = defaultMaxSamples
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Collector{
		source:   source,
		now:      now,
		interval: interval,
		max:      maxSamples,
		snapshot: domain.SystemMetricsSnapshot{
			SampleIntervalSeconds: max(1, int(interval/time.Second)),
			HistoryWindowSeconds:  max(1, int(interval/time.Second)*maxSamples),
			Samples:               make([]domain.SystemMetricSample, 0, maxSamples),
			Disks:                 []domain.SystemDiskUsage{},
		},
	}
}

func (collector *Collector) SetDiskPaths(paths []string) {
	seen := make(map[string]struct{}, len(paths))
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		cleaned = append(cleaned, path)
	}
	collector.mu.Lock()
	collector.paths = cleaned
	collector.mu.Unlock()
}

func (collector *Collector) Sample(ctx context.Context) {
	collector.collectAt(ctx, collector.now().UTC())
}

func (collector *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(collector.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case sampledAt := <-ticker.C:
			collector.collectAt(ctx, sampledAt.UTC())
		}
	}
}

func (collector *Collector) Snapshot() domain.SystemMetricsSnapshot {
	collector.mu.RLock()
	defer collector.mu.RUnlock()
	return cloneSnapshot(collector.snapshot)
}

func (collector *Collector) collectAt(ctx context.Context, sampledAt time.Time) {
	collector.collectMu.Lock()
	defer collector.collectMu.Unlock()

	collector.mu.RLock()
	paths := append([]string(nil), collector.paths...)
	collector.mu.RUnlock()
	reading := collector.source.Read(ctx, paths)

	collector.mu.Lock()
	defer collector.mu.Unlock()

	sample := domain.SystemMetricSample{SampledAt: sampledAt}
	if reading.CPUAvailable {
		sample.CPUUsedPercent = floatMetric(clampPercent(reading.CPUUsedPercent))
	}
	if reading.MemoryAvailable {
		sample.MemoryUsedPercent = floatMetric(clampPercent(reading.MemoryUsedPercent))
	}

	elapsed := sampledAt.Sub(collector.previous.sampledAt).Seconds()
	if elapsed > 0 && reading.NetworkAvailable && collector.previous.networkReady {
		sample.NetworkReceiveBytesPerSecond = floatMetric(counterRate(reading.NetworkBytesRecv, collector.previous.networkReceived, elapsed))
		sample.NetworkSendBytesPerSecond = floatMetric(counterRate(reading.NetworkBytesSent, collector.previous.networkSent, elapsed))
	}
	if elapsed > 0 && reading.DiskIOAvailable && collector.previous.diskReady {
		sample.DiskReadBytesPerSecond = floatMetric(counterRate(reading.DiskReadBytes, collector.previous.diskRead, elapsed))
		sample.DiskWriteBytesPerSecond = floatMetric(counterRate(reading.DiskWrittenBytes, collector.previous.diskWritten, elapsed))
	}

	collector.previous = counterState{
		sampledAt:       sampledAt,
		networkReady:    reading.NetworkAvailable,
		networkReceived: reading.NetworkBytesRecv,
		networkSent:     reading.NetworkBytesSent,
		diskReady:       reading.DiskIOAvailable,
		diskRead:        reading.DiskReadBytes,
		diskWritten:     reading.DiskWrittenBytes,
	}

	collector.snapshot.SampledAt = sampledAt
	collector.snapshot.Availability = domain.SystemMetricsAvailability{
		CPU:          reading.CPUAvailable,
		Memory:       reading.MemoryAvailable,
		Network:      reading.NetworkAvailable,
		DiskIO:       reading.DiskIOAvailable,
		DiskCapacity: reading.DisksAvailable,
	}
	if reading.MemoryAvailable {
		collector.snapshot.Memory = &domain.SystemMemoryUsage{
			UsedBytes:  saturatingInt64(reading.MemoryUsedBytes),
			TotalBytes: saturatingInt64(reading.MemoryTotalBytes),
		}
	} else {
		collector.snapshot.Memory = nil
	}
	if reading.DisksAvailable {
		collector.snapshot.Disks = append([]domain.SystemDiskUsage(nil), reading.Disks...)
	} else {
		collector.snapshot.Disks = []domain.SystemDiskUsage{}
	}
	collector.snapshot.Samples = append(collector.snapshot.Samples, sample)
	if overflow := len(collector.snapshot.Samples) - collector.max; overflow > 0 {
		collector.snapshot.Samples = append([]domain.SystemMetricSample(nil), collector.snapshot.Samples[overflow:]...)
	}
}

func counterRate(current, previous uint64, elapsedSeconds float64) float64 {
	if current < previous || elapsedSeconds <= 0 {
		return 0
	}
	return float64(current-previous) / elapsedSeconds
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return min(value, 100)
}

func saturatingInt64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

func floatMetric(value float64) *float64 {
	return &value
}

func cloneSnapshot(snapshot domain.SystemMetricsSnapshot) domain.SystemMetricsSnapshot {
	cloned := snapshot
	cloned.Samples = make([]domain.SystemMetricSample, len(snapshot.Samples))
	for index, sample := range snapshot.Samples {
		cloned.Samples[index] = sample
		cloned.Samples[index].CPUUsedPercent = cloneMetric(sample.CPUUsedPercent)
		cloned.Samples[index].MemoryUsedPercent = cloneMetric(sample.MemoryUsedPercent)
		cloned.Samples[index].NetworkReceiveBytesPerSecond = cloneMetric(sample.NetworkReceiveBytesPerSecond)
		cloned.Samples[index].NetworkSendBytesPerSecond = cloneMetric(sample.NetworkSendBytesPerSecond)
		cloned.Samples[index].DiskReadBytesPerSecond = cloneMetric(sample.DiskReadBytesPerSecond)
		cloned.Samples[index].DiskWriteBytesPerSecond = cloneMetric(sample.DiskWriteBytesPerSecond)
	}
	cloned.Disks = append([]domain.SystemDiskUsage(nil), snapshot.Disks...)
	if snapshot.Memory != nil {
		memory := *snapshot.Memory
		cloned.Memory = &memory
	}
	return cloned
}

func cloneMetric(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
