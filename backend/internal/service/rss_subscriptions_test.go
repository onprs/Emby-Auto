package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestRSSSubscriptionCommandsValidateBeforeDatabaseAccess(t *testing.T) {
	workflow := NewRSSWorkflow(nil, nil, nil)
	tests := []struct {
		name  string
		input domain.CreateRSSSubscription
		field string
	}{
		{
			name: "missing TMDb ID",
			input: domain.CreateRSSSubscription{
				SeriesTitle: "Show", Name: "Feed", FeedURL: "https://example.test/feed.xml", SourceSeason: 1, PollInterval: time.Minute,
			},
			field: "tmdbSeriesId",
		},
		{
			name: "credential-bearing feed URL",
			input: domain.CreateRSSSubscription{
				TMDbSeriesID: 42, SeriesTitle: "Show", Name: "Feed", FeedURL: "https://user:pass@example.test/feed.xml", SourceSeason: 1, PollInterval: time.Minute,
			},
			field: "feedUrl",
		},
		{
			name: "poll interval too short",
			input: domain.CreateRSSSubscription{
				TMDbSeriesID: 42, SeriesTitle: "Show", Name: "Feed", FeedURL: "https://example.test/feed.xml", SourceSeason: 1, PollInterval: 59 * time.Second,
			},
			field: "pollIntervalSeconds",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := workflow.CreateSubscription(context.Background(), test.input)
			var serviceErr *Error
			if !errors.As(err, &serviceErr) || serviceErr.Code != "invalid_rss_subscription" || serviceErr.Details["field"] != test.field {
				t.Fatalf("CreateSubscription() error = %#v, want invalid field %q", err, test.field)
			}
		})
	}
}

func TestNormalizeRSSKeywordsTrimsAndDeduplicatesCaseInsensitively(t *testing.T) {
	got, err := normalizeRSSKeywords("includeKeywords", []string{" 简日 ", "1080p", "简日", "1080P"})
	if err != nil {
		t.Fatalf("normalizeRSSKeywords() error = %v", err)
	}
	if len(got) != 2 || got[0] != "简日" || got[1] != "1080p" {
		t.Fatalf("normalizeRSSKeywords() = %#v, want [简日 1080p]", got)
	}
}

func TestNormalizeRSSKeywordsRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name     string
		keywords []string
	}{
		{name: "blank", keywords: []string{" "}},
		{name: "too many", keywords: make([]string, 21)},
		{name: "too long", keywords: []string{strings.Repeat("词", 129)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeRSSKeywords("excludeKeywords", test.keywords)
			var serviceErr *Error
			if !errors.As(err, &serviceErr) || serviceErr.Details["field"] != "excludeKeywords" {
				t.Fatalf("normalizeRSSKeywords() error = %#v", err)
			}
		})
	}
}

func TestRSSManualPollRejectsBlankIdempotencyKeyBeforeLookup(t *testing.T) {
	workflow := NewRSSWorkflow(nil, nil, nil)
	_, err := workflow.ScheduleManualPoll(context.Background(), uuid.MustParse("70000000-0000-0000-0000-000000000001"), "   ", uuid.Nil)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "invalid_idempotency_key" || !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ScheduleManualPoll() error = %#v, want invalid_idempotency_key", err)
	}
}

func TestSummarizeRSSSubscriptionProgressAggregatesEffectiveTasks(t *testing.T) {
	views := []domain.AcquisitionView{
		{OverallProgress: 0.16, AggregateStatus: "downloading"},
		{OverallProgress: 1, AggregateStatus: "completed"},
		{OverallProgress: 0.4, AggregateStatus: "failed"},
	}
	progress := summarizeRSSSubscriptionProgress(views)
	if progress.taskCount != 3 || progress.completedTaskCount != 1 || progress.attentionTaskCount != 1 {
		t.Fatalf("progress counts = %#v, want total/completed/attention 3/1/1", progress)
	}
	if progress.overallProgress != 0.52 {
		t.Fatalf("overall progress = %f, want child mean 0.52", progress.overallProgress)
	}

	empty := summarizeRSSSubscriptionProgress(nil)
	if empty.taskCount != 0 || empty.overallProgress != 0 {
		t.Fatalf("empty progress = %#v, want zero", empty)
	}
}

func TestSummarizeRSSSubscriptionImportedProgressIncludesArchivedImportsWithoutDoubleCounting(t *testing.T) {
	views := []domain.AcquisitionView{
		{OverallProgress: 0.5, AggregateStatus: "processing"},
		{OverallProgress: 1, AggregateStatus: "completed"},
		{OverallProgress: 0.4, AggregateStatus: "failed"},
	}
	progress := summarizeRSSSubscriptionImportedProgress(4, views)
	if progress.taskCount != 6 || progress.completedTaskCount != 4 || progress.attentionTaskCount != 1 {
		t.Fatalf("imported progress counts = %#v, want total/completed/attention 6/4/1", progress)
	}
	want := 4.9 / 6.0
	if math.Abs(progress.overallProgress-want) > 1e-12 {
		t.Fatalf("imported progress = %f, want %f", progress.overallProgress, want)
	}
}

func TestApplyRSSSubscriptionProgressKeepsCompletedSubscriptionAtOne(t *testing.T) {
	completedAt := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	subscription := domain.RSSSubscription{CompletedAt: &completedAt}

	applyRSSSubscriptionProgress(&subscription, rssSubscriptionProgress{})

	if subscription.OverallProgress != 1 {
		t.Fatalf("overall progress = %f, want 1 for completed subscription", subscription.OverallProgress)
	}
	if subscription.TaskCount != 0 || subscription.CompletedTaskCount != 0 || subscription.AttentionTaskCount != 0 {
		t.Fatalf("progress counts = %#v, want cleanup-preserved zero counts", subscription)
	}
}

func TestRSSSubscriptionListRejectsInvalidLimitBeforeLookup(t *testing.T) {
	workflow := NewRSSWorkflow(nil, nil, nil)
	for _, limit := range []int{0, 101} {
		_, err := workflow.ListSubscriptions(context.Background(), nil, limit, nil, nil)
		var serviceErr *Error
		if !errors.As(err, &serviceErr) || serviceErr.Code != "invalid_rss_subscription" || serviceErr.Details["field"] != "limit" {
			t.Fatalf("ListSubscriptions(limit=%d) error = %#v", limit, err)
		}
	}
}
