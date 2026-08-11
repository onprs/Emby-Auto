package systemmetrics

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

type GopsutilSource struct{}

func NewGopsutilSource() GopsutilSource {
	return GopsutilSource{}
}

func (GopsutilSource) Read(ctx context.Context, diskPaths []string) HostReading {
	reading := HostReading{}

	if percentages, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(percentages) > 0 {
		reading.CPUAvailable = true
		reading.CPUUsedPercent = percentages[0]
	}
	if memory, err := mem.VirtualMemoryWithContext(ctx); err == nil && memory != nil {
		reading.MemoryAvailable = true
		reading.MemoryTotalBytes = memory.Total
		reading.MemoryUsedBytes = memory.Used
		reading.MemoryUsedPercent = memory.UsedPercent
	}
	if counters, err := gnet.IOCountersWithContext(ctx, true); err == nil {
		loopbacks := loopbackInterfaceNames()
		for _, counter := range counters {
			if _, excluded := loopbacks[strings.ToLower(counter.Name)]; excluded {
				continue
			}
			reading.NetworkBytesRecv += counter.BytesRecv
			reading.NetworkBytesSent += counter.BytesSent
		}
		reading.NetworkAvailable = true
	}
	if counters, err := disk.IOCountersWithContext(ctx); err == nil {
		for _, counter := range counters {
			reading.DiskReadBytes += counter.ReadBytes
			reading.DiskWrittenBytes += counter.WriteBytes
		}
		reading.DiskIOAvailable = true
	}
	if disks := readDiskUsages(ctx, diskPaths); len(disks) > 0 {
		reading.DisksAvailable = true
		reading.Disks = disks
	}
	return reading
}

func loopbackInterfaceNames() map[string]struct{} {
	loopbacks := map[string]struct{}{"lo": {}}
	interfaces, err := net.Interfaces()
	if err != nil {
		return loopbacks
	}
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagLoopback != 0 {
			loopbacks[strings.ToLower(networkInterface.Name)] = struct{}{}
		}
	}
	return loopbacks
}

func readDiskUsages(ctx context.Context, configuredPaths []string) []domain.SystemDiskUsage {
	paths := append([]string(nil), configuredPaths...)
	if workingDirectory, err := os.Getwd(); err == nil {
		paths = append(paths, workingDirectory)
	}
	partitions, _ := disk.PartitionsWithContext(ctx, false)
	mounts := make(map[string]string)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		mount := matchingMount(path, partitions)
		if mount == "" {
			mount = volumeRoot(path)
		}
		if mount == "" {
			mount = path
		}
		key := strings.ToLower(filepath.Clean(mount))
		mounts[key] = mount
	}

	usages := make([]domain.SystemDiskUsage, 0, len(mounts))
	for _, mount := range mounts {
		usage, err := disk.UsageWithContext(ctx, mount)
		if err != nil || usage == nil || usage.Total == 0 {
			continue
		}
		usages = append(usages, domain.SystemDiskUsage{
			Path:        displayMount(mount),
			UsedBytes:   saturatingInt64(usage.Used),
			TotalBytes:  saturatingInt64(usage.Total),
			UsedPercent: clampPercent(usage.UsedPercent),
		})
	}
	sort.Slice(usages, func(left, right int) bool {
		return strings.ToLower(usages[left].Path) < strings.ToLower(usages[right].Path)
	})
	return usages
}

func matchingMount(path string, partitions []disk.PartitionStat) string {
	best := ""
	for _, partition := range partitions {
		mount := strings.TrimSpace(partition.Mountpoint)
		if mount == "" || !pathWithinMount(path, mount) {
			continue
		}
		if len(filepath.Clean(mount)) > len(filepath.Clean(best)) {
			best = mount
		}
	}
	return best
}

func pathWithinMount(path, mount string) bool {
	path = filepath.Clean(path)
	mount = filepath.Clean(mount)
	pathVolume := filepath.VolumeName(path)
	mountVolume := filepath.VolumeName(mount)
	if pathVolume != "" || mountVolume != "" {
		if !strings.EqualFold(pathVolume, mountVolume) {
			return false
		}
	}
	relative, err := filepath.Rel(mount, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func volumeRoot(path string) string {
	volume := filepath.VolumeName(filepath.Clean(path))
	if volume != "" {
		return volume + string(filepath.Separator)
	}
	if filepath.IsAbs(path) {
		return string(filepath.Separator)
	}
	return ""
}

func displayMount(mount string) string {
	cleaned := filepath.Clean(mount)
	if volume := filepath.VolumeName(cleaned); volume != "" {
		return volume
	}
	return cleaned
}
