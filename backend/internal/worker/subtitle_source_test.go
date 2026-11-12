package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

const gbkSubtitleText = "1\n00:00:01,000 --> 00:00:03,000\n这是简体中文字幕测试，包含常用字的一是不了在人有我他这中大来上国个到说们\n"

func gbkSubtitleFixture(t *testing.T) []byte {
	t.Helper()
	encoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), []byte(gbkSubtitleText))
	if err != nil {
		t.Fatalf("encode GBK subtitle: %v", err)
	}
	return encoded
}

func TestPrepareSubtitleSourceConvertsNonUTF8AndCleansUp(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "episode.zh-Hans.srt")
	if err := os.WriteFile(sourcePath, gbkSubtitleFixture(t), 0o640); err != nil {
		t.Fatal(err)
	}
	converted, cleanup, err := prepareSubtitleSource(sourcePath, root)
	if err != nil {
		t.Fatalf("prepareSubtitleSource() error = %v", err)
	}
	if converted == sourcePath {
		t.Fatal("非 UTF-8 字幕沿用了原始文件")
	}
	content, err := os.ReadFile(converted)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "这是简体中文字幕测试") || strings.ContainsRune(string(content), '\uFFFD') {
		t.Fatalf("converted content = %q", content)
	}
	if !strings.HasPrefix(filepath.Base(converted), ".subtitle-source.") || filepath.Ext(converted) != ".srt" {
		t.Fatalf("converted temporary name = %q", filepath.Base(converted))
	}
	cleanup()
	if _, err := os.Stat(converted); !os.IsNotExist(err) {
		t.Fatalf("cleanup left converted subtitle: %v", err)
	}
}

func TestPrepareSubtitleSourceReusesValidUTF8(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "episode.zh-Hans.srt")
	if err := os.WriteFile(sourcePath, []byte(gbkSubtitleText), 0o640); err != nil {
		t.Fatal(err)
	}
	converted, cleanup, err := prepareSubtitleSource(sourcePath, root)
	if err != nil {
		t.Fatalf("prepareSubtitleSource() error = %v", err)
	}
	if converted != sourcePath {
		t.Fatalf("UTF-8 subtitle copied to %q", converted)
	}
	cleanup()
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("cleanup removed the original subtitle: %v", err)
	}
}

func TestPrepareSubtitleSourceRejectsUndecodableEncoding(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "episode.zh-Hans.srt")
	if err := os.WriteFile(sourcePath, []byte{0xFF, 0xFF, 0xFF, 0xFF}, 0o640); err != nil {
		t.Fatal(err)
	}
	_, _, err := prepareSubtitleSource(sourcePath, root)
	var subtitleErr *domain.SubtitleError
	if !errors.As(err, &subtitleErr) || subtitleErr.Code != "subtitle_charset_unsupported" {
		t.Fatalf("prepareSubtitleSource() error = %v, want subtitle_charset_unsupported", err)
	}
}

func TestSubtitlePrepareConvertsGBKExternalCandidate(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	subtitleDirectory := filepath.Join(downloadRoot, "Show")
	if err := os.MkdirAll(subtitleDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640); err != nil {
		t.Fatal(err)
	}
	subtitleBytes := gbkSubtitleFixture(t)
	subtitlePath := filepath.Join(subtitleDirectory, "episode.zh-Hans.srt")
	if err := os.WriteFile(subtitlePath, subtitleBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.New()
	sourceID := uuid.New()
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E01", SubtitleName: "Show - S01E01.ass"},
		ExternalSubtitles: []domain.TaskExternalSubtitle{
			{SourceFileID: sourceID, RelativePath: "Show/episode.zh-Hans.srt", Language: "zh-Hans", Format: domain.SubtitleSRT},
		},
	}}
	var inputHadUTF8 bool
	tools := &mediaToolsStub{
		probe: func(string) (domain.MediaProbe, error) { return domain.MediaProbe{}, nil },
		run: func(args []string) ([]byte, error) {
			inputPath := args[2]
			input, err := os.ReadFile(inputPath)
			if err != nil {
				t.Fatalf("read FFmpeg input: %v", err)
			}
			if !strings.Contains(string(input), "这是简体中文字幕测试") || strings.ContainsRune(string(input), '\uFFFD') {
				t.Fatalf("FFmpeg input is not converted UTF-8: %q", input)
			}
			inputHadUTF8 = true
			return subtitleContentFixture("这是简体中文字幕测试"), nil
		},
	}
	if err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(
		context.Background(),
		domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !inputHadUTF8 {
		t.Fatal("FFmpeg did not receive the converted UTF-8 subtitle")
	}
	if store.completion.FilePath == "" {
		t.Fatal("GBK 字幕未完成产物记录")
	}
	content, err := os.ReadFile(store.completion.FilePath)
	if err != nil || !strings.Contains(string(content), "这是简体中文字幕测试") {
		t.Fatalf("final subtitle = %q, error %v", content, err)
	}
	assertFileContent(t, subtitlePath, subtitleBytes)
	if matches, _ := filepath.Glob(filepath.Join(stagingRoot, taskID.String(), ".subtitle-source.*")); len(matches) != 0 {
		t.Fatalf("converted subtitle temporaries remain: %v", matches)
	}
}

