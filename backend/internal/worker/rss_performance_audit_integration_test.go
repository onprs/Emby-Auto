//go:build integration && rss_audit

package worker

import (
	"context"
	"fmt"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"testing"
	"time"
)

// 使用真实持久化与 River 插入；只有 RSS 来源和外部 Emby 响应由合成数据替代。
func TestRSSAuditWorkerPollScalingIntegration(t *testing.T) {
	for _, entryCount := range []int{10, 100, 500, 1000} {
		t.Run(fmt.Sprintf("entries_%d", entryCount), func(t *testing.T) {
			fixture := newRSSAuditWorkerFixture(t, entryCount)
			client := &rssAuditEmbyClient{latency: 20 * time.Millisecond}
			handler := NewRSSPollHandler(&rssFeedClientStub{feed: fixture.feed}, fixture.workflow, 4).WithRealtimeTargetVerifier(fixture.verifier(client))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			started := time.Now()
			err := handler.Handle(ctx, domain.Operation{ID: fixture.operationID, ResourceType: "rss_subscription", ResourceID: fixture.subscriptionID})
			elapsed := time.Since(started)
			var acquisitions, downloads int
			if scanErr := fixture.pool.QueryRow(context.Background(), "SELECT count(*) FROM acquisitions").Scan(&acquisitions); scanErr != nil {
				t.Fatal(scanErr)
			}
			if scanErr := fixture.pool.QueryRow(context.Background(), "SELECT count(*) FROM downloads").Scan(&downloads); scanErr != nil {
				t.Fatal(scanErr)
			}
			t.Logf("entries=%d elapsed_ms=%.3f emby_series_queries=%d acquisitions=%d downloads=%d err=%v", entryCount, float64(elapsed)/float64(time.Millisecond), client.calls.Load(), acquisitions, downloads, err)
			if err != nil {
				t.Fatalf("默认 30 秒预算内未完成轮询：%v", err)
			}
			if acquisitions != entryCount || downloads != entryCount {
				t.Fatalf("入队不完整：acquisitions=%d downloads=%d，期望=%d", acquisitions, downloads, entryCount)
			}
		})
	}
}
