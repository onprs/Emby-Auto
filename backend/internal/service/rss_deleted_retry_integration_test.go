//go:build integration

package service

import (
	"errors"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestRSSDeletedAcquisitionCanOnlyBeRestartedByAnExplicitRetryIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	initial, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "删除后恢复", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "deleted-source")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(initial.Candidates) != 1 {
		t.Fatalf("准备 RSS 来源失败: %#v / %v", initial, err)
	}
	entryID := initial.Candidates[0].EntryID
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, initial.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	relations, err := fixture.queries.GetRSSEntryRelations(fixture.ctx, repository.UUIDToPG(entryID))
	if err != nil {
		t.Fatal(err)
	}
	acquisitionID := repository.UUIDFromPG(relations.AcquisitionID)
	deletions := NewAcquisitionDeletionWorkflow(fixture.queries, database.NewTransactor(fixture.pool), fixture.workflow.operations)
	deletion, err := deletions.RequestDeletion(fixture.ctx, acquisitionID, "delete-rss-source", fixture.actorID)
	if err != nil {
		t.Fatal(err)
	}
	reads := NewReadService(fixture.queries)
	readEntry := func() domain.RSSEntryView {
		t.Helper()
		page, err := reads.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, nil, nil, nil, nil, nil)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("读取 RSS 条目失败: %#v / %v", page, err)
		}
		return page.Items[0]
	}
	deleting := readEntry()
	if deleting.CanRetry {
		t.Fatal("删除进行中仍显示重试")
	}
	if _, err := fixture.workflow.RetryEntry(fixture.ctx, fixture.subscriptionID, entryID, int32(deleting.Version), "retry-during-delete", fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("删除进行中不应恢复来源: %v", err)
	}
	// 模拟旧处理器已响应取消；本用例只验证正式删除命令完成后的数据库边界。
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operations SET status='cancelled',finished_at=now() WHERE cancel_requested_at IS NOT NULL AND resource_type='download' AND resource_id=$1`, relations.DownloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := deletions.CompleteDeletion(fixture.ctx, acquisitionID, deletion.ID); err != nil {
		t.Fatal(err)
	}
	deleted := readEntry()
	if !deleted.CanRetry || deleted.AcquisitionID != nil || deleted.Status != "enqueueing" {
		t.Fatalf("删除完成后应保留历史并提供手动恢复: %#v", deleted)
	}
	operation, err := fixture.workflow.RetryEntry(fixture.ctx, fixture.subscriptionID, entryID, int32(deleted.Version), "retry-after-delete", fixture.actorID)
	if err != nil {
		t.Fatal(err)
	}
	pending := readEntry()
	if pending.CanRetry || pending.Status != "discovered" || pending.Version <= deleted.Version {
		t.Fatalf("已提交的恢复不应允许重复排队: %#v", pending)
	}
	if _, err := fixture.workflow.RetryEntry(fixture.ctx, fixture.subscriptionID, entryID, int32(pending.Version), "retry-again-while-pending", fixture.actorID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("同一条目不能并行重复恢复: %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE operations SET status='failed',error_code='rss_realtime_unavailable',error_message='实时核查暂时不可用',finished_at=now() WHERE id=$1`, operation.ID); err != nil {
		t.Fatal(err)
	}
	failed := readEntry()
	if !failed.CanRetry {
		t.Fatal("后台恢复失败后丢失了重试入口")
	}
	if _, err := fixture.workflow.RetryEntry(fixture.ctx, fixture.subscriptionID, entryID, int32(failed.Version), "retry-after-background-failure", fixture.actorID); err != nil {
		t.Fatalf("后台恢复失败后无法重新检查: %v", err)
	}
}
