import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';

import type { Acquisition, AcquisitionTaskSummary, Download } from '@/api/generated/types.gen';
import { AcquisitionsPage } from '@/features/acquisitions/acquisitions-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

function failedAcquisition(): Acquisition {
  return {
    id: '11111111-1111-1111-1111-111111111111',
    mediaType: 'episode',
    seriesId: '22222222-2222-2222-2222-222222222222',
    seriesTitle: '下载失败示例',
    sourceKind: 'rss',
    downloadId: '33333333-3333-3333-3333-333333333333',
    download: {
      id: '33333333-3333-3333-3333-333333333333',
      attempt: 2,
      status: 'failed',
      progress: 0.5,
      failureStage: 'sync',
      errorCode: 'download_storage_unavailable',
      errorMessage: 'write C:\\private\\downloads\\episode01.mkv: no space left on device',
      updatedAt: '2026-07-25T02:00:00Z',
    },
    tasks: [],
    mapping: { selectedVideoCount: 0, mappedVideoCount: 0, complete: false },
    aggregateStatus: 'failed',
    currentStage: 'download',
    overallProgress: 0.15,
    stages: [
      { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
      { key: 'download', status: 'failed', progress: 0.5, completedItems: 0, totalItems: 1 },
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
  };
}

function acquisitionTaskSummary(overrides: Partial<AcquisitionTaskSummary>): AcquisitionTaskSummary {
  return {
    id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
    mediaType: 'episode',
    downloadId: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
    state: 'failed',
    videoState: 'failed',
    subtitleState: 'ass_ready',
    canRetry: true,
    updatedAt: '2026-07-25T02:00:00Z',
    ...overrides,
  };
}

function download(acquisition: Acquisition): Download {
  return {
    id: acquisition.downloadId!,
    acquisitionId: acquisition.id,
    attempt: 2,
    clientName: 'qbittorrent',
    status: 'failed',
    progress: 0.5,
    version: 4,
    failureStage: 'sync',
    errorCode: 'download_storage_unavailable',
    errorMessage: 'no space left on device',
    files: [],
    actions: { canRetry: true, canCancel: false, canDelete: true, canEditFileSelection: false, canResolveFiles: false, canRequestAgent: false },
    createdAt: '2026-07-25T01:00:00Z',
    updatedAt: '2026-07-25T02:00:00Z',
  };
}

describe('AcquisitionsPage failure actions', () => {
  it('sorts from clickable column headers and toggles that column direction', async () => {
    const item = failedAcquisition();
    const requests: string[] = [];
    server.use(
      http.get('*/api/v1/acquisitions', ({ request }) => {
        requests.push(request.url);
        return HttpResponse.json({ items: [item] });
      }),
    );
    const { router } = renderWithProviders(<AcquisitionsPage />, {
      routePath: '/acquisitions',
      initialEntry: '/acquisitions',
    });

    await screen.findAllByText('下载失败示例');
    expect(screen.queryByLabelText('排序')).not.toBeInTheDocument();
    await userEvent.click(screen.getAllByRole('button', { name: '整体进度，点击按正序排列' })[0]);
    await waitFor(() => {
      const url = new URL(requests.at(-1)!);
      expect(url.searchParams.get('sortBy')).toBe('progress');
      expect(url.searchParams.get('sortOrder')).toBe('asc');
    });
    expect(router.state.location.search).toMatchObject({ sortBy: 'progress', sortOrder: 'asc' });

    await userEvent.click(screen.getAllByRole('button', { name: '整体进度，点击按逆序排列' })[0]);
    await waitFor(() => {
      const url = new URL(requests.at(-1)!);
      expect(url.searchParams.get('sortBy')).toBe('progress');
      expect(url.searchParams.get('sortOrder')).toBe('desc');
    });
    expect(router.state.location.search).toMatchObject({ sortBy: 'progress', sortOrder: 'desc' });
  });

  it('batch deletes current-page tasks and keeps completion feedback on the task page', async () => {
    const first = failedAcquisition();
    const second: Acquisition = {
      ...failedAcquisition(),
      id: '11111111-1111-1111-1111-111111111112',
      seriesTitle: '第二个任务',
      downloadId: '33333333-3333-3333-3333-333333333334',
      download: { ...failedAcquisition().download!, id: '33333333-3333-3333-3333-333333333334' },
    };
    const deleted = vi.fn();
    server.use(
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [first, second] })),
      http.delete('*/api/v1/acquisitions/:acquisitionId', ({ params }) => {
        deleted(params.acquisitionId);
        return HttpResponse.json({ operationId: params.acquisitionId, status: 'queued' }, { status: 202 });
      }),
      http.get('*/api/v1/operations/:operationId', ({ params }) => HttpResponse.json({
        id: params.operationId,
        resourceType: 'acquisition',
        resourceId: params.operationId,
        kind: 'acquisition.delete',
        status: 'succeeded',
        attempt: 1,
        maxAttempts: 5,
        createdAt: '2026-07-26T03:00:00Z',
        updatedAt: '2026-07-26T03:00:01Z',
      })),
    );
    const { router } = renderWithProviders(<AcquisitionsPage />, {
      routePath: '/acquisitions',
      initialEntry: '/acquisitions',
    });

    await screen.findAllByText('下载失败示例');
    await userEvent.click(screen.getAllByRole('checkbox', { name: '全选当前页任务' })[0]);
    await userEvent.click(screen.getByRole('button', { name: '批量删除' }));
    await userEvent.click(screen.getByRole('button', { name: '确认批量删除' }));

    await waitFor(() => expect(deleted).toHaveBeenCalledTimes(2));
    expect(router.state.location.pathname).toBe('/acquisitions');
    expect(await screen.findByText('已成功删除 2 项')).toBeInTheDocument();
  });

  it('shows a download summary and executes retry download from the menu', async () => {
    const item = failedAcquisition();
    const fullDownload = download(item);
    const retry = vi.fn();
    server.use(
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })),
      http.get(`*/api/v1/downloads/${fullDownload.id}`, () => HttpResponse.json(fullDownload)),
      http.post(`*/api/v1/downloads/${fullDownload.id}/retry`, async ({ request }) => {
        retry({ body: await request.json(), key: request.headers.get('Idempotency-Key') });
        return HttpResponse.json({ download: { ...fullDownload, status: 'enqueue_pending', version: 5 }, operationId: '44444444-4444-4444-4444-444444444444', status: 'queued' }, { status: 202 });
      }),
    );
    renderWithProviders(<AcquisitionsPage />);

    const summaries = await screen.findAllByText('下载失败：磁盘空间不足');
    expect(summaries.length).toBeGreaterThan(0);
    expect(screen.queryByText(/no space left on device/)).not.toBeInTheDocument();

    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    await userEvent.click(await screen.findByRole('menuitem', { name: '重试下载' }));
    await waitFor(() => expect(retry).toHaveBeenCalledTimes(1));
    expect(retry.mock.calls[0][0].body).toEqual({ expectedVersion: 4 });
    expect(retry.mock.calls[0][0].key).toBeTruthy();
  });

  it('allows retry for download_no_main_video with latest version and a single idempotent request', async () => {
    const item: Acquisition = {
      ...failedAcquisition(),
      id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      downloadId: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
      download: {
        id: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
        attempt: 1,
        status: 'failed',
        progress: 1,
        failureStage: 'file_resolution',
        errorCode: 'download_no_main_video',
        errorMessage: 'the torrent contains no selectable main video',
        updatedAt: '2026-07-25T02:00:00Z',
      },
    };
    const latestDownload: Download = {
      id: item.downloadId!,
      acquisitionId: item.id,
      attempt: 1,
      clientName: 'qbittorrent',
      status: 'failed',
      progress: 1,
      version: 9,
      failureStage: 'file_resolution',
      errorCode: 'download_no_main_video',
      errorMessage: 'the torrent contains no selectable main video',
      files: [],
      actions: { canRetry: true, canCancel: false, canDelete: true, canEditFileSelection: false, canResolveFiles: false, canRequestAgent: false },
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
    };
    const retry = vi.fn();
    let resolveRetry: (() => void) | undefined;
    server.use(
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })),
      http.get(`*/api/v1/downloads/${latestDownload.id}`, () => HttpResponse.json(latestDownload)),
      http.post(`*/api/v1/downloads/${latestDownload.id}/retry`, async ({ request }) => {
        retry({ body: await request.json(), key: request.headers.get('Idempotency-Key') });
        await new Promise<void>((resolve) => { resolveRetry = resolve; });
        return HttpResponse.json({ download: { ...latestDownload, status: 'enqueue_pending', version: 10 }, operationId: '44444444-4444-4444-4444-444444444444', status: 'queued' }, { status: 202 });
      }),
    );
    const user = userEvent.setup();
    renderWithProviders(<AcquisitionsPage />);

    await screen.findAllByText('下载失败：没有可处理的正片视频');

    // 第一次打开菜单并触发重试，POST 保持悬挂以模拟 running 期间
    await user.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    const firstRetryItem = await screen.findByRole('menuitem', { name: '重试下载' });
    await user.click(firstRetryItem);
    await waitFor(() => expect(retry).toHaveBeenCalledTimes(1));
    expect(retry.mock.calls[0][0].body).toEqual({ expectedVersion: 9 });
    const firstKey = String(retry.mock.calls[0][0].key ?? '');
    expect(firstKey.trim().length).toBeGreaterThan(0);

    // running 期间重新打开菜单，查询全新的 menuitem 并断言真实 disabled 状态（不复用已 detached 的旧节点）
    await user.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    const retryItemDuringRunning = await screen.findByRole('menuitem', { name: '重试下载' });
    expect(retryItemDuringRunning).toBeDisabled();

    // 点击 disabled 项不应产生第二次 POST（不吞异常，不复用旧节点）
    await user.click(retryItemDuringRunning);
    await waitFor(() => expect(retry).toHaveBeenCalledTimes(1));

    // 释放悬挂的 POST，等待请求完成与 running 状态收敛，不留下未处理 Promise 或 act 警告
    expect(resolveRetry).toBeDefined();
    resolveRetry!();
    await waitFor(() => expect(retryItemDuringRunning).toBeEnabled());
  });

  it('keeps permanent errors without a retry action', async () => {
    const item: Acquisition = {
      ...failedAcquisition(),
      id: 'cccccccc-cccc-cccc-cccc-cccccccccccc',
      downloadId: 'dddddddd-dddd-dddd-dddd-dddddddddddd',
      download: {
        id: 'dddddddd-dddd-dddd-dddd-dddddddddddd',
        attempt: 1,
        status: 'failed',
        progress: 0,
        failureStage: 'enqueue',
        errorCode: 'duplicate_torrent',
        errorMessage: 'torrent already exists',
        updatedAt: '2026-07-25T02:00:00Z',
      },
    };
    const downloadForItem: Download = {
      id: item.downloadId!,
      acquisitionId: item.id,
      attempt: 1,
      clientName: 'qbittorrent',
      status: 'failed',
      progress: 0,
      version: 1,
      failureStage: 'enqueue',
      errorCode: 'duplicate_torrent',
      errorMessage: 'torrent already exists',
      files: [],
      actions: { canRetry: true, canCancel: false, canDelete: true, canEditFileSelection: false, canResolveFiles: false, canRequestAgent: false },
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
    };
    server.use(
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })),
      http.get(`*/api/v1/downloads/${downloadForItem.id}`, () => HttpResponse.json(downloadForItem)),
    );
    renderWithProviders(<AcquisitionsPage />);

    await screen.findAllByText('下载失败：下载资源已存在');
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    expect(screen.queryByRole('menuitem', { name: '重试下载' })).not.toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: '重试任务' })).not.toBeInTheDocument();
  });
});

