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

func TestLinuxPhysicalBlockDevicesResolvesPartitionToPhysicalDisk(t *testing.T) {
	sysfsRoot := t.TempDir()
	sataDisk := filepath.Join(sysfsRoot, "devices", "pci0000:00", "ata4", "block", "sda")
	sataPartition := filepath.Join(sataDisk, "sda1")
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
