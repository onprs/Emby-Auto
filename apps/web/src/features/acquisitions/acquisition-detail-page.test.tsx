import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';

import type { Acquisition, AcquisitionStage, Task } from '@/api/generated/types.gen';
import { AcquisitionDetailPage } from '@/features/acquisitions/acquisition-detail-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const acquisitionId = '11111111-1111-4111-8111-111111111111';
const taskId = '22222222-2222-4222-8222-222222222222';
const downloadId = '33333333-3333-4333-8333-333333333333';
const now = '2026-07-26T02:00:00Z';

function stages(review: AcquisitionStage['status'], taskImport: AcquisitionStage['status']): Acquisition['stages'] {
  return [
    { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1, updatedAt: now },
    { key: 'download', status: 'completed', progress: 1, completedItems: 1, totalItems: 1, updatedAt: now },
    { key: 'mapping', status: 'completed', progress: 1, completedItems: 1, totalItems: 1, updatedAt: now },
    { key: 'transcode', status: 'completed', progress: 1, completedItems: 1, totalItems: 1, updatedAt: now },
    { key: 'subtitle', status: 'completed', progress: 1, completedItems: 1, totalItems: 1, updatedAt: now },
    { key: 'rename', status: 'completed', progress: 1, completedItems: 1, totalItems: 1, updatedAt: now },
    { key: 'organize', status: 'completed', progress: 1, completedItems: 1, totalItems: 1, updatedAt: now },
    { key: 'review', status: review, progress: review === 'completed' ? 1 : 0, completedItems: review === 'completed' ? 1 : 0, totalItems: 1, updatedAt: now },
    { key: 'import', status: taskImport, progress: taskImport === 'completed' ? 1 : 0, completedItems: taskImport === 'completed' ? 1 : 0, totalItems: 1, updatedAt: now },
  ];
}

function acquisition(overrides: Partial<Acquisition> = {}): Acquisition {
  return {
    id: acquisitionId,
    mediaType: 'episode',
    seriesId: '44444444-4444-4444-8444-444444444444',
    seriesTitle: '生命周期测试番剧',
    sourceKind: 'rss',
    sourceSeason: 1,
    sourceEpisode: 1,
    downloadId,
    download: { id: downloadId, attempt: 1, status: 'materialized', progress: 1, updatedAt: now },
    tasks: [{
      id: taskId,
      mediaType: 'episode',
      downloadId,
      sourceSeason: 1,
      sourceEpisode: 1,
      targetSeason: 1,
      targetEpisode: 1,
      targetEpisodeTitle: '第一集',
      state: 'awaiting_review',
      videoState: 'video_ready',
      subtitleState: 'ass_ready',
      artifactBasename: '生命周期测试番剧 - S01E01 - 第一集',
      canRetry: false,
      updatedAt: now,
    }],
    mapping: { selectedVideoCount: 1, mappedVideoCount: 1, complete: true },
    aggregateStatus: 'awaiting_review',
    currentStage: 'review',
    overallProgress: 7 / 9,
    stages: stages('waiting', 'pending'),
    createdAt: '2026-07-26T01:00:00Z',
    updatedAt: now,
    ...overrides,
  };
}

function task(overrides: Partial<Task> = {}): Task {
  return {
    id: taskId,
    acquisitionId,
    downloadId,
    mediaType: 'episode',
    seriesTitle: '生命周期测试番剧',
    sourceSeason: 1,
    sourceEpisode: 1,
    targetSeason: 1,
    targetEpisode: 1,
    targetEpisodeTitle: '第一集',
    state: 'awaiting_review',
    videoState: 'video_ready',
    subtitleState: 'ass_ready',
    version: 5,
    artifacts: {
      id: '55555555-5555-4555-8555-555555555555',
      basename: '生命周期测试番剧 - S01E01 - 第一集',
      video: {
        id: '66666666-6666-4666-8666-666666666666',
        kind: 'video',
        filePath: '/staging/task/Show/Season1/video.mkv',
        format: 'mkv',
        sizeBytes: 1_000,
        checksumSha256: 'a'.repeat(64),
      },
      subtitle: {
        id: '77777777-7777-4777-8777-777777777777',
        kind: 'subtitle',
        filePath: '/staging/task/Show/Season1/video.ass',
        format: 'ass',
        sizeBytes: 100,
        checksumSha256: 'b'.repeat(64),
      },
    },
    operations: [],
    actions: { canRetry: false, canCancel: true, canReview: true, canImport: false },
    createdAt: '2026-07-26T01:30:00Z',
    updatedAt: now,
    ...overrides,
  };
}

