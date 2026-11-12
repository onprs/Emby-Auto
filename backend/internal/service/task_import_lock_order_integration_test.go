//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

// waitForIntegrationPgSleep 等待目标连通的事务在执行 pg_sleep，用于把触发器
// 持锁窗口放大到可稳定观察到另一条业务路径争用同一批行。
func waitForIntegrationPgSleep(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var sleeping int
		if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event = 'PgSleep'
  AND pid <> pg_backend_pid()
`).Scan(&sleeping); err != nil {
			t.Fatal(err)
		}
		if sleeping > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待导入完成事务进入持锁窗口超时")
}

// TestCompleteImportAndSubscriptionEditDoNotDeadlockIntegration 覆盖导入完成
// （订阅行必须先行）与订阅编辑（订阅行 → 进度行）的真实事务竞争。导入触发器
// 的人为延迟只放大既有的持锁时间，不改变任何业务顺序。
func TestCompleteImportAndSubscriptionEditDoNotDeadlockIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	subscriptionID, taskID, importID, operationID := createRSSImportingTask(t, fixture, 1, 2, false)

	if _, err := fixture.pool.Exec(ctx, `
CREATE FUNCTION audit_pause_import_update() RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
    PERFORM pg_sleep(1.5);
    RETURN NEW;
END
$function$;
CREATE TRIGGER zz_audit_pause_import_update
    AFTER UPDATE ON imports
    FOR EACH ROW EXECUTE FUNCTION audit_pause_import_update();
`); err != nil {
		t.Fatal(err)
	}

	var current struct {
		version                   int32
		mappingProfileID          *uuid.UUID
		name                      string
		feedURL                   string
		enabled                   bool
		autoEpisodeMapping        bool
		autoReview                bool
		cleanupSourceOnCompletion bool
		pollIntervalSeconds       int32
		sourceSeason              int32
	}
	if err := fixture.pool.QueryRow(ctx, `
SELECT version, mapping_profile_id, name, feed_url, enabled, auto_episode_mapping,
       auto_review, cleanup_source_on_completion, poll_interval_seconds, source_season
FROM rss_subscriptions
WHERE id = $1
`, subscriptionID).Scan(
		&current.version, &current.mappingProfileID, &current.name, &current.feedURL,
		&current.enabled, &current.autoEpisodeMapping, &current.autoReview,
		&current.cleanupSourceOnCompletion, &current.pollIntervalSeconds, &current.sourceSeason,
	); err != nil {
		t.Fatal(err)
	}
	mappingProfileID := uuid.Nil
	if current.mappingProfileID != nil {
		mappingProfileID = *current.mappingProfileID
	}

	taskWorkflow := NewTaskWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	rssWorkflow := NewRSSWorkflow(db.New(fixture.pool), fixture.transactor, fixture.scheduler)
	importFinished := make(chan error, 1)
	go func() {
		importFinished <- taskWorkflow.CompleteImport(ctx, domain.ImportCompletion{
			TaskID:                  taskID,
			ImportID:                importID,
			OperationID:             operationID,
			DestinationVideoPath:    "/library/LockOrder/Season1/LockOrder - S01E01.mkv",
			DestinationSubtitlePath: "/library/LockOrder/Season1/LockOrder - S01E01.ass",
		})
	}()
	waitForIntegrationPgSleep(t, ctx, fixture.pool)

	editFinished := make(chan error, 1)
	go func() {
		_, err := rssWorkflow.UpdateSubscription(ctx, domain.UpdateRSSSubscription{
			ID:                        subscriptionID,
			ExpectedVersion:           current.version,
			MappingProfileID:          mappingProfileID,
			Name:                      "RSS lock order edited",
			FeedURL:                   current.feedURL,
			Enabled:                   current.enabled,
			AutoEpisodeMapping:        current.autoEpisodeMapping,
			AutoReview:                current.autoReview,
			CleanupSourceOnCompletion: current.cleanupSourceOnCompletion,
			SourceSeason:              int(current.sourceSeason),
			PollInterval:              time.Duration(current.pollIntervalSeconds) * time.Second,
			ActorUserID:               fixture.actorID,
		})
		editFinished <- err
	}()

	for name, finished := range map[string]<-chan error{"导入完成": importFinished, "订阅编辑": editFinished} {
		select {
		case err := <-finished:
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%s等待锁超时: %v", name, err)
				}
				t.Fatalf("%s失败，事务锁顺序仍相反: %v", name, err)
			}
		case <-ctx.Done():
			t.Fatalf("%s未在超时前结束: %v", name, ctx.Err())
		}
	}

	var importStatus, taskState, subscriptionName string
	if err := fixture.pool.QueryRow(ctx, `
SELECT
    (SELECT status FROM imports WHERE id = $1),
    (SELECT state FROM episode_tasks WHERE id = $2),
    (SELECT name FROM rss_subscriptions WHERE id = $3)
`, importID, taskID, subscriptionID).Scan(&importStatus, &taskState, &subscriptionName); err != nil {
		t.Fatal(err)
	}
	if importStatus != "succeeded" || taskState != string(domain.TaskImported) || subscriptionName != "RSS lock order edited" {
		t.Fatalf("并发结果不完整: import=%s task=%s subscription=%s", importStatus, taskState, subscriptionName)
	}
}
