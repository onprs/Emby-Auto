package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type feedLookupConfigurationStub struct {
	configuration domain.Configuration
	err           error
}

func (stub feedLookupConfigurationStub) Load(context.Context) (domain.Configuration, error) {
	return stub.configuration, stub.err
}

type feedCatalogSearcherStub struct {
	calls   []string
	results map[string][]domain.TMDbSeriesSearchResult
	err     error
}

func (stub *feedCatalogSearcherStub) SearchTV(_ context.Context, query string) ([]domain.TMDbSeriesSearchResult, error) {
	stub.calls = append(stub.calls, query)
	if stub.err != nil {
		return nil, stub.err
	}
	return stub.results[query], nil
}

type feedCatalogLookupStoreStub struct {
	created []db.CreateRSSFeedCatalogLookupParams
}

func (stub *feedCatalogLookupStoreStub) CreateRSSFeedCatalogLookup(_ context.Context, params db.CreateRSSFeedCatalogLookupParams) (db.RssFeedCatalogLookup, error) {
	stub.created = append(stub.created, params)
	return db.RssFeedCatalogLookup{
		ID: params.ID, FeedTitle: params.FeedTitle, SuggestedQueries: params.SuggestedQueries,
		SampleTitles: params.SampleTitles, ExpiresAt: params.ExpiresAt,
	}, nil
}

type feedCatalogAgentStub struct {
	created []AutomaticAgentResolutionRequest
	result  AgentResolutionCommandResult
}

func (stub *feedCatalogAgentStub) CreateAutomatic(_ context.Context, input AutomaticAgentResolutionRequest) (AgentResolutionCommandResult, error) {
	stub.created = append(stub.created, input)
	return stub.result, nil
}

type feedFetcherStub struct {
	feed   domain.RSSFeed
	err    error
	gotURL string
}

func (stub *feedFetcherStub) Fetch(_ context.Context, feedURL string) (domain.RSSFeed, error) {
	stub.gotURL = feedURL
	return stub.feed, stub.err
}

func newTestFeedLookup(fetcher *feedFetcherStub) *RSSFeedLookup {
	lookup := NewRSSFeedLookup(feedLookupConfigurationStub{configuration: domain.Configuration{Version: 1}})
	lookup.newClient = func(*http.Client) (RSSFeedFetcher, error) {
		return fetcher, nil
	}
	return lookup
}

func TestRSSFeedLookupRejectsInvalidURL(t *testing.T) {
	lookup := newTestFeedLookup(&feedFetcherStub{})
	for _, feedURL := range []string{"", "not-a-url", "ftp://example.test/feed.xml", "https://user:secret@example.test/feed.xml"} {
		_, err := lookup.Lookup(context.Background(), feedURL)
		var serviceErr *Error
		if !errors.As(err, &serviceErr) || !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Lookup(%q) error = %v, want invalid input", feedURL, err)
		}
		if serviceErr.Code != "invalid_rss_feed_url" {
			t.Fatalf("Lookup(%q) code = %q, want invalid_rss_feed_url", feedURL, serviceErr.Code)
		}
	}
}

func TestRSSFeedLookupReturnsFetchFailureAsServiceError(t *testing.T) {
	lookup := newTestFeedLookup(&feedFetcherStub{err: errors.New("connection refused")})
	_, err := lookup.Lookup(context.Background(), "https://example.test/feed.xml")
	var serviceErr *Error
	if !errors.As(err, &serviceErr) {
		t.Fatalf("Lookup error = %v, want service error", err)
	}
	if serviceErr.Code != "rss_fetch_failed" {
		t.Fatalf("Lookup code = %q, want rss_fetch_failed", serviceErr.Code)
	}
}

func TestRSSFeedLookupSuggestsFeedTitleAndSamplesEntries(t *testing.T) {
	entries := make([]domain.RSSFeedEntry, 0, 7)
	for _, title := range []string{"第 01 集", "第 02 集", "第 03 集", "第 04 集", "第 05 集", "第 06 集", ""} {
		entries = append(entries, domain.RSSFeedEntry{Title: title})
	}
	fetcher := &feedFetcherStub{feed: domain.RSSFeed{Title: "  孤独摇滚  ", Entries: entries}}
	lookup := newTestFeedLookup(fetcher)

	result, err := lookup.Lookup(context.Background(), " https://example.test/feed.xml ")
	if err != nil {
		t.Fatalf("Lookup error = %v", err)
	}
	if fetcher.gotURL != "https://example.test/feed.xml" {
		t.Fatalf("fetched URL = %q, want trimmed feed URL", fetcher.gotURL)
	}
	if result.FeedTitle != "孤独摇滚" {
		t.Fatalf("FeedTitle = %q, want trimmed feed title", result.FeedTitle)
	}
	if result.SuggestedQuery != "孤独摇滚" {
		t.Fatalf("SuggestedQuery = %q, want feed title", result.SuggestedQuery)
	}
	if len(result.SampleTitles) != 5 {
		t.Fatalf("SampleTitles = %v, want at most 5 non-empty entry titles", result.SampleTitles)
	}
}

