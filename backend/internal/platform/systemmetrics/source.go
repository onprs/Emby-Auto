package systemmetrics

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

type GopsutilSource struct {
	hostControl HostControlNetworkReader
}

func NewGopsutilSource(hostControl HostControlNetworkReader) GopsutilSource {
	return GopsutilSource{hostControl: hostControl}
}

// HostControlNetworkReader 通过宿主 host-control socket 读取宿主物理网卡计数。
// ok=false 表示未配置、调用失败或响应无法解析，调用方必须降级而不是用零值伪装。
type HostControlNetworkReader interface {
	ReadHostCounters(context.Context) (received, sent uint64, ok bool)
}

// CommandHostControlNetworkReader 调用发布包内的 emby-auto-host-control 客户端二进制，
// 由它经 Unix socket 向宿主 root 服务请求 host-network-counters。
type CommandHostControlNetworkReader struct {
	Executable string
	Timeout    time.Duration
}

func (reader CommandHostControlNetworkReader) ReadHostCounters(ctx context.Context) (uint64, uint64, bool) {
	if strings.TrimSpace(reader.Executable) == "" {
		return 0, 0, false
	}
	timeout := reader.Timeout
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, reader.Executable, "host-network-counters").Output()
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return 0, 0, false
	}
	received, errRx := strconv.ParseUint(fields[0], 10, 64)
	sent, errTx := strconv.ParseUint(fields[1], 10, 64)
	if errRx != nil || errTx != nil {
		return 0, 0, false
	}
	return received, sent, true
}

