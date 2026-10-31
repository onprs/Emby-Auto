//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

type acquisitionProjectionSnapshot struct {
	progress float64
	revision int64
	version  int32
	dirty    bool
}

func readAcquisitionProjectionSnapshot(ctx context.Context, connection db.DBTX, id uuid.UUID) (acquisitionProjectionSnapshot, error) {
	var snapshot acquisitionProjectionSnapshot
	err := connection.QueryRow(ctx, `SELECT overall_progress,source_revision,model_version,dirty FROM acquisition_list_projections WHERE acquisition_id=$1`, id).
		Scan(&snapshot.progress, &snapshot.revision, &snapshot.version, &snapshot.dirty)
	return snapshot, err
}

func acquisitionPageItem(t *testing.T, ctx context.Context, reads *ReadService, id uuid.UUID) (domain.AcquisitionView, []uuid.UUID) {
	t.Helper()
	field, order := "progress", "desc"
	page, err := reads.ListAcquisitions(ctx, nil, 100, nil, nil, nil, &field, &order)
	if err != nil {
		t.Fatal(err)
	}
	ids := paginationViewIDs(page.Items, func(view domain.AcquisitionView) uuid.UUID { return view.ID })
	for _, view := range page.Items {
		if view.ID == id {
			return view, ids
		}
	}
	t.Fatalf("获取记录未出现在夹具列表中: %s", id)
	return domain.AcquisitionView{}, nil
}

func TestListProjectionsRollbackWithBusinessTransactionIntegration(t *testing.T) {
	fixture := newPaginationFixture(t)
	fixture.seed(t, 8)
	acquisitionID := paginationID("acquisition", 1)
	before, err := readAcquisitionProjectionSnapshot(fixture.ctx, fixture.pool, acquisitionID)
	if err != nil {
		t.Fatal(err)
	}
	viewBefore, _ := acquisitionPageItem(t, fixture.ctx, fixture.reads, acquisitionID)
	forced := errors.New("提交前注入回滚")
	callbackRan := false
	fixture.transactor.RegisterBeforeCommitHook("zz_list_projection_rollback", func(ctx context.Context, scope database.TxScope) error {
		snapshot, err := readAcquisitionProjectionSnapshot(ctx, scope.Tx, acquisitionID)
		if err != nil {
			return err
		}
		if snapshot.dirty || snapshot.version != listProjectionVersion || snapshot.progress <= before.progress {
			return fmt.Errorf("提交前投影尚未随业务重算: %+v", snapshot)
		}
		callbackRan = true
		return forced
	})
	err = fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		_, err := scope.Tx.Exec(fixture.ctx, `UPDATE downloads SET progress=0.95,updated_at=now() WHERE id=$1`, paginationID("download", 1))
		return err
	})
	if !callbackRan || !errors.Is(err, forced) {
		t.Fatalf("没有在投影重算后触发回滚: %t / %v", callbackRan, err)
	}
	after, err := readAcquisitionProjectionSnapshot(fixture.ctx, fixture.pool, acquisitionID)
	if err != nil || after != before {
		t.Fatalf("投影跨过业务回滚: 之前=%+v 之后=%+v 错误=%v", before, after, err)
	}
	viewAfter, _ := acquisitionPageItem(t, fixture.ctx, fixture.reads, acquisitionID)
	if viewAfter.OverallProgress != viewBefore.OverallProgress || !viewAfter.UpdatedAt.Equal(viewBefore.UpdatedAt) {
		t.Fatal("业务状态没有与投影一起回滚")
	}
}

