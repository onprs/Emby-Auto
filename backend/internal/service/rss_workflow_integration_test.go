//go:build integration

package service

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func TestRecordScheduleFailurePersistsValidUTF8MessageIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	entryID := insertScheduleFailureFixture(t, ctx, pool)

	// 错误消息超过 2000 字节且以多字节字符为主；前 2000 个 rune 是明确的前缀。
	prefix := strings.Repeat("前置", 1000) // 2000 runes, 6000 bytes
	message := prefix + "多出的后缀"
	if len(message) <= 2000 {
		t.Fatalf("test message must exceed 2000 bytes, got %d", len(message))
	}

	if err := workflow.recordScheduleFailure(ctx, entryID, "rss_schedule_failed", message); err != nil {
		t.Fatalf("recordScheduleFailure() error = %v", err)
	}

	var status string
	var errorCode, errorMessage *string
	if err := pool.QueryRow(ctx, `
SELECT status, last_error_code, last_error_message
FROM rss_entries WHERE id = $1`, entryID).Scan(&status, &errorCode, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if status != "enqueue_failed" {
		t.Fatalf("entry status = %q, want %q", status, "enqueue_failed")
	}
	if errorCode == nil || *errorCode != "rss_schedule_failed" {
		t.Fatalf("last_error_code = %v, want %q", errorCode, "rss_schedule_failed")
	}
	if errorMessage == nil || *errorMessage != prefix {
		t.Fatalf("last_error_message = %q, want prefix %q", deref(errorMessage), prefix)
	}
	if errorMessage == nil || !utf8.ValidString(*errorMessage) {
		t.Fatalf("last_error_message is not valid UTF-8: %q", deref(errorMessage))
	}
}

func TestRecordScheduleFailurePersistsCleanedInvalidBytesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	entryID := insertScheduleFailureFixture(t, ctx, pool)

	// 消息本身含非法 UTF-8 字节且超长，持久化前必须被清洗并截断为合法 UTF-8。
	// rune 数：前缀(2) + 两个替换字符(2) + 番(2000) = 2004，截断后保留前 2000 个 rune。
	message := "前缀" + string([]byte{0xff, 0xfe}) + strings.Repeat("番", 2000)
	want := "前缀\ufffd\ufffd" + strings.Repeat("番", 1996)

	if err := workflow.recordScheduleFailure(ctx, entryID, "rss_schedule_failed", message); err != nil {
		t.Fatalf("recordScheduleFailure() error = %v", err)
	}

	var status string
	var errorCode, errorMessage *string
	if err := pool.QueryRow(ctx, `
SELECT status, last_error_code, last_error_message
FROM rss_entries WHERE id = $1`, entryID).Scan(&status, &errorCode, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if status != "enqueue_failed" {
		t.Fatalf("entry status = %q, want %q", status, "enqueue_failed")
	}
	if errorCode == nil || *errorCode != "rss_schedule_failed" {
		t.Fatalf("last_error_code = %v, want %q", errorCode, "rss_schedule_failed")
	}
	if errorMessage == nil || *errorMessage != want {
		t.Fatalf("last_error_message = %q, want %q", deref(errorMessage), want)
	}
	if errorMessage == nil || !utf8.ValidString(*errorMessage) {
		t.Fatalf("last_error_message is not valid UTF-8: %q", deref(errorMessage))
	}
}

func TestRecordScheduleFailurePersistsCleanedShortInvalidBytesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	entryID := insertScheduleFailureFixture(t, ctx, pool)

	// 消息含非法 UTF-8 字节但长度未超限：即使不截断也必须清洗为合法 UTF-8。
	message := "前缀" + string([]byte{0xff}) + "后缀"
	want := "前缀\ufffd后缀"

	if err := workflow.recordScheduleFailure(ctx, entryID, "rss_schedule_failed", message); err != nil {
		t.Fatalf("recordScheduleFailure() error = %v", err)
	}

	var status string
	var errorCode, errorMessage *string
	if err := pool.QueryRow(ctx, `
SELECT status, last_error_code, last_error_message
FROM rss_entries WHERE id = $1`, entryID).Scan(&status, &errorCode, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if status != "enqueue_failed" {
		t.Fatalf("entry status = %q, want %q", status, "enqueue_failed")
	}
	if errorCode == nil || *errorCode != "rss_schedule_failed" {
		t.Fatalf("last_error_code = %v, want %q", errorCode, "rss_schedule_failed")
	}
	if errorMessage == nil || *errorMessage != want {
		t.Fatalf("last_error_message = %q, want %q", deref(errorMessage), want)
	}
	if errorMessage == nil || !utf8.ValidString(*errorMessage) {
		t.Fatalf("last_error_message is not valid UTF-8: %q", deref(errorMessage))
	}
}

func insertScheduleFailureFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	seriesID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Failure Series')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, auto_episode_mapping, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Failure Subscription', 'https://example.test/feed.xml', true, true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title)
VALUES ($1, $2, 'identity-1', 'Failure Series S01E01')`, entryID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	return entryID
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