describe('AcquisitionsPage task retry actions', () => {
  function taskAcquisition(overrides: Partial<Acquisition> & { tasks: Acquisition['tasks'] }): Acquisition {
    return {
      id: '55555555-5555-5555-5555-555555555555',
      mediaType: 'episode',
      seriesId: '66666666-6666-6666-6666-666666666666',
      seriesTitle: '任务重试示例',
      sourceKind: 'rss',
      mapping: { selectedVideoCount: 1, mappedVideoCount: 1, complete: true },
      aggregateStatus: 'failed',
      currentStage: 'transcode',
      overallProgress: 0.5,
      stages: [
        { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'download', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'mapping', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'transcode', status: 'failed', progress: 0, completedItems: 0, totalItems: 1 },
        { key: 'subtitle', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'rename', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'organize', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'review', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'import', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      ],
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
      ...overrides,
    } as Acquisition;
  }
  it('shows retry for safe cancelled without stop and performs single GET and POST', async () => {
    const taskId = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
    const downloadId = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb';
    const item = taskAcquisition({
      tasks: [acquisitionTaskSummary({
        id: taskId,
        downloadId,
        sourceSeason: 1,
        sourceEpisode: 1,
        state: 'cancelled',
        videoState: 'failed',
        subtitleState: 'ass_ready',
        canRetry: true,
        updatedAt: '2026-07-25T02:00:00Z',
      })],
    });
    const fullTask = {
      id: taskId,
      acquisitionId: item.id,
      downloadId,
      mediaType: 'episode',
      state: 'cancelled',
      videoState: 'failed',
      subtitleState: 'ass_ready',
      version: 13,
      failureStage: undefined,
      operations: [],
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
    };
    let getCount = 0;
    let postCount = 0;
    let postBody: unknown = null;
    let postKey: string | null = null;
    server.use(
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })),
      http.get(`*/api/v1/tasks/${taskId}`, () => {
        getCount++;
        return HttpResponse.json(fullTask);
      }),
      http.post(`*/api/v1/tasks/${taskId}/retry`, async ({ request }) => {
        postCount++;
        postBody = await request.json();
        postKey = request.headers.get('Idempotency-Key');
        return HttpResponse.json({ task: { ...fullTask, state: 'processing', version: 14 }, operationId: 'cccccccc-cccc-cccc-cccc-cccccccccccc', status: 'queued' }, { status: 202 });
      }),
    );
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    const retryItem = await screen.findByRole('menuitem', { name: '重试任务' });
    expect(retryItem).toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: '停止处理' })).not.toBeInTheDocument();
    await userEvent.click(retryItem);
    await waitFor(() => expect(postCount).toBe(1));
    expect(getCount).toBe(1);
    expect(postBody).toEqual({ expectedVersion: 13 });
    expect(postKey).toBeTruthy();
    expect(String(postKey).trim().length).toBeGreaterThan(0);
  });
  it('does not show retry for ordinary cancelled', async () => {
    const item = taskAcquisition({
      tasks: [{
        id: 'dddddddd-dddd-dddd-dddd-dddddddddddd',
        mediaType: 'episode',
        downloadId: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
        state: 'cancelled',
        videoState: 'cancelled',
        subtitleState: 'cancelled',
        canRetry: false,
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    });
    server.use(http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })));
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    expect(screen.queryByRole('menuitem', { name: '重试任务' })).not.toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: '停止处理' })).not.toBeInTheDocument();
  });
  it('shows retry without stop for processing stuck', async () => {
    const item = taskAcquisition({
      tasks: [{
        id: 'ffffffff-ffff-ffff-ffff-ffffffffffff',
        mediaType: 'episode',
        downloadId: '00000000-0000-0000-0000-000000000000',
        state: 'processing',
        videoState: 'failed',
        subtitleState: 'ass_ready',
        canRetry: true,
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    });
    server.use(http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })));
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    expect(await screen.findByRole('menuitem', { name: '重试任务' })).toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: '停止处理' })).not.toBeInTheDocument();
  });
  it('shows merged summary for dual failed with empty failureStage and single retry', async () => {
    const item = taskAcquisition({
      tasks: [{
        id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaab',
        mediaType: 'episode',
        downloadId: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbc',
        state: 'cancelled',
        videoState: 'failed',
        subtitleState: 'failed',
        canRetry: true,
        failureStage: undefined,
        errorCode: 'ffmpeg_transcode_failed',
        errorMessage: 'video failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    });
    server.use(http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })));
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    const summaries = await screen.findAllByText('视频和字幕处理失败');
    expect(summaries.length).toBeGreaterThan(0);
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    const retryItems = await screen.findAllByRole('menuitem', { name: '重试任务' });
    expect(retryItems).toHaveLength(1);
    expect(screen.queryByRole('menuitem', { name: '停止处理' })).not.toBeInTheDocument();
  });
  it('keeps single video failed summary unchanged', async () => {
    const item = taskAcquisition({
      tasks: [{
        id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaac',
        mediaType: 'episode',
        downloadId: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbd',
        state: 'failed',
        videoState: 'failed',
        subtitleState: 'ass_ready',
        canRetry: true,
        failureStage: undefined,
        errorCode: 'ffmpeg_transcode_failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    });
    server.use(http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })));
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    const summaries = await screen.findAllByText(/视频转码失败/);
    expect(summaries.length).toBeGreaterThan(0);
    expect(screen.queryByText('视频和字幕处理失败')).not.toBeInTheDocument();
  });
  it('keeps single subtitle failed summary unchanged', async () => {
    const item = taskAcquisition({
      tasks: [{
        id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaad',
        mediaType: 'episode',
        downloadId: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbe',
        state: 'failed',
        videoState: 'video_ready',
        subtitleState: 'failed',
        canRetry: true,
        failureStage: undefined,
        errorCode: 'ffmpeg_subtitle_failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    });
    server.use(http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })));
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    const summaries = await screen.findAllByText(/字幕处理失败/);
    expect(summaries.length).toBeGreaterThan(0);
    expect(screen.queryByText('视频和字幕处理失败')).not.toBeInTheDocument();
  });
  it('shows cleanup failure with retry cleanup and single GET+POST', async () => {
    const taskId = 'cccccccc-cccc-cccc-cccc-ccccccccccc1';
    const downloadId = 'dddddddd-dddd-dddd-dddd-dddddddddddd';
    const item = taskAcquisition({
      tasks: [{
        id: taskId,
        mediaType: 'episode',
        downloadId,
        state: 'imported',
        videoState: 'video_ready',
        subtitleState: 'ass_ready',
        cleanupStatus: 'failed',
        canRetry: true,
        errorCode: 'cleanup_delete_failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    });
    const fullTask = {
      id: taskId,
      acquisitionId: item.id,
      downloadId,
      mediaType: 'episode',
      state: 'imported',
      videoState: 'video_ready',
      subtitleState: 'ass_ready',
      version: 7,
      cleanup: {
        id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
        attempt: 1,
        status: 'failed',
        torrentRemoved: false,
        stagedFilesRemoved: false,
        errorCode: 'cleanup_delete_failed',
        errorMessage: 'remove failed',
        createdAt: '2026-07-25T01:00:00Z',
        updatedAt: '2026-07-25T02:00:00Z',
      },
      operations: [],
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
    };
    let getCount = 0;
    let postCount = 0;
    server.use(
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })),
      http.get(`*/api/v1/tasks/${taskId}`, () => {
        getCount++;
        return HttpResponse.json(fullTask);
      }),
      http.post(`*/api/v1/tasks/${taskId}/retry`, async ({ request }) => {
        postCount++;
        return HttpResponse.json({ task: { ...fullTask, version: 8 }, operationId: 'ffffffff-ffff-ffff-ffff-ffffffffffff', status: 'queued' }, { status: 202 });
      }),
    );
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    const summaries = await screen.findAllByText(/清理失败/);
    expect(summaries.length).toBeGreaterThan(0);
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    const retryItem = await screen.findByRole('menuitem', { name: '重试清理' });
    expect(retryItem).toBeInTheDocument();
    await userEvent.click(retryItem);
    await waitFor(() => expect(postCount).toBe(1));
    expect(getCount).toBe(1);
  });
  it('does not show retry for imported cleanup completed', async () => {
    const item = taskAcquisition({
      tasks: [{
        id: 'cccccccc-cccc-cccc-cccc-ccccccccccc2',
        mediaType: 'episode',
        downloadId: 'dddddddd-dddd-dddd-dddd-ddddddddddde',
        state: 'imported',
        videoState: 'video_ready',
        subtitleState: 'ass_ready',
        cleanupStatus: 'completed',
        canRetry: false,
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    });
    server.use(http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })));
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    expect(screen.queryByRole('menuitem', { name: '重试清理' })).not.toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: '重试任务' })).not.toBeInTheDocument();
  });
});

