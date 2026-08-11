package worker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

var validASSFixture = []byte("[Script Info]\nTitle: Episode\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,Hello\n")

type mediaToolsStub struct {
	probe     func(string) (domain.MediaProbe, error)
	run       func([]string) ([]byte, error)
	runArgs   [][]string
	runOutput []byte
	runErr    error
}

func (stub *mediaToolsStub) Probe(_ context.Context, _ string, input string) (domain.MediaProbe, error) {
	return stub.probe(input)
}

func (stub *mediaToolsStub) RunFFmpeg(_ context.Context, _ string, args []string) error {
	stub.runArgs = append(stub.runArgs, append([]string(nil), args...))
	output, err := stub.runOutput, stub.runErr
	if stub.run != nil {
		output, err = stub.run(args)
	}
	if len(output) > 0 {
		if writeErr := os.WriteFile(args[len(args)-1], output, 0o640); writeErr != nil {
			return writeErr
		}
	}
	return err
}

type transcodeStoreStub struct {
	command     domain.TaskMediaCommand
	beginErr    error
	completion  domain.MediaArtifactCompletion
	completeErr error
}

func (stub *transcodeStoreStub) BeginTranscode(context.Context, uuid.UUID) (domain.TaskMediaCommand, error) {
	return stub.command, stub.beginErr
}
func (stub *transcodeStoreStub) CompleteArtifact(_ context.Context, completion domain.MediaArtifactCompletion) error {
	stub.completion = completion
	return stub.completeErr
}

type subtitleStoreStub struct {
	command     domain.TaskMediaCommand
	beginErr    error
	completion  domain.MediaArtifactCompletion
	completeErr error
	scopeID     uuid.UUID
	scopeErr    error
	selection   string
	selErr      error
	scopeCalls  int
}

func (stub *subtitleStoreStub) BeginSubtitle(context.Context, uuid.UUID) (domain.TaskMediaCommand, error) {
	return stub.command, stub.beginErr
}
func (stub *subtitleStoreStub) CompleteArtifact(_ context.Context, completion domain.MediaArtifactCompletion) error {
	stub.completion = completion
	return stub.completeErr
}
func (stub *subtitleStoreStub) CreateSubtitleVideoMatchScope(context.Context, uuid.UUID, []domain.SubtitleMatchCandidate) (uuid.UUID, error) {
	stub.scopeCalls++
	return stub.scopeID, stub.scopeErr
}
func (stub *subtitleStoreStub) GetSubtitleVideoMatchSelection(context.Context, uuid.UUID) (string, error) {
	return stub.selection, stub.selErr
}

func TestTranscodeRunWritesValidatedAtomicOutputAndPersistsArtifact(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	if err := os.MkdirAll(filepath.Join(downloadRoot, "Show"), 0o750); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(downloadRoot, "Show", "Episode01.mkv")
	if err := os.WriteFile(sourcePath, []byte("source-video"), 0o640); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000001")
	operationID := uuid.MustParse("74000000-0000-0000-0000-000000000002")
	profileID := uuid.MustParse("74000000-0000-0000-0000-000000000003")
	store := &transcodeStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, VideoState: domain.VideoTranscoding,
		SavePath: downloadRoot, SourceVideoFileID: uuid.MustParse("74000000-0000-0000-0000-000000000004"), SourceVideoRelativePath: "Show/Episode01.mkv",
		TranscodeProfileID: profileID,
		TranscodeProfile: domain.TranscodeProfile{
			Name: "h264", VideoCodec: "h264", Encoder: "libx264", Container: "mp4", FileExtension: "mp4",
			QualityMode: "crf", QualityValue: 20, AudioPolicy: "transcode", AudioCodec: "aac", Preset: "slow",
			PixelFormat: "yuv420p", ThreadCount: 2, MaxConcurrency: 1,
		},
		Names:                   domain.EpisodeFileNames{BaseName: "Canonical Show - S01E01 - Pilot", VideoName: "Canonical Show - S01E01 - Pilot.mp4", SubtitleName: "Canonical Show - S01E01 - Pilot.ass"},
		OutputRelativeDirectory: filepath.Join("Canonical Show", "Season1"),
	}}
	tools := &mediaToolsStub{runOutput: []byte("transcoded-video"), probe: func(input string) (domain.MediaProbe, error) {
		if input == sourcePath {
			return domain.MediaProbe{FormatNames: []string{"matroska"}, Streams: []domain.MediaStreamProbe{{Type: "video", Codec: "hevc"}, {Type: "audio", Codec: "flac"}}}, nil
		}
		return domain.MediaProbe{FormatNames: []string{"mov", "mp4"}, Streams: []domain.MediaStreamProbe{{Type: "video", Codec: "h264"}, {Type: "audio", Codec: "aac"}}}, nil
	}}
	handler := NewTranscodeRunHandler(mediaTestConfiguration(stagingRoot), tools, store)

	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "episode_task", ResourceID: taskID})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(tools.runArgs) != 1 {
		t.Fatalf("FFmpeg calls = %d", len(tools.runArgs))
	}
	wantPrefix := []string{"-y", "-i", sourcePath, "-map", "0:v:0", "-map", "0:a?", "-sn", "-c:v", "libx264", "-crf", "20", "-preset", "slow", "-pix_fmt", "yuv420p", "-threads", "2", "-c:a", "aac", "-f", "mp4"}
	if !reflect.DeepEqual(tools.runArgs[0][:len(wantPrefix)], wantPrefix) {
		t.Fatalf("FFmpeg args = %#v", tools.runArgs[0])
	}
	wantFinal := filepath.Join(stagingRoot, taskID.String(), "Canonical Show", "Season1", "Canonical Show - S01E01 - Pilot.mp4")
	if store.completion.FilePath != wantFinal || store.completion.Kind != domain.MediaVideo || store.completion.TranscodeProfileID != profileID || len(store.completion.ChecksumSHA256) != 32 {
		t.Fatalf("completion = %#v", store.completion)
	}
	if content, err := os.ReadFile(wantFinal); err != nil || string(content) != "transcoded-video" {
		t.Fatalf("final output = %q, error %v", content, err)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(wantFinal), "*.part.mp4"))
	if len(matches) != 0 {
		t.Fatalf("temporary outputs remain: %v", matches)
	}
}

