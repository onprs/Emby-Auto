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
	return linuxPhysicalBlockDevicesWithReadDir(sysfsRoot, major, minor, os.ReadDir)
}

func linuxPhysicalBlockDevicesWithReadDir(
	sysfsRoot string,
	major, minor uint32,
	readDir func(string) ([]os.DirEntry, error),
) []string {
	devicePath, err := filepath.EvalSymlinks(filepath.Join(sysfsRoot, "dev", "block", fmt.Sprintf("%d:%d", major, minor)))
	if err != nil {
		return nil
	}
	devices, complete := resolveLinuxBlockDevices(devicePath, make(map[string]struct{}), readDir)
	if !complete {
		return nil
	}
	sort.Strings(devices)
	return devices
}

func resolveLinuxBlockDevices(
	devicePath string,
	visiting map[string]struct{},
	readDir func(string) ([]os.DirEntry, error),
) ([]string, bool) {
	resolved, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return nil, false
	}
	if _, exists := visiting[resolved]; exists {
		return nil, false
	}
	visiting[resolved] = struct{}{}
	defer delete(visiting, resolved)

	if _, err := os.Stat(filepath.Join(resolved, "partition")); err == nil {
		parent := filepath.Dir(resolved)
		if parent == resolved {
			return nil, false
		}
		return resolveLinuxBlockDevices(parent, visiting, readDir)
	} else if !os.IsNotExist(err) {
		return nil, false
	}

	slaves, err := readDir(filepath.Join(resolved, "slaves"))
	if err != nil {
		return nil, false
	}
	if len(slaves) > 0 {
		devices := make([]string, 0, len(slaves))
		for _, slave := range slaves {
			resolvedDevices, complete := resolveLinuxBlockDevices(filepath.Join(resolved, "slaves", slave.Name()), visiting, readDir)
			if !complete || len(resolvedDevices) == 0 {
				return nil, false
			}
			devices = append(devices, resolvedDevices...)
		}
		devices = uniqueSortedStrings(devices)
		return devices, len(devices) > 0
	}

	name := filepath.Base(resolved)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil, false
	}
	return []string{name}, true
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
