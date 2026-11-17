package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/encoding/simplifiedchinese"
	xunicode "golang.org/x/text/encoding/unicode"
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
		subtitleTestAttempt(domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID}),
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
		subtitleTestAttempt(domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID}),
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

const subtitleSourceSampleBytesForTest = 256 << 10

// largeUTF8ASSAtSampleBoundary 构造在 256 KiB 采样边界处跨越多字节字符的合法 ASS。
func largeUTF8ASSAtSampleBoundary(t *testing.T, leading []byte, character rune, inside int) []byte {
	t.Helper()
	encoded := []byte(string(character))
	if inside < 0 || inside > len(encoded) {
		t.Fatalf("inside=%d is outside encoded rune length %d", inside, len(encoded))
	}
	header := []byte("[Script Info]\nTitle: ")
	start := subtitleSourceSampleBytesForTest - inside
	if start < len(leading)+len(header) {
		t.Fatalf("fixture prefix=%d exceeds rune start=%d", len(leading)+len(header), start)
	}
	content := make([]byte, 0, subtitleSourceSampleBytesForTest+16<<10)
	content = append(content, leading...)
	content = append(content, header...)
	content = append(content, bytes.Repeat([]byte("x"), start-len(content))...)
	content = append(content, encoded...)
	content = append(content, []byte("\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize\nStyle: Default,Arial,20\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")...)
	for index := 0; index < 40; index++ {
		content = append(content, []byte(fmt.Sprintf("Dialogue: 0,0:00:%02d.00,0:00:%02d.00,Default,,0,0,0,,这是第%d句简体中文字幕测试文本 Bilingual text。\n", index, (index+1)%60, index))...)
	}
	return content
}

func TestPrepareSubtitleSourceReusesLargeUTF8AtSampleBoundary(t *testing.T) {
	tests := []struct {
		name      string
		leading   []byte
		character rune
		inside    int
	}{
		{name: "two byte rune split", character: 'é', inside: 1},
		{name: "three byte rune split after first byte", character: '中', inside: 1},
		{name: "three byte rune split after second byte", character: '中', inside: 2},
		{name: "four byte rune split after first byte", character: '😀', inside: 1},
		{name: "four byte rune split after second byte", character: '😀', inside: 2},
		{name: "four byte rune split after third byte", character: '😀', inside: 3},
		{name: "utf-8 bom and three byte rune split", leading: []byte{0xEF, 0xBB, 0xBF}, character: '中', inside: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			content := largeUTF8ASSAtSampleBoundary(t, test.leading, test.character, test.inside)
			if !utf8.Valid(content) {
				t.Fatal("fixture is not valid UTF-8")
			}
			if utf8.Valid(content[:subtitleSourceSampleBytesForTest]) {
				t.Fatal("fixture does not split a rune at the sample boundary")
			}
			sourcePath := filepath.Join(root, "episode.zh-Hans.ass")
			if err := os.WriteFile(sourcePath, content, 0o640); err != nil {
				t.Fatal(err)
			}
			converted, cleanup, err := prepareSubtitleSource(sourcePath, root)
			if err != nil {
				t.Fatalf("prepareSubtitleSource(valid large UTF-8) error = %v", err)
			}
			if cleanup == nil {
				t.Fatal("prepareSubtitleSource() returned a nil cleanup")
			}
			cleanup()
			if converted != sourcePath {
				t.Fatalf("valid UTF-8 source converted to %q, want the original path", converted)
			}
			got, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) {
				t.Fatal("original UTF-8 source bytes were modified")
			}
			if matches, _ := filepath.Glob(filepath.Join(root, ".subtitle-source.*")); len(matches) != 0 {
				t.Fatalf("converted subtitle temporary created for unchanged UTF-8: %v", matches)
			}
		})
	}
}

