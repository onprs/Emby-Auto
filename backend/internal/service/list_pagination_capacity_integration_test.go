//go:build integration

package service

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type paginationMeasurementKey struct{}
type paginationReadGateKey struct{}
type paginationPausedQueryKey struct{}

type paginationReadGate struct {
	ready  chan struct{}
	resume chan struct{}
	once   sync.Once
}

type paginationMeasurement struct {
	commands atomic.Int64
	rows     atomic.Int64
	largest  atomic.Int64
}

type paginationQueryTracer struct{}

func (paginationQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if measurement, ok := ctx.Value(paginationMeasurementKey{}).(*paginationMeasurement); ok {
		measurement.commands.Add(1)
	}
	if gate, ok := ctx.Value(paginationReadGateKey{}).(*paginationReadGate); ok && strings.Contains(data.SQL, "list_acquisition_page_ids(") {
		return context.WithValue(ctx, paginationPausedQueryKey{}, gate)
	}
	return ctx
}

func (paginationQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if measurement, ok := ctx.Value(paginationMeasurementKey{}).(*paginationMeasurement); ok && data.CommandTag.Select() {
		count := data.CommandTag.RowsAffected()
		measurement.rows.Add(count)
		for previous := measurement.largest.Load(); count > previous; previous = measurement.largest.Load() {
			if measurement.largest.CompareAndSwap(previous, count) {
				break
			}
		}
	}
	if gate, ok := ctx.Value(paginationPausedQueryKey{}).(*paginationReadGate); ok && data.Err == nil {
		gate.once.Do(func() {
			close(gate.ready)
			select {
			case <-gate.resume:
			case <-ctx.Done():
			}
		})
	}
}

type paginationFixture struct {
	ctx            context.Context
	pool           *pgxpool.Pool
	queries        *db.Queries
	transactor     *database.Transactor
	reads          *ReadService
	seriesID       uuid.UUID
	subscriptionID uuid.UUID
	count          int
}

