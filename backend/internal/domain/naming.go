package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var mediaExtensionPattern = regexp.MustCompile(`^[a-z0-9]+$`)

// maxMediaBaseNameBytes 限制规范化文件名（不含扩展名）的 UTF-8 字节数。
// 预留额度供 .ass/.mp4 等扩展名与转码、入库临时文件追加的 operation ID 后缀使用，
// 使正式名与临时名都留在常见文件系统的 255 字节上限内。
const maxMediaBaseNameBytes = 180

// minMediaNamePartBytes 保证剧集标题截断后仍保留可辨识内容与短哈希后缀。
const minMediaNamePartBytes = 24

type EpisodeNamingRequest struct {
	SeriesTitle    string
	Season         int
	Episode        int
	EpisodeTitle   string
	VideoExtension string
}

type MovieNamingRequest struct {
	MovieTitle     string
	ReleaseYear    int
	VideoExtension string
}

type EpisodeFileNames struct {
	BaseName     string
	VideoName    string
	SubtitleName string
}

type EpisodeNamingError struct {
	Code   string
	Field  string
	Reason string
}

func (err *EpisodeNamingError) Error() string {
	return fmt.Sprintf("cannot build episode filename: %s %s", err.Field, err.Reason)
}

func BuildMovieFileNames(request MovieNamingRequest) (EpisodeFileNames, error) {
	movieTitle := sanitizeMediaNamePart(request.MovieTitle)
	if movieTitle == "" {
		return EpisodeFileNames{}, invalidEpisodeName("invalid_media_name", "movieTitle", "must not be blank")
	}
	if request.ReleaseYear < 1870 || request.ReleaseYear > 9999 {
		return EpisodeFileNames{}, invalidEpisodeName("invalid_media_name", "releaseYear", "must be between 1870 and 9999")
	}
	if !mediaExtensionPattern.MatchString(request.VideoExtension) {
		return EpisodeFileNames{}, invalidEpisodeName("invalid_media_name", "videoExtension", "must contain lowercase letters and digits without a dot")
	}
	yearSuffix := fmt.Sprintf("(%04d)", request.ReleaseYear)
	movieTitle = truncateMediaNamePart(movieTitle, maxMediaBaseNameBytes-len(yearSuffix))
	baseName := fitMediaBaseName(movieTitle + yearSuffix)
	return EpisodeFileNames{
		BaseName: baseName, VideoName: baseName + "." + request.VideoExtension, SubtitleName: baseName + ".ass",
	}, nil
}

func BuildEpisodeFileNames(request EpisodeNamingRequest) (EpisodeFileNames, error) {
	seriesTitle := sanitizeMediaNamePart(request.SeriesTitle)
	if seriesTitle == "" {
		return EpisodeFileNames{}, invalidEpisodeName("invalid_media_name", "seriesTitle", "must not be blank")
	}
	if request.Season < 0 {
		return EpisodeFileNames{}, invalidEpisodeName("invalid_media_name", "season", "must be nonnegative")
	}
	if request.Episode <= 0 {
		return EpisodeFileNames{}, invalidEpisodeName("invalid_media_name", "episode", "must be positive")
	}
	if strings.TrimSpace(request.EpisodeTitle) == "" {
		return EpisodeFileNames{}, invalidEpisodeName("mapping_title_missing", "episodeTitle", "must contain the TMDb episode title")
	}
	episodeTitle := sanitizeMediaNamePart(request.EpisodeTitle)
	if episodeTitle == "" {
		return EpisodeFileNames{}, invalidEpisodeName("invalid_media_name", "episodeTitle", "contains no usable filename characters")
	}
	if !mediaExtensionPattern.MatchString(request.VideoExtension) {
		return EpisodeFileNames{}, invalidEpisodeName("invalid_media_name", "videoExtension", "must contain lowercase letters and digits without a dot")
	}

	baseName := fitMediaBaseName(episodeBaseName(seriesTitle, request.Season, request.Episode, episodeTitle))
	return EpisodeFileNames{
		BaseName:     baseName,
		VideoName:    baseName + "." + request.VideoExtension,
		SubtitleName: baseName + ".ass",
	}, nil
}

func sanitizeMediaNamePart(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		if unicode.IsControl(character) || strings.ContainsRune(`<>:"/\|?*`, character) {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(character)
	}
	return strings.Trim(strings.Join(strings.Fields(builder.String()), " "), " .")
}

// episodeBaseName 在总字节上限内组合剧集名、季集标记与标题。
// 优先保留 Emby 识别所需的“剧集名 - SxxEyy - ”结构，超长时分别截断剧集名和标题。
func episodeBaseName(seriesTitle string, season, episode int, episodeTitle string) string {
	marker := fmt.Sprintf(" - S%02dE%02d - ", season, episode)
	seriesBudget := maxMediaBaseNameBytes - len(marker) - minMediaNamePartBytes
	if seriesBudget < minMediaNamePartBytes {
		seriesBudget = minMediaNamePartBytes
	}
	seriesTitle = truncateMediaNamePart(seriesTitle, seriesBudget)
	episodeBudget := maxMediaBaseNameBytes - len(marker) - len(seriesTitle)
	if episodeBudget < minMediaNamePartBytes {
		episodeBudget = minMediaNamePartBytes
	}
	episodeTitle = truncateMediaNamePart(episodeTitle, episodeBudget)
	return seriesTitle + marker + episodeTitle
}

// fitMediaBaseName 是文件名长度不变量的最终保障。
func fitMediaBaseName(baseName string) string {
	if len(baseName) <= maxMediaBaseNameBytes {
		return baseName
	}
	return truncateMediaNamePart(baseName, maxMediaBaseNameBytes)
}

// truncateMediaNamePart 在 UTF-8 边界内截断过长的名称片段，
// 并追加原值短哈希，避免截断后出现同名覆盖。
func truncateMediaNamePart(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	suffix := "-" + mediaNameDigest(value)
	budget := limit - len(suffix)
	if budget < 1 {
		return mediaNameDigest(value)
	}
	var builder strings.Builder
	builder.Grow(budget)
	for _, character := range value {
		if builder.Len()+utf8.RuneLen(character) > budget {
			break
		}
		builder.WriteRune(character)
	}
	trimmed := strings.TrimRight(builder.String(), " .")
	if trimmed == "" {
		return mediaNameDigest(value)
	}
	return trimmed + suffix
}

func mediaNameDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func invalidEpisodeName(code, field, reason string) *EpisodeNamingError {
	return &EpisodeNamingError{Code: code, Field: field, Reason: reason}
}
