package service

import "testing"

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