func TestListSnapshotDoesNotExposeUncommittedProjectionIntegration(t *testing.T) {
	fixture := newPaginationFixture(t)
	fixture.seed(t, 8)
	acquisitionID := paginationID("acquisition", 1)
	before, beforeIDs := acquisitionPageItem(t, fixture.ctx, fixture.reads, acquisitionID)
	ready, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWriter()
	t.Cleanup(releaseWriter)
	fixture.transactor.RegisterBeforeCommitHook("zz_list_projection_pause", func(ctx context.Context, _ database.TxScope) error {
		close(ready)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	completed := make(chan error, 1)
	go func() {
		completed <- fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			_, err := scope.Tx.Exec(fixture.ctx, `UPDATE downloads SET progress=0.95,updated_at=now() WHERE id=$1`, paginationID("download", 1))
			return err
		})
	}()
	select {
	case <-ready:
	case err := <-completed:
		t.Fatalf("写入在提交等待点之前退出: %v", err)
	case <-fixture.ctx.Done():
		t.Fatal(fixture.ctx.Err())
	}
	readContext, cancel := context.WithTimeout(fixture.ctx, 10*time.Second)
	during, duringIDs := acquisitionPageItem(t, readContext, fixture.reads, acquisitionID)
	cancel()
	if during.OverallProgress != before.OverallProgress || !slices.Equal(duringIDs, beforeIDs) {
		t.Fatal("未提交的排序投影或业务进度泄露到列表快照")
	}
	releaseWriter()
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	after, afterIDs := acquisitionPageItem(t, fixture.ctx, fixture.reads, acquisitionID)
	projection, err := readAcquisitionProjectionSnapshot(fixture.ctx, fixture.pool, acquisitionID)
	if err != nil || projection.dirty || projection.progress != after.OverallProgress || after.OverallProgress <= before.OverallProgress {
		t.Fatalf("提交后业务与投影不一致: %+v / %+v / %v", projection, after, err)
	}
	if slices.Equal(afterIDs, beforeIDs) {
		t.Fatal("进度排序没有随提交后的进度变化")
	}
}

func TestListProjectionRecoversMaintenanceWritesAndRejectsNewerVersionIntegration(t *testing.T) {
	fixture := newPaginationFixture(t)
	fixture.seed(t, 8)
	acquisitionID, entryID := paginationID("acquisition", 1), paginationID("entry", 1)
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE downloads SET progress=0.77,updated_at=now() WHERE id=$1`, paginationID("download", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE rss_entries SET title='UPDATED %_ TITLE' WHERE id=$1`, entryID); err != nil {
		t.Fatal(err)
	}
	projection, err := readAcquisitionProjectionSnapshot(fixture.ctx, fixture.pool, acquisitionID)
	if err != nil || !projection.dirty {
		t.Fatalf("直接维护写入没有标记投影过期: %+v / %v", projection, err)
	}
	query := "updated %_"
	page, err := fixture.reads.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, nil, &query, nil, nil, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != entryID || page.Items[0].AcquisitionProgress == nil {
		t.Fatalf("列表没有恢复维护写入: %#v / %v", page, err)
	}
	projection, err = readAcquisitionProjectionSnapshot(fixture.ctx, fixture.pool, acquisitionID)
	if err != nil || projection.dirty || projection.progress != page.Items[0].AcquisitionProgress.OverallProgress {
		t.Fatalf("维护补算后进度仍不一致: %+v / %v", projection, err)
	}
	var titleKey string
	var entryDirty bool
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT title_key,dirty FROM rss_entry_list_projections WHERE entry_id=$1`, entryID).Scan(&titleKey, &entryDirty); err != nil || entryDirty || titleKey != strings.ToLower(page.Items[0].Title) {
		t.Fatalf("条目索引没有恢复: %q / %t / %v", titleKey, entryDirty, err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE acquisition_list_projections SET model_version=$2 WHERE acquisition_id=$1`, acquisitionID, listProjectionVersion+1); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.reads.ListAcquisitions(fixture.ctx, nil, 10, nil, nil, nil, nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("更新版本的投影没有阻止降级读取: %v", err)
	}
	err = fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		_, err := scope.Tx.Exec(fixture.ctx, `UPDATE downloads SET progress=0.88,updated_at=now() WHERE id=$1`, paginationID("download", 1))
		return err
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("更新版本的投影没有阻止降级覆盖: %v", err)
	}
	newer, err := readAcquisitionProjectionSnapshot(fixture.ctx, fixture.pool, acquisitionID)
	if err != nil || newer.version != listProjectionVersion+1 || newer.progress != projection.progress {
		t.Fatalf("新版投影被覆盖: %+v / %v", newer, err)
	}
}

