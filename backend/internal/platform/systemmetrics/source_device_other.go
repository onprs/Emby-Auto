//go:build !linux

package systemmetrics

func platformDiskDevices(_ string, device string) []string {
	if device == "" {
		return nil
	}
	return []string{device}
}
