package domain

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

type MediaKind string

const (
	MediaUnknown  MediaKind = "unknown"
	MediaVideo    MediaKind = "video"
	MediaSubtitle MediaKind = "subtitle"
	MediaExtra    MediaKind = "extra"
	MediaOther    MediaKind = "other"

	maxExternalSubtitleCandidates = 8
)

var (
	ErrUnsafeDownloadPath    = errors.New("unsafe download file path")
	ErrDuplicateDownloadFile = errors.New("duplicate download file")
	ErrNoMainVideo           = errors.New("no main video found")

	extraTokenPattern          = regexp.MustCompile(`(?i)(^|[^[:alnum:]])(?:ncop|nced|op[0-9]*|ed[0-9]*|pv[0-9]*|cm[0-9]*|sp[0-9]*|menu|scans?|sample|trailer|teaser|bonus|creditless)(?:$|[^[:alnum:]])`)
	extraSingleTokenPattern    = regexp.MustCompile(`(?i)^(?:ncop|nced|op[0-9]*|ed[0-9]*|pv[0-9]*|cm[0-9]*|sp[0-9]*|menu|scans?|sample|trailer|teaser|bonus|creditless)$`)
	extraDirectoryTokenPattern = regexp.MustCompile(`[\p{L}\p{N}]+`)
	seasonEpisodePattern       = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])s([0-9]{1,2})[ ._-]*e([0-9]{1,3})(?:v[0-9]+)?(?:$|[^[:alnum:]])`)
	seasonDirectoryPattern     = regexp.MustCompile(`(?i)(?:^|/)(?:season|s)[ ._-]*([0-9]{1,2})(?:/|$)`)
	eastAsianEpisodePattern    = regexp.MustCompile(`(?:第[[:space:]]*)?([0-9]{1,3})[[:space:]]*[话話集]`)
	episodeTokenPattern        = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(?:ep|episode|e)[ ._-]*([0-9]{1,3})(?:v[0-9]+)?(?:$|[^[:alnum:]])`)
	delimitedEpisodePattern    = regexp.MustCompile(`(?i)(?:^|[[:space:]._\[\]-])([0-9]{1,3})(?:v[0-9]+)?(?:$|[[:space:]._\]\)-])`)
	drivePathPattern           = regexp.MustCompile(`(?i)^[a-z]:/`)
	nonNamePattern             = regexp.MustCompile(`[^[:alnum:]]+`)
	trailingLanguagePattern    = regexp.MustCompile(`(?i)[ ._-]+(?:zh[ ._-]*(?:hans|hant|cn|sg|tw|hk|mo)|chs|cht|sc|tc|gb(?:2312|k)?|big5|简体(?:中文)?|簡體(?:中文)?|简中|簡中|ja|jpn|jp|en|eng)$`)
)

var videoExtensions = map[string]struct{}{
	".avi": {}, ".m2ts": {}, ".m4v": {}, ".mkv": {}, ".mov": {},
	".mp4": {}, ".ts": {}, ".webm": {},
}

var subtitleExtensions = map[string]struct{}{
	".ass": {}, ".srt": {}, ".ssa": {}, ".vtt": {},
}

type DownloadFile struct {
	Index        int
	RelativePath string
	SizeBytes    int64
}

type ClassifiedDownloadFile struct {
	DownloadFile
	Kind          MediaKind
	SourceSeason  int
	SourceEpisode int
	Language      string
	Selected      bool
}

type SelectedEpisode struct {
	SourceSeason  int
	SourceEpisode int
	Video         ClassifiedDownloadFile
	Subtitle      *ClassifiedDownloadFile
	Subtitles     []ClassifiedDownloadFile
}

type FileSelectionOptions struct {
	DefaultSeason  int
	DefaultEpisode int
	SingleEpisode  bool
}

type FileSelectionResult struct {
	Files    []ClassifiedDownloadFile
	Episodes []SelectedEpisode
}

type sourceCoordinate struct {
	season  int
	episode int
}

