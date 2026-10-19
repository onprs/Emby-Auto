//go:build integration

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

type rssBatchJobInserter struct {
	base   RiverJobInserter
	calls  int
	failAt int
	cancel context.CancelFunc
}

func (inserter *rssBatchJobInserter) InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, options *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	inserter.calls++
	if inserter.calls == inserter.failAt {
		return nil, errors.New("injected River insertion failure")
	}
	result, err := inserter.base.InsertTx(ctx, tx, args, options)
	if inserter.cancel != nil {
		inserter.cancel()
	}
	return result, err
}

func rssBatchCandidates(t *testing.T, fixture rssTargetFixture, subscriptionID uuid.UUID) []domain.RSSEnqueueCandidate {
	t.Helper()
	feed := domain.RSSFeed{Title: "Batch Feed", Entries: []domain.RSSFeedEntry{
		{GUID: "batch-first", Title: "Batch Series S01E01", DownloadURI: "https://example.test/first.torrent"},
		{GUID: "batch-failure", Title: "Batch Series S01E02", DownloadURI: "https://example.test/failure.torrent"},
		{GUID: "batch-after", Title: "Batch Series S01E02", DownloadURI: "https://example.test/after.torrent"},
	}}
	if _, err := fixture.workflow.PersistPoll(fixture.ctx, uuid.Nil, subscriptionID, feed, domain.RSSPollPersistOptions{}); err != nil {
		t.Fatal(err)
	}
	// 该 fixture 通过原始 SQL 建立订阅，先收敛它留下的 dirty 投影。
	if _, err := fixture.workflow.ReconcileSubscriptionProgress(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	candidates := make([]domain.RSSEnqueueCandidate, 0, len(feed.Entries))
	for _, item := range feed.Entries {
		var id uuid.UUID
		if err := fixture.pool.QueryRow(fixture.ctx, "SELECT id FROM rss_entries WHERE subscription_id = $1 AND guid = $2", subscriptionID, item.GUID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, domain.RSSEnqueueCandidate{EntryID: id, Downloadable: true, Status: domain.RSSDiscovered, ExpectedSubscriptionVersion: 1})
	}
	return candidates
}

func rssBatchRealJobs(t *testing.T, fixture rssTargetFixture) RiverJobInserter {
	t.Helper()
	client, err := river.NewClient(riverpgxv5.New(fixture.pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertRSSBatchCounts(t *testing.T, fixture rssTargetFixture, count int) {
	t.Helper()
	var acquisitions, downloads, operations, jobs, events int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT (SELECT count(*) FROM acquisitions), (SELECT count(*) FROM downloads),
       (SELECT count(*) FROM operations WHERE kind = 'download.enqueue'),
       (SELECT count(*) FROM river_job),
       (SELECT count(*) FROM events WHERE topic = 'rss.entry.enqueueing')
`).Scan(&acquisitions, &downloads, &operations, &jobs, &events); err != nil {
		t.Fatal(err)
	}
	if acquisitions != count || downloads != count || operations != count || jobs != count || events != count {
		t.Fatalf("批次原子性不一致：acquisitions=%d downloads=%d operations=%d river=%d events=%d，期望=%d", acquisitions, downloads, operations, jobs, events, count)
	}
	var dirty bool
	var source, calculated int64
	if err := fixture.pool.QueryRow(fixture.ctx, "SELECT dirty, source_revision, calculated_revision FROM rss_subscription_progress WHERE subscription_id = $1", fixture.subscriptionID).Scan(&dirty, &source, &calculated); err != nil {
		t.Fatal(err)
	}
	if dirty || source != calculated {
		t.Fatalf("提交后进度未同步：dirty=%t source=%d calculated=%d", dirty, source, calculated)
	}
}

func TestRSSBatchEnqueueRollsBackFailedItemAndContinuesIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	candidates := rssBatchCandidates(t, fixture, fixture.subscriptionID)
	inserter := &rssBatchJobInserter{base: rssBatchRealJobs(t, fixture), failAt: 2}
	fixture.workflow.operations = NewOperationScheduler(fixture.workflow.transactor, inserter)
	checkID := rssAuditAbsentCheck(t, fixture)
	result, err := fixture.workflow.ScheduleRSSDownloadsWithRealtimeCheck(fixture.ctx, candidates, checkID)
	if err != nil || len(result.Outcomes) != 3 || result.Outcomes[0].Err != nil || result.Outcomes[1].Err == nil || result.Outcomes[2].Err != nil {
		t.Fatalf("逐项保存点结果=%+v，批次错误=%v", result.Outcomes, err)
	}
	assertRSSBatchCounts(t, fixture, 2)
	var status string
	if err := fixture.pool.QueryRow(fixture.ctx, "SELECT status FROM rss_entries WHERE id = $1", candidates[1].EntryID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "enqueue_failed" {
		t.Fatalf("失败条目状态=%s", status)
	}
	if _, err := fixture.workflow.ScheduleRSSDownloadsWithRealtimeCheck(fixture.ctx, candidates, checkID); err != nil {
		t.Fatal(err)
	}
	assertRSSBatchCounts(t, fixture, 2)
}

func TestRSSBatchEnqueueCommitFailureRollsBackJobsEventsAndProgressIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	candidates := rssBatchCandidates(t, fixture, fixture.subscriptionID)
	fixture.workflow.operations = NewOperationScheduler(fixture.workflow.transactor, rssBatchRealJobs(t, fixture))
	checkID := rssAuditAbsentCheck(t, fixture)
	failure := errors.New("injected before commit failure")
	fixture.workflow.transactor.RegisterBeforeCommitHook("test_batch_commit", func(context.Context, database.TxScope) error { return failure })
	result, err := fixture.workflow.ScheduleRSSDownloadsWithRealtimeCheck(fixture.ctx, candidates, checkID)
	if !errors.Is(err, failure) || len(result.Outcomes) != 0 {
		t.Fatalf("提交失败返回了成功结果：%+v err=%v", result, err)
	}
	assertRSSBatchCounts(t, fixture, 0)
}

func TestRSSBatchEnqueueCancellationRollsBackPartialWorkIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	candidates := rssBatchCandidates(t, fixture, fixture.subscriptionID)
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	fixture.workflow.operations = NewOperationScheduler(fixture.workflow.transactor, &rssBatchJobInserter{base: rssBatchRealJobs(t, fixture), cancel: cancel})
	result, err := fixture.workflow.ScheduleRSSDownloadsWithRealtimeCheck(ctx, candidates, rssAuditAbsentCheck(t, fixture))
	if err == nil || ctx.Err() == nil || len(result.Outcomes) != 0 {
		t.Fatalf("取消后返回了成功结果：%+v err=%v", result, err)
	}
	assertRSSBatchCounts(t, fixture, 0)
}

func TestRSSBatchEnqueueConcurrentSubscriptionsReserveTargetsOnceIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	fixture.workflow.operations = NewOperationScheduler(fixture.workflow.transactor, rssBatchRealJobs(t, fixture))
	secondID := uuid.New()
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO rss_subscriptions (id, series_id, mapping_profile_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, $3, 'Competing Feed', 'https://example.test/competing.xml', true, 900, 1)`, secondID, fixture.seriesID, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	first := rssBatchCandidates(t, fixture, fixture.subscriptionID)[:2]
	second := rssBatchCandidates(t, fixture, secondID)[:2]
	// 反转另一订阅的条目顺序，检验批次目标锁的统一顺序。
	second[0], second[1] = second[1], second[0]
	checkID := rssAuditAbsentCheck(t, fixture)
	start, results := make(chan struct{}), make(chan error, 2)
	for _, candidates := range [][]domain.RSSEnqueueCandidate{first, second} {
		go func() {
			<-start
			result, err := fixture.workflow.ScheduleRSSDownloadsWithRealtimeCheck(fixture.ctx, candidates, checkID)
			if err == nil {
				for _, outcome := range result.Outcomes {
					err = errors.Join(err, outcome.Err)
				}
			}
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	assertRSSBatchCounts(t, fixture, 2)
}

func TestRSSRealtimeCheckExpiresInsideTransactionIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	checkID := uuid.New()
	err := fixture.workflow.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Tx.Exec(fixture.ctx, `
INSERT INTO rss_target_realtime_checks (target_episode_id, check_id, present, match_source, checked_at)
VALUES ($1, $2, false, 'absent', statement_timestamp() - interval '29 seconds')`, fixture.targetEpisodeIDs[0], checkID); err != nil {
			return err
		}
		if _, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(fixture.ctx, scope.Queries, fixture.subscriptionID, 1, 1, uuid.Nil, checkID); err != nil {
			return err
		}
		if _, err := scope.Tx.Exec(fixture.ctx, "SELECT pg_sleep(1.1)"); err != nil {
			return err
		}
		_, err := loadRSSMappedTargetOccupancyWithRealtimeCheck(fixture.ctx, scope.Queries, fixture.subscriptionID, 1, 1, uuid.Nil, checkID)
		var serviceErr *Error
		if !errors.As(err, &serviceErr) || serviceErr.Code != "rss_realtime_check_expired" {
			t.Fatalf("事务内已经过期的证据仍被接受：%v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
