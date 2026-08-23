package worker

import (
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/service"
)

// 验证外层 operation 超时至少覆盖 worker 关键路径的串行预算，避免在合法慢路径完成前被取消。
func TestDownloadEnqueueTimeoutCoversWorkerBudgets(t *testing.T) {
	const safetyMargin = 30 * time.Second
	required := torrentSourceRequestTimeout + qBittorrentRequestTimeout + qBittorrentConfirmTimeout + qBittorrentMetadataTimeout + safetyMargin
	if service.DownloadEnqueueTimeout < required {
		t.Fatalf("DownloadEnqueueTimeout %v 必须至少覆盖 source %v + qB request %v + qB confirm %v + metadata %v + 安全余量 %v = %v", service.DownloadEnqueueTimeout, torrentSourceRequestTimeout, qBittorrentRequestTimeout, qBittorrentConfirmTimeout, qBittorrentMetadataTimeout, safetyMargin, required)
	}
}