describe('AcquisitionsPage multiple retryable count', () => {
  function taskAcquisition(overrides: Partial<Acquisition> & { tasks: Acquisition['tasks'] }): Acquisition {
    return {
      id: '55555555-5555-5555-5555-555555555555',
      mediaType: 'episode',
      seriesId: '66666666-6666-6666-6666-666666666666',
      seriesTitle: '任务重试示例',
      sourceKind: 'rss',
      mapping: { selectedVideoCount: 1, mappedVideoCount: 1, complete: true },
      aggregateStatus: 'failed',
      currentStage: 'transcode',
      overallProgress: 0.5,
      stages: [
        { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'download', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'mapping', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'transcode', status: 'failed', progress: 0, completedItems: 0, totalItems: 1 },
        { key: 'subtitle', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'rename', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'organize', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'review', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'import', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      ],
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
      ...overrides,
    } as Acquisition;
  }
  it('shows count for N=2 and retries both tasks with single menu item', async () => {
    const taskId1 = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa11';
    const taskId2 = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbb22';
    const downloadId = 'cccccccc-cccc-cccc-cccc-cccccccccccc';
    const item = taskAcquisition({
      tasks: [
        {
          id: taskId1,
          mediaType: 'episode',
          downloadId,
          state: 'failed',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: true,
          failureStage: 'video',
          errorCode: 'ffmpeg_transcode_failed',
          updatedAt: '2026-07-25T02:00:00Z',
        } satisfies AcquisitionTaskSummary,
        {
          id: taskId2,
          mediaType: 'episode',
          downloadId,
          state: 'failed',
          videoState: 'video_ready',
          subtitleState: 'failed',
          canRetry: true,
          failureStage: 'subtitle',
          errorCode: 'ffmpeg_subtitle_failed',
          updatedAt: '2026-07-25T02:01:00Z',
        } satisfies AcquisitionTaskSummary,
      ],
    });
    const fullTask1 = {
      id: taskId1,
      acquisitionId: item.id,
      downloadId,
      mediaType: 'episode',
      state: 'failed',
      videoState: 'failed',
      subtitleState: 'ass_ready',
      version: 5,
      failureStage: 'video',
      operations: [],
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
    };
    const fullTask2 = {
      id: taskId2,
      acquisitionId: item.id,
      downloadId,
      mediaType: 'episode',
      state: 'failed',
      videoState: 'video_ready',
      subtitleState: 'failed',
      version: 6,
      failureStage: 'subtitle',
      operations: [],
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:01:00Z',
    };
    let getCount1 = 0;
    let getCount2 = 0;
    let postCount = 0;
    server.use(
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })),
      http.get(`*/api/v1/tasks/${taskId1}`, () => { getCount1++; return HttpResponse.json(fullTask1); }),
      http.get(`*/api/v1/tasks/${taskId2}`, () => { getCount2++; return HttpResponse.json(fullTask2); }),
      http.post(`*/api/v1/tasks/${taskId1}/retry`, async () => { postCount++; return HttpResponse.json({ task: { ...fullTask1, version: 6 }, operationId: 'dddddddd-dddd-dddd-dddd-dddddddddddd', status: 'queued' }, { status: 202 }); }),
      http.post(`*/api/v1/tasks/${taskId2}/retry`, async () => { postCount++; return HttpResponse.json({ task: { ...fullTask2, version: 7 }, operationId: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee', status: 'queued' }, { status: 202 }); }),
    );
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    const summaries = await screen.findAllByText(/（共 2 个任务）/);
    expect(summaries.length).toBeGreaterThan(0);
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    const retryItems = await screen.findAllByRole('menuitem', { name: '重试任务' });
    expect(retryItems).toHaveLength(1);
    await userEvent.click(retryItems[0]);
    await waitFor(() => expect(postCount).toBe(2));
    expect(getCount1).toBe(1);
    expect(getCount2).toBe(1);
  });
  it('does not show count for N=1', async () => {
    const item = taskAcquisition({
      tasks: [{
        id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa33',
        mediaType: 'episode',
        downloadId: 'cccccccc-cccc-cccc-cccc-cccccccccccc',
        state: 'failed',
        videoState: 'failed',
        subtitleState: 'ass_ready',
        canRetry: true,
        failureStage: 'video',
        errorCode: 'ffmpeg_transcode_failed',
        updatedAt: '2026-07-25T02:00:00Z',
      } satisfies AcquisitionTaskSummary],
    });
    server.use(http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [item] })));
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    const summaries = await screen.findAllByText(/视频转码失败/);
    expect(summaries.length).toBeGreaterThan(0);
    expect(screen.queryByText(/（共/)).not.toBeInTheDocument();
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    expect(await screen.findByRole('menuitem', { name: '重试任务' })).toBeInTheDocument();
  });
});

