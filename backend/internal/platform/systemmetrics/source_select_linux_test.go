//go:build linux

package systemmetrics

import (
	"context"
	"reflect"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
)

// TestSelectDiskMountsContainerBindMounts 模拟生产 API 容器：
// 业务目录全部是宿主目录的 bind 子目录，工作目录 /app 命中根设备 bind，
// 根设备代表统一展示为 "/"，数据盘与根设备各保留一条，容器杂物不出现。
// 根设备必须保留选择阶段命中的真实设备名（/dev/mapper/...），而不是从展示
// 路径 "/" 反查到的 overlay。
func TestSelectDiskMountsContainerBindMounts(t *testing.T) {
	partitions := []disk.PartitionStat{
		{Device: "overlay", Mountpoint: "/", Fstype: "overlay"},
		{Device: "/dev/mapper/onpes--server--vg-root", Mountpoint: "/data", Fstype: "ext4"},
		{Device: "/dev/mapper/onpes--server--vg-root", Mountpoint: "/app", Fstype: "ext4"},
		{Device: "/dev/mapper/onpes--server--vg-root", Mountpoint: "/etc/hosts", Fstype: "ext4"},
		{Device: "/dev/mapper/onpes--server--vg-root", Mountpoint: "/go/pkg/mod", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/data/video/auto_emby_cache", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/data/video/auto_emby_work", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/data/video/auto_emby_staging", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/data/video/video1", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/data/video/video2", Fstype: "ext4"},
	}
	paths := []string{
		"/data/video/auto_emby_cache",
		"/data/video/auto_emby_work",
		"/data/video/auto_emby_staging",
		"/data/video/video1",
		"/data/video/video2",
	}
	want := []selectedMount{
		{Device: "/dev/mapper/onpes--server--vg-root", Sample: "/app", Display: "/"},
		{Device: "/dev/sda1", Sample: "/data/video/video1", Display: "/data/video/video1"},
	}
	got := selectDiskMounts(paths, "/app", partitions)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectDiskMounts() = %#v, want %#v", got, want)
	}
}

// TestSelectDiskMountsOverlayOnly 裸容器：唯一候选是 overlay 根挂载时保留它。
func TestSelectDiskMountsOverlayOnly(t *testing.T) {
	partitions := []disk.PartitionStat{
		{Device: "overlay", Mountpoint: "/", Fstype: "overlay"},
	}
	paths := []string{"/srv/media"}
	want := []selectedMount{{Device: "overlay", Sample: "/", Display: "/"}}
	got := selectDiskMounts(paths, "/srv/media", partitions)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectDiskMounts() = %#v, want %#v", got, want)
	}
}

// TestSelectDiskMountsRootPreferred 同一设备同时挂载根与数据目录时优先展示根挂载。
func TestSelectDiskMountsRootPreferred(t *testing.T) {
	partitions := []disk.PartitionStat{
		{Device: "/dev/sda1", Mountpoint: "/", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/data", Fstype: "ext4"},
	}
	paths := []string{"/data/media"}
	want := []selectedMount{{Device: "/dev/sda1", Sample: "/", Display: "/"}}
	got := selectDiskMounts(paths, "/data/media", partitions)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectDiskMounts() = %#v, want %#v", got, want)
	}
}

// TestSelectDiskMountsEmptyPaths 无配置路径时只展示根文件系统，容器杂物不出现。
func TestSelectDiskMountsEmptyPaths(t *testing.T) {
	partitions := []disk.PartitionStat{
		{Device: "overlay", Mountpoint: "/", Fstype: "overlay"},
		{Device: "/dev/mapper/onpes--server--vg-root", Mountpoint: "/etc/hosts", Fstype: "ext4"},
		{Device: "/dev/mapper/onpes--server--vg-root", Mountpoint: "/run.sh", Fstype: "ext4"},
	}
	want := []selectedMount{{Device: "overlay", Sample: "/", Display: "/"}}
	got := selectDiskMounts(nil, "/srv", partitions)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectDiskMounts() = %#v, want %#v", got, want)
	}
}

// TestSelectDiskMountsWorkingDirectoryOnBusinessDisk 工作目录落在业务盘上时，
// 根文件系统仍由 overlay "/" 兜底展示，业务盘只保留一条。
func TestSelectDiskMountsWorkingDirectoryOnBusinessDisk(t *testing.T) {
	partitions := []disk.PartitionStat{
		{Device: "overlay", Mountpoint: "/", Fstype: "overlay"},
		{Device: "/dev/sda1", Mountpoint: "/data/video/video1", Fstype: "ext4"},
	}
	paths := []string{"/data/video/video1"}
	want := []selectedMount{
		{Device: "overlay", Sample: "/", Display: "/"},
		{Device: "/dev/sda1", Sample: "/data/video/video1", Display: "/data/video/video1"},
	}
	got := selectDiskMounts(paths, "/data/video/video1", partitions)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectDiskMounts() = %#v, want %#v", got, want)
	}
}

