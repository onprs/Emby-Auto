//go:build windows

package systemmetrics

import (
	"context"
	"reflect"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
)

// TestSelectDiskMountsWindowsVolumes Windows 上按盘符展示所有卷，不做额外过滤。
func TestSelectDiskMountsWindowsVolumes(t *testing.T) {
	partitions := []disk.PartitionStat{
		{Device: `C:\`, Mountpoint: `C:\`, Fstype: "NTFS"},
		{Device: `D:\`, Mountpoint: `D:\`, Fstype: "NTFS"},
	}
	paths := []string{`D:\media\work`, `C:\project`}
	want := []selectedMount{
		{Device: `C:\`, Sample: `C:\`, Display: `C:\`},
		{Device: `D:\`, Sample: `D:\`, Display: `D:\`},
	}
	got := selectDiskMounts(paths, `C:\project`, partitions)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectDiskMounts() = %#v, want %#v", got, want)
	}
}

// TestReadDiskUsagesWindowsVolumes 覆盖完整采集链路：Windows 盘符的设备名与展示路径一致，
// 容量来自采样路径。
func TestReadDiskUsagesWindowsVolumes(t *testing.T) {
	partitions := []disk.PartitionStat{
		{Device: `C:\`, Mountpoint: `C:\`, Fstype: "NTFS"},
		{Device: `D:\`, Mountpoint: `D:\`, Fstype: "NTFS"},
	}
	originalUsage := usageWithContext
	originalPartitions := partitionsWithContext
	originalResolveDiskDevices := resolveDiskDevices
	usageWithContext = fakeUsage(map[string]*disk.UsageStat{
		`C:\`: {Path: `C:\`, Total: 1_000_000, Used: 600_000, UsedPercent: 60},
		`D:\`: {Path: `D:\`, Total: 2_000_000, Used: 1_000_000, UsedPercent: 50},
	})
	partitionsWithContext = func(context.Context, bool) ([]disk.PartitionStat, error) {
		return partitions, nil
	}
	resolveDiskDevices = func(_ string, device string) []string { return []string{displayMount(device)} }
	defer func() {
		usageWithContext = originalUsage
		partitionsWithContext = originalPartitions
		resolveDiskDevices = originalResolveDiskDevices
	}()

	got := readDiskUsages(t.Context(), []string{`D:\media\work`, `C:\project`})
	if len(got) != 2 {
		t.Fatalf("readDiskUsages() = %#v, want 2 usages", got)
	}
	if got[0].Device != `C:` || got[0].Path != `C:` || got[0].UsedPercent != 60 {
		t.Fatalf("usage[0] = %#v", got[0])
	}
	if got[1].Device != `D:` || got[1].Path != `D:` || got[1].UsedPercent != 50 {
		t.Fatalf("usage[1] = %#v", got[1])
	}
}

func fakeUsage(byPath map[string]*disk.UsageStat) func(context.Context, string) (*disk.UsageStat, error) {
	return func(_ context.Context, path string) (*disk.UsageStat, error) {
		return byPath[path], nil
	}
}
