package domain

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type RSSSubtitleGroup struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type RSSSubtitleGroupPage struct {
	Items []RSSSubtitleGroup
}

type CreateRSSSubtitleGroup struct {
	Name        string
	ActorUserID uuid.UUID
}

type UpdateRSSSubtitleGroup struct {
	ID          uuid.UUID
	Name        string
	ActorUserID uuid.UUID
}

const maxRSSSubtitleGroupCandidates = 8

type rssSubtitleGroupSource struct {
	text  string
	score int
}

type rssSubtitleGroupCandidate struct {
	name  string
	score int
	order int
}

// NormalizeRSSSubtitleGroupName 在持久化和匹配前移除展示用包裹符并稳定空白字符。
func NormalizeRSSSubtitleGroupName(value string) string {
	value = strings.Map(func(token rune) rune {
		switch {
		case token >= '\uFF01' && token <= '\uFF5E':
			return token - 0xFEE0
		case token == '\u00A0' || token == '\u3000':
			return ' '
		default:
			return token
		}
	}, strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	for {
		runes := []rune(value)
		if len(runes) < 2 {
			break
		}
		if _, ok := rssSubtitleGroupClosingBracket(runes[0], runes[len(runes)-1]); !ok {
			break
		}
		value = strings.Join(strings.Fields(string(runes[1:len(runes)-1])), " ")
	}
	return value
}

// ExtractRSSSubtitleGroupCandidates 优先匹配已维护名称，再回退到标题开头的括号标签。
func ExtractRSSSubtitleGroupCandidates(feed RSSFeed, maintained []string) []string {
	sources := make([]rssSubtitleGroupSource, 0, len(feed.Entries)+1)
	if title := strings.TrimSpace(feed.Title); title != "" {
		sources = append(sources, rssSubtitleGroupSource{text: title, score: 1000})
	}
	for index, entry := range feed.Entries {
		if index >= 20 {
			break
		}
		if title := strings.TrimSpace(entry.Title); title != "" {
			sources = append(sources, rssSubtitleGroupSource{text: title, score: 800 - index*10})
		}
	}

	candidates := make(map[string]*rssSubtitleGroupCandidate, len(maintained))
	order := 0
	add := func(name string, score int) {
		name = NormalizeRSSSubtitleGroupName(name)
		if !validRSSSubtitleGroupCandidate(name) {
			return
		}
		key := rssSubtitleGroupKey(name)
		if key == "" {
			return
		}
		if existing, ok := candidates[key]; ok {
			if score > existing.score {
				existing.score = score
			}
			return
		}
		candidates[key] = &rssSubtitleGroupCandidate{name: name, score: score, order: order}
		order++
	}

	for _, source := range sources {
		for maintainedIndex, rawName := range maintained {
			name := NormalizeRSSSubtitleGroupName(rawName)
			if !validRSSSubtitleGroupCandidate(name) || !rssSubtitleGroupTextContains(source.text, name) {
				continue
			}
			score := source.score - maintainedIndex
			for bracketIndex, bracketValue := range leadingRSSSubtitleGroupBrackets(source.text) {
				if rssSubtitleGroupKey(bracketValue) == rssSubtitleGroupKey(name) {
					score += 260 - bracketIndex*20
					break
				}
			}
			add(name, score+utf8.RuneCountInString(name))
		}
	}

	if len(candidates) == 0 {
		for _, source := range sources {
			for bracketIndex, value := range leadingRSSSubtitleGroupBrackets(source.text) {
				add(value, source.score-bracketIndex*20)
			}
		}
	}

	sorted := make([]*rssSubtitleGroupCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		sorted = append(sorted, candidate)
	}
	sort.SliceStable(sorted, func(left, right int) bool {
		if sorted[left].score != sorted[right].score {
			return sorted[left].score > sorted[right].score
		}
		return sorted[left].order < sorted[right].order
	})
	if len(sorted) > maxRSSSubtitleGroupCandidates {
		sorted = sorted[:maxRSSSubtitleGroupCandidates]
	}
	result := make([]string, 0, len(sorted))
	for _, candidate := range sorted {
		result = append(result, candidate.name)
	}
	return result
}

func rssSubtitleGroupTextContains(text, name string) bool {
	text = strings.TrimSpace(text)
	name = NormalizeRSSSubtitleGroupName(name)
	if text == "" || name == "" {
		return false
	}
	lowerText := strings.ToLower(text)
	lowerName := strings.ToLower(name)
	searchOffset := 0
	for searchOffset < len(lowerText) {
		index := strings.Index(lowerText[searchOffset:], lowerName)
		if index < 0 {
			break
		}
		start := searchOffset + index
		end := start + len(lowerName)
		if rssSubtitleGroupBoundary(lowerText, start, end) {
			return true
		}
		searchOffset = start + len(lowerName)
	}

	key := rssSubtitleGroupKey(name)
	return utf8.RuneCountInString(key) >= 3 && strings.Contains(rssSubtitleGroupKey(text), key)
}

func rssSubtitleGroupBoundary(value string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(value[:start])
		if unicode.IsLetter(previous) || unicode.IsNumber(previous) {
			return false
		}
	}
	if end < len(value) {
		next, _ := utf8.DecodeRuneInString(value[end:])
		if unicode.IsLetter(next) || unicode.IsNumber(next) {
			return false
		}
	}
	return true
}

func rssSubtitleGroupKey(value string) string {
	return strings.Map(func(token rune) rune {
		if unicode.IsLetter(token) || unicode.IsNumber(token) {
			return unicode.ToLower(token)
		}
		return -1
	}, value)
}

func leadingRSSSubtitleGroupBrackets(value string) []string {
	remaining := strings.TrimSpace(value)
	result := make([]string, 0, 3)
	for remaining != "" {
		runes := []rune(remaining)
		if len(runes) == 0 {
			break
		}
		closing, ok := rssSubtitleGroupOpeningBracket(runes[0])
		if !ok {
			break
		}
		closingIndex := -1
		for index := 1; index < len(runes); index++ {
			if runes[index] == closing {
				closingIndex = index
				break
			}
		}
		if closingIndex < 0 {
			break
		}
		result = append(result, string(runes[1:closingIndex]))
		remaining = strings.TrimSpace(string(runes[closingIndex+1:]))
	}
	return result
}

func rssSubtitleGroupOpeningBracket(value rune) (rune, bool) {
	switch value {
	case '[', '【', '(', '（', '「', '『', '〖':
		switch value {
		case '[':
			return ']', true
		case '【':
			return '】', true
		case '(':
			return ')', true
		case '（':
			return '）', true
		case '「':
			return '」', true
		case '『':
			return '』', true
		default:
			return '〗', true
		}
	default:
		return 0, false
	}
}

func rssSubtitleGroupClosingBracket(open, close rune) (rune, bool) {
	closing, ok := rssSubtitleGroupOpeningBracket(open)
	return closing, ok && closing == close
}

func validRSSSubtitleGroupCandidate(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > 128 {
		return false
	}
	key := rssSubtitleGroupKey(value)
	if utf8.RuneCountInString(key) < 2 {
		return false
	}
	if allRSSSubtitleGroupDigits(key) {
		return false
	}
	lower := strings.ToLower(value)
	for _, token := range []string{"1080p", "2160p", "720p", "480p", "4k", "hevc", "x264", "x265", "aac", "flac", "mkv", "mp4"} {
		if strings.Contains(lower, token) {
			return false
		}
	}
	return true
}

func allRSSSubtitleGroupDigits(value string) bool {
	for _, token := range value {
		if !unicode.IsNumber(token) {
			return false
		}
	}
	return value != ""
}