// TestReadDiskUsagesContainerBindMounts 覆盖完整采集链路（selectDiskMounts + 容量采样 +
// 物理设备解析）：生产容器中根卷与业务分区必须显示底层物理设备名。
func TestReadDiskUsagesContainerBindMounts(t *testing.T) {
	partitions := []disk.PartitionStat{
		{Device: "overlay", Mountpoint: "/", Fstype: "overlay"},
		{Device: "/dev/mapper/onpes--server--vg-root", Mountpoint: "/app", Fstype: "ext4"},
		{Device: "/dev/mapper/onpes--server--vg-root", Mountpoint: "/etc/hosts", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/data/video/video1", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/data/video/video2", Fstype: "ext4"},
	}
	originalUsage := usageWithContext
	originalPartitions := partitionsWithContext
	originalWorkingDirectory := getWorkingDirectory
	originalResolveDiskDevices := resolveDiskDevices
	usageWithContext = fakeUsage(map[string]*disk.UsageStat{
		"/app":               {Path: "/app", Total: 1_000_000, Used: 600_000, UsedPercent: 60},
		"/data/video/video1": {Path: "/data/video/video1", Total: 2_000_000, Used: 1_000_000, UsedPercent: 50},
	})
	partitionsWithContext = func(context.Context, bool) ([]disk.PartitionStat, error) {
		return partitions, nil
	}
	getWorkingDirectory = func() (string, error) { return "/app", nil }
	resolveDiskDevices = func(samplePath, _ string) []string {
		switch samplePath {
		case "/app":
			return []string{"nvme0n1"}
		case "/data/video/video1":
			return []string{"sda"}
		default:
			return nil
		}
	}
	defer func() {
		usageWithContext = originalUsage
		partitionsWithContext = originalPartitions
		getWorkingDirectory = originalWorkingDirectory
		resolveDiskDevices = originalResolveDiskDevices
	}()

	got := readDiskUsages(t.Context(), []string{
		"/data/video/video1",
		"/data/video/video2",
	})
	if len(got) != 2 {
		t.Fatalf("readDiskUsages() = %#v, want 2 usages", got)
	}
	root, data := got[0], got[1]
	if root.Device != "/dev/mapper/onpes--server--vg-root" || !reflect.DeepEqual(root.PhysicalDevices, []string{"nvme0n1"}) || root.Path != "/" || root.UsedPercent != 60 {
		t.Fatalf("root usage = %#v, want logical root device backed by nvme0n1", root)
	}
	if data.Device != "/dev/sda1" || !reflect.DeepEqual(data.PhysicalDevices, []string{"sda"}) || data.Path != "/data/video/video1" || data.UsedPercent != 50 {
		t.Fatalf("data usage = %#v, want logical partition backed by sda", data)
	}
}

// TestReadDiskUsagesEmptyPartitionEnumerationFallback 分区枚举不完整（partitions 为空）时
// device 必须兜底为非空展示路径，不能违反契约 minLength:1。
func TestReadDiskUsagesEmptyPartitionEnumerationFallback(t *testing.T) {
	originalUsage := usageWithContext
	originalPartitions := partitionsWithContext
	originalWorkingDirectory := getWorkingDirectory
	originalResolveDiskDevices := resolveDiskDevices
	usageWithContext = fakeUsage(map[string]*disk.UsageStat{
		"/": {Path: "/", Total: 5_000, Used: 1_000, UsedPercent: 20},
	})
	partitionsWithContext = func(context.Context, bool) ([]disk.PartitionStat, error) {
		return nil, nil
	}
	getWorkingDirectory = func() (string, error) { return "/srv/media", nil }
	resolveDiskDevices = func(string, string) []string { return nil }
	defer func() {
		usageWithContext = originalUsage
		partitionsWithContext = originalPartitions
		getWorkingDirectory = originalWorkingDirectory
		resolveDiskDevices = originalResolveDiskDevices
	}()

	got := readDiskUsages(t.Context(), []string{"/srv/media"})
	if len(got) != 1 {
		t.Fatalf("readDiskUsages() = %#v, want 1 usage", got)
	}
	if got[0].Device == "" || got[0].Device != "/" || len(got[0].PhysicalDevices) != 0 || got[0].Path != "/" {
		t.Fatalf("usage = %#v, want logical fallback with unresolved physical devices", got[0])
	}
}

func fakeUsage(byPath map[string]*disk.UsageStat) func(context.Context, string) (*disk.UsageStat, error) {
	return func(_ context.Context, path string) (*disk.UsageStat, error) {
		return byPath[path], nil
	}
}
