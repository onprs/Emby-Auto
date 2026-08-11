package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestFormatCascadeSummary(t *testing.T) {
	subscriptionID := uuid.MustParse("70000000-0000-0000-0000-000000000001")
	result := domain.RSSCascadeResult{
		SubscriptionID:   subscriptionID,
		Acquisitions:     5,
		TasksCancelled:   2,
		DownloadsRemoved: 3,
		ImportedKept:     1,
	}
	summary := FormatCascadeSummary(result)
	for _, want := range []string{"共 5 项内容", "停止任务 2 个", "删除下载 3 个", "保留已入库 1 个"} {
		if !contains(summary, want) {
			t.Fatalf("summary %q missing %q", summary, want)
		}
	}
	if contains(summary, "失败") {
		t.Fatalf("summary %q should not mention failures", summary)
	}
}

func TestFormatCascadeSummaryReportsFailures(t *testing.T) {
	result := domain.RSSCascadeResult{
		Acquisitions: 2,
		FailedItems: []domain.RSSCascadeFailure{
			{AcquisitionID: uuid.MustParse("70000000-0000-0000-0000-000000000002"), Stage: "remove_download", Reason: "qBittorrent unavailable"},
		},
	}
	summary := FormatCascadeSummary(result)
	if !contains(summary, "失败 1 项") {
		t.Fatalf("summary %q missing failure count", summary)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
