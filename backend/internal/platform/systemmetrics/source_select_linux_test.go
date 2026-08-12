//go:build linux

package systemmetrics

import (
	"reflect"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
)

// TestSelectDiskMountsContainerBindMounts 模拟生产 API 容器：
// 业务目录全部是宿主目录的 bind 子目录，工作目录 /app 命中根设备 bind，
// 根设备代表统一展示为 "/"，数据盘与根设备各保留一条，容器杂物不出现。
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
	want := []string{"/", "/data/video/video1"}
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
	want := []string{"/"}
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
	want := []string{"/"}
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
	want := []string{"/"}
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
	want := []string{"/", "/data/video/video1"}
	got := selectDiskMounts(paths, "/data/video/video1", partitions)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectDiskMounts() = %#v, want %#v", got, want)
	}
}
