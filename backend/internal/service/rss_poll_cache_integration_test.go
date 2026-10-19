//go:build integration

package service

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestRSSPollCacheExcludesAcquiredEntriesIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	candidates := rssBatchCandidates(t, fixture, fixture.subscriptionID)
	if err := fixture.workflow.ScheduleRSSDownloadWithRealtimeCheck(fixture.ctx, candidates[0], rssAuditAbsentCheck(t, fixture)); err != nil {
		t.Fatal(err)
	}
	feed := domain.RSSFeed{Entries: []domain.RSSFeedEntry{{GUID: "batch-first"}, {GUID: "batch-failure"}}}
	err := fixture.workflow.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Queries.LockRSSPollMappingContext(fixture.ctx, repository.UUIDToPG(fixture.subscriptionID)); err != nil {
			return err
		}
		cache, err := loadRSSPollEntryCache(fixture.ctx, scope.Queries, fixture.subscriptionID, feed)
		if err != nil {
			return err
		}
		if _, exists := cache.entries["guid:batch-first"]; exists {
			t.Fatal("已有获取任务的条目被缓存，可能使用后台入库前的状态")
		}
		if _, exists := cache.entries["guid:batch-failure"]; !exists {
			t.Fatal("未创建获取任务的条目没有批量预取")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
