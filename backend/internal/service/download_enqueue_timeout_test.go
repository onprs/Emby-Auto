package service

import (
	"testing"
	"time"
)

func TestDownloadEnqueueTimeoutIsAtLeastThreeMinutes(t *testing.T) {
	if DownloadEnqueueTimeout < 3*time.Minute {
		t.Fatalf("DownloadEnqueueTimeout = %v, want at least 3m", DownloadEnqueueTimeout)
	}
	// 预算校验：torrent 30s + qB 15s + 元数据 90s = 135s，需至少 3m
	if DownloadEnqueueTimeout != 3*time.Minute {
		t.Fatalf("DownloadEnqueueTimeout = %v, want exactly 3m for unified budget", DownloadEnqueueTimeout)
	}
}
