package domain

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestSelectSubtitleChoosesOnlyUnambiguousSimplifiedChineseSources(t *testing.T) {
	tests := []struct {
		name    string
		request SubtitleSelectionRequest
		want    SubtitlePlan
	}{
		{
			name: "external simplified ASS is normalized while other languages are ignored",
			request: SubtitleSelectionRequest{
				VideoPath: "/downloads/Show.S01E01.mkv",
				External: []SubtitleCandidate{
					{Path: "/downloads/Show.S01E01.en.srt", Format: SubtitleSRT, Language: "en"},
					{Path: "/downloads/Show.S01E01.zh-Hant.ass", Format: SubtitleASS, Language: "zh-Hant"},
					{Path: "/downloads/Show.S01E01.zh-Hans.ass", Format: SubtitleASS, Language: "zh-Hans"},
				},
			},
			want: SubtitlePlan{
				Source:      SubtitleSourceExternal,
				Action:      SubtitleActionConvert,
				InputPath:   "/downloads/Show.S01E01.zh-Hans.ass",
				StreamIndex: -1,
				InputFormat: SubtitleASS,
				Language:    "zh-Hans",
			},
		},
		{
			name: "external Chinese filename marker is converted",
			request: SubtitleSelectionRequest{
				VideoPath: "/downloads/Show.S01E02.mkv",
				External: []SubtitleCandidate{
					{Path: "/downloads/Show.S01E02.简体中文.srt", Format: SubtitleSRT},
				},
			},
			want: SubtitlePlan{
				Source:      SubtitleSourceExternal,
				Action:      SubtitleActionConvert,
				InputPath:   "/downloads/Show.S01E02.简体中文.srt",
				StreamIndex: -1,
				InputFormat: SubtitleSRT,
				Language:    "zh-Hans",
			},
		},
		{
			name: "generic Chinese language uses embedded title to select simplified track",
			request: SubtitleSelectionRequest{
				VideoPath: "/downloads/Show.S01E03.mkv",
				Embedded: []SubtitleStream{
					{Index: 1, Format: SubtitleASS, Language: "zho", Title: "繁體中文"},
					{Index: 2, Format: SubtitleASS, Language: "eng", Title: "English"},
					{Index: 4, Format: SubtitleASS, Language: "chi", Title: "简体中文"},
				},
			},
			want: SubtitlePlan{
				Source:      SubtitleSourceEmbedded,
				Action:      SubtitleActionExtract,
				InputPath:   "/downloads/Show.S01E03.mkv",
				StreamIndex: 4,
				InputFormat: SubtitleASS,
				Language:    "zh-Hans",
			},
		},
		{
			name: "common JPSC release tag identifies embedded simplified bilingual ASS",
			request: SubtitleSelectionRequest{
				VideoPath: "/downloads/[TSDM][Re Zero 4th Season][10][CHS_JP].mkv",
				Embedded: []SubtitleStream{
					{Index: 2, Format: SubtitleASS, Language: "chi", Title: "JPSC", Default: true},
				},
			},
			want: SubtitlePlan{
				Source:      SubtitleSourceEmbedded,
				Action:      SubtitleActionExtract,
				InputPath:   "/downloads/[TSDM][Re Zero 4th Season][10][CHS_JP].mkv",
				StreamIndex: 2,
				InputFormat: SubtitleASS,
				Language:    "zh-Hans",
			},
		},
		{
			name: "content analysis selects simplified generic Chinese embedded track",
			request: SubtitleSelectionRequest{
				VideoPath: "/downloads/Show.S01E04.mkv",
				Embedded: []SubtitleStream{
					{Index: 2, Format: SubtitleASS, Language: "chi", Default: true, Script: ChineseScriptSimplified},
					{Index: 3, Format: SubtitleASS, Language: "chi", Default: true, Script: ChineseScriptTraditional},
				},
			},
			want: SubtitlePlan{
				Source:      SubtitleSourceEmbedded,
				Action:      SubtitleActionExtract,
				InputPath:   "/downloads/Show.S01E04.mkv",
				StreamIndex: 2,
				InputFormat: SubtitleASS,
				Language:    "zh-Hans",
			},
		},
		{
			name: "explicit simplified language tag does not require a title",
			request: SubtitleSelectionRequest{
				VideoPath: "/downloads/Show.S01E04.mkv",
				Embedded: []SubtitleStream{
					{Index: 3, Format: SubtitleWebVTT, Language: "zh-CN", Default: true},
				},
			},
			want: SubtitlePlan{
				Source:      SubtitleSourceEmbedded,
				Action:      SubtitleActionExtract,
				InputPath:   "/downloads/Show.S01E04.mkv",
				StreamIndex: 3,
				InputFormat: SubtitleWebVTT,
				Language:    "zh-Hans",
			},
		},
		{
			name: "external simplified text remains preferred over embedded simplified track",
			request: SubtitleSelectionRequest{
				VideoPath: "/downloads/Show.S01E05.mkv",
				External: []SubtitleCandidate{
					{Path: "/downloads/Show.S01E05.chs.srt", Format: SubtitleSRT, Language: "chs"},
				},
				Embedded: []SubtitleStream{
					{Index: 5, Format: SubtitleASS, Language: "zho", Title: "简体中文", Default: true},
				},
			},
			want: SubtitlePlan{
				Source:      SubtitleSourceExternal,
				Action:      SubtitleActionConvert,
				InputPath:   "/downloads/Show.S01E05.chs.srt",
				StreamIndex: -1,
				InputFormat: SubtitleSRT,
				Language:    "zh-Hans",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SelectSubtitle(test.request)
			if err != nil {
				t.Fatalf("SelectSubtitle() error = %v", err)
			}
			if got.Evidence == "" {
				t.Fatal("SelectSubtitle() returned no language evidence")
			}
			got.Evidence = ""
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("SelectSubtitle() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSimplifiedChineseSubtitleRecognitionRequiresUnambiguousMetadata(t *testing.T) {
	tests := []struct {
		name     string
		language string
		label    string
		want     bool
	}{
		{name: "IETF simplified tag", language: "zh-Hans", want: true},
		{name: "regional simplified tag", language: "zh_CN", want: true},
		{name: "legacy simplified tag", language: "chs", want: true},
		{name: "generic Chinese with simplified title", language: "zho", label: "简体中文", want: true},
		{name: "unset language with traditional spelling of simplified title", label: "簡體中文", want: true},
		{name: "common JPSC embedded title", language: "chi", label: "JPSC", want: true},
		{name: "CHS and Japanese release marker", language: "chi", label: "[CHS_JP]", want: true},
		{name: "simplified Japanese bilingual title", language: "zho", label: "简日双语", want: true},
		{name: "simplified English bilingual title", language: "zho", label: "简英双语", want: true},
		{name: "generic Chinese without a title is ambiguous", language: "chi", want: false},
		{name: "traditional title", language: "zho", label: "繁體中文", want: false},
		{name: "mixed simplified and traditional title", language: "zho", label: "简繁中文字幕", want: false},
		{name: "non Chinese language conflicts with simplified title", language: "eng", label: "简体中文", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isSimplifiedChineseSubtitle(test.language, test.label); got != test.want {
				t.Fatalf("isSimplifiedChineseSubtitle(%q, %q) = %t, want %t", test.language, test.label, got, test.want)
			}
		})
	}
}

func TestSubtitleStreamContentInspectionRequiresAmbiguousTextMetadata(t *testing.T) {
	tests := []struct {
		name   string
		stream SubtitleStream
		want   bool
	}{
		{name: "generic Chinese ASS", stream: SubtitleStream{Format: SubtitleASS, Language: "chi"}, want: true},
		{name: "unknown language ASS", stream: SubtitleStream{Format: SubtitleASS, Language: "und", Title: "Track 1"}, want: true},
		{name: "explicit simplified", stream: SubtitleStream{Format: SubtitleASS, Language: "zh-Hans"}},
		{name: "explicit traditional title", stream: SubtitleStream{Format: SubtitleASS, Language: "zho", Title: "繁體中文"}, want: true},
		{name: "English track", stream: SubtitleStream{Format: SubtitleASS, Language: "eng"}},
		{name: "bitmap subtitle", stream: SubtitleStream{Format: SubtitlePGS, Language: "chi"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SubtitleStreamNeedsContentInspection(test.stream); got != test.want {
				t.Fatalf("SubtitleStreamNeedsContentInspection(%#v) = %t, want %t", test.stream, got, test.want)
			}
		})
	}
}

func TestSelectSubtitleReportsMissingAndBitmapOnlySimplifiedSources(t *testing.T) {
	tests := []struct {
		name    string
		request SubtitleSelectionRequest
		code    string
	}{
		{
			name:    "no subtitle",
			request: SubtitleSelectionRequest{VideoPath: "/downloads/Show.S01E04.mkv"},
			code:    "simplified_chinese_subtitle_not_found",
		},
		{
			name: "simplified PGS only",
			request: SubtitleSelectionRequest{
				VideoPath: "/downloads/Show.S01E07.mkv",
				Embedded:  []SubtitleStream{{Index: 4, Format: SubtitlePGS, Language: "zho", Title: "简体中文"}},
			},
			code: "subtitle_format_unsupported",
		},
		{
			name: "English VobSub is ignored rather than reported as simplified",
			request: SubtitleSelectionRequest{
				VideoPath: "/downloads/Show.S01E08.mkv",
				External:  []SubtitleCandidate{{Path: "/downloads/Show.S01E08.en.idx", Format: SubtitleVobSub, Language: "en"}},
			},
			code: "simplified_chinese_subtitle_not_found",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := SelectSubtitle(test.request)
			var subtitleErr *SubtitleError
			if !errors.As(err, &subtitleErr) || subtitleErr.Code != test.code {
				t.Fatalf("SelectSubtitle() error = %#v, want SubtitleError code %q", err, test.code)
			}
		})
	}
}

func TestBuildSubtitleFFmpegArgsIsExact(t *testing.T) {
	tests := []struct {
		name      string
		plan      SubtitlePlan
		videoPath string
		temporary string
		want      []string
	}{
		{
			name: "convert external SRT",
			plan: SubtitlePlan{
				Source:      SubtitleSourceExternal,
				Action:      SubtitleActionConvert,
				InputPath:   "/downloads/Show.S01E02.zh-Hans.srt",
				StreamIndex: -1,
				InputFormat: SubtitleSRT,
			},
			videoPath: "/downloads/Show.S01E02.mkv",
			temporary: "/work/.Show.S01E02.ass.part.ass",
			want: []string{
				"-y", "-i", "/downloads/Show.S01E02.zh-Hans.srt",
				"-map", "0:0", "-vn", "-an", "-c:s", "ass", "-f", "ass",
				"/work/.Show.S01E02.ass.part.ass",
			},
		},
		{
			name: "extract embedded ASS",
			plan: SubtitlePlan{
				Source:      SubtitleSourceEmbedded,
				Action:      SubtitleActionExtract,
				InputPath:   "/downloads/Show.S01E03.mkv",
				StreamIndex: 4,
				InputFormat: SubtitleASS,
			},
			videoPath: "/downloads/Show.S01E03.mkv",
			temporary: "/work/.Show.S01E03.ass.part.ass",
			want: []string{
				"-y", "-i", "/downloads/Show.S01E03.mkv",
				"-map", "0:4", "-vn", "-an", "-c:s", "ass", "-f", "ass",
				"/work/.Show.S01E03.ass.part.ass",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildSubtitleFFmpegArgs(test.plan, test.videoPath, test.temporary)
			if err != nil {
				t.Fatalf("BuildSubtitleFFmpegArgs() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("arguments = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestValidateASSRequiresScriptInfoEventsAndDialogue(t *testing.T) {
	valid := []byte("[Script Info]\nTitle: Episode 1\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:01.00,0:00:03.00,Default,Hello\n")
	if err := ValidateASS(valid); err != nil {
		t.Fatalf("ValidateASS(valid) error = %v", err)
	}

	invalid := map[string][]byte{
		"missing Script Info": []byte("[Events]\nFormat: Start, End, Text\nDialogue: 0:00:01.00,0:00:03.00,Hello\n"),
		"missing Events":      []byte("[Script Info]\nTitle: Episode 1\nDialogue: 0:00:01.00,0:00:03.00,Hello\n"),
		"missing Dialogue":    []byte("[Script Info]\nTitle: Episode 1\n[Events]\nFormat: Start, End, Text\n"),
	}
	for name, content := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := ValidateASS(content); err == nil {
				t.Fatal("ValidateASS() error = nil")
			}
		})
	}
}

func TestCandidateIDIsStableAndDistinct(t *testing.T) {
	embedded := SubtitlePlan{Source: SubtitleSourceEmbedded, StreamIndex: 2}
	firstFileID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	secondFileID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	first := SubtitlePlan{
		Source: SubtitleSourceExternal, SourceFileID: firstFileID,
		InputPath: "/downloads/first/Show.S01E01.chs.ass",
	}
	second := SubtitlePlan{
		Source: SubtitleSourceExternal, SourceFileID: secondFileID,
		InputPath: "/downloads/second/Show.S01E01.chs.ass",
	}
	if got := CandidateID(embedded); got != "stream:2" {
		t.Fatalf("embedded candidate id = %q", got)
	}
	if got := CandidateID(first); got != "external:"+firstFileID.String() {
		t.Fatalf("external candidate id = %q", got)
	}
	if CandidateID(first) == CandidateID(second) {
		t.Fatal("external files with the same basename must have distinct candidate ids")
	}
	if LegacyCandidateID(first) != "file:Show.S01E01.chs.ass" || LegacyCandidateID(first) != LegacyCandidateID(second) {
		t.Fatalf("legacy candidate ids = %q/%q", LegacyCandidateID(first), LegacyCandidateID(second))
	}
	if CandidateID(embedded) == CandidateID(first) {
		t.Fatal("embedded and external candidate ids must be distinct")
	}
}

func TestTraditionalBilingualTitlesAreCandidates(t *testing.T) {
	tests := []struct {
		label string
		want  SubtitleEvidence
	}{
		{"繁日雙語", SubtitleEvidenceTraditional},
		{"繁日双语", SubtitleEvidenceTraditional},
		{"繁英雙語", SubtitleEvidenceTraditional},
		{"简日双语", SubtitleEvidenceSimplified},
	}
	for _, test := range tests {
		t.Run(test.label, func(t *testing.T) {
			if got := subtitleMetadataEvidence("chi", test.label); got != test.want {
				t.Fatalf("subtitleMetadataEvidence(chi, %q) = %q, want %q", test.label, got, test.want)
			}
		})
	}
}
