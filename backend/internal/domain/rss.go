package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

var btihPattern = regexp.MustCompile(`(?i)^[0-9a-f]{40}$`)

// RSSEnqueueCandidate is an entry that may produce an independent download job.
type RSSEnqueueCandidate struct {
	EntryID      uuid.UUID
	Status       RSSState
	Downloadable bool
}

type RSSIdentityInput struct {
	GUID        string
	BTIH        string
	URL         string
	Title       string
	PublishedAt time.Time
}

type RSSFeed struct {
	Title   string
	Entries []RSSFeedEntry
}

// RSSFeedLookup describes a fetched RSS feed and the bounded catalog lookup
// performed before subscription creation.
type RSSFeedLookup struct {
	FeedURL            string
	FeedTitle          string
	SuggestedQuery     string
	SuggestedQueries   []string
	SampleTitles       []string
	Candidates         []TMDbSeriesSearchResult
	CatalogMatchSource string
	AgentResolutionID  *uuid.UUID
}

type RSSFeedEntry struct {
	GUID            string
	Title           string
	URL             string
	DownloadURI     string
	PublishedAt     time.Time
	UpstreamPayload map[string]any
}

type RSSReleaseAnalysis struct {
	SourceSeason     int
	SourceEpisode    int
	Downloadable     bool
	RejectionReasons []string
}