// ClassifyDownloadFiles validates a qBittorrent manifest and derives only facts
// that are safe to persist before a selection decision exists.
func ClassifyDownloadFiles(files []DownloadFile, options FileSelectionOptions) ([]ClassifiedDownloadFile, error) {
	if options.DefaultSeason <= 0 {
		return nil, fmt.Errorf("default season must be positive")
	}
	if options.SingleEpisode && options.DefaultEpisode <= 0 {
		return nil, fmt.Errorf("single episode number must be positive")
	}

	classifiedFiles := make([]ClassifiedDownloadFile, len(files))
	seenIndexes := make(map[int]struct{}, len(files))
	for index, file := range files {
		if file.Index < 0 {
			return nil, fmt.Errorf("%w: negative qBittorrent index %d", ErrDuplicateDownloadFile, file.Index)
		}
		if _, exists := seenIndexes[file.Index]; exists {
			return nil, fmt.Errorf("%w: qBittorrent index %d", ErrDuplicateDownloadFile, file.Index)
		}
		seenIndexes[file.Index] = struct{}{}
		if err := validateRelativeDownloadPath(file.RelativePath); err != nil {
			return nil, fmt.Errorf("file index %d: %w", file.Index, err)
		}
		if file.SizeBytes < 0 {
			return nil, fmt.Errorf("file index %d has a negative size", file.Index)
		}

		classified := ClassifiedDownloadFile{DownloadFile: file, Kind: classifyDownloadPath(file.RelativePath)}
		if classified.Kind == MediaVideo || classified.Kind == MediaSubtitle {
			season, episode, ok := ParseSourceCoordinate(file.RelativePath, options.DefaultSeason)
			if options.SingleEpisode {
				season, episode, ok = options.DefaultSeason, options.DefaultEpisode, true
			}
			if ok {
				classified.SourceSeason = season
				classified.SourceEpisode = episode
			}
		}
		if classified.Kind == MediaSubtitle {
			classified.Language = detectSubtitleLanguage(file.RelativePath)
		}
		classifiedFiles[index] = classified
	}
	return classifiedFiles, nil
}

// SelectDownloadFiles classifies a qBittorrent file list and deterministically
// selects one largest main video and bounded text subtitle candidates per episode.
func SelectDownloadFiles(files []DownloadFile, options FileSelectionOptions) (FileSelectionResult, error) {
	classifiedFiles, err := ClassifyDownloadFiles(files, options)
	if err != nil {
		return FileSelectionResult{}, err
	}
	result := FileSelectionResult{Files: classifiedFiles}

	videos := make(map[sourceCoordinate]int)
	for index, file := range result.Files {
		if file.Kind != MediaVideo || file.SourceSeason <= 0 || file.SourceEpisode <= 0 {
			continue
		}
		coordinate := sourceCoordinate{season: file.SourceSeason, episode: file.SourceEpisode}
		current, exists := videos[coordinate]
		if !exists || betterVideo(file, result.Files[current]) {
			videos[coordinate] = index
		}
	}
	if len(videos) == 0 {
		return FileSelectionResult{}, ErrNoMainVideo
	}

	coordinates := make([]sourceCoordinate, 0, len(videos))
	for coordinate := range videos {
		coordinates = append(coordinates, coordinate)
	}
	sort.Slice(coordinates, func(left, right int) bool {
		if coordinates[left].season != coordinates[right].season {
			return coordinates[left].season < coordinates[right].season
		}
		return coordinates[left].episode < coordinates[right].episode
	})

	for _, coordinate := range coordinates {
		videoIndex := videos[coordinate]
		result.Files[videoIndex].Selected = true
		subtitleIndexes := selectSubtitles(result.Files, coordinate, result.Files[videoIndex])
		subtitles := make([]ClassifiedDownloadFile, 0, len(subtitleIndexes))
		for _, subtitleIndex := range subtitleIndexes {
			result.Files[subtitleIndex].Selected = true
			subtitles = append(subtitles, result.Files[subtitleIndex])
		}
		var subtitle *ClassifiedDownloadFile
		if len(subtitles) > 0 {
			selected := subtitles[0]
			subtitle = &selected
		}
		result.Episodes = append(result.Episodes, SelectedEpisode{
			SourceSeason: coordinate.season, SourceEpisode: coordinate.episode,
			Video: result.Files[videoIndex], Subtitle: subtitle, Subtitles: subtitles,
		})
	}
	return result, nil
}