func TestListProjectionParentAndEntryWritersDoNotDeadlockIntegration(t *testing.T) {
	fixture := newPaginationFixture(t)
	fixture.seed(t, 8)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	completed := make(chan error, 2)
	for _, parent := range []bool{false, true} {
		go func() {
			for number := 1; number <= 12; number++ {
				err := fixture.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
					if parent {
						_, err := scope.Tx.Exec(ctx, `UPDATE downloads SET progress=$2::numeric/20,updated_at=now() WHERE id=$1`, paginationID("download", 1), number)
						return err
					}
					_, err := scope.Tx.Exec(ctx, `UPDATE rss_entries SET title=$2 WHERE id=$1`, paginationID("entry", 1), fmt.Sprintf("Concurrent entry %02d", number))
					return err
				})
				if err != nil {
					completed <- err
					return
				}
			}
			completed <- nil
		}()
	}
	for range 2 {
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
	}
	entryPage, err := fixture.reads.ListRSSEntries(ctx, fixture.subscriptionID, nil, 10, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	acquisition, err := fixture.reads.GetAcquisition(ctx, paginationID("acquisition", 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entryPage.Items {
		if entry.ID == paginationID("entry", 1) {
			if entry.Title != "Concurrent entry 12" || entry.AcquisitionProgress == nil || entry.AcquisitionProgress.OverallProgress != acquisition.OverallProgress {
				t.Fatalf("并发写入后条目与父进度不一致: %+v", entry)
			}
			return
		}
	}
	t.Fatal("并发写入后的条目缺失")
}

func TestListSnapshotStaysConsistentAcrossConcurrentCommitIntegration(t *testing.T) {
	fixture := newPaginationFixture(t)
	fixture.seed(t, 8)
	acquisitionID := paginationID("acquisition", 1)
	before, beforeIDs := acquisitionPageItem(t, fixture.ctx, fixture.reads, acquisitionID)
	gate := &paginationReadGate{ready: make(chan struct{}), resume: make(chan struct{})}
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(gate.resume) }) }
	defer resume()
	readContext, cancel := context.WithTimeout(fixture.ctx, 10*time.Second)
	defer cancel()
	readContext = context.WithValue(readContext, paginationReadGateKey{}, gate)
	type readResult struct {
		page domain.AcquisitionPage
		err  error
	}
	completed := make(chan readResult, 1)
	go func() {
		field, order := "progress", "desc"
		page, err := fixture.reads.ListAcquisitions(readContext, nil, 100, nil, nil, nil, &field, &order)
		completed <- readResult{page: page, err: err}
	}()
	select {
	case <-gate.ready:
	case result := <-completed:
		t.Fatalf("列表没有停在选页与详情读取之间: %v", result.err)
	case <-readContext.Done():
		t.Fatal(readContext.Err())
	}
	if err := fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		_, err := scope.Tx.Exec(fixture.ctx, `UPDATE downloads SET progress=0.95,updated_at=now() WHERE id=$1`, paginationID("download", 1))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	resume()
	result := <-completed
	if result.err != nil {
		t.Fatal(result.err)
	}
	ids := paginationViewIDs(result.page.Items, func(view domain.AcquisitionView) uuid.UUID { return view.ID })
	index := slices.IndexFunc(result.page.Items, func(view domain.AcquisitionView) bool { return view.ID == acquisitionID })
	if index < 0 || !slices.Equal(ids, beforeIDs) {
		t.Fatal("当前页 ID 在并发提交后改变")
	}
	view := result.page.Items[index]
	if view.OverallProgress != before.OverallProgress || !view.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("选页与详情没有使用同一快照: 之前=%+v 当前=%+v", before, view)
	}
	after, _ := acquisitionPageItem(t, fixture.ctx, fixture.reads, acquisitionID)
	if after.OverallProgress <= before.OverallProgress {
		t.Fatal("下一次请求没有读取到已提交的新进度")
	}
}
