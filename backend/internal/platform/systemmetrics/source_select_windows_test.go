//go:build windows

package systemmetrics

import (
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
	want := []string{`C:\`, `D:\`}
	got := selectDiskMounts(paths, `C:\project`, partitions)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectDiskMounts() = %#v, want %#v", got, want)
	}
}
