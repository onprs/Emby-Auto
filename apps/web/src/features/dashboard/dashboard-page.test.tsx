import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import type { Acquisition, DashboardSummary } from '@/api/generated/types.gen';
import { DashboardPage } from '@/features/dashboard/dashboard-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

function mappingAcquisition(): Acquisition {
  return {
    id: '71000000-0000-0000-0000-000000000001',
    mediaType: 'episode',
    seriesId: '71000000-0000-0000-0000-000000000002',
    seriesTitle: '待确认集数的番剧',
    sourceKind: 'rss',
    sourceSeason: 1,
    sourceEpisode: 7,
    tasks: [],
    mapping: { selectedVideoCount: 3, mappedVideoCount: 1, complete: false },
    aggregateStatus: 'mapping_pending',
    currentStage: 'mapping',
    overallProgress: 0.25,
    stages: [
      { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
      { key: 'download', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
      { key: 'mapping', status: 'waiting', progress: 1 / 3, completedItems: 1, totalItems: 3 },
      { key: 'transcode', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'subtitle', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'rename', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'organize', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'review', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'import', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
    ],
    createdAt: '2026-07-26T06:00:00Z',
    updatedAt: '2026-07-26T07:30:00Z',
  };
}

function dashboardSummary(): DashboardSummary {
  return {
    counts: {
      downloading: 0,
      processing: 0,
      awaitingReview: 0,
      importing: 0,
      attention: 1,
      failed: 0,
      cleanupFailed: 0,
      mappingPending: 1,
    },
    attentionItems: [{ acquisition: mappingAcquisition(), reason: 'mapping_required' }],
    recentOperations: [{
      id: '72000000-0000-0000-0000-000000000001',
      kind: 'download.enqueue',
      status: 'failed',
      errorCode: 'duplicate_torrent',
      updatedAt: '2026-07-26T07:00:00Z',
    }],
    recentImports: [],
    recentScans: [],
    dependencies: {
      qBittorrent: { configured: true, lastTestSuccess: true },
      tmdb: { configured: true, lastTestSuccess: true },
      emby: { configured: true, lastTestSuccess: true },
      mediaTools: { configured: true, lastTestSuccess: true },
      networkProxy: { configured: false },
      agent: { configured: false },
    },
    agentResolutions: {
      total: 0,
      reviewPending: 0,
      applied: 0,
      autoApplied: 0,
      accepted: 0,
      rejected: 0,
      failed: 0,
      inputTokens: 0,
      outputTokens: 0,
      averageLatencyMilliseconds: 0,
    },
    links: {
      downloading: '/acquisitions?phase=downloading',
      processing: '/acquisitions?phase=processing',
      awaitingReview: '/acquisitions?phase=awaiting_review',
      importing: '/acquisitions?phase=importing',
      failed: '/acquisitions?phase=attention',
      cleanupFailed: '/acquisitions?phase=attention',
      mappingPending: '/acquisitions?phase=mapping_pending',
    },
  };
}

const gibibyte = 1024 ** 3;

function systemMetrics() {
  return {
    sampledAt: '2026-07-26T08:00:04Z',
    sampleIntervalSeconds: 2,
    historyWindowSeconds: 120,
    availability: { cpu: true, memory: true, network: true, diskIO: true, diskCapacity: true },
    memory: { usedBytes: 8 * gibibyte, totalBytes: 16 * gibibyte },
    disks: [
      { device: 'nvme0n1', path: '/', usedBytes: 600 * gibibyte, totalBytes: 1000 * gibibyte, usedPercent: 60 },
      { device: 'sda', path: '/data/video/video1', usedBytes: 200 * gibibyte, totalBytes: 400 * gibibyte, usedPercent: 50 },
    ],
    samples: [
      { sampledAt: '2026-07-26T08:00:00Z', cpuUsedPercent: 30, memoryUsedPercent: 48, networkReceiveBytesPerSecond: 1024, networkSendBytesPerSecond: 512, diskReadBytesPerSecond: 2048, diskWriteBytesPerSecond: 1024 },
      { sampledAt: '2026-07-26T08:00:02Z', cpuUsedPercent: 36, memoryUsedPercent: 49, networkReceiveBytesPerSecond: 2048, networkSendBytesPerSecond: 1024, diskReadBytesPerSecond: 4096, diskWriteBytesPerSecond: 2048 },
      { sampledAt: '2026-07-26T08:00:04Z', cpuUsedPercent: 42, memoryUsedPercent: 50, networkReceiveBytesPerSecond: 4096, networkSendBytesPerSecond: 2048, diskReadBytesPerSecond: 8192, diskWriteBytesPerSecond: 4096 },
    ],
  };
}

describe('DashboardPage attention', () => {
  it('shows actionable acquisition information, host resource charts and failed operation history', async () => {
    server.use(
      http.get('*/api/v1/dashboard/summary', () => HttpResponse.json(dashboardSummary())),
      http.get('*/api/v1/dashboard/background-runtime', () => HttpResponse.json({ state: 'stopped' })),
      http.get('*/api/v1/dashboard/system-metrics', () => HttpResponse.json(systemMetrics())),
    );
    renderWithProviders(<DashboardPage />);

    const attention = await screen.findByLabelText('需要处理的任务');
    expect(within(attention).getByText('待确认集数的番剧')).toBeInTheDocument();
    expect(within(attention).getByText(/第 1 季第 7 集 · RSS 自动获取 · 剧集映射/)).toBeInTheDocument();
    expect(within(attention).getByText('需要确认剧集对应关系')).toBeInTheDocument();
    expect(within(attention).getByText('1 / 3 个视频已确认集数，其余文件无法继续处理。')).toBeInTheDocument();
    expect(within(attention).getByText(/完成剩余文件的季集映射/)).toBeInTheDocument();
    expect(within(attention).getByRole('link', { name: '设置剧集映射：待确认集数的番剧' }).getAttribute('href')).toMatch(
      /^\/acquisitions\/71000000-0000-0000-0000-000000000001(?:\?|$)/,
    );
    expect(within(attention).queryByText('添加下载')).not.toBeInTheDocument();
    expect(within(attention).queryByRole('link', { name: /运行/ })).not.toBeInTheDocument();

    expect(await screen.findByLabelText('CPU 使用率图表', undefined, { timeout: 5_000 })).toHaveTextContent('42%');
    const resources = screen.getByLabelText('系统资源');
    expect(within(resources).getByLabelText('内存使用率图表')).toHaveTextContent('8.0 GiB / 16.0 GiB');
    expect(within(resources).getByLabelText('网络速度图表')).toHaveTextContent('4.0 KiB/s');
    expect(within(resources).getByLabelText('磁盘资源图表')).toHaveTextContent('读取 8.0 KiB/s');
    expect(within(resources).getByLabelText('磁盘资源图表')).toHaveTextContent('写入 4.0 KiB/s');
    const diskPanel = within(resources).getByLabelText('磁盘', { exact: true });
    const disks = within(resources).getByLabelText('磁盘容量');
    expect(disks).toHaveTextContent('nvme0n1');
    expect(disks).toHaveTextContent('60%');
    expect(disks).toHaveTextContent('sda');
    expect(disks).toHaveTextContent('50%');
    expect(within(disks).queryByText('/', { exact: true })).not.toBeInTheDocument();
    expect(disks).not.toHaveTextContent('/data/video/video1');
    expect(diskPanel).not.toHaveTextContent('不同颜色代表不同磁盘设备');
    expect(diskPanel).not.toHaveTextContent('容量与 I/O 实时负载');

    const recent = screen.getByLabelText('最近运行记录');
    expect(within(recent).getByText('添加下载')).toBeInTheDocument();
    expect(within(recent).getByText('这个资源已经在下载列表中。')).toBeInTheDocument();
  });

  it('starts and stops the Worker from one runtime control', async () => {
    let state: 'running' | 'stopped' = 'stopped';
    const commands: Array<'running' | 'stopped'> = [];
    server.use(
      http.get('*/api/v1/dashboard/summary', () => HttpResponse.json(dashboardSummary())),
      http.get('*/api/v1/dashboard/background-runtime', () => HttpResponse.json({ state })),
      http.put('*/api/v1/dashboard/background-runtime', async ({ request }) => {
        const body = await request.json() as { state: 'running' | 'stopped' };
        state = body.state;
        commands.push(body.state);
        return HttpResponse.json({ state });
      }),
      http.get('*/api/v1/dashboard/system-metrics', () => HttpResponse.json(systemMetrics())),
    );
    renderWithProviders(<DashboardPage />);

    const control = await screen.findByLabelText('后台任务控制');
    expect(within(control).getByText('后台任务已停止')).toBeInTheDocument();
    await userEvent.click(within(control).getByRole('button', { name: '启动后台任务' }));
    expect(await within(control).findByText('后台任务运行中')).toBeInTheDocument();
    await userEvent.click(within(control).getByRole('button', { name: '停止后台任务' }));
    expect(await within(control).findByText('后台任务已停止')).toBeInTheDocument();
    expect(commands).toEqual(['running', 'stopped']);
  });

  it('keeps business dashboard content visible when resource metrics fail', async () => {
    server.use(
      http.get('*/api/v1/dashboard/summary', () => HttpResponse.json(dashboardSummary())),
      http.get('*/api/v1/dashboard/background-runtime', () => HttpResponse.json({ state: 'stopped' })),
      http.get('*/api/v1/dashboard/system-metrics', () => HttpResponse.json({
        code: 'service_unavailable',
        message: '系统指标暂时不可用',
        details: { dependency: 'system_metrics' },
      }, { status: 503 })),
    );
    renderWithProviders(<DashboardPage />);

    expect(await screen.findByText('待确认集数的番剧')).toBeInTheDocument();
    expect(await screen.findByRole('alert')).toHaveTextContent('系统指标暂时不可用');
    const resources = screen.getByLabelText('系统资源');
    expect(within(resources).getByRole('button', { name: '重试' })).toBeInTheDocument();
    expect(screen.getByLabelText('最近运行记录')).toHaveTextContent('添加下载');
  });
});
