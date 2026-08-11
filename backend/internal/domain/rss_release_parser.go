package domain

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type RSSCoordinateParseStatus string

const (
	RSSCoordinateMatched   RSSCoordinateParseStatus = "matched"
	RSSCoordinateNotFound  RSSCoordinateParseStatus = "not_found"
	RSSCoordinateAmbiguous RSSCoordinateParseStatus = "ambiguous"
)

type RSSCoordinateParseResult struct {
	Status        RSSCoordinateParseStatus
	SourceSeason  int
	SourceEpisode int
	Evidence      string
}

type rssCoordinatePattern struct {
	expression   *regexp.Regexp
	priority     int
	seasonGroup  int
	episodeGroup int
	evidence     string
	weak         bool
}

type rssCoordinateCandidate struct {
	season   int
	episode  int
	priority int
	evidence string
}

const rssReleaseNumber = `[0-9零〇一二三四五六七八九十百两兩]{1,6}`

var (
	rssTechnicalDecimalPattern  = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(?:[0-9]{1,2}[.][0-9](?:ch)?|h[.]?26[456]|x26[456])(?:$|[^[:alnum:]])`)
	rssStandaloneSeasonPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])s[ ._-]*([0-9]{1,2})(?:$|[^[:alnum:]])`),
		regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])season[ ._]*([0-9]{1,2})(?:$|[^[:alnum:]])`),
		regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])([0-9]{1,2})(?:st|nd|rd|th)[ ._-]*season(?:$|[^[:alnum:]])`),
		regexp.MustCompile(`第[[:space:]]*(` + rssReleaseNumber + `)[[:space:]]*季`),
	}
	rssCoordinatePatterns = []rssCoordinatePattern{
		{
			expression: regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])s[ ._-]*([0-9]{1,2})[ ._-]*e[ ._-]*([0-9]{1,4})(?:v[0-9]+)?(?:$|[^[:alnum:]])`),
			priority:   400, seasonGroup: 1, episodeGroup: 2, evidence: "season_episode",
		},
		{
			expression: regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])([0-9]{1,2})x([0-9]{1,4})(?:v[0-9]+)?(?:$|[^[:alnum:]])`),
			priority:   400, seasonGroup: 1, episodeGroup: 2, evidence: "season_x_episode",
		},
		{
			expression: regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])season[ ._-]*([0-9]{1,2})[ ._/-]+(?:episode|ep|e)[ ._-]*([0-9]{1,4})(?:v[0-9]+)?(?:$|[^[:alnum:]])`),
			priority:   400, seasonGroup: 1, episodeGroup: 2, evidence: "season_episode_words",
		},
		{
			expression: regexp.MustCompile(`第[[:space:]]*(` + rssReleaseNumber + `)[[:space:]]*季[^0-9零〇一二三四五六七八九十百两兩]{0,32}第?[[:space:]]*(` + rssReleaseNumber + `)[[:space:]]*[话話集回]`),
			priority:   400, seasonGroup: 1, episodeGroup: 2, evidence: "east_asian_season_episode",
		},
		{
			expression: regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(?:episode|ep|e)[ ._-]*([0-9]{1,4})(?:v[0-9]+)?(?:$|[^[:alnum:]])`),
			priority:   300, episodeGroup: 1, evidence: "episode_marker",
		},
		{
			expression: regexp.MustCompile(`(?:^|[^[:alnum:]])#[[:space:]]*([0-9]{1,4})(?:v[0-9]+)?(?:$|[^[:alnum:]])`),
			priority:   300, episodeGroup: 1, evidence: "hash_marker",
		},
		{
			expression: regexp.MustCompile(`(?:第[[:space:]]*)?(` + rssReleaseNumber + `)[[:space:]]*[话話集回]`),
			priority:   300, episodeGroup: 1, evidence: "east_asian_episode",
		},
		{
			expression: regexp.MustCompile(`(?:^|[[:space:]\]\)】）])-[[:space:]]*([0-9]{1,4})(?:v[0-9]+)?(?:$|[[:space:]\[\(【（])`),
			priority:   200, episodeGroup: 1, evidence: "release_dash",
		},
		{
			expression: regexp.MustCompile(`(?i)[\[\(【（][[:space:]]*([0-9]{1,4})(?:v[0-9]+)?[[:space:]]*[\]\)】）]`),
			priority:   200, episodeGroup: 1, evidence: "bracketed_episode",
		},
		{
			expression: regexp.MustCompile(`(?:^|[[:space:]_.])([0-9]{1,3})(?:v[0-9]+)?(?:$|[[:space:]_.])`),
			priority:   100, episodeGroup: 1, evidence: "delimited_episode", weak: true,
		},
	}
)

