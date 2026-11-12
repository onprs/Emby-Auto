package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSanitizeSubtitleCandidatePathHidesHostDirectories(t *testing.T) {
	tests := map[string]string{
		"Show/Season1/episode.zh-Hans.ass":         "Show/Season1/episode.zh-Hans.ass",
		`Show\Season1\episode.zh-Hans.ass`:         "Show/Season1/episode.zh-Hans.ass",
		"/data/downloads/show/episode.zh-Hans.ass": "episode.zh-Hans.ass",
		`C:\downloads\show\episode.zh-Hans.ass`:    "episode.zh-Hans.ass",
		"//server/share/show/episode.zh-Hans.ass":  "episode.zh-Hans.ass",
		"../outside/episode.zh-Hans.ass":           "episode.zh-Hans.ass",
		"episode.zh-Hans.ass":                      "episode.zh-Hans.ass",
		"":                                         "",
	}
	for input, want := range tests {
		if got := sanitizeSubtitleCandidatePath(input); got != want {
			t.Fatalf("sanitizeSubtitleCandidatePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSanitizeSubtitleCandidateTitleUsesFileNameForHostPaths(t *testing.T) {
	if got := sanitizeSubtitleCandidateTitle("/data/downloads/show/episode.zh-Hans.ass", "episode.zh-Hans.ass"); got != "episode.zh-Hans.ass" {
		t.Fatalf("absolute title = %q", got)
	}
	if got := sanitizeSubtitleCandidateTitle("简体中文", "Show/episode.zh-Hans.ass"); got != "简体中文" {
		t.Fatalf("plain title = %q", got)
	}
	if got := sanitizeSubtitleCandidateTitle("", "Show/episode.zh-Hans.ass"); got != "episode.zh-Hans.ass" {
		t.Fatalf("empty title = %q", got)
	}
}

func TestSubtitleVideoMatchResourceJSONOmitsHostPaths(t *testing.T) {
	match := &scopedSubtitleVideoMatch{
		TaskID: uuid.New(), SeriesTitle: "Show", TargetSeason: 1, TargetEpisode: 2, TargetTitle: "Episode",
		VideoPath: "/data/downloads/show/episode.mkv", SavePath: "/data/downloads",
		inspectionPaths: map[string]string{"external:1": "/data/downloads/show/episode.zh-Hans.ass"},
		Candidates: []scopedSubtitleCandidate{
			{ID: "external:1", Source: "external", Title: "episode.zh-Hans.ass", Path: "show/episode.zh-Hans.ass"},
			{ID: "stream:2", Source: "embedded", StreamIndex: 2, Title: "简体中文"},
		},
	}
	raw := string(subtitleVideoMatchResourceJSON(match))
	for _, forbidden := range []string{"videoPath", "savePath", "inspectionPaths", "/data", "episode.mkv"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("resource JSON contains %q: %s", forbidden, raw)
		}
	}
	for _, required := range []string{"show/episode.zh-Hans.ass", "episode.zh-Hans.ass", "简体中文"} {
		if !strings.Contains(raw, required) {
			t.Fatalf("resource JSON lost candidate data %q: %s", required, raw)
		}
	}
}
