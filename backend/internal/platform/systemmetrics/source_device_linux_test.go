//go:build linux

package systemmetrics

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLinuxPhysicalBlockDevicesResolvesDeviceMapperToPhysicalDisk(t *testing.T) {
	sysfsRoot := t.TempDir()
	nvmeDisk := filepath.Join(sysfsRoot, "devices", "pci0000:00", "nvme", "nvme0", "nvme0n1")
	nvmePartition := filepath.Join(nvmeDisk, "nvme0n1p5")
	dmDevice := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-0")
	mustMkdirAll(t, filepath.Join(dmDevice, "slaves"))
	mustMkdirAll(t, filepath.Join(nvmeDisk, "slaves"))
	mustMkdirAll(t, nvmePartition)
	mustWriteFile(t, filepath.Join(nvmePartition, "partition"), "5\n")
	mustSymlink(t, nvmePartition, filepath.Join(dmDevice, "slaves", "nvme0n1p5"))
	mustMkdirAll(t, filepath.Join(sysfsRoot, "dev", "block"))
	mustSymlink(t, dmDevice, filepath.Join(sysfsRoot, "dev", "block", "254:0"))

	got := linuxPhysicalBlockDevices(sysfsRoot, 254, 0)
	want := []string{"nvme0n1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linuxPhysicalBlockDevices() = %#v, want %#v", got, want)
	}
}

func TestLinuxPhysicalBlockDevicesResolvesDeviceMapperToMultiplePhysicalDisks(t *testing.T) {
	sysfsRoot := t.TempDir()
	dmDevice := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-1")
	mustMkdirAll(t, filepath.Join(dmDevice, "slaves"))
	for _, device := range []string{"sdb", "sda"} {
		diskPath := filepath.Join(sysfsRoot, "devices", "pci0000:00", "block", device)
		partitionPath := filepath.Join(diskPath, device+"1")
		mustMkdirAll(t, filepath.Join(diskPath, "slaves"))
		mustMkdirAll(t, partitionPath)
		mustWriteFile(t, filepath.Join(partitionPath, "partition"), "1\n")
		mustSymlink(t, partitionPath, filepath.Join(dmDevice, "slaves", device+"1"))
	}
	mustMkdirAll(t, filepath.Join(sysfsRoot, "dev", "block"))
	mustSymlink(t, dmDevice, filepath.Join(sysfsRoot, "dev", "block", "254:1"))

	got := linuxPhysicalBlockDevices(sysfsRoot, 254, 1)
	want := []string{"sda", "sdb"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linuxPhysicalBlockDevices() = %#v, want %#v", got, want)
	}
}

func TestLinuxPhysicalBlockDevicesRejectsBrokenSlaveBranch(t *testing.T) {
	sysfsRoot := t.TempDir()
	dmDevice := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-2")
	goodDisk := filepath.Join(sysfsRoot, "devices", "pci0000:00", "block", "sda")
	mustMkdirAll(t, filepath.Join(dmDevice, "slaves"))
	mustMkdirAll(t, filepath.Join(goodDisk, "slaves"))
	mustSymlink(t, goodDisk, filepath.Join(dmDevice, "slaves", "sda"))
	mustSymlink(t, filepath.Join(sysfsRoot, "missing", "sdb"), filepath.Join(dmDevice, "slaves", "sdb"))
	mustMkdirAll(t, filepath.Join(sysfsRoot, "dev", "block"))
	mustSymlink(t, dmDevice, filepath.Join(sysfsRoot, "dev", "block", "254:2"))

	if got := linuxPhysicalBlockDevices(sysfsRoot, 254, 2); len(got) != 0 {
		t.Fatalf("linuxPhysicalBlockDevices() = %#v, want unresolved topology", got)
	}
}

