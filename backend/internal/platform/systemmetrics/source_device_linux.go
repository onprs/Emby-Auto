//go:build linux

package systemmetrics

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

const linuxSysfsRoot = "/sys"

func platformDiskDevices(samplePath, _ string) []string {
	var stat unix.Stat_t
	if err := unix.Stat(samplePath, &stat); err != nil {
		return nil
	}
	return linuxPhysicalBlockDevices(linuxSysfsRoot, unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)))
}

func linuxPhysicalBlockDevices(sysfsRoot string, major, minor uint32) []string {
	devicePath, err := filepath.EvalSymlinks(filepath.Join(sysfsRoot, "dev", "block", fmt.Sprintf("%d:%d", major, minor)))
	if err != nil {
		return nil
	}
	devices := resolveLinuxBlockDevices(devicePath, make(map[string]struct{}))
	sort.Strings(devices)
	return devices
}

func resolveLinuxBlockDevices(devicePath string, visited map[string]struct{}) []string {
	resolved, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return nil
	}
	if _, exists := visited[resolved]; exists {
		return nil
	}
	visited[resolved] = struct{}{}

	if slaves, err := os.ReadDir(filepath.Join(resolved, "slaves")); err == nil && len(slaves) > 0 {
		devices := make([]string, 0, len(slaves))
		for _, slave := range slaves {
			devices = append(devices, resolveLinuxBlockDevices(filepath.Join(resolved, "slaves", slave.Name()), visited)...)
		}
		if len(devices) > 0 {
			return uniqueSortedStrings(devices)
		}
	}

	if _, err := os.Stat(filepath.Join(resolved, "partition")); err == nil {
		return resolveLinuxBlockDevices(filepath.Dir(resolved), visited)
	}
	name := filepath.Base(resolved)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil
	}
	return []string{name}
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}
