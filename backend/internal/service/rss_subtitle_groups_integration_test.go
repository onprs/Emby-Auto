//go:build integration

package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestRSSSubtitleGroupsPersistenceIntegration(t *testing.T) {
	ctx := context.Background()
	_, pool := testutil.NewMigratedPostgres(t)
	workflow := NewRSSSubtitleGroupWorkflow(db.New(pool), database.NewTransactor(pool))
	actor := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, 'subtitle-test', 'fixture-hash')`, actor); err != nil {
		t.Fatal(err)
	}
	created, err := workflow.Create(ctx, domain.CreateRSSSubtitleGroup{Name: " 【 ＲｅｌｅａｓｅＴｅａｍ 】 ", ActorUserID: actor})
	if err != nil || created.Name != "ReleaseTeam" {
		t.Fatalf("新建 = %#v, %v", created, err)
	}

	t.Run("规范化后的重复名称冲突", func(t *testing.T) {
		_, err := workflow.Create(ctx, domain.CreateRSSSubtitleGroup{Name: "[releaseteam]", ActorUserID: actor})
		if !errors.Is(err, ErrStateConflict) {
			t.Fatalf("重复新建 = %v", err)
		}
	})
	t.Run("空格与连字符名称分别持久化和识别", func(t *testing.T) {
		for _, name := range []string{"Release Team", "Release-Team"} {
			if _, err := workflow.Create(ctx, domain.CreateRSSSubtitleGroup{Name: name, ActorUserID: actor}); err != nil {
				t.Fatal(err)
			}
		}
		names, err := workflow.ListRSSSubtitleGroupNames(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := domain.ExtractRSSSubtitleGroupCandidates(domain.RSSFeed{Entries: []domain.RSSFeedEntry{{Title: "[Release-Team] 动画 - 01"}}}, names)
		if len(got) != 1 || got[0] != "Release-Team" {
			t.Fatalf("实际维护列表的识别结果 = %#v", got)
		}
	})
	t.Run("修改自身大小写", func(t *testing.T) {
		updated, err := workflow.Update(ctx, domain.UpdateRSSSubtitleGroup{ID: created.ID, Name: "RELEASETEAM", ActorUserID: actor})
		if err != nil || updated.Name != "RELEASETEAM" {
			t.Fatalf("更新 = %#v, %v", updated, err)
		}
	})
	t.Run("更新不存在记录", func(t *testing.T) {
		_, err := workflow.Update(ctx, domain.UpdateRSSSubtitleGroup{ID: uuid.New(), Name: "MissingTeam", ActorUserID: actor})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("更新不存在记录 = %v", err)
		}
	})
	t.Run("创建事件失败时回滚", func(t *testing.T) {
		_, err := workflow.Create(ctx, domain.CreateRSSSubtitleGroup{Name: "RollbackTeam", ActorUserID: uuid.New()})
		if err == nil {
			t.Fatal("无效操作人应使事件写入失败")
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_subtitle_groups WHERE name = 'RollbackTeam'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("回滚后记录数 = %d, %v", count, err)
		}
	})
	t.Run("更新事件失败时回滚", func(t *testing.T) {
		_, err := workflow.Update(ctx, domain.UpdateRSSSubtitleGroup{ID: created.ID, Name: "RollbackTeam", ActorUserID: uuid.New()})
		if err == nil {
			t.Fatal("无效操作人应使事件写入失败")
		}
		var name string
		if err := pool.QueryRow(ctx, `SELECT name FROM rss_subtitle_groups WHERE id = $1`, created.ID).Scan(&name); err != nil || name != "RELEASETEAM" {
			t.Fatalf("回滚后名称 = %q, %v", name, err)
		}
	})
	t.Run("删除事件失败时回滚", func(t *testing.T) {
		if err := workflow.Delete(ctx, created.ID, uuid.New()); err == nil {
			t.Fatal("无效操作人应使事件写入失败")
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_subtitle_groups WHERE id = $1`, created.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("回滚后记录数 = %d, %v", count, err)
		}
	})
	t.Run("并发重复创建仅成功一次", func(t *testing.T) {
		const count = 12
		results := make(chan error, count)
		var wg sync.WaitGroup
		for index := 0; index < count; index++ {
			wg.Go(func() {
				_, err := workflow.Create(ctx, domain.CreateRSSSubtitleGroup{Name: "ConcurrentTeam", ActorUserID: actor})
				results <- err
			})
		}
		wg.Wait()
		close(results)
		success, conflicts := 0, 0
		for err := range results {
			switch {
			case err == nil:
				success++
			case errors.Is(err, ErrStateConflict):
				conflicts++
			default:
				t.Errorf("非预期错误：%v", err)
			}
		}
		if success != 1 || conflicts != count-1 {
			t.Fatalf("成功=%d 冲突=%d", success, conflicts)
		}
	})
	t.Run("删除记录并保留审计事件", func(t *testing.T) {
		if err := workflow.Delete(ctx, created.ID, actor); err != nil {
			t.Fatal(err)
		}
		if err := workflow.Delete(ctx, created.ID, actor); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("重复删除 = %v", err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE resource_id = $1`, created.ID).Scan(&count); err != nil || count != 3 {
			t.Fatalf("新建、更新、删除事件数 = %d, %v", count, err)
		}
	})
}
