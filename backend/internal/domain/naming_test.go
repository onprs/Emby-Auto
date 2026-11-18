package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildMovieFileNamesUsesCanonicalNameYearAndProfileExtension(t *testing.T) {
	got, err := BuildMovieFileNames(MovieNamingRequest{
		MovieTitle: "流浪地球", ReleaseYear: 2019, VideoExtension: "mkv",
	})
	if err != nil {
		t.Fatalf("BuildMovieFileNames() error = %v", err)
	}
	want := EpisodeFileNames{
		BaseName: "流浪地球(2019)", VideoName: "流浪地球(2019).mkv", SubtitleName: "流浪地球(2019).ass",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildMovieFileNames() = %#v, want %#v", got, want)
	}
}

func TestBuildMovieFileNamesSanitizesTitleAndRejectsInvalidMetadata(t *testing.T) {
	got, err := BuildMovieFileNames(MovieNamingRequest{MovieTitle: "Movie: One/Two", ReleaseYear: 2024, VideoExtension: "webm"})
	if err != nil || got.BaseName != "Movie One Two(2024)" {
		t.Fatalf("BuildMovieFileNames() = %#v, %v", got, err)
	}
	for _, request := range []MovieNamingRequest{
		{MovieTitle: "", ReleaseYear: 2024, VideoExtension: "mp4"},
		{MovieTitle: "Movie", ReleaseYear: 0, VideoExtension: "mp4"},
		{MovieTitle: "Movie", ReleaseYear: 2024, VideoExtension: ".mp4"},
	} {
		if _, err := BuildMovieFileNames(request); err == nil {
			t.Fatalf("BuildMovieFileNames(%#v) error = nil", request)
		}
	}
}

func TestBuildEpisodeFileNamesUsesCanonicalTMDbNameAndProfileExtension(t *testing.T) {
	got, err := BuildEpisodeFileNames(EpisodeNamingRequest{
		SeriesTitle:    "番剧名",
		Season:         2,
		Episode:        1,
		EpisodeTitle:   "集名称",
		VideoExtension: "mkv",
	})
	if err != nil {
		t.Fatalf("BuildEpisodeFileNames() error = %v", err)
	}
	want := EpisodeFileNames{
		BaseName:     "番剧名 - S02E01 - 集名称",
		VideoName:    "番剧名 - S02E01 - 集名称.mkv",
		SubtitleName: "番剧名 - S02E01 - 集名称.ass",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildEpisodeFileNames() = %#v, want %#v", got, want)
	}
}

func TestBuildEpisodeFileNamesKeepsVideoAndSubtitleBasenamesEqual(t *testing.T) {
	got, err := BuildEpisodeFileNames(EpisodeNamingRequest{
		SeriesTitle:    "Canonical/Series: 2026",
		Season:         1,
		Episode:        12,
		EpisodeTitle:   "Finale? \"Home\"",
		VideoExtension: "mp4",
	})
	if err != nil {
		t.Fatalf("BuildEpisodeFileNames() error = %v", err)
	}
	want := EpisodeFileNames{
		BaseName:     "Canonical Series 2026 - S01E12 - Finale Home",
		VideoName:    "Canonical Series 2026 - S01E12 - Finale Home.mp4",
		SubtitleName: "Canonical Series 2026 - S01E12 - Finale Home.ass",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildEpisodeFileNames() = %#v, want %#v", got, want)
	}
}

func TestBuildEpisodeFileNamesRejectsMissingTMDbTitleAndInvalidExtension(t *testing.T) {
	tests := []struct {
		name    string
		request EpisodeNamingRequest
		field   string
		code    string
	}{
		{
			name: "missing episode title",
			request: EpisodeNamingRequest{
				SeriesTitle: "Canonical Series", Season: 1, Episode: 1, VideoExtension: "mkv",
			},
			field: "episodeTitle",
			code:  "mapping_title_missing",
		},
		{
			name: "extension contains a dot",
			request: EpisodeNamingRequest{
				SeriesTitle: "Canonical Series", Season: 1, Episode: 1, EpisodeTitle: "Pilot", VideoExtension: ".mkv",
			},
			field: "videoExtension",
			code:  "invalid_media_name",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildEpisodeFileNames(test.request)
			var namingErr *EpisodeNamingError
			if !errors.As(err, &namingErr) || namingErr.Field != test.field || namingErr.Code != test.code {
				t.Fatalf("BuildEpisodeFileNames() error = %#v, want code %q field %q", err, test.code, test.field)
			}
		})
	}
}