func TestTranscodeRunReusesCommittedOutputAfterCrashReplay(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "source.mkv"), []byte("source"), 0o640)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000005")
	operationID := uuid.MustParse("74000000-0000-0000-0000-000000000006")
	finalDirectory := filepath.Join(stagingRoot, taskID.String())
	_ = os.MkdirAll(finalDirectory, 0o750)
	_ = os.WriteFile(filepath.Join(finalDirectory, "Show - S01E01 - One.mp4"), []byte("existing-output"), 0o640)
	store := &transcodeStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, VideoState: domain.VideoTranscoding,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "source.mkv", TranscodeProfileID: uuid.New(),
		TranscodeProfile: domain.TranscodeProfile{Name: "h264", VideoCodec: "h264", Encoder: "libx264", Container: "mp4", FileExtension: "mp4", QualityMode: "crf", QualityValue: 20, AudioPolicy: "transcode", AudioCodec: "aac", Preset: "medium", PixelFormat: "yuv420p", MaxConcurrency: 1},
		Names:            domain.EpisodeFileNames{BaseName: "Show - S01E01 - One", VideoName: "Show - S01E01 - One.mp4"},
	}}
	tools := &mediaToolsStub{probe: func(input string) (domain.MediaProbe, error) {
		if input == filepath.Join(downloadRoot, "source.mkv") {
			return domain.MediaProbe{Streams: []domain.MediaStreamProbe{{Type: "video", Codec: "hevc"}, {Type: "audio", Codec: "flac"}}}, nil
		}
		return domain.MediaProbe{FormatNames: []string{"mov", "mp4"}, Streams: []domain.MediaStreamProbe{{Type: "video", Codec: "h264"}, {Type: "audio", Codec: "aac"}}}, nil
	}}
	if err := NewTranscodeRunHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "episode_task", ResourceID: taskID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(tools.runArgs) != 0 || store.completion.SizeBytes != int64(len("existing-output")) {
		t.Fatalf("replay ran FFmpeg or missed completion: calls=%d completion=%#v", len(tools.runArgs), store.completion)
	}
}

