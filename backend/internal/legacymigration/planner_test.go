package legacymigration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestRuntimeJSONSourceInventoriesAndMergesWithoutDroppingHistory(t *testing.T) {
	directory := t.TempDir()
	writeJSONFixture(t, directory, "tasks.json", []map[string]any{{
		"task_id": "task-1", "canonical_series": "Series", "history": []map[string]any{{"event": "created"}}, "unknown_field": "preserved",
	}})
	writeJSONFixture(t, directory, "watch_queue.json", []map[string]any{
		{"task_id": "task-1", "canonical_series": "duplicate"},
		{"task_id": "task-2", "canonical_series": "queue-only"},
	})
	writeJSONFixture(t, directory, "rss_drafts.json", []map[string]any{{"draft_id": "rss-1"}})
	writeJSONFixture(t, directory, "intake_drafts.json", []map[string]any{
		{"draft_id": "intake-1", "runtime_task": map[string]any{"task_id": "task-1"}, "tmdb": map[string]any{"tmdb_id": 42}},
		{"draft_id": "intake-2"},
	})
	writeJSONFixture(t, directory, "deleted_tasks.json", []map[string]any{{"task_id": "deleted-1"}})

	snapshot, err := (RuntimeJSONSource{Directory: directory}).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := len(snapshot.Records); got != 2 {
		t.Fatalf("record count = %d, want 2", got)
	}
	if snapshot.Inventory["tasks"] != 1 || snapshot.Inventory["watchQueue"] != 2 || snapshot.Inventory["intakeDrafts"] != 2 || snapshot.Inventory["deletedTasks"] != 1 {
		t.Fatalf("inventory = %#v", snapshot.Inventory)
	}
	var task Record
	for _, record := range snapshot.Records {
		if record.LegacyID == "task-1" {
			task = record
		}
	}
	if len(task.History) != 1 || textValue(task.Payload["unknown_field"]) != "preserved" || positiveInt(objectValue(task.Payload["tmdb"])["tmdb_id"]) != 42 {
		t.Fatalf("task payload/history were not preserved: %#v", task)
	}
	if _, exists := task.Payload["history"]; !exists {
		t.Fatal("complete legacy payload must retain history")
	}
}

func TestBuildPlanCreatesOnlyVerifiedImportedArtifactPair(t *testing.T) {
	root := t.TempDir()
	legacyRoot := filepath.Join(root, "legacy-library")
	mappedRoot := filepath.Join(root, "mapped-library")
	if err := os.MkdirAll(mappedRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	base := "Fixture Series - S01E02 - Episode Two"
	video := filepath.Join(mappedRoot, base+".mkv")
	subtitle := filepath.Join(mappedRoot, base+".ass")
	videoBytes := []byte("fixture-video")
	subtitleBytes := []byte("[Script Info]\nTitle: fixture\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,Fixture\n")
	if err := os.WriteFile(video, videoBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitle, subtitleBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	record := fixtureTaskRecord("task-imported", filepath.Join(legacyRoot, base+".mkv"), filepath.Join(legacyRoot, base+".ass"))
	snapshot := Snapshot{SourceKind: "runtime_json", Records: []Record{record}, Inventory: map[string]int{"tasks": 1}}

	options := verifiedFixturePlanOptions(t)
	options.PathMappings = []PathMapping{{From: legacyRoot, To: mappedRoot}}
	plan, err := BuildPlan(context.Background(), snapshot, options)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(plan.Tasks))
	}
	task := plan.Tasks[0]
	if task.TaskState != "imported" || task.ArtifactSetID.String() == "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("planned task = state %q artifact set %s", task.TaskState, task.ArtifactSetID)
	}
	videoHash := sha256.Sum256(videoBytes)
	subtitleHash := sha256.Sum256(subtitleBytes)
	if !slices.Equal(task.VideoChecksum, videoHash[:]) || !slices.Equal(task.SubtitleChecksum, subtitleHash[:]) {
		t.Fatal("artifact checksums do not match fixture bytes")
	}
	if task.VideoPath != video || task.SubtitlePath != subtitle {
		t.Fatalf("mapped artifact paths = %q, %q", task.VideoPath, task.SubtitlePath)
	}
	if len(task.History) != 1 || plan.Counts.Events != 1 || plan.Counts.ArtifactPairs != 1 {
		t.Fatalf("plan counts/history = %#v / %d", plan.Counts, len(task.History))
	}
	if _, exists := plan.Items[0].Payload["unknown_field"]; !exists {
		t.Fatal("migration item dropped an unknown legacy field")
	}

	mismatchOptions := options
	mismatchOptions.Probe = func(context.Context, string) (domain.MediaProbe, error) {
		return domain.MediaProbe{FormatNames: []string{"matroska"}, Streams: []domain.MediaStreamProbe{{Type: "video", Codec: "h264"}}}, nil
	}
	mismatch, err := BuildPlan(context.Background(), snapshot, mismatchOptions)
	if err != nil {
		t.Fatal(err)
	}
	if mismatch.Tasks[0].ArtifactSetID != uuid.Nil || !containsIssue(mismatch.Issues, "video_profile_mismatch") {
		t.Fatalf("profile mismatch plan = artifact %s issues %#v", mismatch.Tasks[0].ArtifactSetID, mismatch.Issues)
	}
}

