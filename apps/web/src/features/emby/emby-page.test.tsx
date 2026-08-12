import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { delay, http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import type { EmbyScan, Operation } from '@/api/generated/types.gen';
import { EmbyPage } from '@/features/emby/emby-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const scanId = '81000000-0000-4000-8000-000000000001';
const scanOperationId = '81000000-0000-4000-8000-000000000002';
const refreshOperationId = '81000000-0000-4000-8000-000000000003';
const timestamp = '2026-07-26T12:00:00Z';

function embyScan(status: EmbyScan['status']): EmbyScan {
  return {
    id: scanId,
    operationId: scanOperationId,
    status,
    libraryCount: status === 'succeeded' ? 1 : 0,
    itemCount: status === 'succeeded' ? 24 : 0,
    createdAt: timestamp,
    updatedAt: timestamp,
    ...(status === 'succeeded' ? { startedAt: timestamp, completedAt: timestamp } : {}),
    ...(status === 'failed' ? { errorCode: 'service_unavailable', errorMessage: 'emby is unavailable' } : {}),
  };
}

function operation(status: Operation['status']): Operation {
  return {
    id: refreshOperationId,
    kind: 'emby.refresh',
    status,
    idempotencyKey: 'refresh-key',
    maxAttempts: 5,
    attemptCount: status === 'queued' ? 0 : 1,
    createdAt: timestamp,
    updatedAt: timestamp,
    attempts: [],
    ...(status === 'succeeded' ? { startedAt: timestamp, finishedAt: timestamp } : {}),
    ...(status === 'failed' ? { errorCode: 'service_unavailable', errorMessage: 'emby is unavailable', startedAt: timestamp, finishedAt: timestamp } : {}),
  };
}

function catalogHandlers(
  scanTerminalStatus: 'succeeded' | 'failed',
  refreshTerminalStatus: 'succeeded' | 'failed',
  hasExistingCatalog = false,
) {
  let scanCreated = false;
  let scanFinished = false;

  return [
    http.get('http://localhost/api/v1/emby/scans', () => HttpResponse.json({
      items: scanCreated ? [embyScan(scanFinished ? scanTerminalStatus : 'queued')] : [],
    })),
    http.get(`http://localhost/api/v1/emby/scans/${scanId}`, () => {
      scanFinished = true;
      return HttpResponse.json(embyScan(scanTerminalStatus));
    }),
    http.post('http://localhost/api/v1/emby/scans', async () => {
      await delay(75);
      scanCreated = true;
      return HttpResponse.json({ scan: embyScan('queued'), operationId: scanOperationId, status: 'queued' }, { status: 202 });
    }),
    http.get('http://localhost/api/v1/emby/libraries', () => HttpResponse.json(hasExistingCatalog || (scanFinished && scanTerminalStatus === 'succeeded') ? [{
      id: '81000000-0000-4000-8000-000000000004',
      embyId: 'fixture-library',
      name: 'Fixture Library',
      collectionType: 'tvshows',
      locations: ['D:/media/anime'],
      present: true,
      lastSeenAt: timestamp,
    }] : [])),
    http.post('http://localhost/api/v1/emby/refresh', async () => {
      await delay(75);
      return HttpResponse.json({ operationId: refreshOperationId, status: 'queued' }, { status: 202 });
    }),
    http.get(`http://localhost/api/v1/operations/${refreshOperationId}`, () => HttpResponse.json(operation(refreshTerminalStatus))),
  ];
}

describe('EmbyPage commands', () => {
  it('keeps both commands on the media-library page and refreshes the catalog after success', async () => {
    server.use(...catalogHandlers('succeeded', 'succeeded'));
    const user = userEvent.setup();
    const { router } = renderWithProviders(<EmbyPage />, { routePath: '/emby', initialEntry: '/emby' });

    await screen.findByText('暂无记录');
    await user.click(screen.getByRole('button', { name: '请求 Emby 扫描文件' }));
    expect(await screen.findByText('正在等待后台向 Emby 发送扫描请求。')).toBeVisible();
    expect(await screen.findByText('Emby 已接受媒体文件扫描请求。')).toBeVisible();
    expect(router.state.location.pathname).toBe('/emby');

    await user.click(screen.getByRole('button', { name: '从 Emby 更新目录' }));
    expect(await screen.findByText('正在读取 Emby 当前的媒体库和条目。')).toBeVisible();
    expect(await screen.findByText('目录已更新：1 个媒体库，24 个媒体条目。')).toBeVisible();
    expect(await screen.findByRole('link', { name: 'Fixture Library' })).toBeVisible();
    expect(router.state.location.pathname).toBe('/emby');
  });

  it('shows independent command failures without replacing the existing catalog', async () => {
    server.use(...catalogHandlers('failed', 'failed', true));
    const user = userEvent.setup();
    const { router } = renderWithProviders(<EmbyPage />, { routePath: '/emby', initialEntry: '/emby' });

    await screen.findByRole('link', { name: 'Fixture Library' });
    await user.click(screen.getByRole('button', { name: '请求 Emby 扫描文件' }));
    expect(await screen.findByText('后端服务暂时不可用，请稍后再试。')).toBeVisible();

    await user.click(screen.getByRole('button', { name: '从 Emby 更新目录' }));
    await waitFor(() => expect(screen.getAllByText('后端服务暂时不可用，请稍后再试。')).toHaveLength(2));
    expect(screen.getByRole('link', { name: 'Fixture Library' })).toBeVisible();
    expect(router.state.location.pathname).toBe('/emby');
  });
});
