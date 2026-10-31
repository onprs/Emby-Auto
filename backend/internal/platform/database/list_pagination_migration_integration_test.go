//go:build integration

package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestListPaginationMigrationsPreserveSourcesAndRebuildAfterRollbackIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	downgradeApplication(t, ctx, pool, 44)
	seriesID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	acquisitionID, downloadID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series(id,title) VALUES($1,'Pagination migration')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_subscriptions(id,series_id,name,feed_url,source_season) VALUES($1,$2,'Pagination migration','https://example.test/feed.xml',1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_entries(id,subscription_id,identity_key,title,download_uri,downloadable,source_season,source_episode,status) VALUES($1,$2,'migration-entry','Migration S01E01','https://example.test/video.torrent',true,1,1,'enqueued')`, entryID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions(id,series_id,source_kind,rss_entry_id) VALUES($1,$2,'rss',$3)`, acquisitionID, seriesID, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads(id,acquisition_id,status,progress) VALUES($1,$2,'downloading',0.5)`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatal(err)
	}
	reads := service.NewReadService(db.New(pool), database.NewTransactor(pool))
	if err := reads.ReconcileListProjections(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := reads.ListAcquisitions(ctx, nil, 10, nil, nil, nil, nil, nil)
	if err != nil || len(before.Items) != 1 || before.Items[0].ID != acquisitionID {
		t.Fatalf("升级后列表缺少原始获取记录: %#v / %v", before, err)
	}
	downgradeApplication(t, ctx, pool, 44)
	var acquisitionCacheRemoved, entryCacheRemoved bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('acquisition_list_projections') IS NULL,to_regclass('rss_entry_list_projections') IS NULL`).Scan(&acquisitionCacheRemoved, &entryCacheRemoved); err != nil || !acquisitionCacheRemoved || !entryCacheRemoved {
		t.Fatalf("投影回滚未完成: %t / %t / %v", acquisitionCacheRemoved, entryCacheRemoved, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE downloads SET progress=0.9,updated_at=now() WHERE id=$1`, downloadID); err != nil {
		t.Fatalf("回滚后业务写入失败: %v", err)
	}
	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("重新升级失败: %v", err)
	}
	if err := reads.ReconcileListProjections(ctx); err != nil {
		t.Fatalf("重新升级后补算失败: %v", err)
	}
	after, err := reads.ListAcquisitions(ctx, nil, 10, nil, nil, nil, nil, nil)
	if err != nil || len(after.Items) != 1 || after.Items[0].ID != acquisitionID || after.Items[0].OverallProgress <= before.Items[0].OverallProgress {
		t.Fatalf("重建后没有沿用最新业务状态: %#v / %v", after, err)
	}
	entryPage, err := reads.ListRSSEntries(ctx, subscriptionID, nil, 10, nil, nil, nil, nil, nil, nil)
	if err != nil || len(entryPage.Items) != 1 || entryPage.Items[0].ID != entryID || entryPage.Items[0].AcquisitionProgress == nil || entryPage.Items[0].AcquisitionProgress.OverallProgress != after.Items[0].OverallProgress {
		t.Fatalf("重建后的 RSS 与父获取进度不一致: %#v / %v", entryPage, err)
	}
}