func (source GopsutilSource) Read(ctx context.Context, diskPaths []string) HostReading {
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
	if source.hostControl != nil {
		if received, sent, ok := source.hostControl.ReadHostCounters(ctx); ok {
			reading.NetworkBytesRecv = received
			reading.NetworkBytesSent = sent
			reading.NetworkAvailable = true
		}
	} else if counters, err := gnet.IOCountersWithContext(ctx, true); err == nil {
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
	diskDetails := readDiskUsageDetails(ctx, diskPaths)
	if len(diskDetails) > 0 {
		reading.DisksAvailable = true
		reading.Disks = make([]domain.SystemDiskUsage, 0, len(diskDetails))
		for _, detail := range diskDetails {
			reading.Disks = append(reading.Disks, detail.usage)
		}
	}
	diskDevices := displayedDiskDevices(diskDetails)
	if len(diskDevices) > 0 {
		if counters, err := ioCountersWithContext(ctx, diskDevices...); err == nil {
			if read, written, ok := sumDiskIOCounters(counters, diskDevices); ok {
				reading.DiskReadBytes = read
				reading.DiskWrittenBytes = written
				reading.DiskIOAvailable = true
			}
		}
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

// usageWithContext、partitionsWithContext、ioCountersWithContext、getWorkingDirectory
// 与 resolveDiskDevices 可在测试中替换为 fake，以覆盖完整磁盘采集链路。
var (
	usageWithContext      = disk.UsageWithContext
	partitionsWithContext = disk.PartitionsWithContext
	ioCountersWithContext = disk.IOCountersWithContext
	getWorkingDirectory   = os.Getwd
	resolveDiskDevices    = platformDiskDevices
)

type diskUsageDetail struct {
	usage     domain.SystemDiskUsage
	ioDevices []string
}

func readDiskUsages(ctx context.Context, configuredPaths []string) []domain.SystemDiskUsage {
	details := readDiskUsageDetails(ctx, configuredPaths)
	usages := make([]domain.SystemDiskUsage, 0, len(details))
	for _, detail := range details {
		usages = append(usages, detail.usage)
	}
	return usages
}

func readDiskUsageDetails(ctx context.Context, configuredPaths []string) []diskUsageDetail {
	// all=true 保留 bind mount：容器里业务目录（/data/video/*）都是宿主目录的
	// bind 子目录，all=false 会全部跳过，导致磁盘容量只显示根设备。
	partitions, _ := partitionsWithContext(ctx, true)
	workingDirectory, _ := getWorkingDirectory()

	details := make([]diskUsageDetail, 0, 4)
	for _, mount := range selectDiskMounts(configuredPaths, workingDirectory, partitions) {
		usage, err := usageWithContext(ctx, mount.Sample)
		if err != nil || usage == nil || usage.Total == 0 {
			continue
		}
		devices := normalizeDiskDevices(resolveDiskDevices(mount.Sample, mount.Device))
		if len(devices) == 0 {
			device := displayMount(mount.Device)
			if device == "" {
				// 分区枚举不完整时用展示路径兜底，保证 device 非空（契约 minLength:1）。
				device = displayMount(mount.Display)
			}
			devices = []string{device}
		}
		details = append(details, diskUsageDetail{
			usage: domain.SystemDiskUsage{
				Device:      strings.Join(devices, ", "),
				Path:        displayMount(mount.Display),
				UsedBytes:   saturatingInt64(usage.Used),
				TotalBytes:  saturatingInt64(usage.Total),
				UsedPercent: clampPercent(usage.UsedPercent),
			},
			ioDevices: devices,
		})
	}
	sort.Slice(details, func(left, right int) bool {
		return strings.ToLower(details[left].usage.Path) < strings.ToLower(details[right].usage.Path)
	})
	return details
}

func normalizeDiskDevices(devices []string) []string {
	seen := make(map[string]struct{}, len(devices))
	normalized := make([]string, 0, len(devices))
	for _, device := range devices {
		device = displayMount(strings.TrimSpace(device))
		key := diskCounterKey(device)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, device)
	}
	sort.Slice(normalized, func(left, right int) bool {
		return strings.ToLower(normalized[left]) < strings.ToLower(normalized[right])
	})
	return normalized
}

func displayedDiskDevices(details []diskUsageDetail) []string {
	devices := make([]string, 0, len(details))
	for _, detail := range details {
		devices = append(devices, detail.ioDevices...)
	}
	return normalizeDiskDevices(devices)
}

func sumDiskIOCounters(counters map[string]disk.IOCountersStat, devices []string) (uint64, uint64, bool) {
	byDevice := make(map[string]disk.IOCountersStat, len(counters))
	for name, counter := range counters {
		key := diskCounterKey(counter.Name)
		if key == "" {
			key = diskCounterKey(name)
		}
		if key != "" {
			byDevice[key] = counter
		}
	}

	uniqueDevices := normalizeDiskDevices(devices)
	if len(uniqueDevices) == 0 {
		return 0, 0, false
	}
	var read uint64
	var written uint64
	for _, device := range uniqueDevices {
		counter, exists := byDevice[diskCounterKey(device)]
		if !exists {
			return 0, 0, false
		}
		read += counter.ReadBytes
		written += counter.WriteBytes
	}
	return read, written, true
}

func diskCounterKey(device string) string {
	device = strings.TrimRight(strings.TrimSpace(device), `/\`)
	if separator := strings.LastIndexAny(device, `/\`); separator >= 0 {
		device = device[separator+1:]
	}
	return strings.ToLower(device)
}

// mountCandidate 是磁盘挂载候选：stat 为分区信息，root 表示来自工作目录或根挂载（非业务路径命中）。
type mountCandidate struct {
	stat disk.PartitionStat
	root bool
}

// selectedMount 是最终选中的磁盘挂载：Device 保留选择阶段命中的原始设备名（避免从
// 规范化后的展示路径反查丢失真实身份），Sample 是容量采样路径，Display 是展示路径
// （根设备代表可能统一展示为 "/"）。
type selectedMount struct {
	Device  string
	Sample  string
	Display string
}

// selectDiskMounts 从分区列表中选出需要展示容量的挂载点：
//   - 每个业务路径的最深匹配挂载，工作目录的最深匹配挂载，加上 Linux 根挂载 "/"；
//   - 容器杂物（/etc/hosts、/go/pkg/mod 等非业务 bind）不会被业务路径匹配到，自然排除；
//   - 同一设备只保留一个代表挂载点（优先根挂载，其次挂载点最短、再按字母序）；
//   - 根文件系统兜底：根挂载存在时，工作目录或根挂载命中的块设备代表统一展示为 "/"，
//     并排除与其容量重复的 overlay 伪设备（仅当存在真实块设备根候选时排除）。
func selectDiskMounts(paths []string, workingDirectory string, partitions []disk.PartitionStat) []selectedMount {
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

	selected := make([]selectedMount, 0, len(byDevice))
	for device, group := range byDevice {
		if isOverlay(device) && hasRootDevice {
			continue
		}
		representative := representativeMount(group)
		sample := representative.stat.Mountpoint
		display := sample
		// 根文件系统代表统一展示为 "/"（容器里根挂载为 overlay 时，真实根设备由工作目录命中）。
		if representative.root && rootMount != "" && isBlockDevice(device) {
			display = rootMount
		}
		selected = append(selected, selectedMount{Device: device, Sample: sample, Display: display})
	}
	sort.Slice(selected, func(left, right int) bool {
		return preferMount(selected[left].Display, selected[right].Display)
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
	mount = strings.TrimSpace(mount)
	if mount == "" {
		return ""
	}
	cleaned := filepath.Clean(mount)
	if volume := filepath.VolumeName(cleaned); volume != "" {
		return volume
	}
	return cleaned
}
