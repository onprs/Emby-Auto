package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var mediaExtensionPattern = regexp.MustCompile(`^[a-z0-9]+$`)

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
	baseName := fmt.Sprintf("%s(%04d)", movieTitle, request.ReleaseYear)
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

	baseName := fmt.Sprintf(
		"%s - S%02dE%02d - %s",
		seriesTitle,
		request.Season,
		request.Episode,
		episodeTitle,
	)
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

func invalidEpisodeName(code, field, reason string) *EpisodeNamingError {
	return &EpisodeNamingError{Code: code, Field: field, Reason: reason}
}