func TestSubtitlePrepareReusesLargeUTF8ExternalCandidateAtSampleBoundary(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	if err := os.MkdirAll(downloadRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	content := largeUTF8ASSAtSampleBoundary(t, nil, '中', 2)
	sourcePath := filepath.Join(downloadRoot, "episode.zh-Hans.ass")
	if err := os.WriteFile(sourcePath, content, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.New()
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E01", SubtitleName: "Show - S01E01.ass"},
		ExternalSubtitles: []domain.TaskExternalSubtitle{{
			SourceFileID: uuid.New(), RelativePath: "episode.zh-Hans.ass", Language: "zh-Hans", Format: domain.SubtitleASS,
		}},
	}}
	var inputPath string
	tools := &mediaToolsStub{
		probe: func(string) (domain.MediaProbe, error) { return domain.MediaProbe{}, nil },
		run: func(args []string) ([]byte, error) {
			inputPath = args[2]
			input, err := os.ReadFile(inputPath)
			if err != nil {
				t.Fatalf("read FFmpeg input: %v", err)
			}
			if !bytes.Equal(input, content) {
				t.Fatal("FFmpeg input bytes differ from the valid UTF-8 source")
			}
			return subtitleContentFixture("这是简体中文字幕测试"), nil
		},
	}
	if err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store).Handle(
		context.Background(),
		subtitleTestAttempt(domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID}),
	); err != nil {
		t.Fatalf("Handle(valid large UTF-8 external candidate) error = %v", err)
	}
	if inputPath != sourcePath {
		t.Fatalf("FFmpeg input = %q, want the original source %q", inputPath, sourcePath)
	}
	if got, err := os.ReadFile(sourcePath); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("source subtitle changed after preparation: error=%v", err)
	}
	if store.completion.FilePath == "" {
		t.Fatal("subtitle artifact was not completed")
	}
	if matches, _ := filepath.Glob(filepath.Join(stagingRoot, taskID.String(), ".subtitle-source.*")); len(matches) != 0 {
		t.Fatalf("converted subtitle temporaries remain: %v", matches)
	}
}

func TestSubtitleInspectorReusesLargeUTF8CandidateAtSampleBoundary(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	subtitleDirectory := filepath.Join(downloadRoot, "Show")
	if err := os.MkdirAll(subtitleDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	content := largeUTF8ASSAtSampleBoundary(t, nil, '中', 2)
	sourcePath := filepath.Join(subtitleDirectory, "episode.zh-Hans.ass")
	if err := os.WriteFile(sourcePath, content, 0o640); err != nil {
		t.Fatal(err)
	}
	var inputPath string
	tools := &mediaToolsStub{run: func(args []string) ([]byte, error) {
		inputPath = args[2]
		input, err := os.ReadFile(inputPath)
		if err != nil {
			t.Fatalf("read inspector input: %v", err)
		}
		if !bytes.Equal(input, content) {
			t.Fatal("inspector input bytes differ from the valid UTF-8 source")
		}
		return subtitleContentFixture("这是简体中文字幕测试"), nil
	}}
	inspector := NewSubtitleTextInspector(mediaTestConfiguration(filepath.Join(root, "staging")), tools)
	inspection, err := inspector.InspectSubtitleText(context.Background(), domain.SubtitleInspectionRequest{
		VideoPath: filepath.Join(subtitleDirectory, "episode.mkv"), BasePath: downloadRoot,
		CandidateID: "external:" + uuid.NewString(), Source: domain.SubtitleSourceExternal,
		Format: domain.SubtitleASS, Path: "Show/episode.zh-Hans.ass",
	})
	if err != nil {
		t.Fatalf("InspectSubtitleText(valid large UTF-8) error = %v", err)
	}
	if !inspection.Found || inspection.HanCount == 0 || !strings.Contains(inspection.Sample, "这是简体中文字幕测试") {
		t.Fatalf("inspection = %#v", inspection)
	}
	if inputPath != sourcePath {
		t.Fatalf("inspector FFmpeg input = %q, want the original source %q", inputPath, sourcePath)
	}
	if got, err := os.ReadFile(sourcePath); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("inspected source subtitle changed: error=%v", err)
	}
}

