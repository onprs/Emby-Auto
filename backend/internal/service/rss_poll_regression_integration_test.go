//go:build integration

package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func rssAuditAbsentCheck(t *testing.T, fixture rssTargetFixture) uuid.UUID {
	t.Helper()
	checkID := uuid.New()
	if err := fixture.workflow.transactor.WithinTx(fixture.ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		for _, targetID := range fixture.targetEpisodeIDs {
			if _, err := scope.Tx.Exec(fixture.ctx, `
INSERT INTO rss_target_realtime_checks (target_episode_id, check_id, present, match_source, checked_at)
VALUES ($1, $2, false, 'absent', now())`, targetID, checkID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return checkID
}

func TestRSSAuditHardRejectedEntryDoesNotAbortPollIntegration(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		uri     string
		include []string
		exclude []string
	}{
		{name: "排除关键词", title: "Audit Series S01E03 [1080p]", uri: "https://example.test/rejected.torrent", exclude: []string{"1080p"}},
		{name: "包含关键词不匹配", title: "Audit Series S01E03 [1080p]", uri: "https://example.test/rejected.torrent", include: []string{"720p"}},
		{name: "缺少下载地址", title: "Audit Series S01E03 [1080p]"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRSSTargetFixture(t)
			if _, err := fixture.pool.Exec(fixture.ctx, `
UPDATE rss_subscriptions SET include_keywords = $2, exclude_keywords = $3 WHERE id = $1`,
				fixture.subscriptionID, append([]string{}, testCase.include...), append([]string{}, testCase.exclude...)); err != nil {
				t.Fatal(err)
			}
			checkID := rssAuditAbsentCheck(t, fixture)
			feed := domain.RSSFeed{Title: "Audit Feed", Entries: []domain.RSSFeedEntry{
				{GUID: "eligible", Title: "Audit Series S01E01 [720p]", DownloadURI: "https://example.test/eligible.torrent"},
				{GUID: "rejected", Title: testCase.title, DownloadURI: testCase.uri},
			}}
			result, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, feed,
				domain.RSSPollPersistOptions{RealtimeCheckID: checkID})
			if err != nil {
				var entryCount int
				if scanErr := fixture.pool.QueryRow(fixture.ctx, "SELECT count(*) FROM rss_entries WHERE subscription_id = $1", fixture.subscriptionID).Scan(&entryCount); scanErr != nil {
					t.Fatal(scanErr)
				}
				t.Fatalf("硬拒绝条目导致整轮回滚：%v，数据库条目数=%d", err, entryCount)
			}
			if len(result.Candidates) != 1 {
				t.Fatalf("候选数=%d，期望保留 1 个合法条目", len(result.Candidates))
			}
		})
	}
}

func TestRSSAuditPollRetainsResolvedCoordinatesIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	entry := domain.RSSFeedEntry{GUID: "resolved-coordinate", Title: "Audit Series unusual-release", DownloadURI: "https://example.test/resolved.torrent"}
	feed := domain.RSSFeed{Title: "Audit Feed", Entries: []domain.RSSFeedEntry{entry}}
	if _, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, feed, domain.RSSPollPersistOptions{}); err != nil {
		t.Fatal(err)
	}
	var entryID uuid.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, "SELECT id FROM rss_entries WHERE subscription_id = $1", fixture.subscriptionID).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	// 构造已由 Agent 确定坐标、尚未创建 acquisition 的持久状态。
	resolutionID, operationID := uuid.New(), uuid.New()
	if _, err := testutil.ExecFixture(fixture.ctx, fixture.pool, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, finished_at)
VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'succeeded', 3, 60, now());
INSERT INTO agent_resolutions (
  id, operation_id, capability, resource_type, resource_id, trigger, status, applied_at,
  input_fingerprint, configuration_version, protocol, provider_origin, model, prompt_version, toolset_version
) VALUES ($2, $1, 'rss_coordinate', 'rss_entry', $4, 'automatic', 'applied', now(),
  $5, 1, 'openai_chat_completions', 'https://provider.example', 'audit-model', 'rss-coordinate-v1', 'agent-tools-v1');
UPDATE rss_entries SET source_season = 1, source_episode = 1,
  coordinate_source = 'agent_auto', agent_resolution_id = $2,
  downloadable = true, rejection_reasons = ARRAY[]::text[]
WHERE id = $4`, operationID, resolutionID, "rss-audit-resolution-"+resolutionID.String(), entryID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, feed,
		domain.RSSPollPersistOptions{RealtimeCheckID: rssAuditAbsentCheck(t, fixture)})
	if err != nil {
		t.Fatal(err)
	}
	var downloadable bool
	var reasons []string
	if err := fixture.pool.QueryRow(fixture.ctx, "SELECT downloadable, rejection_reasons FROM rss_entries WHERE subscription_id = $1", fixture.subscriptionID).Scan(&downloadable, &reasons); err != nil {
		t.Fatal(err)
	}
	if !downloadable || len(result.Candidates) != 1 {
		t.Fatalf("下一轮丢失已解析条目：downloadable=%t reasons=%v candidates=%d", downloadable, reasons, len(result.Candidates))
	}
}

func TestRSSAuditRepeatedIdentityHasNoPendingAdjudicationIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	entry := domain.RSSFeedEntry{GUID: "repeated-release", Title: "Audit Series S01E01", DownloadURI: "magnet:?xt=urn:btih:1111111111111111111111111111111111111111"}
	result, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID,
		domain.RSSFeed{Title: "Audit Feed", Entries: []domain.RSSFeedEntry{entry, entry}}, domain.RSSPollPersistOptions{AdjudicateReleases: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AgentAdjudicationBatchIDs) != 0 || len(result.Candidates) != 1 {
		t.Fatalf("重复记录阻塞确定性入队：裁决批次数=%d 候选数=%d", len(result.AgentAdjudicationBatchIDs), len(result.Candidates))
	}
}
