package service

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
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

// TestAgentSubtitleMatchContextOmitsVideoPath 验证宿主路径只进入内部指纹：
// 源视频路径或候选原始路径变化必须改变 fingerprint，但脱敏 Resource 不变。
func TestAgentSubtitleMatchContextOmitsVideoPath(t *testing.T) {
	savePath := "/data/downloads/show"
	episodeTitle := "Episode"
	season, episode := int32(1), int32(2)
	row := db.GetSubtitleVideoMatchContextRow{
		TaskID: repository.UUIDToPG(uuid.New()), SeriesTitle: "Show",
		TargetSeasonNumber: &season, TargetEpisodeNumber: &episode, TargetEpisodeTitle: &episodeTitle,
		SourceVideoRelativePath: "Show.S01E01.mkv", SavePath: &savePath, TaskVersion: 7,
	}
	candidatePath := "/data/downloads/show/episode.zh-Hans.ass"
	candidateTitle := "episode.zh-Hans.ass"
	streamIndex := int32(2)
	candidateFormat, candidateLanguage := "ass", "chi"
	embeddedTitle := "简日双语"
	candidateRows := []db.ListSubtitleVideoMatchCandidatesRow{
		{CandidateID: "external:1", Source: "external", Path: &candidatePath, Title: &candidateTitle},
		{CandidateID: "stream:2", Source: "embedded", StreamIndex: &streamIndex, Format: &candidateFormat, Language: &candidateLanguage, Title: &embeddedTitle},
	}
	match, resourceJSON, err := subtitleVideoMatchContent(row, candidateRows)
	if err != nil {
		t.Fatalf("subtitleVideoMatchContent() error = %v", err)
	}
	for _, forbidden := range []string{"videoPath", "savePath", "inspectionPaths", "/data/downloads", "Show.S01E01.mkv"} {
		if strings.Contains(string(resourceJSON), forbidden) {
			t.Fatalf("resource JSON contains %q: %s", forbidden, resourceJSON)
		}
	}
	baseline := sha256.Sum256(subtitleVideoMatchFingerprintInput(match, resourceJSON, row.TaskVersion))

	changedSavePath := "/other/downloads/show"
	changedVideoRow := row
	changedVideoRow.SavePath = &changedSavePath
	videoMatch, videoResource, err := subtitleVideoMatchContent(changedVideoRow, candidateRows)
	if err != nil {
		t.Fatalf("subtitleVideoMatchContent(changed video) error = %v", err)
	}
	if string(videoResource) != string(resourceJSON) {
		t.Fatalf("source video path leaked into resource JSON: %s", videoResource)
	}
	if sha256.Sum256(subtitleVideoMatchFingerprintInput(videoMatch, videoResource, row.TaskVersion)) == baseline {
		t.Fatal("source video path change did not change fingerprint")
	}

	changedCandidatePath := "/elsewhere/cache/episode.zh-Hans.ass"
	changedCandidateRows := []db.ListSubtitleVideoMatchCandidatesRow{
		{CandidateID: "external:1", Source: "external", Path: &changedCandidatePath, Title: &candidateTitle},
		{CandidateID: "stream:2", Source: "embedded", StreamIndex: &streamIndex, Format: &candidateFormat, Language: &candidateLanguage, Title: &embeddedTitle},
	}
	candidateMatch, candidateResource, err := subtitleVideoMatchContent(row, changedCandidateRows)
	if err != nil {
		t.Fatalf("subtitleVideoMatchContent(changed candidate) error = %v", err)
	}
	if string(candidateResource) != string(resourceJSON) {
		t.Fatalf("raw candidate path leaked into resource JSON: %s", candidateResource)
	}
	if sha256.Sum256(subtitleVideoMatchFingerprintInput(candidateMatch, candidateResource, row.TaskVersion)) == baseline {
		t.Fatal("raw candidate path change did not change fingerprint")
	}
}