func TestBuildEpisodeFileNamesCapsLongBaseNameAtUTF8Boundary(t *testing.T) {
	names, err := BuildEpisodeFileNames(EpisodeNamingRequest{
		SeriesTitle:    strings.Repeat("剧", 40),
		Season:         1,
		Episode:        1,
		EpisodeTitle:   strings.Repeat("集", 30),
		VideoExtension: "mkv",
	})
	if err != nil {
		t.Fatalf("BuildEpisodeFileNames() error = %v", err)
	}
	if len(names.BaseName) > maxMediaBaseNameBytes {
		t.Fatalf("baseName bytes = %d, want at most %d", len(names.BaseName), maxMediaBaseNameBytes)
	}
	if !utf8.ValidString(names.BaseName) || strings.ContainsRune(names.BaseName, utf8.RuneError) {
		t.Fatalf("baseName breaks UTF-8: %q", names.BaseName)
	}
	if !strings.Contains(names.BaseName, " - S01E01 - ") {
		t.Fatalf("baseName lost the episode marker: %q", names.BaseName)
	}
	if names.VideoName != names.BaseName+".mkv" || names.SubtitleName != names.BaseName+".ass" {
		t.Fatalf("derived names = %q %q", names.VideoName, names.SubtitleName)
	}
	// 入库临时名额外追加 54 字节（. + operation UUID + .import.part + 扩展名）。
	if len(names.BaseName)+54 > 255 {
		t.Fatalf("temporary name bytes = %d exceed NAME_MAX", len(names.BaseName)+54)
	}
}

func TestBuildEpisodeFileNamesKeepsTruncatedNamesUnique(t *testing.T) {
	prefix := strings.Repeat("同一部很长的剧集名称", 16)
	first, err := BuildEpisodeFileNames(EpisodeNamingRequest{
		SeriesTitle: prefix, Season: 1, Episode: 1, EpisodeTitle: strings.Repeat("相同标题前缀", 12) + "甲", VideoExtension: "mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildEpisodeFileNames(EpisodeNamingRequest{
		SeriesTitle: prefix, Season: 1, Episode: 1, EpisodeTitle: strings.Repeat("相同标题前缀", 12) + "乙", VideoExtension: "mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseName == second.BaseName {
		t.Fatalf("truncated names collide: %q", first.BaseName)
	}
	for _, name := range []string{first.BaseName, second.BaseName} {
		if len(name) > maxMediaBaseNameBytes || !utf8.ValidString(name) {
			t.Fatalf("capped name is invalid: bytes=%d valid=%v", len(name), utf8.ValidString(name))
		}
		if !strings.Contains(name, " - S01E01 - ") {
			t.Fatalf("capped name lost the episode marker: %q", name)
		}
	}
}

func TestBuildMovieFileNamesCapsLongTitleAndKeepsReleaseYear(t *testing.T) {
	names, err := BuildMovieFileNames(MovieNamingRequest{
		MovieTitle: strings.Repeat("长电影标题", 30), ReleaseYear: 2019, VideoExtension: "mkv",
	})
	if err != nil {
		t.Fatalf("BuildMovieFileNames() error = %v", err)
	}
	if len(names.BaseName) > maxMediaBaseNameBytes {
		t.Fatalf("baseName bytes = %d, want at most %d", len(names.BaseName), maxMediaBaseNameBytes)
	}
	if !utf8.ValidString(names.BaseName) || !strings.HasSuffix(names.BaseName, "(2019)") {
		t.Fatalf("baseName = %q, want valid UTF-8 with release year", names.BaseName)
	}
	if len(names.BaseName)+54 > 255 {
		t.Fatalf("temporary name bytes = %d exceed NAME_MAX", len(names.BaseName)+54)
	}
}
