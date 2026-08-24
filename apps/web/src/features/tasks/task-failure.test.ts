import { describe, expect, it } from 'vitest';

import type { Acquisition, AcquisitionTaskSummary, Task } from '@/api/generated/types.gen';
import { acquisitionFailureInfo, sanitizeTechnicalDetails, taskFailureInfo } from '@/features/tasks/task-failure';

function task(overrides: Partial<Task>): Task {
  return {
    id: '11111111-1111-1111-1111-111111111111',
    acquisitionId: '22222222-2222-2222-2222-222222222222',
    downloadId: '33333333-3333-3333-3333-333333333333',
    mediaType: 'episode',
    seriesTitle: '测试番剧',
    sourceSeason: 1,
    sourceEpisode: 1,
    targetSeason: 1,
    targetEpisode: 1,
    state: 'failed',
    videoState: 'failed',
    subtitleState: 'ass_ready',
    version: 2,
    failureStage: 'video',
    operations: [],
    actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
    createdAt: '2026-07-25T01:00:00Z',
    updatedAt: '2026-07-25T02:00:00Z',
    ...overrides,
  };
}

function acquisitionTaskSummary(overrides: Partial<AcquisitionTaskSummary>): AcquisitionTaskSummary {
  return {
    id: '99999999-9999-9999-9999-999999999999',
    mediaType: 'episode',
    downloadId: '33333333-3333-3333-3333-333333333333',
    state: 'failed',
    videoState: 'failed',
    subtitleState: 'ass_ready',
    canRetry: true,
    updatedAt: '2026-07-25T02:00:00Z',
    ...overrides,
  };
}

