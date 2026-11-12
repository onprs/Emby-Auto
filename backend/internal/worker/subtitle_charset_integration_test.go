//go:build integration

package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/mediatools"
)

// 使用真实 FFmpeg 验证 GBK/BIG5 外挂字幕能被检测、转换为 UTF-8 并生成简体 ASS。
func TestSubtitlePrepareConvertsLegacyChineseEncodingsWithRealFFmpegIntegration(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("当前维护环境未安装 FFmpeg")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("当前维护环境未安装 FFprobe")
	}
	root := t.TempDir()
	tools := mediatools.New(mediatools.OSExecutor{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	encode := func(content string, transformer transform.Transformer) []byte {
		t.Helper()
		encoded, _, encodeErr := transform.Bytes(transformer, []byte(content))
		if encodeErr != nil {
			t.Fatalf("编码字幕失败：%v", encodeErr)
		}
		return encoded
	}
	gbkText := "1\n00:00:01,000 --> 00:00:03,000\n这是简体中文字幕测试，包含常用字的一是不了在人有我他这中大来上国个到说们为子和你地出道也时年得就那要下以生会自着去之过家学对可她里后小么心多天而能好都然没日于起还发成事只作当想看\n"
	big5Text := "1\n00:00:01,000 --> 00:00:03,000\n這是繁體中文字幕測試，包含常用字的的一是不了在人有我他這中大來上國個到說們為子和你地出道也時年得就那要下以生會自著去之過家學對可她裡後小麼心多天而能好都然沒日於起還發成事只作當想看\n"
	tests := []struct {
		name            string
		subtitleName    string
		content         []byte
		wantSimplified  string
		wantTraditional string
	}{
		{
			name: "gbk", subtitleName: "episode.简体.GBK.srt",
			content:        encode(gbkText, simplifiedchinese.GB18030.NewEncoder()),
			wantSimplified: "这是简体中文字幕测试", wantTraditional: "這是繁體中文字幕測試",
		},
		{
			name: "big5", subtitleName: "episode.繁體.BIG5.srt",
			content:        encode(big5Text, traditionalchinese.Big5.NewEncoder()),
			wantSimplified: "这是繁体中文字幕测试", wantTraditional: "這是繁體中文字幕測試",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			taskRoot := filepath.Join(root, test.name)
			downloadRoot := filepath.Join(taskRoot, "download")
			stagingRoot := filepath.Join(taskRoot, "staging")
			if err := os.MkdirAll(downloadRoot, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := tools.RunFFmpeg(ctx, ffmpegPath, []string{
				"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=128x96:rate=10:duration=1",
				"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-threads", "1",
				"-f", "matroska", filepath.Join(downloadRoot, "episode.mkv"),
			}); err != nil {
				t.Fatalf("生成测试视频失败：%v", err)
			}
			if err := os.WriteFile(filepath.Join(downloadRoot, test.subtitleName), test.content, 0o640); err != nil {
				t.Fatal(err)
			}
			taskID := uuid.New()
			store := &subtitleStoreStub{command: domain.TaskMediaCommand{
				TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
				SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
				Names: domain.EpisodeFileNames{BaseName: "Show - S01E01", SubtitleName: "Show - S01E01.ass"},
				ExternalSubtitles: []domain.TaskExternalSubtitle{{
					SourceFileID: uuid.New(), RelativePath: test.subtitleName,
					Format: domain.SubtitleSRT,
				}},
			}}
			configuration := mediaTestConfiguration(stagingRoot)
			configuration.configuration.Settings.Paths.FFmpegPath = ffmpegPath
			configuration.configuration.Settings.Paths.FFprobePath = ffprobePath
			if err := NewSubtitlePrepareHandler(configuration, tools, store).Handle(
				ctx, domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID},
			); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			content, readErr := os.ReadFile(store.completion.FilePath)
			if readErr != nil {
				t.Fatalf("读取产物失败：%v", readErr)
			}
			text := string(content)
			if !strings.Contains(text, test.wantSimplified) || strings.Contains(text, test.wantTraditional) {
				t.Fatalf("产物未转换为简体：%q", text)
			}
			if err := domain.ValidateASS(content); err != nil {
				t.Fatalf("产物 ASS 校验失败：%v", err)
			}
			if matches, _ := filepath.Glob(filepath.Join(stagingRoot, taskID.String(), ".subtitle-source.*")); len(matches) != 0 {
				t.Fatalf("转换临时字幕未清理：%v", matches)
			}
		})
	}
}
