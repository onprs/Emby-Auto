//go:build integration

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestRSSPollDiscardsVersionChangedAtPersistenceAndEnqueueIntegration(t *testing.T) {
	for _, queryToReplace := range []int64{1, 2} {
		name := "持久化前替换"
		if queryToReplace == 2 {
			name = "入队前替换"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newRSSAuditWorkerFixture(t, 1)
			updated := false
			client := &rssAuditEmbyClient{onQuery: func(ctx context.Context, query int64) error {
				if query != queryToReplace {
					return nil
				}
				_, err := fixture.workflow.UpdateSubscription(ctx, domain.UpdateRSSSubscription{
					ID: fixture.subscriptionID, ExpectedVersion: 1, MappingProfileID: fixture.profileID,
					Name: "Replacement Feed", FeedURL: "https://example.test/replacement.xml", Enabled: true,
					PollInterval: 15 * time.Minute, SourceSeason: 1, ActorUserID: fixture.actorID,
				})
				updated = err == nil
				return err
			}}
			handler := NewRSSPollHandler(&rssFeedClientStub{feed: fixture.feed}, fixture.workflow, 4).WithRealtimeTargetVerifier(fixture.verifier(client))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := handler.Handle(ctx, domain.Operation{ID: fixture.operationID, ResourceType: "rss_subscription", ResourceID: fixture.subscriptionID}); err != nil {
				t.Fatal(err)
			}
			if !updated {
				t.Fatal("未触发正式订阅更新，测试未覆盖目标边界")
			}
			var acquisitions, failed int
			if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM acquisitions").Scan(&acquisitions); err != nil {
				t.Fatal(err)
			}
			if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM rss_entries WHERE status = 'enqueue_failed'").Scan(&failed); err != nil {
				t.Fatal(err)
			}
			if acquisitions != 0 || failed != 0 {
				t.Fatalf("旧版本响应仍有副作用：acquisitions=%d failed=%d", acquisitions, failed)
			}
		})
	}
}

func TestRSSLegacyFractionalEntryIsRejectedBeforeEnqueueIntegration(t *testing.T) {
	fixture := newRSSAuditWorkerFixture(t, 12)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entryID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, source_season, source_episode)
VALUES ($1, $2, 'guid:legacy-fraction', 'Series S01E12.5', 'https://example.test/fraction.torrent', true, 1, 12)`, entryID, fixture.subscriptionID); err != nil {
		t.Fatal(err)
	}
	checkID, err := fixture.verifier(&rssAuditEmbyClient{}).VerifyEntry(ctx, entryID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.workflow.ScheduleRSSDownloadWithRealtimeCheck(ctx, domain.RSSEnqueueCandidate{EntryID: entryID}, checkID); err != nil {
		t.Fatal(err)
	}
	var acquisitions int
	var downloadable bool
	var reasons []string
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM acquisitions").Scan(&acquisitions); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, "SELECT downloadable, rejection_reasons FROM rss_entries WHERE id = $1", entryID).Scan(&downloadable, &reasons); err != nil {
		t.Fatal(err)
	}
	if acquisitions != 0 || downloadable || len(reasons) != 1 || reasons[0] != "fractional_episode_requires_explicit_mapping" {
		t.Fatalf("历史小数集入队结果：acquisitions=%d downloadable=%t reasons=%v", acquisitions, downloadable, reasons)
	}
}
