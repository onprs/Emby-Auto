//go:build integration

package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

// TestRSSSubscriptionListProgressMatchesSingleSubscriptionReadIntegration
// 验证订阅列表的批量进度计算与单订阅详情路径返回一致的统计，并覆盖
// 批量 SQL 的过滤（deletion_requested_at）、imported 分组聚合与多订阅分组。
func TestRSSSubscriptionListProgressMatchesSingleSubscriptionReadIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRecoveryFixture(t)
	workflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	now := time.Now().UTC()

	subscriptionA, subscriptionB := uuid.New(), uuid.New()
	for _, subscription := range []struct {
		id   uuid.UUID
		name string
	}{
		{id: subscriptionA, name: "Alpha Feed"},
		{id: subscriptionB, name: "Beta Feed"},
	} {
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, $3, $4, true, 900, 1)
`, subscription.id, fixture.seriesID, subscription.name, "https://example.test/"+subscription.id.String()+".xml"); err != nil {
			t.Fatal(err)
		}
	}

	insertEntry := func(subscriptionID uuid.UUID, episode int, importedAt *time.Time) uuid.UUID {
		t.Helper()
		entryID := uuid.New()
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, download_uri, downloadable,
    rejection_reasons, source_season, source_episode, status, imported_at
) VALUES ($1, $2, $3, $4, $5, true, ARRAY[]::text[], 1, $6, 'completed', $7)
`, entryID, subscriptionID, "guid:"+subscriptionID.String()+":"+string(rune('0'+episode)), "Show S01E0"+string(rune('0'+episode)), "https://example.test/"+entryID.String()+".torrent", episode, importedAt); err != nil {
			t.Fatal(err)
		}
		return entryID
	}
	insertAcquisition := func(entryID uuid.UUID, deleted bool) uuid.UUID {
		t.Helper()
		acquisitionID := uuid.New()
		deletedColumn := "NULL"
		if deleted {
			deletedColumn = "now()"
		}
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id, deletion_requested_at, created_at, updated_at)
VALUES ($1, $2, 'rss', $3, `+deletedColumn+`, now(), now())
`, acquisitionID, fixture.seriesID, entryID); err != nil {
			t.Fatal(err)
		}
		return acquisitionID
	}

	// 订阅 A：ent1 无下载（进行中）、ent2 下载失败（需要关注）、ent3 已入库
	// （计入 imported 计数）、ent4 已请求删除（必须被过滤掉不参与统计）。
	entryA1 := insertEntry(subscriptionA, 1, nil)
	entryA2 := insertEntry(subscriptionA, 2, nil)
	insertEntry(subscriptionA, 3, &now)
	entryA4 := insertEntry(subscriptionA, 4, nil)
	insertAcquisition(entryA1, false)
	acquisitionA2 := insertAcquisition(entryA2, false)
	insertAcquisition(entryA4, true)
	// 订阅 B：单条失败下载（需要关注）。
	entryB1 := insertEntry(subscriptionB, 1, nil)
	acquisitionB1 := insertAcquisition(entryB1, false)

	insertFailedDownload := func(acquisitionID uuid.UUID) {
		t.Helper()
		if _, err := fixture.pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, attempt, status, progress, error_code, error_message, updated_at)
VALUES ($1, $2, 1, 'failed', 0, 'qbittorrent_unavailable', 'connection refused', now())
`, uuid.New(), acquisitionID); err != nil {
			t.Fatal(err)
		}
	}
	insertFailedDownload(acquisitionA2)
	insertFailedDownload(acquisitionB1)

	page, err := workflow.ListSubscriptions(ctx, nil, 20, nil, nil)
	if err != nil {
		t.Fatalf("ListSubscriptions() error = %v", err)
	}
	byID := make(map[uuid.UUID]domain.RSSSubscription, len(page.Items))
	for _, item := range page.Items {
		byID[item.ID] = item
	}

	for _, subscriptionID := range []uuid.UUID{subscriptionA, subscriptionB} {
		listed, ok := byID[subscriptionID]
		if !ok {
			t.Fatalf("subscription %s missing from list page: %v", subscriptionID, page.Items)
		}
		detail, err := workflow.GetSubscription(ctx, subscriptionID)
		if err != nil {
			t.Fatalf("GetSubscription(%s) error = %v", subscriptionID, err)
		}
		if listed.TaskCount != detail.TaskCount || listed.CompletedTaskCount != detail.CompletedTaskCount ||
			listed.AttentionTaskCount != detail.AttentionTaskCount ||
			math.Abs(listed.OverallProgress-detail.OverallProgress) > 1e-9 {
			t.Fatalf("subscription %s progress mismatch: list %#v detail %#v", subscriptionID, listed, detail)
		}
	}

	alpha := byID[subscriptionA]
	if alpha.TaskCount != 3 || alpha.CompletedTaskCount != 1 || alpha.AttentionTaskCount != 1 {
		t.Fatalf("subscription A counts = %d/%d/%d, want 3/1/1 (imported + pending + failed, deleted excluded)", alpha.TaskCount, alpha.CompletedTaskCount, alpha.AttentionTaskCount)
	}
	beta := byID[subscriptionB]
	if beta.TaskCount != 1 || beta.CompletedTaskCount != 0 || beta.AttentionTaskCount != 1 {
		t.Fatalf("subscription B counts = %d/%d/%d, want 1/0/1", beta.TaskCount, beta.CompletedTaskCount, beta.AttentionTaskCount)
	}
}
