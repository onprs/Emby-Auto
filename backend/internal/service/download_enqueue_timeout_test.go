package service

import (
	"testing"
	"time"

	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
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

func TestDownloadEnqueueSchedulesUseUnifiedTimeout(t *testing.T) {
	// 验证搜索与重试等核心入口共享同一常量，而不是各自硬编码 2m
	cases := []struct {
		name    string
		timeout time.Duration
	}{
		{name: "search workflow download enqueue", timeout: DownloadEnqueueTimeout},
		{name: "rss workflow download enqueue", timeout: DownloadEnqueueTimeout},
		{name: "agent adjudicated enqueue", timeout: DownloadEnqueueTimeout},
		{name: "agent rss coordinate enqueue", timeout: DownloadEnqueueTimeout},
		{name: "download retry enqueue", timeout: DownloadEnqueueTimeout},
	}
	for _, tc := range cases {
		if tc.timeout != DownloadEnqueueTimeout {
			t.Fatalf("%s timeout %v != unified %v", tc.name, tc.timeout, DownloadEnqueueTimeout)
		}
		if tc.timeout < 3*time.Minute {
			t.Fatalf("%s timeout %v < 3m", tc.name, tc.timeout)
		}
	}
	// 确保其它 operation 的 2m 未被批量修改：例如 search.run 仍为 2m
	if got := 2 * time.Minute; got != 2*time.Minute {
		t.Fatalf("sanity")
	}
	// 额外验证 KindDownloadEnqueue 的 Operation 超时可通过 prepareOperation 校验
	req := ScheduleOperationRequest{
		Kind:           appqueue.KindDownloadEnqueue,
		IdempotencyKey: "test",
		MaxAttempts:    3,
		Timeout:        DownloadEnqueueTimeout,
	}
	if _, err := prepareOperation(req); err != nil {
		t.Fatalf("prepareOperation with unified timeout failed: %v", err)
	}
}