function acquisition(overrides: Partial<Acquisition>): Acquisition {
  return {
    id: '44444444-4444-4444-4444-444444444444',
    mediaType: 'episode',
    seriesId: '55555555-5555-5555-5555-555555555555',
    seriesTitle: '测试番剧',
    sourceKind: 'rss',
    tasks: [],
    mapping: { selectedVideoCount: 0, mappedVideoCount: 0, complete: false },
    aggregateStatus: 'failed',
    currentStage: 'download',
    overallProgress: 0.1,
    stages: [
      { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
      { key: 'download', status: 'failed', progress: 0, completedItems: 0, totalItems: 1 },
      { key: 'mapping', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'transcode', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'subtitle', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'rename', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'organize', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'review', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'import', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
    ],
    createdAt: '2026-07-25T01:00:00Z',
    updatedAt: '2026-07-25T02:00:00Z',
    ...overrides,
  };
}

describe('taskFailureInfo', () => {
  it('describes an unsupported source instead of exposing the FFmpeg exception', () => {
    const info = taskFailureInfo(task({
      errorCode: 'ffmpeg_transcode_failed',
      errorMessage: 'C:\\media\\downloads\\show\\episode01.mkv: Invalid data found when processing input',
    }));

    expect(info?.summary).toBe('视频转码失败：源文件格式不受支持');
    expect(info?.relatedResource).toContain('源视频');
    expect(info?.canRetry).toBe(false);
    expect(info?.recommendation).toContain('重新下载');
    expect(info?.summary).not.toContain('ffmpeg_transcode_failed');
  });

  it('describes an unavailable Emby service and keeps recovery available', () => {
    const info = taskFailureInfo(task({
      failureStage: 'import',
      errorCode: 'emby_refresh_failed',
      errorMessage: 'dial tcp 127.0.0.1:8096: connect: connection refused',
      operations: [{
        id: '66666666-6666-6666-6666-666666666666',
        kind: 'emby.import',
        status: 'failed',
        maxAttempts: 3,
        attemptCount: 2,
        errorCode: 'emby_refresh_failed',
        errorMessage: 'connection refused',
        updatedAt: '2026-07-25T02:00:00Z',
      }],
    }));

    expect(info?.summary).toBe('入库失败：无法连接 Emby 服务');
    expect(info?.canRetry).toBe(true);
    expect(info?.retryKind).toBe('task');
    expect(info?.recommendation).toContain('服务连接');
    expect(info?.attemptLabel).toContain('2/3');
  });

  it.each([
    ['The process cannot access the file because it is being used by another process', '清理失败：文件正在被其他进程占用', '关闭占用文件的程序'],
    ['remove C:\\media\\staging\\episode01.mkv: Access is denied', '清理失败：没有文件删除权限', '检查目录权限'],
  ])('maps cleanup failure %s', (message, summary, recommendation) => {
    const info = taskFailureInfo(task({
      state: 'imported',
      videoState: 'video_ready',
      errorCode: undefined,
      errorMessage: undefined,
      failureStage: undefined,
      actions: { canRetry: false, canCancel: false, canReview: false, canImport: false },
      cleanup: {
        id: '77777777-7777-7777-7777-777777777777',
        attempt: 3,
        status: 'failed',
        torrentRemoved: true,
        stagedFilesRemoved: false,
        errorCode: 'cleanup_delete_failed',
        errorMessage: message,
        createdAt: '2026-07-25T01:00:00Z',
        updatedAt: '2026-07-25T03:00:00Z',
      },
    }));

    expect(info?.summary).toBe(summary);
    expect(info?.retryKind).toBe('cleanup');
    expect(info?.canRetry).toBe(true);
    expect(info?.attemptLabel).toContain('第 3 次');
    expect(info?.recommendation).toContain(recommendation);
  });

  it.each([
    ['subtitle_output_commit_failed', 'subtitle', '字幕处理失败：无法保存字幕结果', true],
    ['source_video_path_invalid', 'video', '视频转码失败：源视频路径不安全', false],
    ['library_path_invalid', 'import', '入库失败：媒体库路径不安全', false],
  ] as const)('maps backend code %s without relying on an English exception', (errorCode, failureStage, summary, canRetry) => {
    const info = taskFailureInfo(task({ errorCode, errorMessage: undefined, failureStage }));

    expect(info?.summary).toBe(summary);
    expect(info?.canRetry).toBe(canRetry);
  });

  it('guides configuration failures to settings without offering a pointless retry', () => {
    const info = taskFailureInfo(task({ errorCode: 'configuration_unavailable', errorMessage: 'runtime configuration is unavailable' }));

    expect(info?.summary).toBe('视频转码失败：媒体处理配置不可用');
    expect(info?.canRetry).toBe(false);
    expect(info?.retryKind).toBe('none');
    expect(info?.recommendation).toContain('设置');
  });

  it('requires a new download when the source file is missing', () => {
    const info = taskFailureInfo(task({ errorCode: 'source_video_probe_failed', errorMessage: 'open D:\\downloads\\episode01.mkv: The system cannot find the file specified' }));

    expect(info?.summary).toBe('视频转码失败：源文件不存在');
    expect(info?.canRetry).toBe(false);
    expect(info?.recommendation).toContain('重新下载');
  });

  it('uses the fixed unknown-error copy and never puts raw internals in the summary', () => {
    const info = taskFailureInfo(task({ errorCode: 'process_error', errorMessage: '{"stack":"panic at worker.go:20"}' }));

    expect(info?.summary).toBe('视频转码失败：未能识别具体失败原因，请查看技术详情或运行日志。');
    expect(info?.detail).toBe('未能识别具体失败原因，请查看技术详情或运行日志。');
    expect(info?.summary).not.toContain('process_error');
    expect(info?.summary).not.toContain('panic');
  });
});

describe('acquisitionFailureInfo', () => {
  it('shows an expired torrent in the main task list', () => {
    const info = acquisitionFailureInfo(acquisition({
      downloadId: '88888888-8888-8888-8888-888888888888',
      download: {
        id: '88888888-8888-8888-8888-888888888888',
        attempt: 1,
        status: 'failed',
        progress: 0,
        failureStage: 'enqueue',
        errorCode: 'qbittorrent_enqueue_failed',
        errorMessage: 'torrent download returned HTTP 404 Not Found',
        updatedAt: '2026-07-25T02:00:00Z',
      },
    }));

    expect(info?.summary).toBe('下载失败：种子文件已失效');
    expect(info?.canRetry).toBe(false);
    expect(info?.recommendation).toContain('更换下载资源');
  });

  it('sends incomplete episode mapping to the mapping workflow instead of retrying', () => {
    const info = acquisitionFailureInfo(acquisition({
      downloadId: '88888888-8888-8888-8888-888888888888',
      download: {
        id: '88888888-8888-8888-8888-888888888888',
        attempt: 1,
        status: 'failed',
        progress: 1,
        failureStage: 'materialize',
        errorCode: 'episode_mapping_required',
        updatedAt: '2026-07-25T02:00:00Z',
      },
    }));

    expect(info).toBeNull();
  });

  it('labels post-download materialization failures as media preparation failures', () => {
    const info = acquisitionFailureInfo(acquisition({
      downloadId: '88888888-8888-8888-8888-888888888888',
      download: {
        id: '88888888-8888-8888-8888-888888888888',
        attempt: 1,
        status: 'failed',
        progress: 1,
        failureStage: 'materialize',
        errorCode: 'media_storage_unavailable',
        updatedAt: '2026-07-25T02:00:00Z',
      },
    }));

    expect(info?.summary).toBe('准备媒体处理失败：无法保存处理进度');
    expect(info?.canRetry).toBe(true);
    expect(info?.retryKind).toBe('download');
    expect(info?.retryLabel).toBe('重试准备处理');
  });

  it('shows disk exhaustion as a recoverable download failure', () => {
    const info = acquisitionFailureInfo(acquisition({
      downloadId: '88888888-8888-8888-8888-888888888888',
      download: {
        id: '88888888-8888-8888-8888-888888888888',
        attempt: 2,
        status: 'failed',
        progress: 0.42,
        failureStage: 'sync',
        errorCode: 'download_storage_unavailable',
        errorMessage: 'write C:\\media\\downloads\\episode01.mkv: no space left on device',
        updatedAt: '2026-07-25T02:00:00Z',
      },
    }));

    expect(info?.summary).toBe('下载失败：磁盘空间不足');
    expect(info?.canRetry).toBe(true);
    expect(info?.retryKind).toBe('download');
    expect(info?.attemptLabel).toContain('第 2 次');
  });
});

describe('taskFailureInfo dual branch', () => {
  it('shows both video and subtitle failures together with distinct reasons', () => {
    const info = taskFailureInfo(task({
      state: 'failed',
      videoState: 'failed',
      subtitleState: 'failed',
      failureStage: 'video',
      errorCode: 'ffmpeg_transcode_failed',
      errorMessage: 'video failed',
      operations: [
        {
          id: 'aaaaaaa1-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
          kind: 'transcode.run',
          status: 'failed',
          maxAttempts: 3,
          attemptCount: 1,
          errorCode: 'ffmpeg_transcode_failed',
          errorMessage: 'video encode error',
          updatedAt: '2026-07-25T02:00:00Z',
        },
        {
          id: 'bbbbbbb2-bbbb-bbbb-bbbb-bbbbbbbbbbb2',
          kind: 'subtitle.prepare',
          status: 'failed',
          maxAttempts: 3,
          attemptCount: 2,
          errorCode: 'ffmpeg_subtitle_failed',
          errorMessage: 'subtitle convert error',
          updatedAt: '2026-07-25T02:30:00Z',
        },
      ],
    }));
    expect(info?.summary).toContain('视频和字幕处理失败');
    expect(info?.stageLabel).toBe('视频和字幕');
    expect(info?.branches).toHaveLength(2);
    expect(info?.branches?.[0].stage).toBe('video');
    expect(info?.branches?.[1].stage).toBe('subtitle');
    expect(info?.branches?.[0].latestOperationKind).toBe('transcode.run');
    expect(info?.branches?.[1].latestOperationKind).toBe('subtitle.prepare');
    expect(info?.branches?.[0].detail).toContain('FFmpeg');
    expect(info?.branches?.[1].detail).toContain('字幕');
    expect(info?.branches?.[0].latestOperationId).toBe('aaaaaaa1-aaaa-aaaa-aaaa-aaaaaaaaaaa1');
    expect(info?.branches?.[1].latestOperationId).toBe('bbbbbbb2-bbbb-bbbb-bbbb-bbbbbbbbbbb2');
    expect(info?.canRetry).toBe(true);
    expect(info?.retryLabel).toBe('重试任务');
    // 技术详情仍需 sanitize，且包含两个分支
    expect(info?.technicalDetails).not.toContain('C:\\media');
    expect(info?.branches?.[0].technicalDetails).not.toContain('C:\\media');
  });

  it('keeps single video branch unchanged', () => {
    const info = taskFailureInfo(task({
      videoState: 'failed',
      subtitleState: 'ass_ready',
      failureStage: 'video',
      operations: [{
        id: 'ccccccc3-cccc-cccc-cccc-ccccccccccc3',
        kind: 'transcode.run',
        status: 'failed',
        maxAttempts: 3,
        attemptCount: 1,
        errorCode: 'ffmpeg_transcode_failed',
        errorMessage: 'video error',
        updatedAt: '2026-07-25T02:00:00Z',
      }],
    }));
    expect(info?.summary).toBe('视频转码失败：FFmpeg 未能完成视频转换');
    expect(info?.branches).toBeUndefined();
    expect(info?.stage).toBe('video');
  });

  it('handles processing stuck with dual failed via same dual path', () => {
    const info = taskFailureInfo(task({
      state: 'processing',
      videoState: 'failed',
      subtitleState: 'failed',
      failureStage: undefined,
      operations: [
        {
          id: 'ddddddd4-dddd-dddd-dddd-ddddddddddd4',
          kind: 'transcode.run',
          status: 'failed',
          maxAttempts: 3,
          attemptCount: 1,
          errorCode: 'ffmpeg_transcode_failed',
          errorMessage: 'video error',
          updatedAt: '2026-07-25T02:00:00Z',
        },
        {
          id: 'eeeeeee5-eeee-eeee-eeee-eeeeeeeeeee5',
          kind: 'subtitle.prepare',
          status: 'failed',
          maxAttempts: 3,
          attemptCount: 1,
          errorCode: 'ffmpeg_subtitle_failed',
          errorMessage: 'subtitle error',
          updatedAt: '2026-07-25T02:00:00Z',
        },
      ],
    }));
    expect(info?.summary).toContain('视频和字幕处理失败');
    expect(info?.branches).toHaveLength(2);
    expect(info?.canRetry).toBe(true);
  });
});

describe('taskFailureInfo cancelled', () => {
  it('shows retry for cancelled + video failed + subtitle ready', () => {
    const info = taskFailureInfo(task({
      state: 'cancelled',
      videoState: 'failed',
      subtitleState: 'ass_ready',
      failureStage: undefined,
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      operations: [{
        id: 'aaaaaaa6-aaaa-aaaa-aaaa-aaaaaaaaaaa6',
        kind: 'transcode.run',
        status: 'failed',
        maxAttempts: 3,
        attemptCount: 1,
        errorCode: 'ffmpeg_transcode_failed',
        errorMessage: 'video failed',
        updatedAt: '2026-07-25T02:00:00Z',
      }],
    }));
    expect(info?.summary).toContain('视频转码失败');
    expect(info?.canRetry).toBe(true);
    expect(info?.retryKind).toBe('task');
  });
  it('rejects ordinary cancelled with cancelled branches', () => {
    const info = taskFailureInfo(task({
      state: 'cancelled',
      videoState: 'cancelled',
      subtitleState: 'cancelled',
      failureStage: undefined,
      actions: { canRetry: false, canCancel: false, canReview: false, canImport: false },
    }));
    expect(info).toBeNull();
  });
  it('rejects cancelled with active branch', () => {
    const info = taskFailureInfo(task({
      state: 'cancelled',
      videoState: 'transcoding',
      subtitleState: 'ass_ready',
      failureStage: undefined,
      actions: { canRetry: false, canCancel: false, canReview: false, canImport: false },
    }));
    expect(info).toBeNull();
  });
  it('obeys backend canRetry for same cancelled branch', () => {
    const withRetry = taskFailureInfo(task({
      state: 'cancelled',
      videoState: 'failed',
      subtitleState: 'ass_ready',
      failureStage: undefined,
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      operations: [{
        id: 'aaaaaaa6-aaaa-aaaa-aaaa-aaaaaaaaaaa6',
        kind: 'transcode.run',
        status: 'failed',
        maxAttempts: 3,
        attemptCount: 1,
        errorCode: 'ffmpeg_transcode_failed',
        errorMessage: 'video failed',
        updatedAt: '2026-07-25T02:00:00Z',
      }],
    }));
    const withoutRetry = taskFailureInfo(task({
      state: 'cancelled',
      videoState: 'failed',
      subtitleState: 'ass_ready',
      failureStage: undefined,
      actions: { canRetry: false, canCancel: false, canReview: false, canImport: false },
      operations: [{
        id: 'aaaaaaa6-aaaa-aaaa-aaaa-aaaaaaaaaaa6',
        kind: 'transcode.run',
        status: 'failed',
        maxAttempts: 3,
        attemptCount: 1,
        errorCode: 'ffmpeg_transcode_failed',
        errorMessage: 'video failed',
        updatedAt: '2026-07-25T02:00:00Z',
      }],
    }));
    expect(withRetry).not.toBeNull();
    expect(withRetry?.canRetry).toBe(true);
    expect(withRetry?.retryLabel).toBe('重试任务');
    expect(withoutRetry).toBeNull();
  });
  it('hides dual failed cancelled when backend denies retry', () => {
    const denied = taskFailureInfo(task({
      state: 'cancelled',
      videoState: 'failed',
      subtitleState: 'failed',
      failureStage: undefined,
      actions: { canRetry: false, canCancel: false, canReview: false, canImport: false },
      operations: [
        {
          id: 'aaaaaaa1-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
          kind: 'transcode.run',
          status: 'failed',
          maxAttempts: 3,
          attemptCount: 1,
          errorCode: 'ffmpeg_transcode_failed',
          errorMessage: 'video encode error',
          updatedAt: '2026-07-25T02:00:00Z',
        },
        {
          id: 'bbbbbbb2-bbbb-bbbb-bbbb-bbbbbbbbbbb2',
          kind: 'subtitle.prepare',
          status: 'failed',
          maxAttempts: 3,
          attemptCount: 1,
          errorCode: 'ffmpeg_subtitle_failed',
          errorMessage: 'subtitle error',
          updatedAt: '2026-07-25T02:00:00Z',
        },
      ],
    }));
    const allowed = taskFailureInfo(task({
      state: 'cancelled',
      videoState: 'failed',
      subtitleState: 'failed',
      failureStage: undefined,
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      operations: [
        {
          id: 'aaaaaaa1-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
          kind: 'transcode.run',
          status: 'failed',
          maxAttempts: 3,
          attemptCount: 1,
          errorCode: 'ffmpeg_transcode_failed',
          errorMessage: 'video encode error',
          updatedAt: '2026-07-25T02:00:00Z',
        },
        {
          id: 'bbbbbbb2-bbbb-bbbb-bbbb-bbbbbbbbbbb2',
          kind: 'subtitle.prepare',
          status: 'failed',
          maxAttempts: 3,
          attemptCount: 1,
          errorCode: 'ffmpeg_subtitle_failed',
          errorMessage: 'subtitle error',
          updatedAt: '2026-07-25T02:00:00Z',
        },
      ],
    }));
    expect(denied).toBeNull();
    expect(allowed).not.toBeNull();
    expect(allowed?.summary).toContain('视频和字幕处理失败');
    expect(allowed?.stageLabel).toBe('视频和字幕');
  });
});

describe('acquisitionFailureInfo with canRetry', () => {
  it('derives stage from failed branch when failureStage empty and canRetry true', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [{
        id: '99999999-9999-9999-9999-999999999999',
        mediaType: 'episode',
        downloadId: '33333333-3333-3333-3333-333333333333',
        sourceSeason: 1,
        sourceEpisode: 1,
        targetSeason: 1,
        targetEpisode: 1,
        state: 'cancelled',
        videoState: 'failed',
        subtitleState: 'ass_ready',
        canRetry: true,
        failureStage: undefined,
        errorCode: 'ffmpeg_transcode_failed',
        errorMessage: 'video failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    }));
    expect(info?.summary).toContain('视频转码失败');
    expect(info?.canRetry).toBe(true);
    expect(info?.stage).toBe('video');
  });
  it('prefers canRetry true cancelled over failed without canRetry', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [
        {
          id: 'aaaaaaa7-aaaa-aaaa-aaaa-aaaaaaaaaaa7',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'failed',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: false,
          failureStage: undefined,
          updatedAt: '2026-07-25T02:00:00Z',
        } satisfies AcquisitionTaskSummary,
        {
          id: 'bbbbbbb8-bbbb-bbbb-bbbb-bbbbbbbbbbb8',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'cancelled',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: true,
          failureStage: undefined,
          errorCode: 'ffmpeg_transcode_failed',
          updatedAt: '2026-07-25T02:01:00Z',
        } satisfies AcquisitionTaskSummary,
      ],
    }));
    expect(info?.canRetry).toBe(true);
    expect(info?.stage).toBe('video');
  });
  it('shows merged video and subtitle failure for acquisition dual with empty failureStage', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [{
        id: 'cccccccc-cccc-cccc-cccc-cccccccccccc',
        mediaType: 'episode',
        downloadId: '33333333-3333-3333-3333-333333333333',
        state: 'cancelled',
        videoState: 'failed',
        subtitleState: 'failed',
        canRetry: true,
        failureStage: undefined,
        errorCode: 'ffmpeg_transcode_failed',
        errorMessage: 'video failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    }));
    expect(info?.summary).toBe('视频和字幕处理失败');
    expect(info?.stageLabel).toBe('视频和字幕');
    expect(info?.canRetry).toBe(true);
    expect(info?.retryKind).toBe('task');
    expect(info?.retryLabel).toBe('重试任务');
    expect(info?.branches).toBeUndefined();
    expect(info?.technicalDetails).not.toContain('ffmpeg_transcode_failed');
  });
  it('keeps single video failure unchanged for acquisition', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [{
        id: 'dddddddd-dddd-dddd-dddd-dddddddddddd',
        mediaType: 'episode',
        downloadId: '33333333-3333-3333-3333-333333333333',
        state: 'failed',
        videoState: 'failed',
        subtitleState: 'ass_ready',
        canRetry: true,
        failureStage: undefined,
        errorCode: 'ffmpeg_transcode_failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    }));
    expect(info?.summary).toContain('视频转码失败');
    expect(info?.stage).toBe('video');
  });
  it('keeps single subtitle failure unchanged for acquisition', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [{
        id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
        mediaType: 'episode',
        downloadId: '33333333-3333-3333-3333-333333333333',
        state: 'failed',
        videoState: 'video_ready',
        subtitleState: 'failed',
        canRetry: true,
        failureStage: undefined,
        errorCode: 'ffmpeg_subtitle_failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    }));
    expect(info?.summary).toContain('字幕处理失败');
    expect(info?.stage).toBe('subtitle');
  });
});

describe('sanitizeTechnicalDetails', () => {
  it('removes credentials, auth headers, cookies, and absolute server paths', () => {
    const raw = 'Authorization: Bearer abc.def.ghi password=hunter2 Cookie: sid=secret {"apiKey":"json-secret"} C:\\media\\downloads\\show\\episode01.mkv /srv/emby/work/subtitle.ass';
    const sanitized = sanitizeTechnicalDetails(raw);

    expect(sanitized).not.toContain('abc.def.ghi');
    expect(sanitized).not.toContain('hunter2');
    expect(sanitized).not.toContain('sid=secret');
    expect(sanitized).not.toContain('json-secret');
    expect(sanitized).not.toContain('C:\\media\\downloads\\show');
    expect(sanitized).not.toContain('/srv/emby/work');
    expect(sanitized).toContain('episode01.mkv');
    expect(sanitized).toContain('subtitle.ass');
  });
});

describe('acquisitionFailureInfo cleanup', () => {
  it('shows cleanup failure for imported task with failed cleanup', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [{
        id: '99999999-9999-9999-9999-999999999991',
        mediaType: 'episode',
        downloadId: '33333333-3333-3333-3333-333333333333',
        state: 'imported',
        videoState: 'video_ready',
        subtitleState: 'ass_ready',
        cleanupStatus: 'failed',
        canRetry: true,
        errorCode: 'cleanup_delete_failed',
        errorMessage: 'remove failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    }));
    expect(info?.stage).toBe('cleanup');
    expect(info?.summary).toContain('清理失败');
    expect(info?.canRetry).toBe(true);
    expect(info?.retryKind).toBe('cleanup');
    expect(info?.retryLabel).toBe('重试清理');
    expect(info?.relatedResource).toBe('关联下载、种子任务和转码临时文件');
  });

  it('uses generic cleanup copy when no specific error', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [acquisitionTaskSummary({
        id: '99999999-9999-9999-9999-999999999992',
        state: 'imported',
        videoState: 'video_ready',
        subtitleState: 'ass_ready',
        cleanupStatus: 'failed',
        canRetry: true,
        updatedAt: '2026-07-25T02:00:00Z',
      })],
    }));
    expect(info?.stage).toBe('cleanup');
    expect(info?.summary).toBe('清理失败：未能识别具体失败原因，请查看技术详情或运行日志。');
    expect(info?.summary).not.toContain('无法删除临时文件');
    expect(info?.summary).not.toContain('权限');
    expect(info?.summary).not.toContain('占用');
    expect(info?.canRetry).toBe(true);
    expect(info?.retryKind).toBe('cleanup');
    expect(info?.retryLabel).toBe('重试清理');
  });

  it('hides retry for imported cleanup completed', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [{
        id: '99999999-9999-9999-9999-999999999993',
        mediaType: 'episode',
        downloadId: '33333333-3333-3333-3333-333333333333',
        state: 'imported',
        videoState: 'video_ready',
        subtitleState: 'ass_ready',
        cleanupStatus: 'completed',
        canRetry: false,
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    }));
    expect(info).toBeNull();
  });
});

