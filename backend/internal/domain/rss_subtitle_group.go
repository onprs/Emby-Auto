package domain

import (
	"regexp"
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

const (
	maxRSSSubtitleGroupCandidates = 8
	rssSubtitleGroupTextMatch     = 1
	rssSubtitleGroupUnknownLabel  = 2
	rssSubtitleGroupJointLabel    = 3
	rssSubtitleGroupExactLabel    = 4
)

type rssSubtitleGroupName struct {
	name         string
	key          string
	canMatchText bool
}

type rssSubtitleGroupLabel struct {
	name     string
	key      string
	known    bool
	metadata bool
	matched  bool
}

type rssSubtitleGroupSource struct {
	text   string
	labels []rssSubtitleGroupLabel
	score  int
}

type rssSubtitleGroupCandidate struct {
	name  string
	key   string
	rank  int
	score int
}

var rssSubtitleGroupMetadataPattern = regexp.MustCompile(`^(?:[0-9]{3,4}p|[248]k|[0-9]{1,2}bit|(?:s[0-9]{1,2})?(?:e(?:p(?:isode)?)?)?[0-9]{1,4}(?:v[0-9]+)?|第[0-9]+[话話集])$`)

var rssSubtitleGroupMetadataTokens = map[string]struct{}{
	"bdrip": {}, "brrip": {}, "bluray": {}, "blu": {}, "ray": {}, "bd": {}, "dvd": {}, "dvdrip": {},
	"web": {}, "webdl": {}, "dl": {}, "webrip": {}, "hdtv": {}, "raw": {}, "remux": {},
	"av1": {}, "avc": {}, "hevc": {}, "h264": {}, "h265": {}, "h.264": {}, "h.265": {},
	"x264": {}, "x265": {}, "hi10p": {}, "aac": {}, "flac": {}, "opus": {}, "ac3": {}, "dts": {},
	"mkv": {}, "mp4": {}, "ass": {}, "srt": {},
	"chs": {}, "cht": {}, "chinese": {}, "jpn": {}, "jp": {}, "japanese": {}, "eng": {}, "english": {}, "gb": {}, "big5": {},
	"简体": {}, "簡體": {}, "繁体": {}, "繁體": {}, "简体中文": {}, "簡體中文": {}, "繁体中文": {}, "繁體中文": {},
	"简日": {}, "簡日": {}, "繁日": {}, "简繁": {}, "簡繁": {}, "简繁内封": {}, "簡繁內封": {},
	"简繁字幕": {}, "簡繁字幕": {}, "简体字幕": {}, "簡體字幕": {}, "繁体字幕": {}, "繁體字幕": {},
	"双语": {}, "雙語": {}, "双语字幕": {}, "雙語字幕": {}, "内封": {}, "內封": {}, "外挂": {}, "外掛": {},
}

// NormalizeRSSSubtitleGroupName 只移除覆盖整个名称的成对括号，保留名称内部的空格和标点。
func NormalizeRSSSubtitleGroupName(value string) string {
	value = normalizeRSSSubtitleGroupText(value)
	runes := []rune(value)
	for len(runes) >= 2 && rssSubtitleGroupBracketEnd(runes) == len(runes)-1 {
		runes = runes[1 : len(runes)-1]
		for len(runes) > 0 && unicode.IsSpace(runes[0]) {
			runes = runes[1:]
		}
		for len(runes) > 0 && unicode.IsSpace(runes[len(runes)-1]) {
			runes = runes[:len(runes)-1]
		}
	}
	return string(runes)
}

// RSSSubtitleGroupNameKey 与维护表的唯一键使用相同规则，不合并不同的空格或标点形式。
func RSSSubtitleGroupNameKey(value string) string {
	return strings.ToLower(NormalizeRSSSubtitleGroupName(value))
}

// ValidRSSSubtitleGroupName 允许可见短名称和数字名称，拒绝无可见内容或数据库不能表示的控制字符。
func ValidRSSSubtitleGroupName(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return false
	}
	visible := false
	for _, token := range value {
		if unicode.IsControl(token) && !unicode.IsSpace(token) {
			return false
		}
		if unicode.IsLetter(token) || unicode.IsNumber(token) || unicode.IsPunct(token) || unicode.IsSymbol(token) {
			visible = true
		}
	}
	return visible
}