func TestSuggestRSSFeedQueryFallsBackToCleanedEntryTitle(t *testing.T) {
	cases := []struct {
		name string
		feed domain.RSSFeed
		want string
	}{
		{
			name: "feed title wins",
			feed: domain.RSSFeed{Title: "My Show", Entries: []domain.RSSFeedEntry{{Title: "[Group] Other - 01 [1080p]"}}},
			want: "My Show",
		},
		{
			name: "entry title cleaned",
			feed: domain.RSSFeed{Entries: []domain.RSSFeedEntry{{Title: "[SubsGroup] Frieren - 01 [1080p][HEVC]"}}},
			want: "Frieren",
		},
		{
			name: "episode and quality tokens removed",
			feed: domain.RSSFeed{Entries: []domain.RSSFeedEntry{{Title: "Dungeon Meshi - 12 [BDRip 1080p x264 AAC]"}}},
			want: "Dungeon Meshi",
		},
		{
			name: "skips unusable entries",
			feed: domain.RSSFeed{Entries: []domain.RSSFeedEntry{{Title: "01 1080p"}, {Title: "[Group] Apothecary Diaries - 03 [1080p]"}}},
			want: "Apothecary Diaries",
		},
		{
			name: "nothing usable",
			feed: domain.RSSFeed{Entries: []domain.RSSFeedEntry{{Title: ""}}},
			want: "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := SuggestRSSFeedQuery(testCase.feed); got != testCase.want {
				t.Fatalf("SuggestRSSFeedQuery() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestSuggestRSSFeedQueriesIncludesEveryUsefulFeedKeywordAndReleaseTitle(t *testing.T) {
	feed := domain.RSSFeed{
		Title:   "发布组 向日葵马戏团 内封 LoliHouse",
		Entries: []domain.RSSFeedEntry{{Title: "[LoliHouse] 向日葵马戏团 - 01 [1080p]"}},
	}
	want := []string{"发布组 向日葵马戏团 内封 LoliHouse", "向日葵马戏团", "发布组", "LoliHouse"}
	if got := SuggestRSSFeedQueries(feed); !reflect.DeepEqual(got, want) {
		t.Fatalf("SuggestRSSFeedQueries() = %#v, want %#v", got, want)
	}
}

func TestRSSFeedLookupSearchesCompleteQueryPlanAndDeduplicatesCandidates(t *testing.T) {
	feed := domain.RSSFeed{
		Title:   "发布组 向日葵马戏团 内封 LoliHouse",
		Entries: []domain.RSSFeedEntry{{Title: "[LoliHouse] 向日葵马戏团 - 01 [1080p]"}},
	}
	fetcher := &feedFetcherStub{feed: feed}
	searcher := &feedCatalogSearcherStub{results: map[string][]domain.TMDbSeriesSearchResult{
		"发布组 向日葵马戏团 内封 LoliHouse": {{TMDbSeriesID: 202, Name: "无关但先返回的作品"}},
		"向日葵马戏团":                  {{TMDbSeriesID: 101, Name: "向日葵马戏团"}},
		"LoliHouse":               {{TMDbSeriesID: 101, Name: "duplicate localization"}},
	}}
	store := &feedCatalogLookupStoreStub{}
	agent := &feedCatalogAgentStub{}
	lookup := newTestFeedLookup(fetcher).WithCatalogMatching(searcher, store, agent)

	result, err := lookup.Lookup(context.Background(), "https://example.test/feed.xml")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	wantQueries := []string{"发布组 向日葵马戏团 内封 LoliHouse", "向日葵马戏团", "发布组", "LoliHouse"}
	if !reflect.DeepEqual(searcher.calls, wantQueries) {
		t.Fatalf("TMDb queries = %#v, want %#v", searcher.calls, wantQueries)
	}
	if result.CatalogMatchSource != "deterministic" || len(result.Candidates) != 2 || result.Candidates[0].TMDbSeriesID != 101 || result.Candidates[1].TMDbSeriesID != 202 {
		t.Fatalf("Lookup() catalog result = %+v", result)
	}
	if len(store.created) != 0 || len(agent.created) != 0 {
		t.Fatalf("deterministic match scheduled Agent: store=%d agent=%d", len(store.created), len(agent.created))
	}
}

func TestRSSFeedLookupSchedulesAgentOnlyAfterEveryDeterministicQueryMisses(t *testing.T) {
	resolutionID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	fixedNow := time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC)
	fetcher := &feedFetcherStub{feed: domain.RSSFeed{Title: "Group Unknown", Entries: []domain.RSSFeedEntry{{Title: "[Group] Unknown - 01"}}}}
	searcher := &feedCatalogSearcherStub{results: map[string][]domain.TMDbSeriesSearchResult{}}
	store := &feedCatalogLookupStoreStub{}
	agent := &feedCatalogAgentStub{result: AgentResolutionCommandResult{Resolution: domain.AgentResolution{ID: resolutionID}}}
	lookup := newTestFeedLookup(fetcher).WithCatalogMatching(searcher, store, agent)
	lookup.configuration = feedLookupConfigurationStub{configuration: domain.Configuration{Version: 1, Settings: domain.RuntimeSettings{
		Agent: domain.AgentSettings{Enabled: true, CatalogMatchEnabled: true},
	}}}
	lookup.now = func() time.Time { return fixedNow }

	result, err := lookup.Lookup(context.Background(), "https://example.test/feed.xml")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if len(searcher.calls) != len(result.SuggestedQueries) || len(searcher.calls) < 2 {
		t.Fatalf("TMDb calls = %#v, queries = %#v", searcher.calls, result.SuggestedQueries)
	}
	if result.CatalogMatchSource != "agent_pending" || result.AgentResolutionID == nil || *result.AgentResolutionID != resolutionID {
		t.Fatalf("Lookup() Agent result = %+v", result)
	}
	if len(store.created) != 1 || len(agent.created) != 1 {
		t.Fatalf("fallback scheduling calls: store=%d agent=%d", len(store.created), len(agent.created))
	}
	lookupID := uuid.UUID(store.created[0].ID.Bytes)
	if agent.created[0].Capability != domain.AgentCapabilityCatalogCandidate || agent.created[0].ResourceID != lookupID {
		t.Fatalf("Agent CreateAutomatic() = %+v", agent.created[0])
	}
	if !store.created[0].ExpiresAt.Time.Equal(fixedNow.Add(rssFeedLookupTTL)) {
		t.Fatalf("lookup expiry = %s", store.created[0].ExpiresAt.Time)
	}
}

func TestRSSFeedLookupDoesNotScheduleAgentWhenCatalogCapabilityIsDisabled(t *testing.T) {
	fetcher := &feedFetcherStub{feed: domain.RSSFeed{Title: "Unknown Show"}}
	searcher := &feedCatalogSearcherStub{results: map[string][]domain.TMDbSeriesSearchResult{}}
	store := &feedCatalogLookupStoreStub{}
	agent := &feedCatalogAgentStub{}
	lookup := newTestFeedLookup(fetcher).WithCatalogMatching(searcher, store, agent)
	lookup.configuration = feedLookupConfigurationStub{configuration: domain.Configuration{Version: 1, Settings: domain.RuntimeSettings{
		Agent: domain.AgentSettings{Enabled: true, CatalogMatchEnabled: false},
	}}}

	result, err := lookup.Lookup(context.Background(), "https://example.test/feed.xml")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if result.CatalogMatchSource != "none" || len(store.created) != 0 || len(agent.created) != 0 {
		t.Fatalf("disabled Agent fallback result = %+v store=%d agent=%d", result, len(store.created), len(agent.created))
	}
}

func TestRSSFeedLookupDoesNotUseAgentForTMDbFailure(t *testing.T) {
	fetcher := &feedFetcherStub{feed: domain.RSSFeed{Title: "Known Show"}}
	searcher := &feedCatalogSearcherStub{err: errors.New("upstream timeout")}
	store := &feedCatalogLookupStoreStub{}
	agent := &feedCatalogAgentStub{}
	lookup := newTestFeedLookup(fetcher).WithCatalogMatching(searcher, store, agent)
	lookup.configuration = feedLookupConfigurationStub{configuration: domain.Configuration{Version: 1, Settings: domain.RuntimeSettings{
		Agent: domain.AgentSettings{Enabled: true, CatalogMatchEnabled: true},
	}}}

	_, err := lookup.Lookup(context.Background(), "https://example.test/feed.xml")
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "rss_catalog_search_failed" {
		t.Fatalf("Lookup() error = %v, want rss_catalog_search_failed", err)
	}
	if len(store.created) != 0 || len(agent.created) != 0 {
		t.Fatalf("TMDb failure scheduled Agent: store=%d agent=%d", len(store.created), len(agent.created))
	}
}
