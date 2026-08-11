package maintenance

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseRSSHistoryDumpSelectsTargetAndDecodesCopyText(t *testing.T) {
	targetID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	otherID := uuid.MustParse("10000000-0000-0000-0000-000000000002")
	seriesID := uuid.MustParse("20000000-0000-0000-0000-000000000001")
	subscriptionColumns := []string{
		"id", "series_id", "name", "feed_url", "poll_interval_seconds", "last_polled_at",
		"version", "created_at", "source_season", "auto_review", "delete_imported_on_completion",
	}
	entryColumns := []string{
		"id", "subscription_id", "identity_key", "guid", "btih", "canonical_url", "title",
		"published_at", "enqueue_attempts", "upstream_payload", "discovered_at", "download_uri",
		"downloadable", "source_season", "source_episode", "duplicate_count",
	}
	dump := strings.Join([]string{
		"-- PostgreSQL database dump",
		copyHeader("rss_subscriptions", subscriptionColumns),
		copyRow(otherID, seriesID, "Other", "https://example.test/other.xml", 900, nil, 1, "2026-07-28 10:00:00+00", 1, false, false),
		copyRow(targetID, seriesID, `Completed\tRSS`, "https://example.test/completed.xml", 900, "2026-07-28 11:00:00.123456+00", 2, "2026-07-28 10:00:00+00", 1, false, false),
		`\.`,
		copyHeader("rss_entries", entryColumns),
		copyRow(uuid.New(), otherID, "guid:other", nil, nil, nil, "Other", nil, 1, `{}`, "2026-07-28 10:00:00+00", "https://example.test/other.torrent", true, 1, 1, 0),
		copyRow(uuid.MustParse("30000000-0000-0000-0000-000000000001"), targetID, "guid:one", "one", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "https://example.test/1", `Episode\tOne`, "2026-07-28 10:01:00+00", 1, `{"source":"backup"}`, "2026-07-28 10:02:00+00", "https://example.test/1.torrent", true, 1, 1, 0),
		copyRow(uuid.MustParse("30000000-0000-0000-0000-000000000002"), targetID, "guid:two", nil, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil, "Episode 2", nil, 2, `{}`, "2026-07-28 10:03:00+00", "https://example.test/2.torrent", true, 1, 2, 1),
		`\.`,
	}, "\n")

	snapshot, err := ParseRSSHistoryDump(strings.NewReader(dump), targetID)
	if err != nil {
		t.Fatalf("ParseRSSHistoryDump() error = %v", err)
	}
	if snapshot.Subscription.ID != targetID || snapshot.Subscription.SeriesID != seriesID {
		t.Fatalf("subscription IDs = %s / %s", snapshot.Subscription.ID, snapshot.Subscription.SeriesID)
	}
	if snapshot.Subscription.Name != "Completed\tRSS" || snapshot.Subscription.LastPolledAt == nil || snapshot.Subscription.Version != 2 {
		t.Fatalf("subscription = %#v", snapshot.Subscription)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(snapshot.Entries))
	}
	if snapshot.Entries[0].Title != "Episode\tOne" || string(snapshot.Entries[0].UpstreamPayload) != `{"source":"backup"}` {
		t.Fatalf("first entry = %#v", snapshot.Entries[0])
	}
	if snapshot.Entries[1].GUID != nil || snapshot.Entries[1].PublishedAt != nil || snapshot.Entries[1].DuplicateCount != 1 {
		t.Fatalf("second entry = %#v", snapshot.Entries[1])
	}
}