// ParseRSSReleaseCoordinate extracts one source coordinate from a complete RSS
// release title. It ranks all matching evidence and refuses conflicting peers.
func ParseRSSReleaseCoordinate(title string, defaultSeason int) RSSCoordinateParseResult {
	normalized := normalizeRSSReleaseTitle(title)
	if normalized == "" || defaultSeason <= 0 {
		return RSSCoordinateParseResult{Status: RSSCoordinateNotFound}
	}

	candidates := make([]rssCoordinateCandidate, 0, 4)
	for _, pattern := range rssCoordinatePatterns {
		input := normalized
		if pattern.weak {
			input = rssTechnicalDecimalPattern.ReplaceAllString(input, " ")
		}
		for _, matches := range findRSSPatternSubmatches(pattern.expression, input) {
			episode := parseRSSReleaseNumber(matches[pattern.episodeGroup])
			if episode <= 0 {
				continue
			}
			season := 0
			if pattern.seasonGroup > 0 {
				season = parseRSSReleaseNumber(matches[pattern.seasonGroup])
				if season <= 0 {
					continue
				}
			}
			candidates = append(candidates, rssCoordinateCandidate{
				season: season, episode: episode, priority: pattern.priority, evidence: pattern.evidence,
			})
		}
	}
	if len(candidates) == 0 {
		return RSSCoordinateParseResult{Status: RSSCoordinateNotFound}
	}

	sort.SliceStable(candidates, func(left, right int) bool {
		return candidates[left].priority > candidates[right].priority
	})
	highestPriority := candidates[0].priority
	resolved := make(map[EpisodeCoordinate]string)
	for _, candidate := range candidates {
		if candidate.priority != highestPriority {
			break
		}
		season := candidate.season
		if season == 0 {
			var ok bool
			season, ok = resolveRSSReleaseSeason(normalized, defaultSeason)
			if !ok {
				return RSSCoordinateParseResult{Status: RSSCoordinateAmbiguous}
			}
		}
		coordinate := EpisodeCoordinate{Season: season, Episode: candidate.episode}
		if _, exists := resolved[coordinate]; !exists {
			resolved[coordinate] = candidate.evidence
		}
	}
	if len(resolved) != 1 {
		return RSSCoordinateParseResult{Status: RSSCoordinateAmbiguous}
	}
	for coordinate, evidence := range resolved {
		return RSSCoordinateParseResult{
			Status: RSSCoordinateMatched, SourceSeason: coordinate.Season,
			SourceEpisode: coordinate.Episode, Evidence: evidence,
		}
	}
	return RSSCoordinateParseResult{Status: RSSCoordinateNotFound}
}

func resolveRSSReleaseSeason(title string, defaultSeason int) (int, bool) {
	seasons := make(map[int]struct{})
	for _, pattern := range rssStandaloneSeasonPatterns {
		for _, matches := range findRSSPatternSubmatches(pattern, title) {
			season := parseRSSReleaseNumber(matches[1])
			if season > 0 {
				seasons[season] = struct{}{}
			}
		}
	}
	if len(seasons) == 0 {
		return defaultSeason, true
	}
	if len(seasons) != 1 {
		return 0, false
	}
	for season := range seasons {
		return season, true
	}
	return 0, false
}

func findRSSPatternSubmatches(expression *regexp.Regexp, input string) [][]string {
	matches := make([][]string, 0, 2)
	searchOffset := 0
	for searchOffset < len(input) {
		indexes := expression.FindStringSubmatchIndex(input[searchOffset:])
		if indexes == nil {
			break
		}
		groups := make([]string, len(indexes)/2)
		for group := range groups {
			start := indexes[group*2]
			end := indexes[group*2+1]
			if start >= 0 {
				groups[group] = input[searchOffset+start : searchOffset+end]
			}
		}
		matches = append(matches, groups)

		matchStart := searchOffset + indexes[0]
		matchEnd := searchOffset + indexes[1]
		_, boundarySize := utf8.DecodeLastRuneInString(input[:matchEnd])
		nextOffset := matchEnd - boundarySize
		if boundarySize == 0 || nextOffset <= matchStart {
			_, firstSize := utf8.DecodeRuneInString(input[matchStart:])
			if firstSize == 0 {
				break
			}
			nextOffset = matchStart + firstSize
		}
		searchOffset = nextOffset
	}
	return matches
}

func normalizeRSSReleaseTitle(title string) string {
	normalized := strings.Map(func(value rune) rune {
		switch {
		case value >= '\uFF01' && value <= '\uFF5E':
			return value - 0xFEE0
		case strings.ContainsRune("‐‑‒–—―−", value):
			return '-'
		case value == '\u00A0' || value == '\u3000':
			return ' '
		default:
			return value
		}
	}, strings.TrimSpace(title))
	return strings.Join(strings.Fields(normalized), " ")
}

func parseRSSReleaseNumber(value string) int {
	value = strings.TrimSpace(value)
	if number := decimal(value); number > 0 {
		return number
	}

	digits := map[rune]int{
		'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '兩': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
	}
	units := map[rune]int{'十': 10, '百': 100}
	total := 0
	current := 0
	for _, token := range value {
		if digit, ok := digits[token]; ok {
			current = digit
			continue
		}
		unit, ok := units[token]
		if !ok {
			return 0
		}
		if current == 0 {
			current = 1
		}
		total += current * unit
		current = 0
	}
	return total + current
}