// BuildRSSIdentity applies the stable RSS identity priority used by the
// subscription uniqueness constraint: GUID, BTIH, canonical URL, then a
// title/published-time digest.
var (
	rssRangePattern       = regexp.MustCompile(`(?i)(?:^|[^0-9])([0-9]{1,3})[[:space:]]*[-~+&—–][[:space:]]*([0-9]{1,3})(?:$|[^0-9])`)
	rssMarkedRangePattern = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(?:s[0-9]{1,2}[ ._-]*)?(?:episode|ep|e)[ ._-]*[0-9]{1,4}(?:v[0-9]+)?[[:space:]]*[-~+&][[:space:]]*(?:episode|ep|e)?[ ._-]*[0-9]{1,4}(?:v[0-9]+)?(?:$|[^[:alnum:]])`)
	rssCompilationPattern = regexp.MustCompile(`(?i)(総集編|总集篇|合集|合輯|recap|compilation|complete|全集)`)
)

func AnalyzeRSSRelease(title, downloadURI string, defaultSeason int, includeKeywords, excludeKeywords []string) RSSReleaseAnalysis {
	analysis := RSSReleaseAnalysis{}
	filterTitle := strings.TrimSpace(title)
	normalizedTitle := normalizeRSSReleaseTitle(filterTitle)
	if rssRangePattern.MatchString(normalizedTitle) || rssMarkedRangePattern.MatchString(normalizedTitle) {
		analysis.RejectionReasons = append(analysis.RejectionReasons, "episode_range_batch")
		return analysis
	}
	if extraTokenPattern.MatchString(normalizedTitle) || rssCompilationPattern.MatchString(normalizedTitle) {
		analysis.RejectionReasons = append(analysis.RejectionReasons, "non_episode_extra")
		return analysis
	}
	coordinate := ParseRSSReleaseCoordinate(normalizedTitle, defaultSeason)
	switch coordinate.Status {
	case RSSCoordinateMatched:
		analysis.SourceSeason = coordinate.SourceSeason
		analysis.SourceEpisode = coordinate.SourceEpisode
	case RSSCoordinateAmbiguous:
		analysis.RejectionReasons = append(analysis.RejectionReasons, "episode_ambiguous")
	default:
		analysis.RejectionReasons = append(analysis.RejectionReasons, "episode_not_detected")
	}
	if !isDownloadURI(downloadURI) {
		analysis.RejectionReasons = append(analysis.RejectionReasons, "download_uri_missing")
	}
	if reason := RSSFilterRejectionReason(filterTitle, includeKeywords, excludeKeywords); reason != "" {
		analysis.RejectionReasons = append(analysis.RejectionReasons, reason)
	}
	analysis.Downloadable = len(analysis.RejectionReasons) == 0
	return analysis
}

// RSSFilterRejectionReason returns the title-filter rejection reason. Exclude
// keywords take precedence when a title matches both rule sets.
func RSSFilterRejectionReason(title string, includeKeywords, excludeKeywords []string) string {
	normalizedTitle := strings.ToLower(title)
	for _, keyword := range excludeKeywords {
		keyword = strings.TrimSpace(keyword)
		if keyword != "" && strings.Contains(normalizedTitle, strings.ToLower(keyword)) {
			return "title_excluded"
		}
	}
	if len(includeKeywords) == 0 {
		return ""
	}
	for _, keyword := range includeKeywords {
		keyword = strings.TrimSpace(keyword)
		if keyword != "" && strings.Contains(normalizedTitle, strings.ToLower(keyword)) {
			return ""
		}
	}
	return "title_include_mismatch"
}

// CanAdjudicateRSSRelease permits Agent escalation only after deterministic
// parsing has passed hard transport/filter rules but cannot resolve one
// unambiguous episode coordinate.
func CanAdjudicateRSSRelease(title, downloadURI string, defaultSeason int, includeKeywords, excludeKeywords []string) bool {
	return canAdjudicateRSSReleaseAnalysis(AnalyzeRSSRelease(title, downloadURI, defaultSeason, includeKeywords, excludeKeywords))
}

// PlanRSSReleaseAdjudication evaluates uniqueness across the complete poll.
// Individually parseable releases still require adjudication when multiple
// candidates claim the same season and episode coordinate.
func PlanRSSReleaseAdjudication(entries []RSSFeedEntry, defaultSeason int, includeKeywords, excludeKeywords []string) []bool {
	planned := make([]bool, len(entries))
	coordinates := make(map[EpisodeCoordinate][]int)
	for index, entry := range entries {
		analysis := AnalyzeRSSRelease(entry.Title, entry.DownloadURI, defaultSeason, includeKeywords, excludeKeywords)
		planned[index] = canAdjudicateRSSReleaseAnalysis(analysis)
		if analysis.Downloadable {
			coordinate := EpisodeCoordinate{Season: analysis.SourceSeason, Episode: analysis.SourceEpisode}
			coordinates[coordinate] = append(coordinates[coordinate], index)
		}
	}
	for _, indexes := range coordinates {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			planned[index] = true
		}
	}
	return planned
}

func canAdjudicateRSSReleaseAnalysis(analysis RSSReleaseAnalysis) bool {
	if analysis.Downloadable || len(analysis.RejectionReasons) == 0 {
		return false
	}
	for _, reason := range analysis.RejectionReasons {
		switch reason {
		case "episode_not_detected", "episode_ambiguous":
		default:
			return false
		}
	}
	return true
}

func BuildRSSIdentity(input RSSIdentityInput) (string, error) {
	if guid := strings.TrimSpace(input.GUID); guid != "" {
		return "guid:" + guid, nil
	}
	if hash := normalizeBTIH(input.BTIH); hash != "" {
		return "btih:" + hash, nil
	}
	if hash := btihFromMagnet(input.URL); hash != "" {
		return "btih:" + hash, nil
	}
	if canonicalURL, ok := canonicalHTTPURL(input.URL); ok {
		return "url:" + canonicalURL, nil
	}

	title := strings.Join(strings.Fields(input.Title), " ")
	if title == "" || input.PublishedAt.IsZero() {
		return "", errors.New("RSS entry requires a GUID, BTIH, canonical HTTP(S) URL, or title with published time")
	}
	digest := sha256.Sum256([]byte(title + "\n" + input.PublishedAt.UTC().Format(time.RFC3339Nano)))
	return "title-time:" + hex.EncodeToString(digest[:]), nil
}

func normalizeBTIH(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.ToLower(value), "urn:btih:")
	if !btihPattern.MatchString(value) {
		return ""
	}
	return strings.ToLower(value)
}

func btihFromMagnet(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") {
		return ""
	}
	for _, exactTopic := range parsed.Query()["xt"] {
		if hash := normalizeBTIH(exactTopic); hash != "" {
			return hash
		}
	}
	return ""
}

func isDownloadURI(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.User != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "magnet":
		return parsed.RawQuery != ""
	case "http", "https":
		return parsed.Host != ""
	default:
		return false
	}
}

func canonicalHTTPURL(rawURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.User != nil || parsed.Host == "" {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}

	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}

	query := parsed.Query()
	for key, values := range query {
		sort.Strings(values)
		query[key] = values
	}
	parsed.RawQuery = query.Encode()
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), true
}
