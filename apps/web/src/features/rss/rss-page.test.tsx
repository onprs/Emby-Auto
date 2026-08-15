import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';

import type { RssSubscription } from '@/api/generated/types.gen';
import { RssPage } from '@/features/rss/rss-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

function subscription(id: string, name: string): RssSubscription {
  return {
    id,
    seriesId: '10000000-0000-0000-0000-000000000001',
    tmdbSeriesId: 42,
    seriesTitle: '排序测试作品',
    name,
    feedUrl: `https://example.test/${id}.xml`,
    includeKeywords: [],
    excludeKeywords: [],
    enabled: true,
    autoEpisodeMapping: false,
    autoReview: false,
    cleanupSourceOnCompletion: false,
    sourceSeason: 1,
    pollIntervalSeconds: 900,
    overallProgress: 0.362,
    taskCount: 3,
    completedTaskCount: 1,
    attentionTaskCount: 0,
    nextPollAt: '2026-07-26T03:00:00Z',
    version: 2,
    createdAt: '2026-07-26T01:00:00Z',
    updatedAt: '2026-07-26T02:00:00Z',
  };
}

describe('RssPage list controls', () => {
  it('sorts through subscription column headers without a separate sort select', async () => {
    const item = subscription('20000000-0000-0000-0000-000000000001', '每周更新');
    const requests: string[] = [];
    server.use(
      http.get('*/api/v1/rss/subscriptions', ({ request }) => {
        requests.push(request.url);
        return HttpResponse.json({ items: [item] });
      }),
    );
    const { router } = renderWithProviders(<RssPage />, { routePath: '/rss', initialEntry: '/rss' });

    await screen.findAllByText(item.name);
    expect(screen.queryByLabelText('排序')).not.toBeInTheDocument();
    expect(screen.getAllByRole('progressbar', { name: `${item.name}总进度` })[0]).toHaveAttribute('aria-valuenow', '36.2');
    expect(screen.getAllByText('1 / 3 个任务已完成')[0]).toBeInTheDocument();
    expect(screen.getAllByText('36.2%')[0]).toBeInTheDocument();

    await userEvent.click(screen.getAllByRole('button', { name: '总进度，点击按正序排列' })[0]);
    await waitFor(() => {
      const url = new URL(requests.at(-1)!);
      expect(url.searchParams.get('sortBy')).toBe('progress');
      expect(url.searchParams.get('sortOrder')).toBe('asc');
    });
    expect(router.state.location.search).toMatchObject({ sortBy: 'progress', sortOrder: 'asc' });

    await userEvent.click(screen.getAllByRole('button', { name: '总进度，点击按逆序排列' })[0]);
    await waitFor(() => expect(new URL(requests.at(-1)!).searchParams.get('sortOrder')).toBe('desc'));
  });

  it('searches subscriptions by keyword through the query box', async () => {
    const item = subscription('20000000-0000-0000-0000-000000000005', '葬送的芙莉莲');
    const requests: string[] = [];
    server.use(
      http.get('*/api/v1/rss/subscriptions', ({ request }) => {
        requests.push(request.url);
        return HttpResponse.json({ items: [item] });
      }),
    );
    const { router } = renderWithProviders(<RssPage />, { routePath: '/rss', initialEntry: '/rss' });

    await screen.findAllByText(item.name);
    await userEvent.type(screen.getByLabelText('搜索订阅'), '芙莉莲');
    await userEvent.click(screen.getByRole('button', { name: '搜索' }));
    await waitFor(() => {
      const url = new URL(requests.at(-1)!);
      expect(url.searchParams.get('query')).toBe('芙莉莲');
    });
    expect(router.state.location.search).toMatchObject({ query: '芙莉莲' });
  });

  it('shows retained completed subscriptions as complete instead of waiting for content', async () => {
    const item: RssSubscription = {
      ...subscription('20000000-0000-0000-0000-000000000003', '已完成订阅'),
      enabled: false,
      completedAt: '2026-07-29T05:07:02Z',
      nextPollAt: undefined,
      overallProgress: 0,
      taskCount: 0,
      completedTaskCount: 0,
    };
    server.use(http.get('*/api/v1/rss/subscriptions', () => HttpResponse.json({ items: [item] })));

    renderWithProviders(<RssPage />, { routePath: '/rss', initialEntry: '/rss' });

    expect((await screen.findAllByText('订阅已完成')).length).toBeGreaterThan(0);
    expect(screen.queryByText(/等待发现内容/)).not.toBeInTheDocument();
    for (const progress of screen.getAllByRole('progressbar', { name: `${item.name}总进度` })) {
      expect(progress).toHaveAttribute('aria-valuenow', '100');
    }
    expect(screen.getAllByText(/下次检查 已完成|^已完成$/).length).toBeGreaterThan(0);
  });

  it('shows an incomplete disabled subscription as paused without forcing completion', async () => {
    const item: RssSubscription = {
      ...subscription('20000000-0000-0000-0000-000000000004', '暂停订阅'),
      enabled: false,
      nextPollAt: undefined,
      overallProgress: 0.5,
      taskCount: 2,
      completedTaskCount: 1,
    };
    server.use(http.get('*/api/v1/rss/subscriptions', () => HttpResponse.json({ items: [item] })));

    renderWithProviders(<RssPage />, { routePath: '/rss', initialEntry: '/rss' });

    expect((await screen.findAllByText('已暂停 · 1 / 2 个任务已完成')).length).toBeGreaterThan(0);
    expect(screen.queryByText(/已停用/)).not.toBeInTheDocument();
    for (const progress of screen.getAllByRole('progressbar', { name: `${item.name}总进度` })) {
      expect(progress).toHaveAttribute('aria-valuenow', '50');
    }
  });

  it('rejects an RSS response from a backend that does not provide aggregate progress', async () => {
    const item = subscription('20000000-0000-0000-0000-000000000002', '旧版订阅响应');
    const { overallProgress: _overallProgress, taskCount: _taskCount, completedTaskCount: _completedTaskCount, attentionTaskCount: _attentionTaskCount, ...legacyItem } = item;
    server.use(http.get('*/api/v1/rss/subscriptions', () => HttpResponse.json({ items: [legacyItem] })));

    renderWithProviders(<RssPage />, { routePath: '/rss', initialEntry: '/rss' });

    expect(await screen.findByText('后端服务版本与当前页面不匹配，请重启 API 和 Worker 后重试。')).toBeInTheDocument();
  });

  it('batch deletes the current selection and reports completion on the RSS page', async () => {
    const items = [
      subscription('20000000-0000-0000-0000-000000000011', '订阅 A'),
      subscription('20000000-0000-0000-0000-000000000012', '订阅 B'),
    ];
    const deleted = vi.fn();
    server.use(
      http.get('*/api/v1/rss/subscriptions', () => HttpResponse.json({ items })),
      http.delete('*/api/v1/rss/subscriptions/:subscriptionId', ({ params }) => {
        deleted(params.subscriptionId);
        return HttpResponse.json({
          operationId: params.subscriptionId,
          resourceType: 'rss_subscription',
          resourceId: params.subscriptionId,
          kind: 'rss.subscription.delete',
          status: 'queued',
        }, { status: 202 });
      }),
      http.get('*/api/v1/operations/:operationId', ({ params }) => HttpResponse.json({
        id: params.operationId,
        resourceType: 'rss_subscription',
        resourceId: params.operationId,
        kind: 'rss.subscription.delete',
        status: 'succeeded',
        attempt: 1,
        maxAttempts: 3,
        createdAt: '2026-07-26T03:00:00Z',
        updatedAt: '2026-07-26T03:00:01Z',
      })),
    );
    const { router } = renderWithProviders(<RssPage />, { routePath: '/rss', initialEntry: '/rss' });

    await screen.findAllByText('订阅 A');
    await userEvent.click(screen.getAllByRole('checkbox', { name: '全选当前页订阅' })[0]);
    await userEvent.click(screen.getByRole('button', { name: '批量删除' }));
    await userEvent.click(screen.getByRole('button', { name: '确认批量删除' }));

    await waitFor(() => expect(deleted).toHaveBeenCalledTimes(2));
    expect(router.state.location.pathname).toBe('/rss');
    expect(await screen.findByText('已成功删除 2 项')).toBeInTheDocument();
  });
});