func TestSubtitlePrepareNormalizesAndValidatesExternalASS(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.zh-Hans.ass"), validASSFixture, 0o640)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000007")
	operationID := uuid.MustParse("74000000-0000-0000-0000-000000000008")
	subtitleFileID := uuid.MustParse("74000000-0000-0000-0000-000000000009")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		ExternalSubtitles: []domain.TaskExternalSubtitle{{SourceFileID: subtitleFileID, RelativePath: "episode.zh-Hans.ass", Language: "zh-Hans", Format: domain.SubtitleASS}},
		Names:             domain.EpisodeFileNames{BaseName: "Show - S01E01 - One", SubtitleName: "Show - S01E01 - One.ass"},
	}}
	tools := &mediaToolsStub{runOutput: validASSFixture, probe: func(string) (domain.MediaProbe, error) { return domain.MediaProbe{}, nil }}
	if err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "episode_task", ResourceID: taskID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(tools.runArgs) != 1 || store.completion.SourceFileID != subtitleFileID || store.completion.Kind != domain.MediaSubtitle || store.completion.Format != "ass" {
		t.Fatalf("subtitle completion = %#v, FFmpeg calls=%d", store.completion, len(tools.runArgs))
	}
	if store.completion.Metadata["language"] != "zh-Hans" {
		t.Fatalf("subtitle language metadata = %#v", store.completion.Metadata)
	}
	if err := validateASSFile(store.completion.FilePath); err != nil {
		t.Fatalf("final ASS invalid: %v", err)
	}
}

func TestSubtitlePrepareRecognizesJPSCTrackAsSimplifiedBilingualASS(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "movie.mkv"), []byte("video"), 0o640)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000020")
	operationID := uuid.MustParse("74000000-0000-0000-0000-000000000021")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "movie.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Canonical Movie(2026)", SubtitleName: "Canonical Movie(2026).ass"},
	}}
	tools := &mediaToolsStub{
		runOutput: validASSFixture,
		probe: func(string) (domain.MediaProbe, error) {
			return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
				{Index: 2, Type: "subtitle", Codec: "ass", Language: "zho", Title: "繁體中文", Default: true},
				{Index: 4, Type: "subtitle", Codec: "ass", Language: "chi", Title: "JPSC"},
			}}, nil
		},
	}
	if err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "episode_task", ResourceID: taskID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(tools.runArgs) != 1 || len(tools.runArgs[0]) < 5 || tools.runArgs[0][4] != "0:4" {
		t.Fatalf("FFmpeg calls = %#v, want embedded stream 4", tools.runArgs)
	}
	if store.completion.Metadata["language"] != "zh-Hans" || store.completion.Metadata["source"] != domain.SubtitleSourceEmbedded {
		t.Fatalf("subtitle completion metadata = %#v", store.completion.Metadata)
	}
}

func TestSubtitlePrepareInspectsAmbiguousEmbeddedASSContent(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000024")
	operationID := uuid.MustParse("74000000-0000-0000-0000-000000000025")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E04", SubtitleName: "Show - S01E04.ass"},
	}}
	simplified := subtitleContentFixture(strings.Repeat("我们已经说过这个问题，后来还是决定继续学习简体中文。", 5))
	traditional := subtitleContentFixture(strings.Repeat("我們已經說過這個問題，後來還是決定繼續學習繁體中文。", 5))
	tools := &mediaToolsStub{
		probe: func(string) (domain.MediaProbe, error) {
			return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
				{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi", Default: true},
				{Index: 3, Type: "subtitle", Codec: "ass", Language: "chi", Default: true},
			}}, nil
		},
		run: func(args []string) ([]byte, error) {
			switch args[4] {
			case "0:2":
				return simplified, nil
			case "0:3":
				return traditional, nil
			default:
				return nil, fmt.Errorf("unexpected stream map %q", args[4])
			}
		},
	}
	if err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "episode_task", ResourceID: taskID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	wantMaps := []string{"0:2"}
	if len(tools.runArgs) != len(wantMaps) {
		t.Fatalf("FFmpeg calls = %#v, want maps %v", tools.runArgs, wantMaps)
	}
	for index, want := range wantMaps {
		if tools.runArgs[index][4] != want {
			t.Fatalf("FFmpeg call %d map = %q, want %q", index, tools.runArgs[index][4], want)
		}
	}
	if store.completion.Kind != domain.MediaSubtitle || store.completion.Metadata["language"] != "zh-Hans" {
		t.Fatalf("completion = %#v", store.completion)
	}
	if remaining := subtitleInspectionOutputs(t, stagingRoot); len(remaining) != 0 {
		t.Fatalf("inspection outputs remain: %v", remaining)
	}
}

