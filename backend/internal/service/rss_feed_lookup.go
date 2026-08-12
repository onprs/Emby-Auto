package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/proxyhttp"
	"github.com/onprs/emby-auto/backend/internal/platform/rssfeed"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

const (
	maxRSSFeedQueries    = 8
	maxRSSCatalogResults = 40
	rssFeedLookupTTL     = 24 * time.Hour
)

// RSSFeedLookupConfiguration loads runtime settings so feed lookups honor the
// configured network proxy, matching the worker's RSS poll behavior.
type RSSFeedLookupConfiguration interface {
	Load(context.Context) (domain.Configuration, error)
}

// RSSFeedFetcher fetches and parses one RSS feed.
type RSSFeedFetcher interface {
	Fetch(ctx context.Context, feedURL string) (domain.RSSFeed, error)
}

type RSSFeedCatalogSearcher interface {
	SearchTV(context.Context, string) ([]domain.TMDbSeriesSearchResult, error)
}

type RSSFeedCatalogAgent interface {
	CreateAutomatic(context.Context, AutomaticAgentResolutionRequest) (AgentResolutionCommandResult, error)
}

type RSSFeedCatalogLookupStore interface {
	CreateRSSFeedCatalogLookup(context.Context, db.CreateRSSFeedCatalogLookupParams) (db.RssFeedCatalogLookup, error)
}

// RSSFeedLookup fetches a candidate RSS feed, performs bounded deterministic
// TMDb searches, and schedules catalog Agent fallback only after a clean miss.
type RSSFeedLookup struct {
	configuration RSSFeedLookupConfiguration
	catalogSearch RSSFeedCatalogSearcher
	queries       RSSFeedCatalogLookupStore
	agent         RSSFeedCatalogAgent
	newClient     func(httpClient *http.Client) (RSSFeedFetcher, error)
	now           func() time.Time
}

func NewRSSFeedLookup(configuration RSSFeedLookupConfiguration) *RSSFeedLookup {
	return &RSSFeedLookup{
		configuration: configuration,
		newClient: func(httpClient *http.Client) (RSSFeedFetcher, error) {
			return rssfeed.NewClient(rssfeed.ClientOptions{HTTPClient: httpClient})
		},
		now: time.Now,
	}
}

func (service *RSSFeedLookup) WithCatalogMatching(searcher RSSFeedCatalogSearcher, queries RSSFeedCatalogLookupStore, agent RSSFeedCatalogAgent) *RSSFeedLookup {
	service.catalogSearch = searcher
	service.queries = queries
	service.agent = agent
	return service
}