func newPaginationFixture(t *testing.T) *paginationFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	t.Cleanup(cancel)
	_, original := testutil.NewMigratedPostgres(t)
	configuration := original.Config()
	configuration.ConnConfig.Tracer = paginationQueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	fixture := &paginationFixture{
		ctx: ctx, pool: pool, queries: queries, transactor: transactor,
		reads: NewReadService(queries, transactor), seriesID: uuid.New(), subscriptionID: uuid.New(),
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series(id,title) VALUES($1,'List capacity series')`, fixture.seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_subscriptions(id,series_id,name,feed_url) VALUES($1,$2,'List capacity feed','https://fixture.test/list.xml')`, fixture.subscriptionID, fixture.seriesID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func paginationID(kind string, number int) uuid.UUID {
	return uuid.NewMD5(uuid.Nil, []byte(fmt.Sprintf("list-%s-%d", kind, number)))
}

// 批量扩充相同规模的条目、获取任务和下载，覆盖排序键相同的游标情况。
func (fixture *paginationFixture) seed(t *testing.T, total int) {
	t.Helper()
	if total <= fixture.count {
		t.Fatal("分页容量夹具应逐步增加数据")
	}
	// UUID v3 包含命名空间，因此将生成好的 ID 按批次传入，避免 SQL 哈希差异。
	entries := make([]pgtype.UUID, 0, total-fixture.count)
	acquisitions := make([]pgtype.UUID, 0, total-fixture.count)
	downloads := make([]pgtype.UUID, 0, total-fixture.count)
	files := make([]pgtype.UUID, 0, total-fixture.count)
	for number := fixture.count + 1; number <= total; number++ {
		entries = append(entries, repository.UUIDToPG(paginationID("entry", number)))
		acquisitions = append(acquisitions, repository.UUIDToPG(paginationID("acquisition", number)))
		downloads = append(downloads, repository.UUIDToPG(paginationID("download", number)))
		files = append(files, repository.UUIDToPG(paginationID("file", number)))
	}
	seedStarted := time.Now()
	base := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO rss_entries(id,subscription_id,identity_key,title,source_season,source_episode,discovered_at,updated_at)
SELECT id,$2,'list-entry-' || (position+$3), 'List entry ' || (position+$3),
       CASE WHEN (position+$3)%7=0 OR (position+$3)%11=0 THEN NULL ELSE 1 END,
       CASE WHEN (position+$3)%7=0 OR (position+$3)%11=0 THEN NULL ELSE ((position+$3)%24+1)::integer END,
       $4::timestamptz + ((position+$3)%13) * interval '1 second',
       $4::timestamptz + ((position+$3)%13) * interval '1 second'
FROM unnest($1::uuid[]) WITH ORDINALITY AS item(id,position)`, []any{entries, fixture.subscriptionID, fixture.count, base}},
		{`INSERT INTO acquisitions(id,series_id,source_kind,rss_entry_id,source_payload,created_at,updated_at)
SELECT acquisition_id,$3,'rss',entry_id,
       jsonb_build_object(
           'sourceSeason',CASE WHEN (position+$4)%7=0 OR (position+$4)%11=0 THEN NULL ELSE 1 END,
           'sourceEpisode',CASE WHEN (position+$4)%7=0 OR (position+$4)%11=0 THEN NULL ELSE ((position+$4)%24+1)::integer END),
       $5::timestamptz + ((position+$4)%13) * interval '1 second',
       $5::timestamptz + ((position+$4)%13) * interval '1 second'
FROM unnest($1::uuid[],$2::uuid[]) WITH ORDINALITY AS item(acquisition_id,entry_id,position)`, []any{acquisitions, entries, fixture.seriesID, fixture.count, base}},
		{`INSERT INTO downloads(id,acquisition_id,status,attempt,progress,client_state,failure_stage,error_code,created_at,updated_at)
SELECT download_id,acquisition_id,CASE WHEN (position+$3)%4=0 THEN 'failed' ELSE 'downloading' END,
       ((position+$3)%3+1)::integer, ((position+$3)%10)::numeric/10,
       CASE (position+$3)%5 WHEN 0 THEN NULL WHEN 1 THEN 'pausedDL' WHEN 2 THEN 'queuedDL' WHEN 3 THEN 'error' ELSE 'downloading' END,
       CASE WHEN (position+$3)%4=0 THEN 'sync' END,
       CASE WHEN (position+$3)%4=0 THEN 'capacity_download_failure' END,
       $4::timestamptz + ((position+$3)%13) * interval '1 second',
       $4::timestamptz + ((position+$3)%13) * interval '1 second'
FROM unnest($1::uuid[],$2::uuid[]) WITH ORDINALITY AS item(download_id,acquisition_id,position)`, []any{downloads, acquisitions, fixture.count, base}},
		{`INSERT INTO download_files(id,download_id,file_index,relative_path,size_bytes,media_kind,selected)
SELECT file_id,download_id,1,'video.mkv',1048576,'video',true
FROM unnest($1::uuid[],$2::uuid[]) AS item(file_id,download_id)`, []any{files, downloads}},
	}
	for _, statement := range statements {
		if _, err := fixture.pool.Exec(fixture.ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	fixture.count = total
	seedDuration := time.Since(seedStarted)
	refreshStarted := time.Now()
	measurement := &paginationMeasurement{}
	refreshContext := context.WithValue(fixture.ctx, paginationMeasurementKey{}, measurement)
	if err := fixture.reads.ReconcileListProjections(refreshContext); err != nil {
		t.Fatalf("恢复列表投影: %v", err)
	}
	if measurement.largest.Load() > int64(listProjectionBatchSize) {
		t.Fatalf("补算未按批次读取: 最大单次=%d", measurement.largest.Load())
	}
	t.Logf("历史=%d 数据准备=%s 投影恢复=%s 最大补算批次=%d", total, seedDuration, time.Since(refreshStarted), measurement.largest.Load())
}

type paginationCost struct {
	commands, rows, largest int64
	allocated               uint64
}

func measurePaginationRead(t *testing.T, fixture *paginationFixture, kind string, read func(context.Context) error) paginationCost {
	t.Helper()
	measurement := &paginationMeasurement{}
	ctx := context.WithValue(fixture.ctx, paginationMeasurementKey{}, measurement)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	if err := read(ctx); err != nil {
		t.Fatalf("读取 %s 列表: %v", kind, err)
	}
	runtime.ReadMemStats(&after)
	cost := paginationCost{measurement.commands.Load(), measurement.rows.Load(), measurement.largest.Load(), after.TotalAlloc - before.TotalAlloc}
	t.Logf("历史=%d 类型=%s SQL=%d 返回行=%d 最大单次=%d 分配=%dB 耗时=%s", fixture.count, kind, cost.commands, cost.rows, cost.largest, cost.allocated, time.Since(started))
	return cost
}

func TestDatabaseListPaginationCostStaysBoundedIntegration(t *testing.T) {
	fixture := newPaginationFixture(t)
	const pageSize = 10
	readers := []struct {
		name        string
		maxCommands int64
		read        func(context.Context) error
	}{
		{"下载", 7, func(ctx context.Context) error {
			page, err := fixture.reads.ListDownloads(ctx, nil, pageSize, nil, nil, nil, nil, nil)
			if err == nil && (len(page.Items) != pageSize || page.NextCursor == nil) {
				return fmt.Errorf("下载页大小或游标不正确")
			}
			return err
		}},
		{"获取", 13, func(ctx context.Context) error {
			page, err := fixture.reads.ListAcquisitions(ctx, nil, pageSize, nil, nil, nil, nil, nil)
			if err == nil && (len(page.Items) != pageSize || page.NextCursor == nil) {
				return fmt.Errorf("获取页大小或游标不正确")
			}
			return err
		}},
		{"RSS 条目", 18, func(ctx context.Context) error {
			page, err := fixture.reads.ListRSSEntries(ctx, fixture.subscriptionID, nil, pageSize, nil, nil, nil, nil, nil, nil)
			if err == nil && (len(page.Items) != pageSize || page.NextCursor == nil) {
				return fmt.Errorf("RSS 页大小或游标不正确")
			}
			return err
		}},
		{"仪表盘", 25, func(ctx context.Context) error {
			view, err := fixture.reads.DashboardSummary(ctx)
			if err == nil && (view.Counts.Attention != fixture.count/4 || len(view.AttentionItems) != 5) {
				return fmt.Errorf("待处理总数或详情上限不正确: %d/%d", view.Counts.Attention, len(view.AttentionItems))
			}
			return err
		}},
	}
	baseline := make(map[string]paginationCost)
	for _, total := range []int{64, 8192} {
		fixture.seed(t, total)
		for _, reader := range readers {
			cost := measurePaginationRead(t, fixture, reader.name, reader.read)
			if cost.commands > reader.maxCommands || cost.rows > 100 || cost.largest > pageSize {
				t.Errorf("%s 每页读取超过固定预算: %+v", reader.name, cost)
			}
			if total == 64 {
				baseline[reader.name] = cost
			} else {
				previous := baseline[reader.name]
				if cost.commands != previous.commands || cost.rows != previous.rows {
					t.Errorf("%s 读取随历史增长: 小规模=%+v 大规模=%+v", reader.name, previous, cost)
				}
				if cost.allocated > max(uint64(1<<20), previous.allocated*4) {
					t.Errorf("%s 每页分配随历史增长: 小规模=%d 大规模=%d", reader.name, previous.allocated, cost.allocated)
				}
			}
		}
		// 单独验证数据库返回的候选 ID 也受限，避免把全量 ID 藏入一个数组结果。
		downloadIDs, err := fixture.queries.ListDownloadPageIDs(fixture.ctx, db.ListDownloadPageIDsParams{SortBy: "updated_at", DescendingOrder: true, RowLimit: pageSize + 1})
		if err != nil || len(downloadIDs) != pageSize+1 {
			t.Fatalf("下载候选 ID 未分页: %d / %v", len(downloadIDs), err)
		}
		acquisitionIDs, err := fixture.queries.ListAcquisitionPageIDs(fixture.ctx, db.ListAcquisitionPageIDsParams{SortBy: "updated_at", DescendingOrder: true, RowLimit: pageSize + 1})
		if err != nil || len(acquisitionIDs) != pageSize+1 {
			t.Fatalf("获取候选 ID 未分页: %d / %v", len(acquisitionIDs), err)
		}
		entryIDs, err := fixture.queries.ListRSSEntryPageIDs(fixture.ctx, db.ListRSSEntryPageIDsParams{SubscriptionID: repository.UUIDToPG(fixture.subscriptionID), SortBy: "discovered_at", DescendingOrder: true, RowLimit: pageSize + 1})
		if err != nil || len(entryIDs) != pageSize+1 {
			t.Fatalf("条目候选 ID 未分页: %d / %v", len(entryIDs), err)
		}
	}
}
