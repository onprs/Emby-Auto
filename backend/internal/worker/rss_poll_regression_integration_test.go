//go:build integration

package worker

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type rssAuditWorkerFixture struct {
	pool           *pgxpool.Pool
	workflow       *service.RSSWorkflow
	transactor     *database.Transactor
	subscriptionID uuid.UUID
	actorID        uuid.UUID
	profileID      uuid.UUID
	operationID    uuid.UUID
	feed           domain.RSSFeed
}

func newRSSAuditWorkerFixture(t *testing.T, entryCount int) rssAuditWorkerFixture {
	t.Helper()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := rssAuditWorkerFixture{
		pool: pool, transactor: transactor,
		workflow:       service.NewRSSWorkflow(db.New(pool), transactor, service.NewOperationScheduler(transactor, riverClient)),
		subscriptionID: uuid.New(), actorID: uuid.New(), profileID: uuid.New(), operationID: uuid.New(),
	}
	seriesID, seasonID := uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(context.Background(), pool, `
INSERT INTO admin_users (id, username, password_hash) VALUES ($7, 'audit-admin', 'audit-hash');
INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, 800001, 'Audit Series');
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($2, $1, 1, $6);
INSERT INTO media_episodes (id, season_id, tmdb_episode_id, episode_number, title)
SELECT gen_random_uuid(), $2, 900000 + number, number, 'Audit Episode ' || number FROM generate_series(1, $6) AS number;
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active, decision_source)
VALUES ($3, $1, 'audit-profile', 1, ARRAY[$6::integer], true, 'deterministic');
INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, absolute_episode, target_episode_id, mapping_status, match_source)
SELECT gen_random_uuid(), $3, 1, episode_number, episode_number, id, 'mapped', 'absolute' FROM media_episodes WHERE season_id = $2;
INSERT INTO rss_subscriptions (id, series_id, mapping_profile_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($4, $1, $3, 'Audit Feed', 'https://example.test/original.xml', true, 900, 1);
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($5, 'rss.poll', 'rss_subscription', $4, 'rss-audit-poll', 'running', 5, 30)`,
		seriesID, seasonID, fixture.profileID, fixture.subscriptionID, fixture.operationID, entryCount, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	fixture.feed = domain.RSSFeed{Title: "Audit Feed", Entries: make([]domain.RSSFeedEntry, entryCount)}
	for index := range fixture.feed.Entries {
		fixture.feed.Entries[index] = domain.RSSFeedEntry{
			GUID: fmt.Sprintf("audit-%d", index), Title: fmt.Sprintf("Audit Series S01E%04d", index+1),
			DownloadURI: fmt.Sprintf("https://example.test/%d.torrent", index+1),
		}
	}
	return fixture
}

type rssAuditEmbyClient struct {
	calls   atomic.Int64
	latency time.Duration
	onQuery func(context.Context, int64) error
}

func (client *rssAuditEmbyClient) SeriesEpisodesByTMDb(ctx context.Context, _ int64) ([]domain.EmbyLibraryItemCatalog, error) {
	query := client.calls.Add(1)
	if client.onQuery != nil {
		if err := client.onQuery(ctx, query); err != nil {
			return nil, err
		}
	}
	if client.latency > 0 {
		timer := time.NewTimer(client.latency)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, nil
}

func (fixture rssAuditWorkerFixture) verifier(client *rssAuditEmbyClient) *RSSRealtimeEmbyVerifier {
	return NewRSSRealtimeEmbyVerifier(realtimeConfigurationStub{}, db.New(fixture.pool), fixture.transactor,
		func(emby.ClientOptions) (RSSRealtimeEmbyClient, error) { return client, nil })
}

func TestRSSAuditConcurrentPollsDoNotDuplicateAcquisitionsIntegration(t *testing.T) {
	fixture := newRSSAuditWorkerFixture(t, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	secondOperationID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'rss.poll', 'rss_subscription', $2, 'rss-audit-concurrent-poll', 'running', 5, 30)`, secondOperationID, fixture.subscriptionID); err != nil {
		t.Fatal(err)
	}
	client := &rssAuditEmbyClient{}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, operationID := range []uuid.UUID{fixture.operationID, secondOperationID} {
		handler := NewRSSPollHandler(&rssFeedClientStub{feed: fixture.feed}, fixture.workflow, 4).WithRealtimeTargetVerifier(fixture.verifier(client))
		go func() {
			<-start
			results <- handler.Handle(ctx, domain.Operation{ID: operationID, ResourceType: "rss_subscription", ResourceID: fixture.subscriptionID})
		}()
	}
	close(start)
	for index := 0; index < 2; index++ {
		if err := <-results; err != nil {
			t.Fatalf("并发轮询失败：%v", err)
		}
	}
	var acquisitions, downloads, duplicateTargets int
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM acquisitions").Scan(&acquisitions); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM downloads").Scan(&downloads); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `
SELECT count(*) FROM (
 SELECT entry.source_season, entry.source_episode
 FROM acquisitions AS acquisition JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
 GROUP BY entry.source_season, entry.source_episode HAVING count(*) > 1
) AS duplicates`).Scan(&duplicateTargets); err != nil {
		t.Fatal(err)
	}
	if acquisitions != 10 || downloads != 10 || duplicateTargets != 0 {
		t.Fatalf("并发结果不符合幂等约束：acquisitions=%d downloads=%d duplicate_targets=%d", acquisitions, downloads, duplicateTargets)
	}
}

func TestRSSAuditFractionalFeedDoesNotEnqueueRegularTargetIntegration(t *testing.T) {
	for _, title := range []string{"Audit Series S01E12.5 [1080p]", "Audit Series 第12.5集 [1080p]"} {
		t.Run(title, func(t *testing.T) {
			fixture := newRSSAuditWorkerFixture(t, 12)
			feed := domain.RSSFeed{Title: "Audit Feed", Entries: []domain.RSSFeedEntry{{
				GUID: "audit-fractional", Title: title, DownloadURI: "https://example.test/fractional.torrent",
			}}}
			client := &rssAuditEmbyClient{}
			handler := NewRSSPollHandler(&rssFeedClientStub{feed: feed}, fixture.workflow, 4).WithRealtimeTargetVerifier(fixture.verifier(client))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := handler.Handle(ctx, domain.Operation{ID: fixture.operationID, ResourceType: "rss_subscription", ResourceID: fixture.subscriptionID}); err != nil {
				t.Fatal(err)
			}
			var acquisitions int
			var sourceEpisode int
			if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM acquisitions").Scan(&acquisitions); err != nil {
				t.Fatal(err)
			}
			if err := fixture.pool.QueryRow(ctx, "SELECT COALESCE(source_episode, 0) FROM rss_entries").Scan(&sourceEpisode); err != nil {
				t.Fatal(err)
			}
			if acquisitions != 0 {
				t.Fatalf("小数集已创建整数正片获取任务：acquisitions=%d source_episode=%d", acquisitions, sourceEpisode)
			}
		})
	}
}

type rssAuditFeedFunc func(context.Context, string) (domain.RSSFeed, error)

func (fetch rssAuditFeedFunc) Fetch(ctx context.Context, feedURL string) (domain.RSSFeed, error) {
	return fetch(ctx, feedURL)
}

func TestRSSAuditFeedReplacementDiscardsInflightPollIntegration(t *testing.T) {
	fixture := newRSSAuditWorkerFixture(t, 1)
	client := &rssAuditEmbyClient{}
	fetch := rssAuditFeedFunc(func(ctx context.Context, feedURL string) (domain.RSSFeed, error) {
		if feedURL != "https://example.test/original.xml" {
			t.Fatalf("起始 URL=%s", feedURL)
		}
		// 模拟旧来源响应返回之前，用户通过正式入口修改订阅源。
		_, err := fixture.workflow.UpdateSubscription(ctx, domain.UpdateRSSSubscription{
			ID: fixture.subscriptionID, ExpectedVersion: 1, MappingProfileID: fixture.profileID,
			Name: "Audit Feed", FeedURL: "https://example.test/replacement.xml", Enabled: true,
			PollInterval: 15 * time.Minute, SourceSeason: 1, ActorUserID: fixture.actorID,
		})
		if err != nil {
			t.Fatalf("通过正式入口更新订阅失败：%v", err)
		}
		return fixture.feed, nil
	})
	handler := NewRSSPollHandler(fetch, fixture.workflow, 4).WithRealtimeTargetVerifier(fixture.verifier(client))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := handler.Handle(ctx, domain.Operation{
		ID: fixture.operationID, ResourceType: "rss_subscription", ResourceID: fixture.subscriptionID,
		Payload: []byte(`{"continuous":true,"subscriptionVersion":1}`),
	})
	var acquisitions, entries int
	if scanErr := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM acquisitions").Scan(&acquisitions); scanErr != nil {
		t.Fatal(scanErr)
	}
	if scanErr := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM rss_entries").Scan(&entries); scanErr != nil {
		t.Fatal(scanErr)
	}
	if acquisitions != 0 || entries != 0 {
		t.Fatalf("旧来源在订阅更新后仍被消费：entries=%d acquisitions=%d handle_error=%v", entries, acquisitions, err)
	}
}
