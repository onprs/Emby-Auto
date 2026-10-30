//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestSubscriptionDeletionWinningTheLockPreventsDownloadRetryIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 10*time.Second)
	defer cancel()
	initial, err := fixture.workflow.PersistPoll(ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "订阅删除并发", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "subscription-delete")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(initial.Candidates) != 1 {
		t.Fatalf("准备来源失败: %#v / %v", initial, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(ctx, initial.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	relations, err := fixture.queries.GetRSSEntryRelations(ctx, repository.UUIDToPG(initial.Candidates[0].EntryID))
	if err != nil {
		t.Fatal(err)
	}
	downloadID := repository.UUIDFromPG(relations.DownloadID)
	commands := NewDownloadCommandWorkflow(database.NewTransactor(fixture.pool), fixture.workflow.operations)
	stopped, _, err := commands.Cancel(ctx, downloadID, 1, "stop-before-parent-delete", fixture.actorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE operations SET status=CASE WHEN kind='download.cancel' THEN 'succeeded' ELSE 'cancelled' END,finished_at=now() WHERE resource_type='download' AND resource_id=$1`, downloadID); err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT id FROM rss_subscriptions WHERE id=$1 FOR UPDATE`, fixture.subscriptionID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, _, err := commands.Retry(ctx, downloadID, int32(stopped.Version), "retry-during-parent-delete", fixture.actorID)
		finished <- err
	}()
	waitForBlockedIntegrationQuery(t, ctx, fixture.pool, "LockRSSSubscriptionForMappingAcquisition")
	if _, err := tx.Exec(ctx, `UPDATE rss_subscriptions SET deleted_at=now(),enabled=false,next_poll_at=NULL,version=version+1 WHERE id=$1`, fixture.subscriptionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var conflict *Error
	if err := <-finished; !errors.As(err, &conflict) || conflict.Code != "deletion_in_progress" {
		t.Fatalf("删除中的订阅仍恢复了下载: %v", err)
	}
	view, err := NewReadService(fixture.queries).GetDownload(ctx, downloadID)
	if err != nil || view.Status != "cancelled" || view.Version != stopped.Version {
		t.Fatalf("订阅删除并发修改了停止状态: %#v / %v", view, err)
	}
}
