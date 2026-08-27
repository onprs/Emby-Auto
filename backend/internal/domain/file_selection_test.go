package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestSelectDownloadFilesSplitsSeasonPackAndChoosesLargestMainVideo(t *testing.T) {
	files := []DownloadFile{
		{Index: 0, RelativePath: "Show/Show - 01.mkv", SizeBytes: 1_000},
		{Index: 1, RelativePath: "Show/Show - 01 [1080p].mp4", SizeBytes: 2_000},
		{Index: 2, RelativePath: "Show/Show - 02.mkv", SizeBytes: 1_100},
		{Index: 3, RelativePath: "Show/Show NCED 02.mkv", SizeBytes: 3_000},
		{Index: 4, RelativePath: "Show/PV/Show PV 03.mkv", SizeBytes: 4_000},
		{Index: 5, RelativePath: "Show/sample.mkv", SizeBytes: 5_000},
		{Index: 6, RelativePath: "Show/readme.txt", SizeBytes: 9_999},
	}

	result, err := SelectDownloadFiles(files, FileSelectionOptions{DefaultSeason: 1})
	if err != nil {
		t.Fatalf("SelectDownloadFiles() error = %v", err)
	}
	if len(result.Episodes) != 2 {
		t.Fatalf("episode count = %d, want 2", len(result.Episodes))
	}
	if got := result.Episodes[0]; got.SourceSeason != 1 || got.SourceEpisode != 1 || got.Video.Index != 1 {
		t.Fatalf("episode 1 = %#v, want season 1 episode 1 file index 1", got)
	}
	if got := result.Episodes[1]; got.SourceSeason != 1 || got.SourceEpisode != 2 || got.Video.Index != 2 {
		t.Fatalf("episode 2 = %#v, want season 1 episode 2 file index 2", got)
	}

	wantKinds := []MediaKind{MediaVideo, MediaVideo, MediaVideo, MediaExtra, MediaExtra, MediaExtra, MediaOther}
	wantSelected := []bool{false, true, true, false, false, false, false}
	for index, got := range result.Files {
		if got.Kind != wantKinds[index] || got.Selected != wantSelected[index] {
			t.Fatalf("classified file %d = kind %q selected %t, want %q/%t", index, got.Kind, got.Selected, wantKinds[index], wantSelected[index])
		}
	}
}

func TestSelectDownloadFilesKeepsBoundedChineseSubtitleCandidates(t *testing.T) {
	files := []DownloadFile{
		{Index: 10, RelativePath: "Season 2/Show.S02E01.mkv", SizeBytes: 2_000},
		{Index: 11, RelativePath: "Season 2/Show.S02E01.zh-Hans.ass", SizeBytes: 80},
		{Index: 12, RelativePath: "Season 2/Show.S02E01.zh-Hant.ass", SizeBytes: 80},
		{Index: 13, RelativePath: "Season 2/subs/Show.S02E01.en.srt", SizeBytes: 90},
		{Index: 14, RelativePath: "Season 2/Show.S02E02.mkv", SizeBytes: 2_100},
		{Index: 15, RelativePath: "Season 2/Show.S02E02.繁體中文.srt", SizeBytes: 70},
		{Index: 16, RelativePath: "Season 2/Show.S02E02.简繁中文字幕.ass", SizeBytes: 80},
		{Index: 17, RelativePath: "Season 2/Show.S02E03.mkv", SizeBytes: 2_200},
		{Index: 18, RelativePath: "Season 2/Show.S02E03.简体中文.srt", SizeBytes: 75},
	}

	result, err := SelectDownloadFiles(files, FileSelectionOptions{DefaultSeason: 1})
	if err != nil {
		t.Fatalf("SelectDownloadFiles() error = %v", err)
	}
	if len(result.Episodes) != 3 {
		t.Fatalf("episode count = %d, want 3", len(result.Episodes))
	}
	if got := result.Episodes[0]; got.SourceSeason != 2 || got.SourceEpisode != 1 || got.Subtitle == nil || got.Subtitle.Index != 11 || len(got.Subtitles) != 2 || got.Subtitles[1].Index != 12 {
		t.Fatalf("episode 1 pairing = %#v, want simplified then traditional candidates", got)
	}
	if got := result.Episodes[1]; got.SourceSeason != 2 || got.SourceEpisode != 2 || got.Subtitle == nil || got.Subtitle.Index != 16 || len(got.Subtitles) != 2 || got.Subtitles[1].Index != 15 {
		t.Fatalf("episode 2 pairing = %#v, want mixed then traditional candidates", got)
	}
	if got := result.Episodes[2]; got.SourceSeason != 2 || got.SourceEpisode != 3 || got.Subtitle == nil || got.Subtitle.Index != 18 || len(got.Subtitles) != 1 {
		t.Fatalf("episode 3 pairing = %#v, want S02E03 simplified subtitle index 18", got)
	}
	if result.Files[1].Language != "zh-Hans" || result.Files[2].Language != "zh-Hant" || result.Files[3].Language != "en" || result.Files[5].Language != "zh-Hant" || result.Files[6].Language != "" || result.Files[8].Language != "zh-Hans" {
		t.Fatalf("subtitle language classification = %#v", result.Files)
	}
	for _, index := range []int{1, 2, 5, 6, 8} {
		if !result.Files[index].Selected {
			t.Fatalf("usable Chinese subtitle file index %d was not selected", result.Files[index].Index)
		}
	}
	if result.Files[3].Selected {
		t.Fatalf("English subtitle file index %d was selected", result.Files[3].Index)
	}
}