func ParseSourceCoordinate(filePath string, defaultSeason int) (int, int, bool) {
	normalized := strings.ReplaceAll(strings.TrimSpace(filePath), `\`, "/")
	base := path.Base(normalized)
	stem := strings.TrimSuffix(base, path.Ext(base))
	if matches := seasonEpisodePattern.FindStringSubmatch(stem); len(matches) == 3 {
		season := decimal(matches[1])
		episode := decimal(matches[2])
		if season > 0 && episode > 0 {
			return season, episode, true
		}
	}

	season := defaultSeason
	if matches := seasonDirectoryPattern.FindStringSubmatch(normalized); len(matches) == 2 {
		season = decimal(matches[1])
	}
	for _, pattern := range []*regexp.Regexp{eastAsianEpisodePattern, episodeTokenPattern, delimitedEpisodePattern} {
		matches := pattern.FindStringSubmatch(stem)
		if len(matches) != 2 {
			continue
		}
		episode := decimal(matches[1])
		if season > 0 && episode > 0 {
			return season, episode, true
		}
	}
	return 0, 0, false
}

func classifyDownloadPath(filePath string) MediaKind {
	normalized := strings.ReplaceAll(filePath, `\`, "/")
	// 叶子 basename 保留现有 token 匹配：含 extra 标记的文件始终为 extra
	if extraTokenPattern.MatchString(path.Base(normalized)) {
		return MediaExtra
	}
	// 目录段：仅顶层允许复合发布标签不传播，判定是否独立 extra 目录；第二层及更深仍沿用现有 extraToken 匹配，
	// 以保持 `Pack/NCOP 1080p/01.mkv`、`Pack/Bonus Features/01.mkv` 等真实嵌套 extra 语义。
	dir := path.Dir(normalized)
	if dir != "." {
		segments := strings.Split(dir, "/")
		for index, segment := range segments {
			if index == 0 {
				if isIndependentExtraDirectory(segment) {
					return MediaExtra
				}
			} else {
				if extraTokenPattern.MatchString(segment) {
					return MediaExtra
				}
			}
		}
	}
	extension := strings.ToLower(path.Ext(normalized))
	if _, ok := videoExtensions[extension]; ok {
		return MediaVideo
	}
	if _, ok := subtitleExtensions[extension]; ok {
		return MediaSubtitle
	}
	return MediaOther
}

// isIndependentExtraDirectory 判断目录段是否为独立的 extra 目录。
// 保守规则：目录名经 extraToken 预检后，提取全部字母数字 token；
// 若存在非数字、非 extra 单 token 的实质词，则视为复合发布标签而非独立 extra 目录。
// 纯数字 token 被忽略，以支持 “SP 01”、“NCOP 02” 等带序号的独立目录。
func isIndependentExtraDirectory(segment string) bool {
	trimmed := strings.TrimSpace(segment)
	if trimmed == "" || trimmed == "." {
		return false
	}
	if !extraTokenPattern.MatchString(trimmed) {
		return false
	}
	tokens := extraDirectoryTokenPattern.FindAllString(strings.ToLower(trimmed), -1)
	if len(tokens) == 0 {
		return false
	}
	hasExtra := false
	for _, token := range tokens {
		if isAllDigits(token) {
			continue
		}
		if extraSingleTokenPattern.MatchString(token) {
			hasExtra = true
			continue
		}
		return false
	}
	return hasExtra
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func validateRelativeDownloadPath(filePath string) error {
	normalized := strings.ReplaceAll(strings.TrimSpace(filePath), `\`, "/")
	if normalized == "" || strings.ContainsRune(normalized, '\x00') || strings.HasPrefix(normalized, "/") || drivePathPattern.MatchString(normalized) {
		return ErrUnsafeDownloadPath
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return ErrUnsafeDownloadPath
		}
	}
	return nil
}

func betterVideo(candidate, current ClassifiedDownloadFile) bool {
	if candidate.SizeBytes != current.SizeBytes {
		return candidate.SizeBytes > current.SizeBytes
	}
	if candidate.Index != current.Index {
		return candidate.Index < current.Index
	}
	return candidate.RelativePath < current.RelativePath
}

type subtitleFileChoice struct {
	index int
	score int
}

func selectSubtitles(files []ClassifiedDownloadFile, coordinate sourceCoordinate, video ClassifiedDownloadFile) []int {
	choices := make([]subtitleFileChoice, 0)
	for index, candidate := range files {
		if candidate.Kind != MediaSubtitle || candidate.SourceSeason != coordinate.season || candidate.SourceEpisode != coordinate.episode {
			continue
		}
		label := path.Base(strings.ReplaceAll(candidate.RelativePath, `\`, "/"))
		evidence := subtitleMetadataEvidence(candidate.Language, label)
		if evidence == SubtitleEvidenceOther {
			continue
		}
		choices = append(choices, subtitleFileChoice{
			index: index,
			score: subtitleEvidenceScore(evidence)*10 + subtitleScore(candidate, video),
		})
	}
	sort.SliceStable(choices, func(left, right int) bool {
		if choices[left].score != choices[right].score {
			return choices[left].score > choices[right].score
		}
		return subtitleTieBreak(files[choices[left].index], files[choices[right].index])
	})
	if len(choices) > maxExternalSubtitleCandidates {
		choices = choices[:maxExternalSubtitleCandidates]
	}
	indexes := make([]int, 0, len(choices))
	for _, choice := range choices {
		indexes = append(indexes, choice.index)
	}
	return indexes
}

func subtitleScore(subtitle, video ClassifiedDownloadFile) int {
	score := 0
	if normalizedMediaStem(subtitle.RelativePath, true) == normalizedMediaStem(video.RelativePath, false) {
		score += 1_000
	}
	if path.Dir(strings.ReplaceAll(subtitle.RelativePath, `\`, "/")) == path.Dir(strings.ReplaceAll(video.RelativePath, `\`, "/")) {
		score += 100
	}
	if strings.EqualFold(path.Ext(subtitle.RelativePath), ".ass") {
		score += 10
	}
	return score
}

func subtitleTieBreak(candidate, current ClassifiedDownloadFile) bool {
	if candidate.Index != current.Index {
		return candidate.Index < current.Index
	}
	return candidate.RelativePath < current.RelativePath
}

func normalizedMediaStem(filePath string, stripLanguage bool) string {
	base := path.Base(strings.ReplaceAll(filePath, `\`, "/"))
	stem := strings.TrimSuffix(base, path.Ext(base))
	if stripLanguage {
		stem = trailingLanguagePattern.ReplaceAllString(stem, "")
	}
	return strings.TrimSpace(nonNamePattern.ReplaceAllString(strings.ToLower(stem), " "))
}

func detectSubtitleLanguage(filePath string) string {
	label := path.Base(strings.ReplaceAll(filePath, `\`, "/"))
	simplified, traditional, otherLanguage := subtitleLabelSignals(label)
	if simplified && !traditional && !otherLanguage {
		return "zh-Hans"
	}
	if traditional && !simplified && !otherLanguage {
		return "zh-Hant"
	}

	normalized := normalizeSubtitleLabel(strings.ToLower(label))
	if containsSubtitleLabelToken(normalized, "ja", "jpn", "jp", "japanese") {
		return "ja"
	}
	if containsSubtitleLabelToken(normalized, "en", "eng", "english") {
		return "en"
	}
	return ""
}

func decimal(value string) int {
	result := 0
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0
		}
		result = result*10 + int(character-'0')
	}
	return result
}
