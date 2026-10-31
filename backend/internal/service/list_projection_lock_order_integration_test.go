//go:build integration

package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

func waitForListProjectionBlock(t *testing.T, fixture *paginationFixture, blocker, blocked uint32, completed <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(fixture.ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := fixture.pool.QueryRow(ctx, `SELECT $1::integer=ANY(pg_blocking_pids($2::integer))`, blocker, blocked).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ticker.C:
		case err := <-completed:
			t.Fatalf("事务在锁等待之前结束: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestListProjectionBulkEntryAndCatalogUpdatesUseConsistentLocksIntegration(t *testing.T) {
	fixture := newPaginationFixture(t)
	fixture.seed(t, 8)
	type relatedEntry struct {
		acquisitionID uuid.UUID
		entryID       uuid.UUID
	}
	entries := make([]relatedEntry, 8)
	for index := range entries {
		entries[index] = relatedEntry{acquisitionID: paginationID("acquisition", index+1), entryID: paginationID("entry", index+1)}
	}
	slices.SortFunc(entries, func(left, right relatedEntry) int {
		return strings.Compare(left.acquisitionID.String(), right.acquisitionID.String())
	})
	first, second := entries[len(entries)-1], entries[0]
	entryUpdated, continueEntries := make(chan struct{}), make(chan struct{})
	var continueOnce sync.Once
	resume := func() { continueOnce.Do(func() { close(continueEntries) }) }
	defer resume()
	entryPID, catalogPID := make(chan uint32, 1), make(chan uint32, 1)
	completed := make(chan error, 2)
	go func() {
		completed <- fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			entryPID <- scope.Tx.Conn().PgConn().PID()
			if _, err := scope.Tx.Exec(fixture.ctx, `UPDATE rss_entries SET title='First updated entry' WHERE id=$1`, first.entryID); err != nil {
				return err
			}
			close(entryUpdated)
			select {
			case <-continueEntries:
			case <-fixture.ctx.Done():
				return fixture.ctx.Err()
			}
			_, err := scope.Tx.Exec(fixture.ctx, `UPDATE rss_entries SET title='Second updated entry' WHERE id=$1`, second.entryID)
			return err
		})
	}()
	select {
	case <-entryUpdated:
	case err := <-completed:
		t.Fatalf("条目事务提前结束: %v", err)
	case <-fixture.ctx.Done():
		t.Fatal(fixture.ctx.Err())
	}
	go func() {
		completed <- fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			catalogPID <- scope.Tx.Conn().PgConn().PID()
			_, err := scope.Tx.Exec(fixture.ctx, `UPDATE media_series SET title='Updated catalog title',updated_at=now() WHERE id=$1`, fixture.seriesID)
			return err
		})
	}()
	waitForListProjectionBlock(t, fixture, <-entryPID, <-catalogPID, completed)
	resume()
	for range 2 {
		if err := <-completed; err != nil {
			t.Errorf("批量条目与目录更新发生锁冲突: %v", err)
		}
	}
	for _, kind := range []listProjectionKind{acquisitionListProjection, rssEntryListProjection} {
		state, err := fixture.queries.GetListProjectionReadiness(fixture.ctx, db.GetListProjectionReadinessParams{Kind: string(kind), TargetModelVersion: listProjectionVersion})
		if err != nil || state.NeedsRefresh {
			t.Fatalf("事务提交后投影仍过期: %s / %+v / %v", kind, state, err)
		}
	}
	var titleCount int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM acquisition_list_projections WHERE content_title='updated catalog title'`).Scan(&titleCount); err != nil || titleCount != 8 {
		t.Fatalf("目录标题排序键没有同步: %d / %v", titleCount, err)
	}
	query := "updated entry"
	page, err := fixture.reads.ListRSSEntries(fixture.ctx, fixture.subscriptionID, nil, 10, nil, nil, &query, nil, nil, nil)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("两个条目的标题索引没有同步: %#v / %v", page, err)
	}
}

func TestListProjectionRecoveryDoesNotReverseSubscriptionLocksIntegration(t *testing.T) {
	fixture := newPaginationFixture(t)
	fixture.seed(t, 8)
	acquisitionID := paginationID("acquisition", 1)
	before, _ := acquisitionPageItem(t, fixture.ctx, fixture.reads, acquisitionID)
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE acquisition_list_projections SET dirty=true,source_revision=source_revision+1,dirtied_transaction_id=txid_current() WHERE acquisition_id=$1`, acquisitionID); err != nil {
		t.Fatal(err)
	}
	parentLocked, continueRecovery := make(chan struct{}), make(chan struct{})
	var continueOnce sync.Once
	resume := func() { continueOnce.Do(func() { close(continueRecovery) }) }
	defer resume()
	recoveryPID, writerPID := make(chan uint32, 1), make(chan uint32, 1)
	completed := make(chan error, 2)
	go func() {
		completed <- fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			recoveryPID <- scope.Tx.Conn().PgConn().PID()
			rows, err := scope.Queries.LockAcquisitionListProjectionBatch(fixture.ctx, db.LockAcquisitionListProjectionBatchParams{TransactionOnly: false, BatchSize: 1})
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				return fmt.Errorf("恢复候选数量错误: %d", len(rows))
			}
			close(parentLocked)
			select {
			case <-continueRecovery:
			case <-fixture.ctx.Done():
				return fixture.ctx.Err()
			}
			return calculateAcquisitionListProjections(fixture.ctx, scope.Queries, rows)
		})
	}()
	select {
	case <-parentLocked:
	case err := <-completed:
		t.Fatalf("恢复事务提前结束: %v", err)
	case <-fixture.ctx.Done():
		t.Fatal(fixture.ctx.Err())
	}
	go func() {
		completed <- fixture.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			writerPID <- scope.Tx.Conn().PgConn().PID()
			_, err := scope.Tx.Exec(fixture.ctx, `UPDATE downloads SET progress=0.95,updated_at=now() WHERE id=$1`, paginationID("download", 1))
			return err
		})
	}()
	waitForListProjectionBlock(t, fixture, <-recoveryPID, <-writerPID, completed)
	resume()
	for range 2 {
		if err := <-completed; err != nil {
			t.Errorf("后台补算与业务写入发生锁冲突: %v", err)
		}
	}
	after, _ := acquisitionPageItem(t, fixture.ctx, fixture.reads, acquisitionID)
	if after.OverallProgress <= before.OverallProgress {
		t.Fatal("业务提交没有覆盖补算时读取的旧进度")
	}
}
