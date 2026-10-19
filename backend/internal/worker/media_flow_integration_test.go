//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/mediatools"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

func TestSingleTaskRealMediaProcessingThroughCleanupIntegration(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("当前维护环境未安装 FFmpeg")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("当前维护环境未安装 FFprobe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root := t.TempDir()
	downloadRoot, stagingRoot, libraryRoot := filepath.Join(root, "downloads"), filepath.Join(root, "staging"), filepath.Join(root, "library")
	savePath := filepath.Join(downloadRoot, "single-task")
	for _, path := range []string{savePath, libraryRoot} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	tools := mediatools.New(mediatools.OSExecutor{})
	if err := tools.RunFFmpeg(ctx, ffmpegPath, []string{
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000", "-t", "2",
		"-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", "-c:a", "flac", filepath.Join(savePath, "source.mkv"),
	}); err != nil {
		t.Fatalf("生成测试媒体失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(savePath, "source.zh-Hans.srt"), []byte("1\n00:00:00,100 --> 00:00:01,800\n中文字幕完整流程测试\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	configuration := mediaTestConfiguration(stagingRoot)
	configuration.configuration.Settings.Paths.FFmpegPath = ffmpegPath
	configuration.configuration.Settings.Paths.FFprobePath = ffprobePath
	configuration.configuration.Settings.Paths.DownloadRoot = downloadRoot
	configuration.configuration.Settings.Paths.AnimeLibraryRoot = libraryRoot
	taskID, profileID, sourceID := uuid.New(), uuid.New(), uuid.New()
	baseName := "媒体流程测试 - S01E01 - 起始"
	command := domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, VideoState: domain.VideoTranscoding,
		SubtitleState: domain.SubtitleExtractingConverting, SavePath: savePath,
		SourceVideoFileID: sourceID, SourceVideoRelativePath: "source.mkv", TranscodeProfileID: profileID,
		TranscodeProfile: domain.TranscodeProfile{
			Name: "软件转码测试", VideoCodec: "h264", Encoder: "libx264", Container: "mp4", FileExtension: "mp4",
			QualityMode: "crf", QualityValue: 23, AudioPolicy: "transcode", AudioCodec: "aac",
			Preset: "fast", PixelFormat: "yuv420p", ThreadCount: 2, MaxConcurrency: 1,
		},
		ExternalSubtitles: []domain.TaskExternalSubtitle{{SourceFileID: uuid.New(), RelativePath: "source.zh-Hans.srt", Language: "zh-Hans", Format: domain.SubtitleSRT}},
		Names:             domain.EpisodeFileNames{BaseName: baseName, VideoName: baseName + ".mp4", SubtitleName: baseName + ".ass"},
	}
	operation := func() domain.Operation {
		return domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID}
	}
	timeStage := func(name string, run func() error) {
		t.Helper()
		started := time.Now()
		if err := run(); err != nil {
			t.Fatalf("%s失败：%v", name, err)
		}
		t.Logf("%s耗时：%s", name, time.Since(started))
	}
	started := time.Now()
	videoStore := &transcodeStoreStub{command: command}
	subtitleStore := &subtitleStoreStub{command: command}
	timeStage("视频转码与校验", func() error {
		return NewTranscodeRunHandler(configuration, tools, videoStore).Handle(ctx, operation())
	})
	timeStage("字幕转换与校验", func() error {
		return NewSubtitlePrepareHandler(configuration, tools, subtitleStore).Handle(ctx, operation())
	})
	artifact := func(completion domain.MediaArtifactCompletion) domain.MediaArtifact {
		t.Helper()
		if err := domain.ValidateArtifactCompletion(completion); err != nil {
			t.Fatal(err)
		}
		return domain.MediaArtifact{
			ID: uuid.New(), TaskID: taskID, SourceFileID: completion.SourceFileID, TranscodeProfileID: completion.TranscodeProfileID,
			Kind: completion.Kind, BaseName: completion.BaseName, FilePath: completion.FilePath,
			Format: completion.Format, SizeBytes: completion.SizeBytes, ChecksumSHA256: completion.ChecksumSHA256,
		}
	}
	video, subtitle := artifact(videoStore.completion), artifact(subtitleStore.completion)
	finalizeStore := &finalizeStoreStub{command: domain.FinalizeMediaCommand{
		TaskID: taskID, State: domain.TaskFinalizing, TranscodeProfileID: profileID, Video: video, Subtitle: subtitle,
	}}
	timeStage("产物配对校验", func() error { return NewMediaFinalizeHandler(finalizeStore).Handle(ctx, operation()) })
	if !finalizeStore.completed {
		t.Fatal("产物配对未完成")
	}
	importID := uuid.New()
	importStore := &taskImportStoreStub{command: domain.ImportCommand{
		TaskID: taskID, ImportID: importID, TaskState: domain.TaskImporting, ImportState: "running",
		SeriesTitle: "媒体流程测试", Season: 1, BaseName: baseName, Video: video, Subtitle: subtitle,
	}}
	importOperation := operation()
	importOperation.Payload, err = json.Marshal(map[string]uuid.UUID{"importId": importID})
	if err != nil {
		t.Fatal(err)
	}
	timeStage("复制入库与校验", func() error {
		return NewEmbyImportHandler(configuration, importStore, newTestImportedLibraryAccess()).Handle(ctx, importOperation)
	})
	cleanupID := uuid.New()
	cleanupStore := &cleanupStoreStub{command: domain.CleanupCommand{
		TaskID: taskID, CleanupID: cleanupID, TaskState: domain.TaskImported, CleanupState: domain.CleanupRunning,
		DownloadID: uuid.New(), TorrentHash: "media-flow-test", DownloadPath: savePath,
		StagedVideoPath: video.FilePath, StagedSubtitlePath: subtitle.FilePath, DownloadRemovable: true,
	}}
	cleanupOperation := operation()
	cleanupOperation.Payload, err = json.Marshal(map[string]uuid.UUID{"cleanupId": cleanupID})
	if err != nil {
		t.Fatal(err)
	}
	torrentClient := &cleanupTorrentStub{}
	timeStage("源文件与暂存文件清理", func() error {
		return NewCleanupRunHandler(configuration, cleanupStore, func(qbittorrent.ClientOptions) (CleanupTorrentClient, error) {
			return torrentClient, nil
		}).Handle(ctx, cleanupOperation)
	})
	if !cleanupStore.completion.TorrentRemoved || !cleanupStore.completion.StagedFilesRemoved || len(torrentClient.deleteCalls) != 1 {
		t.Fatalf("清理结果不完整：%#v", cleanupStore.completion)
	}
	for _, path := range []string{savePath, video.FilePath, subtitle.FilePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("清理后仍有缓存 %q：%v", path, err)
		}
	}
	for _, item := range []struct {
		path     string
		artifact domain.MediaArtifact
	}{
		{path: importStore.completion.DestinationVideoPath, artifact: video},
		{path: importStore.completion.DestinationSubtitlePath, artifact: subtitle},
	} {
		if err := verifyFileAgainstArtifact(ctx, item.path, item.artifact); err != nil {
			t.Fatalf("清理后正式资源不完整：%v", err)
		}
	}
	t.Logf("单任务媒体处理至清理总耗时：%s，视频产物：%d 字节，字幕产物：%d 字节", time.Since(started), video.SizeBytes, subtitle.SizeBytes)
}
