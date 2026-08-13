package systemmetrics

import (
	"context"
	"reflect"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/shirou/gopsutil/v4/disk"
)

func TestGopsutilSourceSumsOnlyDisplayedPhysicalDisks(t *testing.T) {
	originalUsage := usageWithContext
	originalPartitions := partitionsWithContext
	originalWorkingDirectory := getWorkingDirectory
	originalResolveDiskDevices := resolveDiskDevices
	originalIOCounters := ioCountersWithContext
	usageWithContext = fakeDiskIOUsage(map[string]*disk.UsageStat{
		"/app":  {Path: "/app", Total: 1_000, Used: 600, UsedPercent: 60},
		"/data": {Path: "/data", Total: 2_000, Used: 1_000, UsedPercent: 50},
	})
	partitionsWithContext = func(context.Context, bool) ([]disk.PartitionStat, error) {
		return []disk.PartitionStat{
			{Device: "overlay", Mountpoint: "/", Fstype: "overlay"},
			{Device: "/dev/mapper/root", Mountpoint: "/app", Fstype: "ext4"},
			{Device: "/dev/sda1", Mountpoint: "/data", Fstype: "ext4"},
		}, nil
	}
	getWorkingDirectory = func() (string, error) { return "/app", nil }
	resolveDiskDevices = func(samplePath, _ string) []string {
		if samplePath == "/app" {
			return []string{"nvme0n1"}
		}
		return []string{"sda"}
	}
	var requested []string
	ioCountersWithContext = func(_ context.Context, names ...string) (map[string]disk.IOCountersStat, error) {
		requested = append([]string(nil), names...)
		return map[string]disk.IOCountersStat{
			"nvme0n1": {Name: "nvme0n1", ReadBytes: 1_000, WriteBytes: 2_000},
			"sda":     {Name: "sda", ReadBytes: 3_000, WriteBytes: 4_000},
			"sdb":     {Name: "sdb", ReadBytes: 50_000, WriteBytes: 60_000},
		}, nil
	}
	defer func() {
		usageWithContext = originalUsage
		partitionsWithContext = originalPartitions
		getWorkingDirectory = originalWorkingDirectory
		resolveDiskDevices = originalResolveDiskDevices
		ioCountersWithContext = originalIOCounters
	}()

	reading := NewGopsutilSource(nil).Read(t.Context(), []string{"/data/media"})
	if !reflect.DeepEqual(requested, []string{"nvme0n1", "sda"}) {
		t.Fatalf("requested counters = %#v, want displayed physical devices", requested)
	}
	if !reading.DiskIOAvailable || reading.DiskReadBytes != 4_000 || reading.DiskWrittenBytes != 6_000 {
		t.Fatalf("disk I/O = %d/%d available=%t, want 4000/6000 true", reading.DiskReadBytes, reading.DiskWrittenBytes, reading.DiskIOAvailable)
	}
	if len(reading.Disks) != 2 || !reflect.DeepEqual(reading.Disks[0].PhysicalDevices, []string{"nvme0n1"}) || !reflect.DeepEqual(reading.Disks[1].PhysicalDevices, []string{"sda"}) {
		t.Fatalf("displayed disks = %#v", reading.Disks)
	}
	if reading.Disks[0].Device == "nvme0n1" || reading.Disks[1].Device == "sda" {
		t.Fatalf("logical devices were replaced by physical devices: %#v", reading.Disks)
	}
	if !reflect.DeepEqual(reading.DiskDevices, []string{"nvme0n1", "sda"}) {
		t.Fatalf("disk counter devices = %#v", reading.DiskDevices)
	}
}