func TestSelectDownloadFilesCapsExternalSubtitleCandidates(t *testing.T) {
	files := []DownloadFile{{Index: 0, RelativePath: "Show.S01E01.mkv", SizeBytes: 2_000}}
	for index := 1; index <= 10; index++ {
		files = append(files, DownloadFile{
			Index: index, RelativePath: fmt.Sprintf("Show.S01E01.track%02d.ass", index), SizeBytes: 100,
		})
	}
	result, err := SelectDownloadFiles(files, FileSelectionOptions{DefaultSeason: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Episodes) != 1 || len(result.Episodes[0].Subtitles) != maxExternalSubtitleCandidates {
		t.Fatalf("selection = %#v", result.Episodes)
	}
	selected := 0
	for _, file := range result.Files {
		if file.Selected {
			selected++
		}
	}
	if selected != 1+maxExternalSubtitleCandidates {
		t.Fatalf("selected files = %d, want %d", selected, 1+maxExternalSubtitleCandidates)
	}
}

func TestSelectDownloadFilesUsesRequestedCoordinatesForSingleEpisode(t *testing.T) {
	files := []DownloadFile{
		{Index: 0, RelativePath: "release/video.mkv", SizeBytes: 3_000},
		{Index: 1, RelativePath: "release/video.简中.ass", SizeBytes: 100},
	}

	result, err := SelectDownloadFiles(files, FileSelectionOptions{
		DefaultSeason:  3,
		DefaultEpisode: 7,
		SingleEpisode:  true,
	})
	if err != nil {
		t.Fatalf("SelectDownloadFiles() error = %v", err)
	}
	if len(result.Episodes) != 1 {
		t.Fatalf("episode count = %d, want 1", len(result.Episodes))
	}
	got := result.Episodes[0]
	if got.SourceSeason != 3 || got.SourceEpisode != 7 || got.Video.Index != 0 || got.Subtitle == nil || got.Subtitle.Index != 1 {
		t.Fatalf("single episode = %#v, want S03E07 video 0 subtitle 1", got)
	}
}

func TestParseSourceCoordinateRecognizesSupportedFormsWithoutUsingResolution(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		defaultSeason int
		wantSeason    int
		wantEpisode   int
		wantFraction  int
	}{
		{name: "season episode token", path: "Show.S01E12.1080p.mkv", defaultSeason: 4, wantSeason: 1, wantEpisode: 12},
		{name: "season episode decimal", path: "Show.S01E12.5.1080p.mkv", defaultSeason: 4, wantSeason: 1, wantEpisode: 12, wantFraction: 50},
		{name: "dash episode", path: "[Group] Show - 02 [1080p].mkv", defaultSeason: 1, wantSeason: 1, wantEpisode: 2},
		{name: "dash decimal episode", path: "[Group] Show - 14.5 [1080p].mkv", defaultSeason: 1, wantSeason: 1, wantEpisode: 14, wantFraction: 50},
		{name: "bracketed decimal episode", path: "[Synthetic-Group][Sample Show][12.5][Special][1080P].mkv", defaultSeason: 1, wantSeason: 1, wantEpisode: 12, wantFraction: 50},
		{name: "uppercase revision", path: "[Group] Show [02V2] [1080p].mkv", defaultSeason: 1, wantSeason: 1, wantEpisode: 2},
		{name: "east asian episode", path: "Show 第03話.mkv", defaultSeason: 1, wantSeason: 1, wantEpisode: 3},
		{name: "east asian decimal episode", path: "Show 第12.5话.mkv", defaultSeason: 1, wantSeason: 1, wantEpisode: 12, wantFraction: 50},
		{name: "season directory", path: "Season 2/Show - 04.mkv", defaultSeason: 1, wantSeason: 2, wantEpisode: 4},
		{name: "episode token", path: "Show EP29 WEB-DL.mkv", defaultSeason: 1, wantSeason: 1, wantEpisode: 29},
		{
			name:          "ordinal season does not replace bracketed episode",
			path:          "[TSDM][Re Zero kara Hajimeru Isekai Seikatsu 4th Season][10][Webrip][HEVC-10bit 1080p AAC][CHS_JP].mkv",
			defaultSeason: 4,
			wantSeason:    4,
			wantEpisode:   10,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinate, ok := ParseSourceCoordinate(test.path, test.defaultSeason)
			if !ok || coordinate.Season != test.wantSeason || coordinate.Episode != test.wantEpisode ||
				coordinate.EpisodeFractionHundredths != test.wantFraction {
				t.Fatalf(
					"ParseSourceCoordinate(%q, %d) = %#v/%t, want S%dE%d fraction %d/true",
					test.path, test.defaultSeason, coordinate, ok, test.wantSeason, test.wantEpisode, test.wantFraction,
				)
			}
		})
	}

	if coordinate, ok := ParseSourceCoordinate("Show 1080p 2026.mkv", 1); ok || coordinate != (EpisodeCoordinate{}) {
		t.Fatalf("resolution-only filename parsed as %#v/%t, want no coordinate", coordinate, ok)
	}
}

