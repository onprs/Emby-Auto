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
	// all=true 保留 bind mount：容器里业务目录（/data/video/*）都是宿主目录的
	// bind 子目录，all=false 会全部跳过，导致磁盘容量只显示根设备。
	partitions, _ := disk.PartitionsWithContext(ctx, true)
	workingDirectory, _ := os.Getwd()

	usages := make([]domain.SystemDiskUsage, 0, 4)
	for _, mount := range selectDiskMounts(configuredPaths, workingDirectory, partitions) {
		usage, err := disk.UsageWithContext(ctx, mount)
		if err != nil || usage == nil || usage.Total == 0 {
			continue
		}
		usages = append(usages, domain.SystemDiskUsage{
			Device:      partitionAt(partitions, mount).Device,
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

// mountCandidate 是磁盘挂载候选：stat 为分区信息，root 表示来自工作目录或根挂载（非业务路径命中）。
type mountCandidate struct {
	stat disk.PartitionStat
	root bool
}

// selectDiskMounts 从分区列表中选出需要展示容量的挂载点：
//   - 每个业务路径的最深匹配挂载，工作目录的最深匹配挂载，加上 Linux 根挂载 "/"；
//   - 容器杂物（/etc/hosts、/go/pkg/mod 等非业务 bind）不会被业务路径匹配到，自然排除；
//   - 同一设备只保留一个代表挂载点（优先根挂载，其次挂载点最短、再按字母序）；
//   - 根文件系统兜底：根挂载存在时，工作目录或根挂载命中的块设备代表统一展示为 "/"，
//     并排除与其容量重复的 overlay 伪设备（仅当存在真实块设备根候选时排除）。
func selectDiskMounts(paths []string, workingDirectory string, partitions []disk.PartitionStat) []string {
	seen := make(map[string]struct{})
	var business []mountCandidate
	var others []mountCandidate
	addCandidate := func(list *[]mountCandidate, mount string, root bool) {
		mount = strings.TrimSpace(mount)
		if mount == "" {
			return
		}
		key := strings.ToLower(filepath.Clean(mount))
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		*list = append(*list, mountCandidate{stat: partitionAt(partitions, mount), root: root})
	}

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
		addCandidate(&business, mount, false)
	}
	if strings.TrimSpace(workingDirectory) != "" {
		mount := matchingMount(workingDirectory, partitions)
		if mount == "" {
			mount = volumeRoot(workingDirectory)
		}
		if mount == "" {
			mount = workingDirectory
		}
		addCandidate(&others, mount, true)
	}
	rootMount := rootMountpoint(partitions)
	if rootMount != "" {
		addCandidate(&others, rootMount, true)
	}

	// 根文件系统候选里是否存在真实块设备（非 overlay）：存在时 overlay 伪设备与其容量重复，应让位。
	hasRootDevice := false
	for _, candidate := range others {
		if !isOverlay(candidate.stat.Device) {
			hasRootDevice = true
			break
		}
	}

	byDevice := make(map[string][]mountCandidate)
	all := append(append([]mountCandidate(nil), business...), others...)
	for _, candidate := range all {
		byDevice[candidate.stat.Device] = append(byDevice[candidate.stat.Device], candidate)
	}

	selected := make([]string, 0, len(byDevice))
	for device, group := range byDevice {
		if isOverlay(device) && hasRootDevice {
			continue
		}
		representative := representativeMount(group)
		mount := representative.stat.Mountpoint
		// 根文件系统代表统一展示为 "/"（容器里根挂载为 overlay 时，真实根设备由工作目录命中）。
		if representative.root && rootMount != "" && isBlockDevice(device) {
			mount = rootMount
		}
		selected = append(selected, mount)
	}
	sort.Slice(selected, func(left, right int) bool {
		return preferMount(selected[left], selected[right])
	})
	return selected
}

func partitionAt(partitions []disk.PartitionStat, mount string) disk.PartitionStat {
	mount = filepath.Clean(mount)
	for _, partition := range partitions {
		if strings.EqualFold(filepath.Clean(partition.Mountpoint), mount) {
			return partition
		}
	}
	return disk.PartitionStat{Mountpoint: mount}
}

func rootMountpoint(partitions []disk.PartitionStat) string {
	for _, partition := range partitions {
		if filepath.Clean(partition.Mountpoint) == string(filepath.Separator) {
			return partition.Mountpoint
		}
	}
	return ""
}

func isOverlay(device string) bool {
	return strings.EqualFold(device, "overlay")
}

// isBlockDevice 判断设备是否为真实块设备（Linux 绝对路径设备；Windows 盘符不算）。
func isBlockDevice(device string) bool {
	return strings.HasPrefix(device, "/")
}

// representativeMount 从同一设备的候选挂载点中选出代表：
// 优先根挂载，其次挂载点最短（/data 优于 /app），再按字母序保证确定性。
func representativeMount(group []mountCandidate) mountCandidate {
	best := group[0]
	for _, candidate := range group[1:] {
		if preferMount(candidate.stat.Mountpoint, best.stat.Mountpoint) {
			best = candidate
		}
	}
	return best
}

// preferMount 返回 a 是否优于 b 作为代表挂载点。
func preferMount(a, b string) bool {
	cleanA := filepath.Clean(a)
	cleanB := filepath.Clean(b)
	rootA := cleanA == string(filepath.Separator)
	rootB := cleanB == string(filepath.Separator)
	if rootA != rootB {
		return rootA
	}
	if len(cleanA) != len(cleanB) {
		return len(cleanA) < len(cleanB)
	}
	return strings.ToLower(cleanA) < strings.ToLower(cleanB)
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