describe('AcquisitionsPage partial retry refresh', () => {
  function taskAcquisition(overrides: Partial<Acquisition> & { tasks: Acquisition['tasks'] }): Acquisition {
    return {
      id: '55555555-5555-5555-5555-555555555555',
      mediaType: 'episode',
      seriesId: '66666666-6666-6666-6666-666666666666',
      seriesTitle: '任务重试示例',
      sourceKind: 'rss',
      mapping: { selectedVideoCount: 1, mappedVideoCount: 1, complete: true },
      aggregateStatus: 'failed',
      currentStage: 'transcode',
      overallProgress: 0.5,
      stages: [
        { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'download', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'mapping', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'transcode', status: 'failed', progress: 0, completedItems: 0, totalItems: 1 },
        { key: 'subtitle', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'rename', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'organize', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'review', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'import', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      ],
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
      ...overrides,
    } as Acquisition;
  }

  it('refreshes list after first task succeeds and second fails, keeps error and shows converged count', async () => {
    const taskId1 = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa11';
    const taskId2 = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbb22';
    const downloadId = 'cccccccc-cccc-cccc-cccc-cccccccccccc';
    const itemInitial = taskAcquisition({
      tasks: [
        acquisitionTaskSummary({
          id: taskId1,
          downloadId,
          state: 'failed',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: true,
          failureStage: 'video',
          errorCode: 'ffmpeg_transcode_failed',
          updatedAt: '2026-07-25T02:00:00Z',
        }),
        acquisitionTaskSummary({
          id: taskId2,
          downloadId,
          state: 'failed',
          videoState: 'video_ready',
          subtitleState: 'failed',
          canRetry: true,
          failureStage: 'subtitle',
          errorCode: 'ffmpeg_subtitle_failed',
          updatedAt: '2026-07-25T02:01:00Z',
        }),
      ],
    });
    const itemAfter = taskAcquisition({
      tasks: [
        acquisitionTaskSummary({
          id: taskId2,
          downloadId,
          state: 'failed',
          videoState: 'video_ready',
          subtitleState: 'failed',
          canRetry: true,
          failureStage: 'subtitle',
          errorCode: 'ffmpeg_subtitle_failed',
          updatedAt: '2026-07-25T02:01:00Z',
        }),
      ],
    });
    const fullTask1 = {
      id: taskId1,
      acquisitionId: itemInitial.id,
      downloadId,
      mediaType: 'episode',
      state: 'failed',
      videoState: 'failed',
      subtitleState: 'ass_ready',
      version: 5,
      failureStage: 'video',
      operations: [],
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
    };
    const fullTask2 = {
      id: taskId2,
      acquisitionId: itemInitial.id,
      downloadId,
      mediaType: 'episode',
      state: 'failed',
      videoState: 'video_ready',
      subtitleState: 'failed',
      version: 6,
      failureStage: 'subtitle',
      operations: [],
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:01:00Z',
    };
    let getAcquisitionsCount = 0;
    let postCount1 = 0;
    let postCount2 = 0;
    server.use(
      http.get('*/api/v1/acquisitions', () => {
        getAcquisitionsCount++;
        if (getAcquisitionsCount === 1) return HttpResponse.json({ items: [itemInitial] });
        return HttpResponse.json({ items: [itemAfter] });
      }),
      http.get(`*/api/v1/tasks/${taskId1}`, () => HttpResponse.json(fullTask1)),
      http.get(`*/api/v1/tasks/${taskId2}`, () => HttpResponse.json(fullTask2)),
      http.post(`*/api/v1/tasks/${taskId1}/retry`, async () => {
        postCount1++;
        return HttpResponse.json({ task: { ...fullTask1, version: 6 }, operationId: 'dddddddd-dddd-dddd-dddd-dddddddddddd', status: 'queued' }, { status: 202 });
      }),
      http.post(`*/api/v1/tasks/${taskId2}/retry`, async () => {
        postCount2++;
        return HttpResponse.json({ code: 'state_conflict', message: 'task version changed', details: {}, requestId: 'req-1' }, { status: 409 });
      }),
    );
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    expect((await screen.findAllByText(/（共 2 个任务）/)).length).toBeGreaterThan(0);
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    await userEvent.click(await screen.findByRole('menuitem', { name: '重试任务' }));
    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument());
    expect(postCount1).toBe(1);
    expect(postCount2).toBe(1);
    expect(screen.getByRole('alert').textContent).toContain('task version changed');
    await waitFor(() => expect(getAcquisitionsCount).toBeGreaterThan(1));
    // 列表已刷新：不再显示“共 2 个任务”，仅剩第二个任务的失败摘要
    expect(screen.queryByText(/（共 2 个任务）/)).not.toBeInTheDocument();
    expect((await screen.findAllByText(/字幕处理失败/)).length).toBeGreaterThan(0);
    // 首个任务未被重复重试
    expect(postCount1).toBe(1);
    expect(postCount2).toBe(1);
  });

  it('refreshes even when single task retry fails immediately without duplicate POST', async () => {
    const taskId = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa99';
    const downloadId = 'cccccccc-cccc-cccc-cccc-cccccccccccc';
    const item = taskAcquisition({
      tasks: [
        acquisitionTaskSummary({
          id: taskId,
          downloadId,
          state: 'failed',
          videoState: 'failed',
          subtitleState: 'ass_ready',
          canRetry: true,
          failureStage: 'video',
          errorCode: 'ffmpeg_transcode_failed',
          updatedAt: '2026-07-25T02:00:00Z',
        }),
      ],
    });
    const fullTask = {
      id: taskId,
      acquisitionId: item.id,
      downloadId,
      mediaType: 'episode',
      state: 'failed',
      videoState: 'failed',
      subtitleState: 'ass_ready',
      version: 5,
      failureStage: 'video',
      operations: [],
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      createdAt: '2026-07-25T01:00:00Z',
      updatedAt: '2026-07-25T02:00:00Z',
    };
    let getAcquisitionsCount = 0;
    let postCount = 0;
    server.use(
      http.get('*/api/v1/acquisitions', () => {
        getAcquisitionsCount++;
        return HttpResponse.json({ items: [item] });
      }),
      http.get(`*/api/v1/tasks/${taskId}`, () => HttpResponse.json(fullTask)),
      http.post(`*/api/v1/tasks/${taskId}/retry`, async () => {
        postCount++;
        return HttpResponse.json({ code: 'state_conflict', message: 'task version changed', details: {}, requestId: 'req-1' }, { status: 409 });
      }),
    );
    renderWithProviders(<AcquisitionsPage />);
    await screen.findAllByText('任务重试示例');
    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    await userEvent.click(await screen.findByRole('menuitem', { name: '重试任务' }));
    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument());
    expect(postCount).toBe(1);
    await waitFor(() => expect(getAcquisitionsCount).toBeGreaterThan(1));
    // 失败后仍刷新，但不重复提交
    expect(postCount).toBe(1);
    expect(screen.getByRole('alert').textContent).toContain('task version changed');
  });
});