func utf16SurrogateHighBytes(character rune, endianness xunicode.Endianness) []byte {
	high := utf16.Encode([]rune{character})[0]
	encoded := []byte{byte(high >> 8), byte(high)}
	if endianness == xunicode.LittleEndian {
		encoded[0], encoded[1] = encoded[1], encoded[0]
	}
	return encoded
}

func utf16BoundarySRTTextForTest(t *testing.T, unitsBefore int) string {
	t.Helper()
	blocks := make([]string, 0, unitsBefore/64)
	length := 0
	index := 1
	blockText := "这是简体中文字幕测试文本 Bilingual subtitle text。\n\n"
	for {
		block := fmt.Sprintf("%d\n00:00:01,000 --> 00:00:03,000\n%s", index, blockText)
		blockLength := len(utf16.Encode([]rune(block)))
		if length+blockLength > unitsBefore {
			break
		}
		blocks = append(blocks, block)
		length += blockLength
		index++
	}
	for len(blocks) > 0 {
		prefix := fmt.Sprintf("%d\n00:00:01,000 --> 00:00:03,000\n", index)
		if length+len(utf16.Encode([]rune(prefix))) <= unitsBefore {
			break
		}
		last := blocks[len(blocks)-1]
		length -= len(utf16.Encode([]rune(last)))
		blocks = blocks[:len(blocks)-1]
		index--
	}
	prefix := fmt.Sprintf("%d\n00:00:01,000 --> 00:00:03,000\n", index)
	remaining := unitsBefore - length - len(utf16.Encode([]rune(prefix)))
	if remaining < 0 {
		t.Fatalf("UTF-16 fixture cannot place the final cue within %d units", unitsBefore)
	}
	var builder strings.Builder
	for _, block := range blocks {
		builder.WriteString(block)
	}
	builder.WriteString(prefix)
	builder.WriteString(strings.Repeat("a", remaining))
	builder.WriteString("😀后续字幕文本\n\n")
	return builder.String()
}

func TestPrepareSubtitleSourceAcceptsBOMLessUTF16AcrossSampleBoundary(t *testing.T) {
	for _, test := range []struct {
		name       string
		endianness xunicode.Endianness
	}{
		{name: "little endian", endianness: xunicode.LittleEndian},
		{name: "big endian", endianness: xunicode.BigEndian},
	} {
		t.Run(test.name, func(t *testing.T) {
			unitsBefore := subtitleSourceSampleBytesForTest/2 - 1
			text := utf16BoundarySRTTextForTest(t, unitsBefore)
			encoded, _, err := transform.Bytes(xunicode.UTF16(test.endianness, xunicode.IgnoreBOM).NewEncoder(), []byte(text))
			if err != nil {
				t.Fatalf("encode UTF-16 fixture: %v", err)
			}
			if len(encoded) <= subtitleSourceSampleBytesForTest {
				t.Fatalf("fixture size = %d, want > %d", len(encoded), subtitleSourceSampleBytesForTest)
			}
			high := utf16SurrogateHighBytes('😀', test.endianness)
			sample := encoded[:subtitleSourceSampleBytesForTest]
			if !bytes.Equal(sample[len(sample)-len(high):], high) {
				t.Fatalf("sample does not end with the high surrogate: % x, want % x", sample[len(sample)-len(high):], high)
			}
			root := t.TempDir()
			sourcePath := filepath.Join(root, "episode.zh-Hans.srt")
			if err := os.WriteFile(sourcePath, encoded, 0o640); err != nil {
				t.Fatal(err)
			}
			converted, cleanup, err := prepareSubtitleSource(sourcePath, root)
			if err != nil {
				t.Fatalf("prepareSubtitleSource(BOM-less UTF-16 crossing sample boundary) error = %v", err)
			}
			if converted == sourcePath {
				t.Fatal("UTF-16 source was reused as UTF-8")
			}
			got, err := os.ReadFile(converted)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, []byte(text)) {
				t.Fatal("converted UTF-16 subtitle does not match the source text")
			}
			cleanup()
			if _, err := os.Stat(converted); !os.IsNotExist(err) {
				t.Fatalf("cleanup left the converted subtitle: %v", err)
			}
		})
	}
}

