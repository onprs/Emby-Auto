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

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/mediatools"
)

// 该回归使用真实 FFmpeg/FFprobe 构造“首视频流是封面、正片在后面的合成 MKV”，
// 验证转码按探测选中的正片流映射，并用真实产物验证输出完整性校验。
func TestTranscodeSelectsMainVideoStreamWithRealMediaIntegration(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("当前维护环境未安装 FFmpeg")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("当前维护环境未安装 FFprobe")
	}
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "downloads")
	stagingRoot := filepath.Join(root, "staging")
	if err := os.MkdirAll(downloadRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tools := mediatools.New(mediatools.OSExecutor{})
	run := func(args ...string) {
		t.Helper()
		if err := tools.RunFFmpeg(ctx, ffmpegPath, args); err != nil {
			t.Fatalf("FFmpeg %v 失败：%v", args, err)
		}
	}
	realVideo := filepath.Join(root, "real.mkv")
	cover := filepath.Join(root, "cover.png")
	sourcePath := filepath.Join(downloadRoot, "cover-first.mkv")
	run("-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-map", "0:v:0", "-map", "1:a:0", "-c:v", "libx264", "-preset", "ultrafast",
		"-pix_fmt", "yuv420p", "-threads", "1", "-c:a", "aac", "-f", "matroska", realVideo)
	run("-v", "error", "-y", "-f", "lavfi", "-i", "color=c=red:s=120x160:d=1", "-frames:v", "1", cover)
	// 封面视频流排在正片之前，与审查复现使用的封装一致。
	run("-v", "error", "-y", "-i", cover, "-i", realVideo,
		"-map", "0:v", "-map", "1:v", "-map", "1:a", "-c", "copy", "-f", "matroska", sourcePath)

	inputProbe, err := tools.ProbeWithPacketCounts(ctx, ffprobePath, sourcePath)
	if err != nil {
		t.Fatalf("源文件探测失败：%v", err)
	}
	mainVideo, ok := domain.SelectMainVideoStream(inputProbe)
	if !ok || mainVideo.Index != 1 || mainVideo.PacketCount < 90 {
		t.Fatalf("主视频流选择 = %#v, %v", mainVideo, ok)
	}

	taskID, operationID := uuid.New(), uuid.New()
	profile := domain.TranscodeProfile{
		Name: "integration-h264", VideoCodec: "h264", Encoder: "libx264", Container: "matroska",
		FileExtension: "mkv", QualityMode: "crf", QualityValue: 28, AudioPolicy: "transcode",
		AudioCodec: "aac", Preset: "ultrafast", PixelFormat: "yuv420p", ThreadCount: 1, MaxConcurrency: 1,
	}
	store := &transcodeStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, VideoState: domain.VideoTranscoding,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "cover-first.mkv",
		TranscodeProfileID: uuid.New(), TranscodeProfile: profile,
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E01 - Cover", VideoName: "Show - S01E01 - Cover.mkv"},
	}}
	configuration := mediaTestConfiguration(stagingRoot)
	configuration.configuration.Settings.Paths.FFmpegPath = ffmpegPath
	configuration.configuration.Settings.Paths.FFprobePath = ffprobePath
	handler := NewTranscodeRunHandler(configuration, tools, store)
	if err := handler.Handle(ctx, domain.Operation{ID: operationID, ResourceType: "episode_task", ResourceID: taskID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.completion.FilePath == "" {
		t.Fatal("转码未完成媒体产物记录")
	}
	outputProbe, err := tools.ProbeWithPacketCounts(ctx, ffprobePath, store.completion.FilePath)
	if err != nil {
		t.Fatalf("产物探测失败：%v", err)
	}
	outputVideo, ok := domain.SelectMainVideoStream(outputProbe)
	if !ok || outputVideo.PacketCount < 90 || outputVideo.DurationSeconds < 3.5 {
		t.Fatalf("产物主视频流 = %#v, %v", outputVideo, ok)
	}

	// 旧映射 0:v:0 会选中封面流，产物只有 1 帧；同一校验必须拒绝它。
	oldMappingOutput := filepath.Join(root, "old-mapping.mkv")
	run("-v", "error", "-y", "-i", sourcePath,
		"-map", "0:v:0", "-map", "0:a?", "-sn",
		"-c:v", "libx264", "-crf", "28", "-preset", "ultrafast",
		"-pix_fmt", "yuv420p", "-threads", "1", "-c:a", "aac", "-f", "matroska", oldMappingOutput)
	oldMappingProbe, err := tools.ProbeWithPacketCounts(ctx, ffprobePath, oldMappingOutput)
	if err != nil {
		t.Fatalf("旧映射产物探测失败：%v", err)
	}
	expectation, err := domain.BuildTranscodeProbeExpectation(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.ValidateTranscodeProbe(expectation, inputProbe, oldMappingProbe, oldMappingOutput); err == nil {
		t.Fatal("输出完整性校验接受了封面冒充正片的产物")
	}
}

// 附加图片流必须被排除；真实 MP4 的封面轨道常见 attached_pic 标记。
func TestTranscodeExcludesAttachedPictureStreamWithRealMediaIntegration(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("当前维护环境未安装 FFmpeg")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("当前维护环境未安装 FFprobe")
	}
	root := t.TempDir()
	ctx := context.Background()
	tools := mediatools.New(mediatools.OSExecutor{})
	run := func(args ...string) {
		t.Helper()
		if err := tools.RunFFmpeg(ctx, ffmpegPath, args); err != nil {
			t.Fatalf("FFmpeg %v 失败：%v", args, err)
		}
	}
	realVideo := filepath.Join(root, "real.mp4")
	cover := filepath.Join(root, "cover.jpg")
	attached := filepath.Join(root, "attached.mp4")
	run("-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-map", "0:v:0", "-map", "1:a:0",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-threads", "1",
		"-c:a", "aac", "-f", "mp4", realVideo)
	run("-v", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=120x160:d=1", "-frames:v", "1", cover)
	run("-v", "error", "-y", "-i", realVideo, "-i", cover, "-map", "0", "-map", "1",
		"-c", "copy", "-disposition:v:1", "attached_pic", "-f", "mp4", attached)
	probe, err := tools.ProbeWithPacketCounts(ctx, ffprobePath, attached)
	if err != nil {
		t.Fatalf("探测失败：%v", err)
	}
	mainVideo, ok := domain.SelectMainVideoStream(probe)
	if !ok || mainVideo.Index != 0 {
		t.Fatalf("附加图片被选为正片：%#v, %v", mainVideo, ok)
	}
	profile := domain.DefaultTranscodeProfile()
	args, err := domain.BuildTranscodeFFmpegArgs(profile, attached, filepath.Join(root, "out.mp4"), mainVideo.Index)
	if err != nil {
		t.Fatal(err)
	}
	if !containsArgumentPair(args, "-map", "0:0") {
		t.Fatalf("转码参数未映射正片流：%#v", args)
	}
}

func containsArgumentPair(args []string, first, second string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == first && args[index+1] == second {
			return true
		}
	}
	return false
}

// 包数与时长相同的多视频流必须尊重容器默认轨，不能固定回退到首个流。
func TestTranscodePrefersDefaultTrackOnEqualMetricsWithRealMediaIntegration(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("当前维护环境未安装 FFmpeg")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("当前维护环境未安装 FFprobe")
	}
	root := t.TempDir()
	ctx := context.Background()
	tools := mediatools.New(mediatools.OSExecutor{})
	run := func(args ...string) {
		t.Helper()
		if err := tools.RunFFmpeg(ctx, ffmpegPath, args); err != nil {
			t.Fatalf("FFmpeg %v 失败：%v", args, err)
		}
	}
	small := filepath.Join(root, "small.mkv")
	large := filepath.Join(root, "large.mkv")
	audio := filepath.Join(root, "audio.m4a")
	source := filepath.Join(root, "multi-video.mkv")
	run("-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=3",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-threads", "1", "-f", "matroska", small)
	run("-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=3",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-threads", "1", "-f", "matroska", large)
	run("-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-c:a", "aac", "-f", "mp4", audio)
	run("-v", "error", "-y", "-i", small, "-i", audio, "-i", large,
		"-map", "0:v", "-map", "1:a", "-map", "2:v", "-c", "copy",
		"-disposition:v:0", "0", "-disposition:v:1", "default", "-f", "matroska", source)

	probe, err := tools.ProbeWithPacketCounts(ctx, ffprobePath, source)
	if err != nil {
		t.Fatalf("探测失败：%v", err)
	}
	mainVideo, ok := domain.SelectMainVideoStream(probe)
	if !ok || mainVideo.Index != 2 || !mainVideo.Default {
		t.Fatalf("默认视频轨选择 = %#v, %v, want index 2 default", mainVideo, ok)
	}
	if mainVideo.PacketCount != 75 || mainVideo.PacketCount < 2 {
		t.Fatalf("默认视频轨包数 = %d", mainVideo.PacketCount)
	}
	profile := domain.TranscodeProfile{
		Name: "default-track", VideoCodec: "h264", Encoder: "libx264", Container: "matroska",
		FileExtension: "mkv", QualityMode: "crf", QualityValue: 30, AudioPolicy: "transcode",
		AudioCodec: "aac", Preset: "ultrafast", PixelFormat: "yuv420p", ThreadCount: 1, MaxConcurrency: 1,
	}
	output := filepath.Join(root, "out.mkv")
	args, err := domain.BuildTranscodeFFmpegArgs(profile, source, output, mainVideo.Index)
	if err != nil {
		t.Fatal(err)
	}
	if !containsArgumentPair(args, "-map", "0:2") {
		t.Fatalf("转码参数未映射默认视频轨：%#v", args)
	}
	run(args...)
	dimensions, err := exec.Command(ffprobePath, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", output).Output()
	if err != nil {
		t.Fatalf("读取产物尺寸失败：%v", err)
	}
	if strings.TrimSpace(string(dimensions)) != "320,240" {
		t.Fatalf("产物尺寸 = %q, want 320,240 (default track)", strings.TrimSpace(string(dimensions)))
	}
}