describe('acquisitionFailureInfo multiple retryable count', () => {
  it('shows count when two tasks are retryable', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [
        {
          id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'failed',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: true,
          failureStage: 'video',
          errorCode: 'ffmpeg_transcode_failed',
          updatedAt: '2026-07-25T02:00:00Z',
        } satisfies AcquisitionTaskSummary,
        {
          id: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'failed',
          videoState: 'video_ready',
          subtitleState: 'failed',
          canRetry: true,
          failureStage: 'subtitle',
          errorCode: 'ffmpeg_subtitle_failed',
          updatedAt: '2026-07-25T02:01:00Z',
        } satisfies AcquisitionTaskSummary,
      ],
    }));
    expect(info?.summary).toContain('（共 2 个任务）');
    expect(info?.stage).toBe('video');
    expect(info?.canRetry).toBe(true);
  });

  it('does not show count when only one retryable task', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [{
        id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa2',
        mediaType: 'episode',
        downloadId: '33333333-3333-3333-3333-333333333333',
        state: 'failed',
        videoState: 'failed',
        subtitleState: 'ass_ready',
        canRetry: true,
        failureStage: 'video',
        errorCode: 'ffmpeg_transcode_failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    }));
    expect(info?.summary).not.toContain('（共');
    expect(info?.summary).toContain('视频转码失败');
  });

  it('shows count for dual-branch merged summary', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [
        {
          id: 'cccccccc-cccc-cccc-cccc-ccccccccccc1',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'cancelled',
          videoState: 'failed',
          subtitleState: 'failed',
          canRetry: true,
          failureStage: undefined,
          errorCode: 'ffmpeg_transcode_failed',
          updatedAt: '2026-07-25T02:00:00Z',
        } satisfies AcquisitionTaskSummary,
        {
          id: 'dddddddd-dddd-dddd-dddd-dddddddddddd',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'failed',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: true,
          failureStage: 'video',
          updatedAt: '2026-07-25T02:01:00Z',
        } satisfies AcquisitionTaskSummary,
      ],
    }));
    // 首个为双分支合并，保持合并文案并追加数量
    expect(info?.summary).toContain('视频和字幕处理失败');
    expect(info?.summary).toContain('（共 2 个任务）');
  });

  it('shows count for cleanup followed by another retryable', () => {
    const info = acquisitionFailureInfo(acquisition({
      tasks: [
        {
          id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeee1',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'imported',
          videoState: 'video_ready',
          subtitleState: 'ass_ready',
          cleanupStatus: 'failed',
          canRetry: true,
          errorCode: 'cleanup_delete_failed',
          updatedAt: '2026-07-25T02:00:00Z',
        } satisfies AcquisitionTaskSummary,
        {
          id: 'ffffffff-ffff-ffff-ffff-fffffffffff1',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'failed',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: true,
          failureStage: 'video',
          updatedAt: '2026-07-25T02:01:00Z',
        } satisfies AcquisitionTaskSummary,
      ],
    }));
    expect(info?.stage).toBe('cleanup');
    expect(info?.summary).toContain('清理失败');
    expect(info?.summary).toContain('（共 2 个任务）');
    expect(info?.retryKind).toBe('cleanup');
  });

  it('keeps download priority and does not mix task count', () => {
    const info = acquisitionFailureInfo(acquisition({
      downloadId: '88888888-8888-8888-8888-888888888888',
      download: {
        id: '88888888-8888-8888-8888-888888888888',
        attempt: 1,
        status: 'failed',
        progress: 0,
        failureStage: 'enqueue',
        errorCode: 'qbittorrent_enqueue_failed',
        errorMessage: 'torrent download returned HTTP 404 Not Found',
        updatedAt: '2026-07-25T02:00:00Z',
      },
      tasks: [
        {
          id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa3',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'failed',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: true,
          failureStage: 'video',
          updatedAt: '2026-07-25T02:00:00Z',
        } satisfies AcquisitionTaskSummary,
        {
          id: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb2',
          mediaType: 'episode',
          downloadId: '33333333-3333-3333-3333-333333333333',
          state: 'failed',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: true,
          failureStage: 'video',
          updatedAt: '2026-07-25T02:01:00Z',
        } satisfies AcquisitionTaskSummary,
      ],
    }));
    expect(info?.summary).not.toContain('（共');
    expect(info?.summary).toContain('下载失败');
    expect(info?.stage).toBe('download');
  });
});