func TestParseRSSHistoryDumpReadsCurrentSourceCleanupPolicy(t *testing.T) {
	subscriptionID := uuid.New()
	seriesID := uuid.New()
	entryID := uuid.New()
	dump := strings.Join([]string{
		copyHeader("rss_subscriptions", []string{
			"id", "series_id", "name", "feed_url", "poll_interval_seconds", "last_polled_at",
			"version", "created_at", "source_season", "auto_review", "cleanup_source_on_completion",
		}),
		copyRow(subscriptionID, seriesID, "Completed", "https://example.test/completed.xml", 900, nil, 1, "2026-07-28 10:00:00+00", 1, false, true),
		`\.`,
		copyHeader("rss_entries", []string{
			"id", "subscription_id", "identity_key", "guid", "btih", "canonical_url", "title",
			"published_at", "enqueue_attempts", "upstream_payload", "discovered_at", "download_uri",
			"downloadable", "source_season", "source_episode", "duplicate_count",
		}),
		copyRow(entryID, subscriptionID, "guid:one", nil, nil, nil, "Episode 1", nil, 1, `{}`, "2026-07-28 10:01:00+00", "https://example.test/1.torrent", true, 1, 1, 0),
		`\.`,
	}, "\n")

	snapshot, err := ParseRSSHistoryDump(strings.NewReader(dump), subscriptionID)
	if err != nil {
		t.Fatalf("ParseRSSHistoryDump() error = %v", err)
	}
	if !snapshot.Subscription.CleanupSourceOnCompletion {
		t.Fatal("current source cleanup policy was not restored")
	}
}

func TestParseRSSHistoryDumpRejectsMissingRequiredTable(t *testing.T) {
	targetID := uuid.New()
	_, err := ParseRSSHistoryDump(strings.NewReader("-- empty dump\n"), targetID)
	if err == nil || !strings.Contains(err.Error(), "must contain") {
		t.Fatalf("ParseRSSHistoryDump() error = %v", err)
	}
}

func TestValidateRestoreRequestRequiresCompleteUniqueEpisodeHistory(t *testing.T) {
	request := validRestoreRequest(t)
	if err := ValidateRestoreRequest(request); err != nil {
		t.Fatalf("ValidateRestoreRequest(valid) error = %v", err)
	}

	duplicate := request
	duplicate.Snapshot.Entries = append([]RSSEntryHistory(nil), request.Snapshot.Entries...)
	duplicate.Snapshot.Entries[1].SourceEpisode = 1
	if err := ValidateRestoreRequest(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate source episode") {
		t.Fatalf("ValidateRestoreRequest(duplicate) error = %v", err)
	}

	wrongCount := request
	wrongCount.ExpectedEntryCount = 3
	if err := ValidateRestoreRequest(wrongCount); err == nil || !strings.Contains(err.Error(), "entry count") {
		t.Fatalf("ValidateRestoreRequest(count) error = %v", err)
	}
}

func validRestoreRequest(t *testing.T) RestoreCompletedRSSHistoryRequest {
	t.Helper()
	createdAt := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	subscription := RSSSubscriptionHistory{
		ID: uuid.New(), SeriesID: uuid.New(), Name: "Completed", FeedURL: "https://example.test/feed.xml",
		PollIntervalSeconds: 900, Version: 1, SourceSeason: 1, CreatedAt: createdAt,
	}
	entries := make([]RSSEntryHistory, 2)
	for index := range entries {
		episode := int32(index + 1)
		entries[index] = RSSEntryHistory{
			ID: uuid.New(), IdentityKey: fmt.Sprintf("guid:%d", episode), Title: fmt.Sprintf("Episode %d", episode),
			EnqueueAttempts: 1, UpstreamPayload: []byte(`{}`), DiscoveredAt: createdAt,
			DownloadURI: fmt.Sprintf("https://example.test/%d.torrent", episode), SourceSeason: 1, SourceEpisode: episode,
		}
	}
	return RestoreCompletedRSSHistoryRequest{
		Snapshot: RSSHistorySnapshot{Subscription: subscription, Entries: entries}, MappingProfileID: uuid.New(),
		ExpectedEntryCount: 2, ExpectedFinalEpisode: 2,
	}
}

func copyHeader(table string, columns []string) string {
	return fmt.Sprintf("COPY public.%s (%s) FROM stdin;", table, strings.Join(columns, ", "))
}

func copyRow(values ...any) string {
	fields := make([]string, len(values))
	for index, value := range values {
		switch typed := value.(type) {
		case nil:
			fields[index] = `\N`
		case bool:
			if typed {
				fields[index] = "t"
			} else {
				fields[index] = "f"
			}
		default:
			fields[index] = fmt.Sprint(value)
		}
	}
	return strings.Join(fields, "\t")
}