func TestLinuxPhysicalBlockDevicesRejectsUnreadableSlaveBranch(t *testing.T) {
	sysfsRoot := t.TempDir()
	dmDevice := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-3")
	goodDisk := filepath.Join(sysfsRoot, "devices", "pci0000:00", "block", "sda")
	unreadableDisk := filepath.Join(sysfsRoot, "devices", "pci0000:00", "block", "sdb")
	for _, path := range []string{dmDevice, goodDisk, unreadableDisk} {
		mustMkdirAll(t, filepath.Join(path, "slaves"))
	}
	mustSymlink(t, goodDisk, filepath.Join(dmDevice, "slaves", "sda"))
	mustSymlink(t, unreadableDisk, filepath.Join(dmDevice, "slaves", "sdb"))
	mustMkdirAll(t, filepath.Join(sysfsRoot, "dev", "block"))
	mustSymlink(t, dmDevice, filepath.Join(sysfsRoot, "dev", "block", "254:3"))

	readDir := func(path string) ([]os.DirEntry, error) {
		if filepath.Clean(path) == filepath.Join(unreadableDisk, "slaves") {
			return nil, os.ErrPermission
		}
		return os.ReadDir(path)
	}

	if got := linuxPhysicalBlockDevicesWithReadDir(sysfsRoot, 254, 3, readDir); len(got) != 0 {
		t.Fatalf("linuxPhysicalBlockDevices() = %#v, want unresolved topology", got)
	}
}

func TestLinuxPhysicalBlockDevicesRejectsCyclicSlaveBranch(t *testing.T) {
	sysfsRoot := t.TempDir()
	top := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-4")
	first := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-5")
	second := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-6")
	goodDisk := filepath.Join(sysfsRoot, "devices", "pci0000:00", "block", "sda")
	for _, path := range []string{top, first, second, goodDisk} {
		mustMkdirAll(t, filepath.Join(path, "slaves"))
	}
	mustSymlink(t, goodDisk, filepath.Join(top, "slaves", "sda"))
	mustSymlink(t, first, filepath.Join(top, "slaves", "dm-5"))
	mustSymlink(t, second, filepath.Join(first, "slaves", "dm-6"))
	mustSymlink(t, first, filepath.Join(second, "slaves", "dm-5"))
	mustMkdirAll(t, filepath.Join(sysfsRoot, "dev", "block"))
	mustSymlink(t, top, filepath.Join(sysfsRoot, "dev", "block", "254:4"))

	if got := linuxPhysicalBlockDevices(sysfsRoot, 254, 4); len(got) != 0 {
		t.Fatalf("linuxPhysicalBlockDevices() = %#v, want unresolved topology", got)
	}
}

func TestLinuxPhysicalBlockDevicesAllowsConvergingSlaveBranches(t *testing.T) {
	sysfsRoot := t.TempDir()
	top := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-6")
	left := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-7")
	right := filepath.Join(sysfsRoot, "devices", "virtual", "block", "dm-8")
	diskPath := filepath.Join(sysfsRoot, "devices", "pci0000:00", "block", "sda")
	for _, path := range []string{top, left, right, diskPath} {
		mustMkdirAll(t, filepath.Join(path, "slaves"))
	}
	mustSymlink(t, left, filepath.Join(top, "slaves", "dm-7"))
	mustSymlink(t, right, filepath.Join(top, "slaves", "dm-8"))
	mustSymlink(t, diskPath, filepath.Join(left, "slaves", "sda"))
	mustSymlink(t, diskPath, filepath.Join(right, "slaves", "sda"))
	mustMkdirAll(t, filepath.Join(sysfsRoot, "dev", "block"))
	mustSymlink(t, top, filepath.Join(sysfsRoot, "dev", "block", "254:6"))

	got := linuxPhysicalBlockDevices(sysfsRoot, 254, 6)
	want := []string{"sda"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linuxPhysicalBlockDevices() = %#v, want %#v", got, want)
	}
}

func TestLinuxPhysicalBlockDevicesResolvesPartitionToPhysicalDisk(t *testing.T) {
	sysfsRoot := t.TempDir()
	sataDisk := filepath.Join(sysfsRoot, "devices", "pci0000:00", "ata4", "block", "sda")
	sataPartition := filepath.Join(sataDisk, "sda1")
	mustMkdirAll(t, filepath.Join(sataDisk, "slaves"))
	mustMkdirAll(t, sataPartition)
	mustWriteFile(t, filepath.Join(sataPartition, "partition"), "1\n")
	mustMkdirAll(t, filepath.Join(sysfsRoot, "dev", "block"))
	mustSymlink(t, sataPartition, filepath.Join(sysfsRoot, "dev", "block", "8:1"))

	got := linuxPhysicalBlockDevices(sysfsRoot, 8, 1)
	want := []string{"sda"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linuxPhysicalBlockDevices() = %#v, want %#v", got, want)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}