func TestSubtitlePrepareFallsBackAfterInvalidPreferredCandidate(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000029")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E05", SubtitleName: "Show - S01E05.ass"},
	}}
	traditional := subtitleContentFixture(strings.Repeat("我們已經決定繼續學習繁體中文。", 5))
	tools := &mediaToolsStub{
		probe: func(string) (domain.MediaProbe, error) {
			return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
				{Index: 2, Type: "subtitle", Codec: "ass", Language: "zh-Hans"},
				{Index: 3, Type: "subtitle", Codec: "ass", Language: "zh-Hant"},
			}}, nil
		},
		run: func(args []string) ([]byte, error) {
			if args[4] == "0:2" {
				return []byte("not an ASS document"), nil
			}
			return traditional, nil
		},
	}
	if err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(tools.runArgs) != 2 || tools.runArgs[0][4] != "0:2" || tools.runArgs[1][4] != "0:3" {
		t.Fatalf("fallback calls = %#v", tools.runArgs)
	}
	if store.completion.Metadata["languageEvidence"] != domain.SubtitleEvidenceTraditional || store.completion.Metadata["candidateAttempts"] != 2 {
		t.Fatalf("fallback metadata = %#v", store.completion.Metadata)
	}
}

func TestSubtitlePrepareAcceptsGenericChineseContent(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000026")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E04", SubtitleName: "Show - S01E04.ass"},
	}}
	neutral := subtitleContentFixture(strings.Repeat("天地人生山水日月大小上下。", 8))
	tools := &mediaToolsStub{
		probe: func(string) (domain.MediaProbe, error) {
			return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
				{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi"},
				{Index: 3, Type: "subtitle", Codec: "ass", Language: "chi"},
			}}, nil
		},
		runOutput: neutral,
	}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(tools.runArgs) != 1 || store.completion.TaskID != taskID {
		t.Fatalf("generic Chinese subtitle was not committed: calls=%#v completion=%#v", tools.runArgs, store.completion)
	}
	if remaining := subtitleInspectionOutputs(t, stagingRoot); len(remaining) != 0 {
		t.Fatalf("inspection outputs remain: %v", remaining)
	}
}

func TestSubtitlePrepareInspectionFailureIsRetryableAndCleansPartialOutput(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000027")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E04", SubtitleName: "Show - S01E04.ass"},
	}}
	tools := &mediaToolsStub{
		probe: func(string) (domain.MediaProbe, error) {
			return domain.MediaProbe{Streams: []domain.MediaStreamProbe{{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi"}}}, nil
		},
		run: func([]string) ([]byte, error) { return []byte("partial"), errors.New("ffmpeg stopped") },
	}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "ffmpeg_subtitle_candidates_failed" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v", err)
	}
	if remaining := subtitleInspectionOutputs(t, stagingRoot); len(remaining) != 0 {
		t.Fatalf("inspection outputs remain: %v", remaining)
	}
}

func TestSubtitlePrepareCapsCandidateAttempts(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000028")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E04", SubtitleName: "Show - S01E04.ass"},
	}}
	streams := make([]domain.MediaStreamProbe, 17)
	for index := range streams {
		streams[index] = domain.MediaStreamProbe{Index: index + 2, Type: "subtitle", Codec: "ass", Language: "chi"}
	}
	tools := &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) {
		return domain.MediaProbe{Streams: streams}, nil
	}}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(filepath.Join(root, "staging")), tools, store).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "subtitle_candidates_exhausted" || failure.Retryable {
		t.Fatalf("Handle() error = %#v", err)
	}
	if len(tools.runArgs) != maxSubtitleCandidateAttempts {
		t.Fatalf("candidate attempts = %d, want %d", len(tools.runArgs), maxSubtitleCandidateAttempts)
	}
}

func TestSubtitlePrepareConvertsTraditionalFallbackToSimplified(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000022")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E01", SubtitleName: "Show - S01E01.ass"},
	}}
	traditional := subtitleWithoutScriptInfoFixture(strings.Repeat("我們已經說過這個問題，後來還是決定繼續學習繁體中文。", 5))
	tools := &mediaToolsStub{runOutput: traditional, probe: func(string) (domain.MediaProbe, error) {
		return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
			{Index: 2, Type: "subtitle", Codec: "ass", Language: "zho", Title: "繁體中文"},
			{Index: 3, Type: "subtitle", Codec: "ass", Language: "eng", Title: "English"},
		}}, nil
	}}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(filepath.Join(root, "staging")), tools, store).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	content, readErr := os.ReadFile(store.completion.FilePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(content), "我们已经说过这个问题") || strings.Contains(string(content), "我們已經說過這個問題") {
		t.Fatalf("traditional subtitle was not normalized: %q", content)
	}
	if len(tools.runArgs) != 1 || store.completion.Metadata["languageEvidence"] != domain.SubtitleEvidenceTraditional {
		t.Fatalf("traditional subtitle completion = %#v, calls=%#v", store.completion, tools.runArgs)
	}
}