func TestRecoverLegacyFractionalSourceCoordinateRequiresExactCollapsedIdentity(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		persisted  EpisodeCoordinate
		want       EpisodeCoordinate
		wantChange bool
	}{
		{
			name: "single digit fraction", path: "Sample.Show.12.5.mkv",
			persisted: EpisodeCoordinate{Season: 1, Episode: 125},
			want:      EpisodeCoordinate{Season: 1, Episode: 12, EpisodeFractionHundredths: 50}, wantChange: true,
		},
		{
			name: "two digit fraction", path: "Sample.Show.12.05.mkv",
			persisted: EpisodeCoordinate{Season: 1, Episode: 125},
			want:      EpisodeCoordinate{Season: 1, Episode: 12, EpisodeFractionHundredths: 5}, wantChange: true,
		},
		{
			name: "integer identity", path: "Sample.Show.125.mkv",
			persisted: EpisodeCoordinate{Season: 1, Episode: 125},
			want:      EpisodeCoordinate{Season: 1, Episode: 125},
		},
		{
			name: "already structured", path: "Sample.Show.12.5.mkv",
			persisted: EpisodeCoordinate{Season: 1, Episode: 12, EpisodeFractionHundredths: 50},
			want:      EpisodeCoordinate{Season: 1, Episode: 12, EpisodeFractionHundredths: 50},
		},
		{
			name: "manual coordinate does not match legacy collapse", path: "Sample.Show.12.5.mkv",
			persisted: EpisodeCoordinate{Season: 1, Episode: 12},
			want:      EpisodeCoordinate{Season: 1, Episode: 12},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, changed := RecoverLegacyFractionalSourceCoordinate(test.path, test.persisted)
			if got != test.want || changed != test.wantChange {
				t.Fatalf("RecoverLegacyFractionalSourceCoordinate() = %#v/%t, want %#v/%t", got, changed, test.want, test.wantChange)
			}
		})
	}
}

func TestSelectDownloadFilesRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	tests := []struct {
		name  string
		files []DownloadFile
		want  error
	}{
		{
			name:  "parent traversal",
			files: []DownloadFile{{Index: 0, RelativePath: "../Show - 01.mkv", SizeBytes: 100}},
			want:  ErrUnsafeDownloadPath,
		},
		{
			name: "duplicate qB index",
			files: []DownloadFile{
				{Index: 2, RelativePath: "Show - 01.mkv", SizeBytes: 100},
				{Index: 2, RelativePath: "Show - 02.mkv", SizeBytes: 100},
			},
			want: ErrDuplicateDownloadFile,
		},
		{
			name:  "no main video",
			files: []DownloadFile{{Index: 0, RelativePath: "Show NCOP.mkv", SizeBytes: 100}},
			want:  ErrNoMainVideo,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := SelectDownloadFiles(test.files, FileSelectionOptions{DefaultSeason: 1})
			if !errors.Is(err, test.want) {
				t.Fatalf("SelectDownloadFiles() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSelectDownloadFilesPairsBilingualSubtitlesAndIgnoresCommentaryExtras(t *testing.T) {
	files := []DownloadFile{
		{Index: 0, RelativePath: "Pack/谈话/[Group][Talk][01].sc.ass", SizeBytes: 50},
		{Index: 1, RelativePath: "Pack/谈话/[Group][Talk][01].mkv", SizeBytes: 500},
		{Index: 2, RelativePath: "Pack/[Group][01][1080P].mkv", SizeBytes: 2_000},
		{Index: 3, RelativePath: "Pack/[Group][01][1080P].scjp.ass", SizeBytes: 80},
		{Index: 4, RelativePath: "Pack/[Group][01][1080P].tcjp.ass", SizeBytes: 80},
		{Index: 5, RelativePath: "Pack/[Group][12][1080P].mkv", SizeBytes: 2_100},
		{Index: 6, RelativePath: "Pack/[Group][12][1080P].scjp.ass", SizeBytes: 85},
		{Index: 7, RelativePath: "Pack/[Group][12.5][Special][1080P].mkv", SizeBytes: 2_150},
		{Index: 8, RelativePath: "Pack/[Group][12.5][Special][1080P].scjp.ass", SizeBytes: 85},
	}

	result, err := SelectDownloadFiles(files, FileSelectionOptions{DefaultSeason: 1})
	if err != nil {
		t.Fatalf("SelectDownloadFiles() error = %v", err)
	}
	if len(result.Episodes) != 3 {
		t.Fatalf("episode count = %d, want 3", len(result.Episodes))
	}

	// Episode 1
	ep1 := result.Episodes[0]
	if ep1.SourceSeason != 1 || ep1.SourceEpisode != 1 || ep1.Video.Index != 2 || ep1.Subtitle == nil || ep1.Subtitle.Index != 3 {
		t.Fatalf("episode 1 = %#v, want video 2, subtitle 3 (scjp)", ep1)
	}
	// Episode 12
	ep12 := result.Episodes[1]
	if ep12.SourceSeason != 1 || ep12.SourceEpisode != 12 || ep12.Video.Index != 5 || ep12.Subtitle == nil || ep12.Subtitle.Index != 6 {
		t.Fatalf("episode 12 = %#v, want video 5, subtitle 6", ep12)
	}
	// Episode 12.5 (distinct from 12)
	ep125 := result.Episodes[2]
	if ep125.SourceSeason != 1 || ep125.SourceEpisode != 12 || ep125.SourceEpisodeFractionHundredths != 50 || ep125.Video.Index != 7 || ep125.Subtitle == nil || ep125.Subtitle.Index != 8 {
		t.Fatalf("episode 12.5 = %#v, want video 7, subtitle 8", ep125)
	}

	if result.Files[0].Kind != MediaExtra || result.Files[1].Kind != MediaExtra {
		t.Fatalf("talk files kind = %q / %q, want extra", result.Files[0].Kind, result.Files[1].Kind)
	}
	if result.Files[3].Language != "zh-Hans" || result.Files[4].Language != "zh-Hant" {
		t.Fatalf("bilingual subtitle languages = %q / %q, want zh-Hans / zh-Hant", result.Files[3].Language, result.Files[4].Language)
	}
}