func TestBuildPlanRejectsMalformedASSArtifact(t *testing.T) {
	root := t.TempDir()
	base := "Fixture - S01E01 - One"
	video := filepath.Join(root, base+".mkv")
	subtitle := filepath.Join(root, base+".ass")
	if err := os.WriteFile(video, []byte("video"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitle, []byte("[Script Info]\nTitle: missing events\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	record := fixtureTaskRecord("task-malformed-ass", video, subtitle)
	plan, err := BuildPlan(context.Background(), Snapshot{SourceKind: "runtime_json", Records: []Record{record}}, verifiedFixturePlanOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tasks[0].ArtifactSetID != uuid.Nil || !containsIssue(plan.Issues, "subtitle_artifact_invalid") {
		t.Fatalf("malformed ASS plan = artifact %s issues %#v", plan.Tasks[0].ArtifactSetID, plan.Issues)
	}
}

func TestBuildPlanMarksMissingArtifactAsFailedWithoutArtifactSet(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "Fixture - S01E01 - One.mkv")
	if err := os.WriteFile(video, []byte("video"), 0o640); err != nil {
		t.Fatal(err)
	}
	record := fixtureTaskRecord("task-missing-subtitle", video, filepath.Join(root, "Fixture - S01E01 - One.ass"))
	plan, err := BuildPlan(context.Background(), Snapshot{SourceKind: "runtime_json", Records: []Record{record}}, PlanOptions{ProfileExtension: "mkv", VerifyFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tasks[0].TaskState != "failed" || plan.Tasks[0].ArtifactSetID.String() != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("task = %#v", plan.Tasks[0])
	}
	if !containsIssue(plan.Issues, "subtitle_artifact_unavailable") {
		t.Fatalf("issues = %#v", plan.Issues)
	}
}

func TestBuildPlanPreservesExplicitTMDbSeasonZero(t *testing.T) {
	record := fixtureTaskRecord("special-task", "missing-special.mkv", "missing-special.ass")
	record.Payload["tmdb_season"] = 0
	record.Payload["tmdb_episode"] = 1
	record.Payload["canonical_episode_title"] = "Special One"
	plan, err := BuildPlan(context.Background(), Snapshot{SourceKind: "runtime_json", Records: []Record{record}}, PlanOptions{ProfileExtension: "mkv", VerifyFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tasks[0].TargetSeason != 0 || plan.Tasks[0].MappingID == uuid.Nil {
		t.Fatalf("special mapping = season %d mapping %s", plan.Tasks[0].TargetSeason, plan.Tasks[0].MappingID)
	}
}

func TestPlanFingerprintBindsProfilePathMappingsAndVerificationMode(t *testing.T) {
	record := fixtureTaskRecord("fingerprint-task", "missing/video.mkv", "missing/video.ass")
	snapshot := Snapshot{SourceKind: "runtime_json", Records: []Record{record}}
	base, err := BuildPlan(context.Background(), snapshot, PlanOptions{ProfileExtension: "mkv", VerifyFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	profileChanged, err := BuildPlan(context.Background(), snapshot, PlanOptions{ProfileExtension: "mp4", VerifyFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	mappingChanged, err := BuildPlan(context.Background(), snapshot, PlanOptions{
		ProfileExtension: "mkv", VerifyFiles: true, PathMappings: []PathMapping{{From: "missing", To: "elsewhere"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	verificationChanged, err := BuildPlan(context.Background(), snapshot, PlanOptions{ProfileExtension: "mkv", VerifyFiles: false})
	if err != nil {
		t.Fatal(err)
	}
	for name, fingerprint := range map[string][]byte{
		"profile": profileChanged.Fingerprint, "path mapping": mappingChanged.Fingerprint, "verification": verificationChanged.Fingerprint,
	} {
		if slices.Equal(base.Fingerprint, fingerprint) {
			t.Fatalf("%s did not change the plan fingerprint", name)
		}
	}
}

func TestBuildPlanRejectsCredentialBearingRSSURL(t *testing.T) {
	payload := map[string]any{
		"draft_id": "rss-secret", "canonical_series": "Series", "source_season": 1,
		"feed_url": "https://user:password@example.test/feed.xml",
	}
	record := makeRecord(t, "rss_draft", "rss-secret", payload)
	plan, err := BuildPlan(context.Background(), Snapshot{SourceKind: "runtime_json", RSSDrafts: []Record{record}}, PlanOptions{ProfileExtension: "mkv", VerifyFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Subscriptions) != 0 || plan.Counts.Invalid != 1 || !containsIssue(plan.Issues, "rss_feed_url_invalid") {
		t.Fatalf("plan = %#v", plan)
	}
	encoded, err := json.Marshal(plan.Items[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "user:password") || strings.Contains(string(encoded), "password@") {
		t.Fatalf("sanitized migration payload retained URL credentials: %s", encoded)
	}
}

func verifiedFixturePlanOptions(t *testing.T) PlanOptions {
	t.Helper()
	profile, err := NewArtifactProfile("av1", "matroska", "mkv", "copy", "")
	if err != nil {
		t.Fatal(err)
	}
	return PlanOptions{
		ProfileExtension: "mkv", VerifyFiles: true, ArtifactProfile: profile,
		Probe: func(context.Context, string) (domain.MediaProbe, error) {
			return domain.MediaProbe{
				FormatNames: []string{"matroska", "webm"},
				Streams:     []domain.MediaStreamProbe{{Type: "video", Codec: "av1"}, {Type: "audio", Codec: "flac"}},
			}, nil
		},
	}
}

func fixtureTaskRecord(id, libraryVideo, librarySubtitle string) Record {
	payload := map[string]any{
		"task_id": id, "canonical_series": "Fixture Series", "canonical_episode_title": "Episode Two",
		"source_season": 1, "source_episode": 2, "tmdb_season": 1, "tmdb_episode": 2,
		"status": "imported", "review_status": "completed", "import_status": "imported",
		"paths":         map[string]any{"library_video": libraryVideo, "library_sub": librarySubtitle},
		"source":        map[string]any{"file_name": "source.mkv", "file_index": 0},
		"history":       []any{map[string]any{"event": "imported", "created_at": "2026-01-02T03:04:05Z"}},
		"unknown_field": map[string]any{"nested": true},
	}
	record := Record{Kind: "task", LegacyID: id, Payload: payload, History: objectSlice(payload["history"]), CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	record.Raw, _ = json.Marshal(payload)
	return record
}

func makeRecord(t *testing.T, kind, id string, payload map[string]any) Record {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return Record{Kind: kind, LegacyID: id, Payload: payload, Raw: raw}
}

func writeJSONFixture(t *testing.T, directory, name string, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), payload, 0o640); err != nil {
		t.Fatal(err)
	}
}

func containsIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