func (service *RSSFeedLookup) Lookup(ctx context.Context, feedURL string) (domain.RSSFeedLookup, error) {
	trimmed := strings.TrimSpace(feedURL)
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return domain.RSSFeedLookup{}, NewError(
			"invalid_rss_feed_url",
			"the RSS feed URL must be an HTTP(S) URL without embedded credentials",
			ErrInvalidInput,
			map[string]any{"field": "feedUrl"},
		)
	}
	if service.configuration == nil || service.newClient == nil {
		return domain.RSSFeedLookup{}, fmt.Errorf("RSS feed lookup is not configured")
	}
	configuration, err := service.configuration.Load(ctx)
	if err != nil {
		return domain.RSSFeedLookup{}, fmt.Errorf("load configuration for RSS feed lookup: %w", err)
	}
	httpClient, err := proxyhttp.NewClient(configuration.Settings.NetworkProxy)
	if err != nil {
		return domain.RSSFeedLookup{}, fmt.Errorf("build RSS feed HTTP client: %w", err)
	}
	client, err := service.newClient(httpClient)
	if err != nil {
		return domain.RSSFeedLookup{}, fmt.Errorf("build RSS feed client: %w", err)
	}
	feed, err := client.Fetch(ctx, trimmed)
	if err != nil {
		return domain.RSSFeedLookup{}, NewError(
			"rss_fetch_failed",
			"the RSS feed could not be fetched",
			err,
			map[string]any{"feedUrl": trimmed},
		)
	}

	queries := SuggestRSSFeedQueries(feed)
	lookup := domain.RSSFeedLookup{
		FeedURL:            trimmed,
		FeedTitle:          strings.TrimSpace(feed.Title),
		SuggestedQueries:   queries,
		SampleTitles:       sampleRSSFeedTitles(feed),
		Candidates:         []domain.TMDbSeriesSearchResult{},
		CatalogMatchSource: "none",
	}
	if len(queries) > 0 {
		lookup.SuggestedQuery = queries[0]
	}
	if service.catalogSearch != nil {
		lookup.Candidates, err = service.searchCatalogQueries(ctx, queries)
		if err != nil {
			return domain.RSSFeedLookup{}, NewError(
				"rss_catalog_search_failed",
				"TMDb could not be searched while identifying the RSS feed",
				err,
				map[string]any{"dependency": "tmdb"},
			)
		}
	}
	if len(lookup.Candidates) > 0 {
		lookup.CatalogMatchSource = "deterministic"
		return lookup, nil
	}

	settings := configuration.Settings.Agent.WithDefaults()
	if len(queries) == 0 || !settings.Enabled || !settings.CatalogMatchEnabled || service.queries == nil || service.agent == nil {
		return lookup, nil
	}
	lookupID := uuid.New()
	if _, err := service.queries.CreateRSSFeedCatalogLookup(ctx, db.CreateRSSFeedCatalogLookupParams{
		ID:               repository.UUIDToPG(lookupID),
		FeedTitle:        lookup.FeedTitle,
		SuggestedQueries: lookup.SuggestedQueries,
		SampleTitles:     lookup.SampleTitles,
		ExpiresAt:        pgtype.Timestamptz{Time: service.now().UTC().Add(rssFeedLookupTTL), Valid: true},
	}); err != nil {
		return domain.RSSFeedLookup{}, fmt.Errorf("persist RSS feed catalog lookup: %w", err)
	}
	resolution, err := service.agent.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilityCatalogCandidate,
		ResourceID: lookupID,
	})
	if err != nil {
		return domain.RSSFeedLookup{}, fmt.Errorf("schedule RSS catalog Agent fallback: %w", err)
	}
	lookup.CatalogMatchSource = "agent_pending"
	lookup.AgentResolutionID = &resolution.Resolution.ID
	return lookup, nil
}

func (service *RSSFeedLookup) searchCatalogQueries(ctx context.Context, queries []string) ([]domain.TMDbSeriesSearchResult, error) {
	type scoredCandidate struct {
		item       domain.TMDbSeriesSearchResult
		score      int
		firstOrder int
	}
	byID := make(map[int64]*scoredCandidate, maxRSSFeedQueries*20)
	order := 0
	for queryIndex, query := range queries {
		items, err := service.catalogSearch.SearchTV(ctx, query)
		if err != nil {
			return nil, err
		}
		if len(items) > 20 {
			items = items[:20]
		}
		for _, item := range items {
			if item.TMDbSeriesID <= 0 {
				continue
			}
			score := rssCatalogCandidateScore(item, query, queryIndex)
			if existing := byID[item.TMDbSeriesID]; existing != nil {
				if score > existing.score {
					existing.score = score
				}
				continue
			}
			byID[item.TMDbSeriesID] = &scoredCandidate{item: item, score: score, firstOrder: order}
			order++
		}
	}
	scored := make([]*scoredCandidate, 0, len(byID))
	for _, candidate := range byID {
		scored = append(scored, candidate)
	}
	sort.SliceStable(scored, func(left, right int) bool {
		if scored[left].score != scored[right].score {
			return scored[left].score > scored[right].score
		}
		return scored[left].firstOrder < scored[right].firstOrder
	})
	if len(scored) > maxRSSCatalogResults {
		scored = scored[:maxRSSCatalogResults]
	}
	results := make([]domain.TMDbSeriesSearchResult, 0, len(scored))
	for _, candidate := range scored {
		results = append(results, candidate.item)
	}
	return results, nil
}

func rssCatalogCandidateScore(item domain.TMDbSeriesSearchResult, query string, queryIndex int) int {
	normalize := func(value string) string {
		return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	}
	query = normalize(query)
	priority := maxRSSFeedQueries - queryIndex
	for _, name := range []string{item.Name, item.OriginalName} {
		name = normalize(name)
		if name == "" {
			continue
		}
		if name == query {
			return 300 + priority
		}
		if strings.Contains(name, query) || strings.Contains(query, name) {
			return 200 + priority
		}
	}
	return 100 + priority
}