func normalizeRSSSubtitleGroupText(value string) string {
	value = strings.Map(func(token rune) rune {
		switch {
		case token >= '\uFF01' && token <= '\uFF5E':
			return token - 0xFEE0
		case token == '\u00A0' || token == '\u3000':
			return ' '
		default:
			return token
		}
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

// ExtractRSSSubtitleGroupCandidates 按完整标签、联合发布标签、未知标签、正文完整名称的证据排序。
func ExtractRSSSubtitleGroupCandidates(feed RSSFeed, maintained []string) []string {
	byKey := make(map[string]rssSubtitleGroupName, len(maintained))
	for _, rawName := range maintained {
		name := NormalizeRSSSubtitleGroupName(rawName)
		if !ValidRSSSubtitleGroupName(name) {
			continue
		}
		key := strings.ToLower(name)
		if existing, ok := byKey[key]; !ok || name < existing.name {
			byKey[key] = rssSubtitleGroupName{name: name, key: key, canMatchText: validRSSSubtitleGroupCandidate(name)}
		}
	}
	names := make([]rssSubtitleGroupName, 0, len(byKey))
	for _, name := range byKey {
		names = append(names, name)
	}
	sort.Slice(names, func(left, right int) bool { return names[left].key < names[right].key })

	sources := make([]rssSubtitleGroupSource, 0, 21)
	appendSource := func(title string, score int) {
		text := normalizeRSSSubtitleGroupText(title)
		if text == "" {
			return
		}
		labels, remaining := leadingRSSSubtitleGroupBrackets(text)
		source := rssSubtitleGroupSource{text: strings.ToLower(remaining), score: score}
		for _, label := range labels {
			name := NormalizeRSSSubtitleGroupName(label)
			key := strings.ToLower(name)
			source.labels = append(source.labels, rssSubtitleGroupLabel{name: name, key: key, metadata: isRSSSubtitleGroupMetadata(key)})
		}
		sources = append(sources, source)
	}
	appendSource(feed.Title, 1000)
	for index, entry := range feed.Entries {
		if index >= 20 {
			break
		}
		appendSource(entry.Title, 800-index*10)
	}

	candidates := make(map[string]*rssSubtitleGroupCandidate)
	add := func(name, key string, rank, score int) {
		if existing, ok := candidates[key]; ok {
			if rank > existing.rank || rank == existing.rank && score > existing.score {
				existing.rank, existing.score = rank, score
			}
			return
		}
		candidates[key] = &rssSubtitleGroupCandidate{name: name, key: key, rank: rank, score: score}
	}

	for _, source := range sources {
		for index := range source.labels {
			label := &source.labels[index]
			if name, ok := byKey[label.key]; ok {
				add(name.name, name.key, rssSubtitleGroupExactLabel, source.score-index*20)
				label.known, label.matched = true, true
			}
		}
		for _, name := range names {
			if !name.canMatchText {
				continue
			}
			for index := range source.labels {
				label := &source.labels[index]
				if label.known || label.metadata {
					continue
				}
				if rssSubtitleGroupContains(label.key, name.key, rssSubtitleGroupJointBoundary) {
					add(name.name, name.key, rssSubtitleGroupJointLabel, source.score-index*20+utf8.RuneCountInString(name.name))
					label.matched = true
				}
			}
			if rssSubtitleGroupContains(source.text, name.key, rssSubtitleGroupBoundary) {
				add(name.name, name.key, rssSubtitleGroupTextMatch, source.score+utf8.RuneCountInString(name.name))
			}
		}
		for index, label := range source.labels {
			if !label.matched && validRSSSubtitleGroupCandidate(label.name) {
				add(label.name, label.key, rssSubtitleGroupUnknownLabel, source.score-index*20)
			}
		}
	}

	sorted := make([]*rssSubtitleGroupCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		sorted = append(sorted, candidate)
	}
	sort.Slice(sorted, func(left, right int) bool {
		if sorted[left].rank != sorted[right].rank {
			return sorted[left].rank > sorted[right].rank
		}
		if sorted[left].score != sorted[right].score {
			return sorted[left].score > sorted[right].score
		}
		return sorted[left].key < sorted[right].key
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

// 参数已在当前请求中完成规范化，联合发布标签与正文使用各自的边界规则。
func rssSubtitleGroupContains(text, key string, boundary func(string, int, int) bool) bool {
	if text == "" || key == "" {
		return false
	}
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], key)
		if index < 0 {
			return false
		}
		start := offset + index
		if boundary(text, start, start+len(key)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		offset = start + size
	}
	return false
}

func rssSubtitleGroupJointBoundary(value string, start, end int) bool {
	// 空格和连字符属于名称；只有明确的联合发布分隔符才能拆分标签。
	const separators = "&+/|,、×"
	before := strings.TrimSpace(value[:start])
	after := strings.TrimSpace(value[end:])
	if before != "" {
		previous, _ := utf8.DecodeLastRuneInString(before)
		if !strings.ContainsRune(separators, previous) {
			return false
		}
	}
	if after != "" {
		next, _ := utf8.DecodeRuneInString(after)
		if !strings.ContainsRune(separators, next) {
			return false
		}
	}
	return true
}

func rssSubtitleGroupBoundary(value string, start, end int) bool {
	isWord := func(token rune) bool {
		return unicode.IsLetter(token) || unicode.IsNumber(token) || unicode.IsMark(token) || unicode.Is(unicode.Cf, token)
	}
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(value[:start])
		if isWord(previous) {
			return false
		}
	}
	if end < len(value) {
		next, _ := utf8.DecodeRuneInString(value[end:])
		if isWord(next) {
			return false
		}
	}
	return true
}

func leadingRSSSubtitleGroupBrackets(value string) ([]string, string) {
	runes := []rune(value)
	result := make([]string, 0, 3)
	for len(runes) > 0 {
		end := rssSubtitleGroupBracketEnd(runes)
		if end < 0 {
			break
		}
		result = append(result, string(runes[1:end]))
		runes = runes[end+1:]
		for len(runes) > 0 && unicode.IsSpace(runes[0]) {
			runes = runes[1:]
		}
	}
	return result, string(runes)
}

func rssSubtitleGroupBracketEnd(value []rune) int {
	if len(value) == 0 {
		return -1
	}
	closing, ok := rssSubtitleGroupOpeningBracket(value[0])
	if !ok {
		return -1
	}
	stack := []rune{closing}
	for index := 1; index < len(value); index++ {
		token := value[index]
		if closing, ok := rssSubtitleGroupOpeningBracket(token); ok {
			stack = append(stack, closing)
		} else if strings.ContainsRune("]】)）」』〗", token) {
			if token != stack[len(stack)-1] {
				return -1
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return index
			}
		}
	}
	return -1
}

func rssSubtitleGroupOpeningBracket(value rune) (rune, bool) {
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
	case '〖':
		return '〗', true
	default:
		return 0, false
	}
}

func validRSSSubtitleGroupCandidate(value string) bool {
	if !ValidRSSSubtitleGroupName(value) || isRSSSubtitleGroupMetadata(strings.ToLower(value)) {
		return false
	}
	characters, letters := 0, false
	for _, token := range value {
		if unicode.IsLetter(token) || unicode.IsNumber(token) {
			characters++
			letters = letters || unicode.IsLetter(token)
		}
	}
	return characters >= 2 && letters
}

func isRSSSubtitleGroupMetadata(value string) bool {
	tokens := strings.FieldsFunc(value, func(token rune) bool {
		return unicode.IsSpace(token) || strings.ContainsRune("-_+/&|,", token)
	})
	if len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		if _, ok := rssSubtitleGroupMetadataTokens[token]; !ok && !rssSubtitleGroupMetadataPattern.MatchString(token) {
			return false
		}
	}
	return true
}
