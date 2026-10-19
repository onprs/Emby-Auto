//go:build integration && rss_audit

package service

import (
	"fmt"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"testing"
	"time"
)

// 用真实 PostgreSQL 统计每次轮询的 SQL 次数、数据库执行时间和端到端耗时。
func TestRSSAuditPersistPollScalingIntegration(t *testing.T) {
	for _, entryCount := range []int{10, 100, 500, 1000} {
		t.Run(fmt.Sprintf("entries_%d", entryCount), func(t *testing.T) {
			fixture := newRSSTargetFixture(t)
			if _, err := fixture.pool.Exec(fixture.ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
				t.Fatal(err)
			}
			feed := domain.RSSFeed{Title: "Audit Feed", Entries: make([]domain.RSSFeedEntry, entryCount)}
			for index := range feed.Entries {
				feed.Entries[index] = domain.RSSFeedEntry{
					GUID: fmt.Sprintf("audit-%d", index), Title: fmt.Sprintf("Audit Series S01E%02d [release %d]", index%2+1, index),
					DownloadURI: fmt.Sprintf("magnet:?xt=urn:btih:%040x", index+1),
				}
			}
			for _, phase := range []string{"first", "repeat"} {
				if _, err := fixture.pool.Exec(fixture.ctx, "SELECT pg_stat_statements_reset()"); err != nil {
					t.Fatal(err)
				}
				checkID := rssAuditAbsentCheck(t, fixture)
				started := time.Now()
				result, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, feed,
					domain.RSSPollPersistOptions{RealtimeCheckID: checkID})
				elapsed := time.Since(started)
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Candidates) != entryCount {
					t.Fatalf("候选数=%d，期望=%d", len(result.Candidates), entryCount)
				}
				var calls int64
				var databaseMS float64
				if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT COALESCE(sum(calls), 0)::bigint, COALESCE(sum(total_exec_time), 0)::double precision
FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
AND toplevel`).Scan(&calls, &databaseMS); err != nil {
					t.Fatal(err)
				}
				var sourceRevision int64
				if err := fixture.pool.QueryRow(fixture.ctx, "SELECT source_revision FROM rss_subscription_progress WHERE subscription_id = $1", fixture.subscriptionID).Scan(&sourceRevision); err != nil {
					t.Fatal(err)
				}
				t.Logf("phase=%s entries=%d elapsed_ms=%.3f sql_calls=%d database_ms=%.3f progress_revision=%d", phase, entryCount, float64(elapsed)/float64(time.Millisecond), calls, databaseMS, sourceRevision)
			}
		})
	}
}