function mockDetail(acquisitionValue: Acquisition, taskValue: Task) {
  server.use(
    http.get(`*/api/v1/acquisitions/${acquisitionId}`, () => HttpResponse.json(acquisitionValue)),
    http.get(`*/api/v1/tasks/${taskId}`, () => HttpResponse.json(taskValue)),
  );
}

describe('AcquisitionDetailPage lifecycle', () => {
  it('keeps Agent assistance inside the lifecycle without a separate suggestion panel', async () => {
    const acquisitionValue = acquisition();
    mockDetail(acquisitionValue, task());
    let agentResolutionRequests = 0;
    server.use(
      http.get('*/api/v1/agent/resolutions', () => {
        agentResolutionRequests++;
        return HttpResponse.json({ items: [] });
      }),
    );

    renderWithProviders(<AcquisitionDetailPage acquisitionId={acquisitionId} />);

    expect(await screen.findByRole('heading', { name: '生命周期测试番剧' })).toBeInTheDocument();
    expect(screen.queryByText('Agent 建议')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '请求建议' })).not.toBeInTheDocument();
    expect(agentResolutionRequests).toBe(0);
  });

  it('reports an API version mismatch instead of crashing when lifecycle arrays are missing', async () => {
    const legacyResponse = { ...acquisition() } as Record<string, unknown>;
    delete legacyResponse.stages;
    server.use(
      http.get(`*/api/v1/acquisitions/${acquisitionId}`, () => HttpResponse.json(legacyResponse)),
    );

    renderWithProviders(<AcquisitionDetailPage acquisitionId={acquisitionId} />);

    expect(await screen.findByText('后端服务版本与当前页面不匹配，请重启 API 和 Worker 后重试。')).toBeInTheDocument();
    expect(screen.queryByText(/Cannot read properties of undefined/)).not.toBeInTheDocument();
  });

  it('reviews from the unified task and automatically queues import without a second command', async () => {
    const acquisitionValue = acquisition();
    const taskValue = task();
    const review = vi.fn();
    const separateImport = vi.fn();
    mockDetail(acquisitionValue, taskValue);
    server.use(
      http.post(`*/api/v1/tasks/${taskId}/review`, async ({ request }) => {
        review({ body: await request.json(), key: request.headers.get('Idempotency-Key') });
        return HttpResponse.json({
          ...taskValue,
          state: 'import_queued',
          version: 7,
          review: {
            id: '88888888-8888-4888-8888-888888888888',
            decision: 'approved',
            notes: '',
            reviewedAt: now,
          },
          import: {
            id: '99999999-9999-4999-8999-999999999999',
            attempt: 1,
            status: 'queued',
            createdAt: now,
            updatedAt: now,
          },
          actions: { canRetry: false, canCancel: true, canReview: false, canImport: false },
        });
      }),
      http.post(`*/api/v1/tasks/${taskId}/import`, () => {
        separateImport();
        return HttpResponse.json({}, { status: 202 });
      }),
    );

    renderWithProviders(<AcquisitionDetailPage acquisitionId={acquisitionId} />);

    expect(await screen.findByRole('heading', { name: '生命周期测试番剧' })).toBeInTheDocument();
    for (const label of ['来源导入', '下载', '剧集映射', '视频转码', '字幕处理', '文件重命名', '目录标准化', '人工审核', 'Emby 入库']) {
      expect(screen.getAllByText(label, { exact: true }).length).toBeGreaterThan(0);
    }
    expect(screen.getAllByText('生命周期测试番剧 - S01E01 - 第一集', { exact: true }).length).toBeGreaterThan(0);
    const approve = await screen.findByRole('button', { name: '审核通过并入库' });
    expect(screen.queryByRole('button', { name: /继续历史任务入库|入库到 Emby/ })).not.toBeInTheDocument();

    await userEvent.click(approve);
    await waitFor(() => expect(review).toHaveBeenCalledTimes(1));
    expect(review.mock.calls[0][0].body).toMatchObject({ decision: 'approved', expectedVersion: 5 });
    expect(review.mock.calls[0][0].key).toBeTruthy();
    expect(await screen.findByText('审核已通过，入库任务已自动创建。')).toBeInTheDocument();
    expect(separateImport).not.toHaveBeenCalled();
  });

  it('shows verified media destinations, Emby refresh, and cleanup results', async () => {
    const destinationVideo = '/library/生命周期测试番剧/Season1/生命周期测试番剧 - S01E01 - 第一集.mkv';
    const destinationSubtitle = '/library/生命周期测试番剧/Season1/生命周期测试番剧 - S01E01 - 第一集.ass';
    const importedTask = task({
      state: 'imported',
      version: 9,
      review: { id: '88888888-8888-4888-8888-888888888888', decision: 'approved', notes: '检查通过', reviewedAt: now },
      import: {
        id: '99999999-9999-4999-8999-999999999999',
        attempt: 1,
        status: 'succeeded',
        destinationVideoPath: destinationVideo,
        destinationSubtitlePath: destinationSubtitle,
        createdAt: now,
        updatedAt: now,
      },
      cleanup: {
        id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
        attempt: 1,
        status: 'completed',
        torrentRemoved: true,
        stagedFilesRemoved: true,
        createdAt: now,
        updatedAt: now,
      },
      operations: [{
        id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',
        kind: 'emby.refresh',
        status: 'succeeded',
        maxAttempts: 5,
        attemptCount: 1,
        updatedAt: now,
      }],
      actions: { canRetry: false, canCancel: false, canReview: false, canImport: false },
    });
    const completed = acquisition({
      tasks: [{
        ...acquisition().tasks[0],
        state: 'imported',
        reviewDecision: 'approved',
        reviewedAt: now,
        importStatus: 'succeeded',
        destinationVideoPath: destinationVideo,
        destinationSubtitlePath: destinationSubtitle,
        embyRefreshStatus: 'succeeded',
        cleanupStatus: 'completed',
      }],
      aggregateStatus: 'completed',
      currentStage: 'import',
      overallProgress: 1,
      stages: stages('completed', 'completed'),
    });
    mockDetail(completed, importedTask);

    renderWithProviders(<AcquisitionDetailPage acquisitionId={acquisitionId} />);

    expect(await screen.findByText(destinationVideo)).toBeInTheDocument();
    expect(screen.getByText(destinationSubtitle)).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Emby 入库结果' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '入库后清理' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '查看 Emby 刷新记录' })).toBeInTheDocument();
    expect(screen.getByText('生命周期完成')).toBeInTheDocument();
  });

  it('shows archived RSS lifecycle without requesting deleted task or download resources', async () => {
    const deletedTaskRequest = vi.fn();
    const archived = acquisition({
      archived: true,
      archivedAt: '2026-07-26T02:10:00Z',
      downloadId: undefined,
      download: undefined,
      tasks: [],
      aggregateStatus: 'completed',
      currentStage: 'import',
      overallProgress: 1,
      stages: stages('completed', 'completed'),
    });
    server.use(
      http.get(`*/api/v1/acquisitions/${acquisitionId}`, () => HttpResponse.json(archived)),
      http.get(`*/api/v1/tasks/${taskId}`, () => {
        deletedTaskRequest();
        return HttpResponse.json({}, { status: 404 });
      }),
    );

    renderWithProviders(<AcquisitionDetailPage acquisitionId={acquisitionId} />);

    expect(await screen.findByText('已归档')).toBeInTheDocument();
    expect(screen.getByText('已完成 Emby 入库')).toBeInTheDocument();
    expect(screen.getByText(/源下载、实时任务与临时文件已按订阅完成策略清理/)).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: '下载文件' })).not.toBeInTheDocument();
    expect(screen.queryByText('当前阶段尚未生成媒体处理项。')).not.toBeInTheDocument();
    expect(deletedTaskRequest).not.toHaveBeenCalled();
    for (const label of ['来源导入', '下载', '剧集映射', '视频转码', '字幕处理', '文件重命名', '目录标准化', '人工审核', 'Emby 入库']) {
      expect(screen.getAllByText(label, { exact: true }).length).toBeGreaterThan(0);
    }
  });
});