type finalizeStoreStub struct {
	command   domain.FinalizeMediaCommand
	loadErr   error
	completed bool
}

func (stub *finalizeStoreStub) LoadFinalizeCommand(context.Context, uuid.UUID) (domain.FinalizeMediaCommand, error) {
	return stub.command, stub.loadErr
}
func (stub *finalizeStoreStub) CompleteFinalize(context.Context, uuid.UUID, uuid.UUID) error {
	stub.completed = true
	return nil
}

func TestMediaFinalizeVerifiesPairedFilesBeforeAwaitingReview(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "Show - S01E01 - One.mp4")
	subtitlePath := filepath.Join(root, "Show - S01E01 - One.ass")
	videoContent := []byte("video-artifact")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	videoChecksum := sha256.Sum256(videoContent)
	subtitleChecksum := sha256.Sum256(validASSFixture)
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000010")
	store := &finalizeStoreStub{command: domain.FinalizeMediaCommand{
		TaskID: taskID,
		State:  domain.TaskFinalizing,
		Video: domain.MediaArtifact{
			BaseName: "Show - S01E01 - One", FilePath: videoPath, SizeBytes: int64(len(videoContent)), ChecksumSHA256: videoChecksum[:],
		},
		Subtitle: domain.MediaArtifact{
			BaseName: "Show - S01E01 - One", FilePath: subtitlePath, SizeBytes: int64(len(validASSFixture)), ChecksumSHA256: subtitleChecksum[:],
		},
	}}
	err := NewMediaFinalizeHandler(store).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.completed {
		t.Fatal("CompleteFinalize() was not called")
	}
}

func TestDownloadMaterializeReturnsDomainFailureWithoutRetry(t *testing.T) {
	store := downloadMaterializeStoreStub{err: &domain.MediaWorkflowError{Code: "episode_mapping_required", Message: "mapping missing"}}
	err := NewDownloadMaterializeHandler(&store).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "download", ResourceID: uuid.New()})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "episode_mapping_required" || failure.Retryable {
		t.Fatalf("Handle() error = %#v", err)
	}
}

type downloadMaterializeStoreStub struct{ err error }

func (stub *downloadMaterializeStoreStub) MaterializeDownload(context.Context, uuid.UUID, uuid.UUID) error {
	return stub.err
}

func TestBuildMediaOutputPathsKeepsCanonicalDirectoryInsideTaskRoot(t *testing.T) {
	root := t.TempDir()
	taskID := uuid.MustParse("74000000-0000-0000-0000-000000000030")
	operationID := uuid.MustParse("74000000-0000-0000-0000-000000000031")
	paths, err := buildMediaOutputPaths(root, taskID, operationID, filepath.Join("Canonical Show", "Season2", "episode.mkv"))
	if err != nil {
		t.Fatalf("buildMediaOutputPaths() error = %v", err)
	}
	want := filepath.Join(root, taskID.String(), "Canonical Show", "Season2", "episode.mkv")
	if paths.Final != want || paths.Directory != filepath.Dir(want) {
		t.Fatalf("paths = %#v, want final %q", paths, want)
	}
	if _, err := buildMediaOutputPaths(root, taskID, operationID, filepath.Join("..", "outside.mkv")); err == nil {
		t.Fatal("buildMediaOutputPaths(path traversal) error = nil")
	}
}

func subtitleContentFixture(text string) []byte {
	return []byte(fmt.Sprintf("[Script Info]\nTitle: Fixture\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,%s\n", text))
}

func subtitleWithoutScriptInfoFixture(text string) []byte {
	return []byte(fmt.Sprintf("[V4+ Styles]\nFormat: Name, Fontname, Fontsize\nStyle: Default,Arial,20\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,%s\n", text))
}

func subtitleInspectionOutputs(t *testing.T, root string) []string {
	t.Helper()
	var matches []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.Contains(entry.Name(), ".inspect-") {
			matches = append(matches, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return matches
}

func mediaTestConfiguration(stagingRoot string) *downloadConfigurationStub {
	return &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{
		StagingRoot: stagingRoot,
		FFmpegPath:  "ffmpeg-test",
		FFprobePath: "ffprobe-test",
	}}}}
}
