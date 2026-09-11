package service

import (
	"context"
	"errors"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

type rssSubtitleGroupSourceStub struct {
	names []string
	err   error
}

func (stub rssSubtitleGroupSourceStub) ListRSSSubtitleGroupNames(context.Context) ([]string, error) {
	return stub.names, stub.err
}

func TestRSSFeedLookupReturnsSubtitleGroupCandidates(t *testing.T) {
	lookup := newTestFeedLookup(&feedFetcherStub{feed: domain.RSSFeed{
		Title:   "Show RSS",
		Entries: []domain.RSSFeedEntry{{Title: "【LoliHouse】 Show - 01 [1080p]"}},
	}}).WithSubtitleGroups(rssSubtitleGroupSourceStub{names: []string{"LoliHouse"}})

	result, err := lookup.Lookup(context.Background(), "https://example.test/feed.xml")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if result.SubtitleGroup != "LoliHouse" || len(result.SubtitleGroupCandidates) != 1 || result.SubtitleGroupCandidates[0] != "LoliHouse" {
		t.Fatalf("subtitle group result = %#v", result)
	}
}

func TestRSSFeedLookupReportsSubtitleGroupListFailure(t *testing.T) {
	lookup := newTestFeedLookup(&feedFetcherStub{feed: domain.RSSFeed{Title: "Show RSS"}}).WithSubtitleGroups(rssSubtitleGroupSourceStub{err: errors.New("database unavailable")})

	_, err := lookup.Lookup(context.Background(), "https://example.test/feed.xml")
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "rss_subtitle_groups_load_failed" {
		t.Fatalf("Lookup() error = %v, want rss_subtitle_groups_load_failed", err)
	}
}