func TestGopsutilSourceKeepsLogicalDeviceForMultiDiskVolume(t *testing.T) {
	originalUsage := usageWithContext
	originalPartitions := partitionsWithContext
	originalWorkingDirectory := getWorkingDirectory
	originalResolveDiskDevices := resolveDiskDevices
	originalIOCounters := ioCountersWithContext
	usageWithContext = fakeDiskIOUsage(map[string]*disk.UsageStat{
		"/data": {Path: "/data", Total: 2_000, Used: 1_000, UsedPercent: 50},
	})
	partitionsWithContext = func(context.Context, bool) ([]disk.PartitionStat, error) {
		return []disk.PartitionStat{{Device: "/dev/mapper/media", Mountpoint: "/data", Fstype: "ext4"}}, nil
	}
	getWorkingDirectory = func() (string, error) { return "/data", nil }
	resolveDiskDevices = func(string, string) []string { return []string{"sdb", "sda", "sda"} }
	ioCountersWithContext = func(_ context.Context, names ...string) (map[string]disk.IOCountersStat, error) {
		if !reflect.DeepEqual(names, []string{"sda", "sdb"}) {
			t.Fatalf("counter devices = %#v", names)
		}
		return map[string]disk.IOCountersStat{
			"sda": {Name: "sda", ReadBytes: 1_000, WriteBytes: 2_000},
			"sdb": {Name: "sdb", ReadBytes: 3_000, WriteBytes: 4_000},
		}, nil
	}
	defer func() {
		usageWithContext = originalUsage
		partitionsWithContext = originalPartitions
		getWorkingDirectory = originalWorkingDirectory
		resolveDiskDevices = originalResolveDiskDevices
		ioCountersWithContext = originalIOCounters
	}()

	reading := NewGopsutilSource(nil).Read(t.Context(), []string{"/data/media"})
	if len(reading.Disks) != 1 || displayMount(reading.Disks[0].Device) != displayMount("/dev/mapper/media") || !reflect.DeepEqual(reading.Disks[0].PhysicalDevices, []string{"sda", "sdb"}) {
		t.Fatalf("disk usage = %#v", reading.Disks)
	}
	if !reading.DiskIOAvailable || reading.DiskReadBytes != 4_000 || reading.DiskWrittenBytes != 6_000 || !reflect.DeepEqual(reading.DiskDevices, []string{"sda", "sdb"}) {
		t.Fatalf("disk I/O = %#v", reading)
	}
}

func TestGopsutilSourceMakesDiskIOUnavailableWhenAnyDisplayedDeviceIsUnresolved(t *testing.T) {
	details := []diskUsageDetail{
		{usage: domain.SystemDiskUsage{Device: "/dev/sda1", PhysicalDevices: []string{"sda"}}, ioDevices: []string{"sda"}},
		{usage: domain.SystemDiskUsage{Device: "overlay"}},
	}
	if allDiskDevicesResolved(details) {
		t.Fatal("allDiskDevicesResolved() = true with an unresolved displayed disk")
	}
}

func fakeDiskIOUsage(byPath map[string]*disk.UsageStat) func(context.Context, string) (*disk.UsageStat, error) {
	return func(_ context.Context, path string) (*disk.UsageStat, error) {
		return byPath[path], nil
	}
}

func TestSumDiskIOCountersIncludesEveryDisplayedPhysicalDiskOnce(t *testing.T) {
	counters := map[string]disk.IOCountersStat{
		"nvme0n1":   {Name: "nvme0n1", ReadBytes: 1_000, WriteBytes: 2_000},
		"nvme0n1p5": {Name: "nvme0n1p5", ReadBytes: 900, WriteBytes: 1_900},
		"dm-0":      {Name: "dm-0", ReadBytes: 800, WriteBytes: 1_800},
		"sda":       {Name: "sda", ReadBytes: 3_000, WriteBytes: 4_000},
		"sda1":      {Name: "sda1", ReadBytes: 2_900, WriteBytes: 3_900},
		"sdb":       {Name: "sdb", ReadBytes: 50_000, WriteBytes: 60_000},
	}

	read, written, ok := sumDiskIOCounters(counters, []string{"nvme0n1", "sda", "sda"})
	if !ok || read != 4_000 || written != 6_000 {
		t.Fatalf("sumDiskIOCounters() = %d/%d ok=%t, want 4000/6000 true", read, written, ok)
	}
}

func TestSumDiskIOCountersRejectsPartialDisplayedDiskTotal(t *testing.T) {
	counters := map[string]disk.IOCountersStat{
		"nvme0n1": {Name: "nvme0n1", ReadBytes: 1_000, WriteBytes: 2_000},
	}

	read, written, ok := sumDiskIOCounters(counters, []string{"nvme0n1", "sda"})
	if ok || read != 0 || written != 0 {
		t.Fatalf("sumDiskIOCounters() = %d/%d ok=%t, want 0/0 false", read, written, ok)
	}
}