func TestSubtitlePrepareReportsCharsetFailureWithoutFFmpeg(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	if err := os.MkdirAll(downloadRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadRoot, "episode.zh-Hans.srt"), []byte{0xFF, 0xFF, 0xFF, 0xFF}, 0o640); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.New()
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E01", SubtitleName: "Show - S01E01.ass"},
		ExternalSubtitles: []domain.TaskExternalSubtitle{
			{SourceFileID: uuid.New(), RelativePath: "episode.zh-Hans.srt", Language: "zh-Hans", Format: domain.SubtitleSRT},
		},
	}}
	tools := &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) { return domain.MediaProbe{}, nil }}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(
		context.Background(),
		domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
	)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "subtitle_charset_unsupported" || failure.Retryable {
		t.Fatalf("Handle() error = %#v, want permanent subtitle_charset_unsupported", err)
	}
	if len(tools.runArgs) != 0 {
		t.Fatalf("FFmpeg ran for an undecodable subtitle: %#v", tools.runArgs)
	}
}

func TestSubtitleMatchCandidatesExposeOnlyRelativePaths(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	sourceID := uuid.New()
	absolutePath := filepath.Join(downloadRoot, "Show", "episode.zh-Hans.srt")
	plans := []domain.SubtitlePlan{{
		Source: domain.SubtitleSourceExternal, SourceFileID: sourceID,
		InputPath: absolutePath, InputFormat: domain.SubtitleSRT, Language: "zh-Hans",
	}}
	external := []domain.SubtitleCandidate{{
		SourceFileID: sourceID, Path: absolutePath, RelativePath: "Show/episode.zh-Hans.srt",
		Format: domain.SubtitleSRT, Language: "zh-Hans",
	}}
	candidates := subtitleMatchCandidates(plans, nil, external)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0].Path != "Show/episode.zh-Hans.srt" || candidates[0].Title != "episode.zh-Hans.srt" {
		t.Fatalf("candidate exposes host path: %#v", candidates[0])
	}
	if strings.Contains(candidates[0].Path, downloadRoot) || strings.Contains(candidates[0].Title, downloadRoot) {
		t.Fatalf("candidate contains absolute download root: %#v", candidates[0])
	}

	legacy := []domain.SubtitleCandidate{{SourceFileID: sourceID, Path: absolutePath, Format: domain.SubtitleSRT}}
	candidates = subtitleMatchCandidates(plans, nil, legacy)
	if candidates[0].Path != "episode.zh-Hans.srt" || candidates[0].Title != "episode.zh-Hans.srt" {
		t.Fatalf("legacy candidate path = %#v", candidates[0])
	}
}

func TestSubtitleInspectorResolvesRelativeCandidateAndConvertsEncoding(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	subtitleDirectory := filepath.Join(downloadRoot, "Show")
	if err := os.MkdirAll(subtitleDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subtitleDirectory, "episode.zh-Hans.srt"), gbkSubtitleFixture(t), 0o640); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(downloadRoot, "Show", "episode.mkv")
	var inputPath string
	tools := &mediaToolsStub{run: func(args []string) ([]byte, error) {
		inputPath = args[2]
		input, err := os.ReadFile(inputPath)
		if err != nil {
			t.Fatalf("read inspector input: %v", err)
		}
		if !strings.Contains(string(input), "这是简体中文字幕测试") {
			t.Fatalf("inspector input is not converted UTF-8: %q", input)
		}
		return subtitleContentFixture("这是简体中文字幕测试"), nil
	}}
	inspector := NewSubtitleTextInspector(mediaTestConfiguration(filepath.Join(root, "staging")), tools)
	inspection, err := inspector.InspectSubtitleText(context.Background(), domain.SubtitleInspectionRequest{
		VideoPath: videoPath, BasePath: downloadRoot, CandidateID: "external:" + uuid.NewString(),
		Source: domain.SubtitleSourceExternal, Format: domain.SubtitleSRT, Path: "Show/episode.zh-Hans.srt",
	})
	if err != nil {
		t.Fatalf("InspectSubtitleText() error = %v", err)
	}
	if !inspection.Found || inspection.HanCount == 0 || !strings.Contains(inspection.Sample, "这是简体中文字幕测试") {
		t.Fatalf("inspection = %#v", inspection)
	}
	if filepath.Dir(inputPath) == subtitleDirectory {
		t.Fatalf("inspector wrote converted input next to the source: %q", inputPath)
	}
}

func TestSubtitleInspectorRejectsRelativePathOutsideDownloadRoot(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	if err := os.MkdirAll(downloadRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	tools := &mediaToolsStub{run: func([]string) ([]byte, error) {
		t.Fatal("FFmpeg must not run for an unsafe candidate path")
		return nil, nil
	}}
	inspector := NewSubtitleTextInspector(mediaTestConfiguration(filepath.Join(root, "staging")), tools)
	_, err := inspector.InspectSubtitleText(context.Background(), domain.SubtitleInspectionRequest{
		VideoPath: filepath.Join(downloadRoot, "episode.mkv"), BasePath: downloadRoot,
		CandidateID: "external:" + uuid.NewString(), Source: domain.SubtitleSourceExternal,
		Format: domain.SubtitleSRT, Path: "../outside.srt",
	})
	if err == nil {
		t.Fatal("InspectSubtitleText() accepted a path outside the download root")
	}
	if len(tools.runArgs) != 0 {
		t.Fatalf("FFmpeg ran for an unsafe path: %#v", tools.runArgs)
	}
}
