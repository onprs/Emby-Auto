package domain

import "time"

// SystemMetricsSnapshot is a bounded in-memory view of the API host's resources.
type SystemMetricsSnapshot struct {
	SampledAt             time.Time
	SampleIntervalSeconds int
	HistoryWindowSeconds  int
	Availability          SystemMetricsAvailability
	Samples               []SystemMetricSample
	Memory                *SystemMemoryUsage
	Disks                 []SystemDiskUsage
}

type SystemMetricsAvailability struct {
	CPU          bool
	Memory       bool
	Network      bool
	DiskIO       bool
	DiskCapacity bool
}

type SystemMetricSample struct {
	SampledAt                    time.Time
	CPUUsedPercent               *float64
	MemoryUsedPercent            *float64
	NetworkReceiveBytesPerSecond *float64
	NetworkSendBytesPerSecond    *float64
	DiskReadBytesPerSecond       *float64
	DiskWriteBytesPerSecond      *float64
}

type SystemMemoryUsage struct {
	UsedBytes  int64
	TotalBytes int64
}

type SystemDiskUsage struct {
	Path        string
	UsedBytes   int64
	TotalBytes  int64
	UsedPercent float64
}
