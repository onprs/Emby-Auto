package httpapi

import (
	"context"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

func (server *Server) GetDashboardSystemMetrics(
	ctx context.Context,
	_ GetDashboardSystemMetricsRequestObject,
) (GetDashboardSystemMetricsResponseObject, error) {
	if server.systemMetrics == nil {
		return GetDashboardSystemMetrics503JSONResponse{
			ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "system_metrics"),
		}, nil
	}
	return GetDashboardSystemMetrics200JSONResponse(systemMetricsResponse(server.systemMetrics.Snapshot())), nil
}

func systemMetricsResponse(snapshot domain.SystemMetricsSnapshot) SystemMetricsSnapshot {
	response := SystemMetricsSnapshot{
		SampledAt:             snapshot.SampledAt,
		SampleIntervalSeconds: int32(snapshot.SampleIntervalSeconds),
		HistoryWindowSeconds:  int32(snapshot.HistoryWindowSeconds),
		Availability: SystemMetricsAvailability{
			Cpu:          snapshot.Availability.CPU,
			Memory:       snapshot.Availability.Memory,
			Network:      snapshot.Availability.Network,
			DiskIO:       snapshot.Availability.DiskIO,
			DiskCapacity: snapshot.Availability.DiskCapacity,
		},
		Samples: make([]SystemMetricSample, 0, len(snapshot.Samples)),
		Disks:   make([]SystemDiskUsage, 0, len(snapshot.Disks)),
	}
	if snapshot.Memory != nil {
		response.Memory = &SystemMemoryUsage{
			UsedBytes:  snapshot.Memory.UsedBytes,
			TotalBytes: snapshot.Memory.TotalBytes,
		}
	}
	for _, sample := range snapshot.Samples {
		response.Samples = append(response.Samples, SystemMetricSample{
			SampledAt:                    sample.SampledAt,
			CpuUsedPercent:               sample.CPUUsedPercent,
			MemoryUsedPercent:            sample.MemoryUsedPercent,
			NetworkReceiveBytesPerSecond: sample.NetworkReceiveBytesPerSecond,
			NetworkSendBytesPerSecond:    sample.NetworkSendBytesPerSecond,
			DiskReadBytesPerSecond:       sample.DiskReadBytesPerSecond,
			DiskWriteBytesPerSecond:      sample.DiskWriteBytesPerSecond,
		})
	}
	for _, usage := range snapshot.Disks {
		response.Disks = append(response.Disks, SystemDiskUsage{
			Path:        usage.Path,
			UsedBytes:   usage.UsedBytes,
			TotalBytes:  usage.TotalBytes,
			UsedPercent: usage.UsedPercent,
		})
	}
	return response
}

func metricsDiskPaths(paths domain.PathSettings) []string {
	return []string{
		paths.DownloadRoot,
		paths.WorkRoot,
		paths.StagingRoot,
		paths.EffectiveAnimeLibraryRoot(),
		paths.MovieLibraryRoot,
	}
}