func TestPrepareSubtitleSourceRejectsDanglingUTF16SurrogateAtRealEOF(t *testing.T) {
	text := "1\n00:00:01,000 --> 00:00:03,000\n真实文件末尾悬挂代理\n"
	for _, test := range []struct {
		name       string
		endianness xunicode.Endianness
		bomPolicy  xunicode.BOMPolicy
	}{
		{name: "bomless le", endianness: xunicode.LittleEndian, bomPolicy: xunicode.IgnoreBOM},
		{name: "bomless be", endianness: xunicode.BigEndian, bomPolicy: xunicode.IgnoreBOM},
		{name: "bom le", endianness: xunicode.LittleEndian, bomPolicy: xunicode.UseBOM},
		{name: "bom be", endianness: xunicode.BigEndian, bomPolicy: xunicode.UseBOM},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, _, err := transform.Bytes(xunicode.UTF16(test.endianness, test.bomPolicy).NewEncoder(), []byte(text))
			if err != nil {
				t.Fatalf("encode UTF-16 fixture: %v", err)
			}
			encoded = append(encoded, utf16SurrogateHighBytes('😀', test.endianness)...)
			root := t.TempDir()
			sourcePath := filepath.Join(root, "episode.zh-Hans.srt")
			if err := os.WriteFile(sourcePath, encoded, 0o640); err != nil {
				t.Fatal(err)
			}
			_, _, err = prepareSubtitleSource(sourcePath, root)
			var subtitleErr *domain.SubtitleError
			if !errors.As(err, &subtitleErr) || subtitleErr.Code != "subtitle_charset_unsupported" {
				t.Fatalf("prepareSubtitleSource(dangling surrogate at EOF) error = %v, want subtitle_charset_unsupported", err)
			}
			if matches, _ := filepath.Glob(filepath.Join(root, ".subtitle-source.*")); len(matches) != 0 {
				t.Fatalf("failed conversion left temporary files: %v", matches)
			}
		})
	}
}

func TestPrepareSubtitleSourceRejectsNullBytesAfterSampleBoundary(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "episode.zh-Hans.srt")
	content := append(bytes.Repeat([]byte("a"), subtitleSourceSampleBytesForTest+16), 0x00)
	if err := os.WriteFile(sourcePath, content, 0o640); err != nil {
		t.Fatal(err)
	}
	_, _, err := prepareSubtitleSource(sourcePath, root)
	var subtitleErr *domain.SubtitleError
	if !errors.As(err, &subtitleErr) || subtitleErr.Code != "subtitle_charset_unsupported" {
		t.Fatalf("prepareSubtitleSource(null after sample boundary) error = %v, want subtitle_charset_unsupported", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, ".subtitle-source.*")); len(matches) != 0 {
		t.Fatalf("failed conversion left temporary files: %v", matches)
	}
}

func TestReadSubtitleSourceFileEnforcesSizeBounds(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty.srt")
	if err := os.WriteFile(empty, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readSubtitleSourceFile(empty); err == nil {
		t.Fatal("readSubtitleSourceFile() accepted an empty subtitle")
	}
	large := filepath.Join(root, "large.srt")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxSubtitleSourceBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readSubtitleSourceFile(large); err == nil {
		t.Fatalf("readSubtitleSourceFile() accepted a subtitle larger than %d bytes", maxSubtitleSourceBytes)
	}
}