func sampleRSSFeedTitles(feed domain.RSSFeed) []string {
	titles := make([]string, 0, 5)
	for _, entry := range feed.Entries {
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			continue
		}
		titles = append(titles, boundedAgentText(title, 2048))
		if len(titles) == 5 {
			break
		}
	}
	return titles
}

var (
	rssBracketGroupPattern  = regexp.MustCompile(`[\[\(【（][^\]\)】）]*[\]\)】）]`)
	rssSeasonEpisodePattern = regexp.MustCompile(`(?i)s\d{1,2}\s*e\d{1,4}`)
	rssEpisodeTokenPattern  = regexp.MustCompile(`(?i)(?:^|[\s\-_./])(?:第?\s*\d{1,4}\s*[集话話回]|e(?:p)?\d{1,4}|#\d{1,4})(?:$|[\s\-_./])`)
	rssBareEpisodePattern   = regexp.MustCompile(`(?:^|[\s\-_./])\d{1,4}(?:$|[\s\-_./])`)
	rssQualityTokenPattern  = regexp.MustCompile(`(?i)\b(?:1080p|720p|2160p|4k|480p|x264|x265|h\.?264|h\.?265|hevc|avc|bdrip|bd|web-?dl|webrip|hdr|10bit|8bit|aac|flac|mp4|mkv)\b`)
	rssSeparatorPattern     = regexp.MustCompile(`[\s\-_./·]+`)
	rssQueryEdgePattern     = regexp.MustCompile(`^[\p{P}\p{S}_]+|[\p{P}\p{S}_]+$`)
)

var rssFeedQueryNoise = map[string]struct{}{
	"rss": {}, "feed": {}, "anime": {}, "内封": {}, "內封": {}, "简中": {}, "簡中": {},
	"繁中": {}, "简日": {}, "簡日": {}, "繁日": {}, "双语": {}, "雙語": {},
}

// SuggestRSSFeedQuery remains the primary deterministic query for callers that
// only need one value. New matching code must use SuggestRSSFeedQueries.
func SuggestRSSFeedQuery(feed domain.RSSFeed) string {
	queries := SuggestRSSFeedQueries(feed)
	if len(queries) == 0 {
		return ""
	}
	return queries[0]
}

// SuggestRSSFeedQueries derives a bounded query plan from the full feed title,
// cleaned release titles, and every useful whitespace-delimited feed keyword.
func SuggestRSSFeedQueries(feed domain.RSSFeed) []string {
	queries := make([]string, 0, maxRSSFeedQueries)
	seen := make(map[string]struct{}, maxRSSFeedQueries)
	add := func(value string) {
		value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
		if value == "" || utf8.RuneCountInString(value) < 2 || utf8.RuneCountInString(value) > 512 || len(queries) == maxRSSFeedQueries {
			return
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		queries = append(queries, value)
	}

	feedTitle := strings.TrimSpace(feed.Title)
	add(feedTitle)
	for _, entry := range feed.Entries {
		add(cleanReleaseTitleForQuery(entry.Title))
		if len(queries) >= 4 {
			break
		}
	}
	for _, field := range strings.Fields(feedTitle) {
		field = rssQueryEdgePattern.ReplaceAllString(field, "")
		if _, noise := rssFeedQueryNoise[strings.ToLower(field)]; noise {
			continue
		}
		add(field)
	}
	return queries
}

func cleanReleaseTitleForQuery(title string) string {
	candidate := strings.TrimSpace(title)
	candidate = rssBracketGroupPattern.ReplaceAllString(candidate, " ")
	candidate = rssQualityTokenPattern.ReplaceAllString(candidate, " ")
	candidate = rssSeasonEpisodePattern.ReplaceAllString(candidate, " ")
	candidate = rssEpisodeTokenPattern.ReplaceAllString(candidate, " ")
	candidate = rssBareEpisodePattern.ReplaceAllString(candidate, " ")
	candidate = rssSeparatorPattern.ReplaceAllString(candidate, " ")
	candidate = strings.TrimSpace(candidate)
	if len([]rune(candidate)) < 2 {
		return ""
	}
	return candidate
}
