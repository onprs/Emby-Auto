import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';

import type { Acquisition, Download } from '@/api/generated/types.gen';
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
});
