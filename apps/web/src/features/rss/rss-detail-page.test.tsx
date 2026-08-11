import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import type { RssEntry, RssSubscription } from '@/api/generated/types.gen';
import { RssDetailPage } from '@/features/rss/rss-detail-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const subscriptionId = '30000000-0000-0000-0000-000000000001';
const subscription: RssSubscription = {
  id: subscriptionId,
  seriesId: '30000000-0000-0000-0000-000000000002',
  tmdbSeriesId: 42,
  seriesTitle: '条目排序作品',
  name: '测试订阅',
  feedUrl: 'https://example.test/feed.xml',
  includeKeywords: ['简日', '1080p'],
  excludeKeywords: ['720p'],
  enabled: true,
  autoEpisodeMapping: false,
  autoReview: false,
  cleanupSourceOnCompletion: false,
  sourceSeason: 1,
  pollIntervalSeconds: 900,
  overallProgress: 0.625,
  taskCount: 4,
  completedTaskCount: 2,
  attentionTaskCount: 1,
  version: 1,
  createdAt: '2026-07-26T01:00:00Z',
  updatedAt: '2026-07-26T02:00:00Z',
};
const entry: RssEntry = {
  id: '30000000-0000-0000-0000-000000000003',
  subscriptionId,
  acquisitionId: '30000000-0000-0000-0000-000000000004',
  acquisitionProgress: {
    aggregateStatus: 'processing',
    currentStage: 'transcode',
    overallProgress: 0.473,
  },
  title: '第 2 集',
  status: 'enqueued',
  classification: 'enqueued',
  duplicateCount: 0,
  adjudicationState: 'not_required',
  downloadUriAvailable: true,
  sourceSeason: 1,
  sourceEpisode: 2,
  createdAt: '2026-07-26T02:00:00Z',
  updatedAt: '2026-07-26T02:00:00Z',
};

const filteredEntry: RssEntry = {
  ...entry,
  id: '30000000-0000-0000-0000-000000000005',
  acquisitionId: undefined,
  acquisitionProgress: undefined,
  title: '被过滤的第 3 集',
  status: 'discovered',
  classification: 'rejected',
  rejectReason: 'title_include_mismatch',
  sourceEpisode: 3,
};

function groupedEntryPage(request: Request, confirmed: RssEntry[], skipped: RssEntry[] = []) {
  const group = new URL(request.url).searchParams.get('group');
  return HttpResponse.json({ items: group === 'skipped' ? skipped : confirmed });
}

describe('RssDetailPage entries', () => {
  it('shows fine-grained progress, sorts by it, and opens the episode detail from the title', async () => {
    const requests: string[] = [];
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(subscription)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, ({ request }) => {
        requests.push(request.url);
        return groupedEntryPage(request, [entry], [filteredEntry]);
      }),
    );
    const { router } = renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    expect(await screen.findByText('已确认的 RSS 任务')).toBeInTheDocument();
    expect(screen.getByText('已跳过的 RSS 更新')).toBeInTheDocument();
    expect(screen.getAllByText('未命中包含词')).toHaveLength(2);
    expect(requests.some((url) => new URL(url).searchParams.get('group') === 'confirmed')).toBe(true);
    expect(requests.some((url) => new URL(url).searchParams.get('group') === 'skipped')).toBe(true);
    expect(await screen.findByText('简日、1080p')).toBeInTheDocument();
    expect(screen.getByText('720p')).toBeInTheDocument();
    expect((await screen.findAllByRole('link', { name: entry.title }))).toHaveLength(2);
    expect(screen.getByRole('progressbar', { name: `${subscription.name}总进度` })).toHaveAttribute('aria-valuenow', '62.5');
    expect(screen.getByText('1 个需处理 · 2 / 4 已完成')).toBeInTheDocument();
    expect(screen.queryByText('查看处理进度')).not.toBeInTheDocument();
    for (const progress of screen.getAllByRole('progressbar', { name: `${entry.title}处理进度` })) {
      expect(progress).toHaveAttribute('aria-valuenow', '47.3');
      expect(progress).toHaveAttribute('aria-valuetext', '47.3%');
    }

    await userEvent.click(screen.getAllByRole('button', { name: '处理进度，点击按正序排列' })[0]);
    await waitFor(() => {
      const url = new URL(requests.at(-1)!);
      expect(url.searchParams.get('sortBy')).toBe('progress');
      expect(url.searchParams.get('sortOrder')).toBe('asc');
    });
    expect(router.state.location.search).toMatchObject({ sortBy: 'progress', sortOrder: 'asc' });

    await userEvent.click(screen.getAllByRole('button', { name: '处理进度，点击按逆序排列' })[0]);
    await waitFor(() => expect(new URL(requests.at(-1)!).searchParams.get('sortOrder')).toBe('desc'));

    await userEvent.click(screen.getAllByRole('link', { name: entry.title })[0]);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/acquisitions/${entry.acquisitionId}`));
  });

  it('shows historical enqueued catalog fulfillment only in the skipped list', async () => {
    const catalogFulfilledEntry: RssEntry = {
      ...entry,
      id: '30000000-0000-0000-0000-000000000006',
      acquisitionId: undefined,
      acquisitionProgress: undefined,
      title: '媒体库已有的第 1 集',
      status: 'enqueued',
      classification: 'rejected',
      rejectReason: 'target_episode_in_library',
      downloadUriAvailable: false,
      sourceEpisode: 1,
      importedAt: '2026-08-04T08:26:00Z',
    };
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(subscription)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, ({ request }) => groupedEntryPage(request, [], [catalogFulfilledEntry])),
    );

    renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    expect(await screen.findByText('暂无已确认任务')).toBeInTheDocument();
    expect(screen.getByText('运行中')).toBeInTheDocument();
    expect(screen.getAllByText('媒体库已有该集')).toHaveLength(2);
    expect(screen.queryByText('已安排下载')).not.toBeInTheDocument();
  });

  it('keeps background Agent adjudication out of the RSS task UI', async () => {
    let agentRequests = 0;
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(subscription)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, ({ request }) => groupedEntryPage(request, [])),
      http.get('*/api/v1/agent/resolutions', () => {
        agentRequests += 1;
        return HttpResponse.json({ items: [] });
      }),
    );

    renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    expect(await screen.findByText('暂无已确认任务')).toBeInTheDocument();
    expect(screen.queryByText('等待发布筛选')).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '发布筛选' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '接受' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '拒绝' })).not.toBeInTheDocument();
    expect(agentRequests).toBe(0);
  });

  it('presents a retained completed subscription without runnable commands', async () => {
    const completed: RssSubscription = {
      ...subscription,
      enabled: false,
      completedAt: '2026-07-29T05:07:02Z',
      nextPollAt: undefined,
      overallProgress: 1,
      taskCount: 12,
      completedTaskCount: 12,
      attentionTaskCount: 0,
    };
    const archivedEntry: RssEntry = {
      ...entry,
      importedAt: '2026-07-29T05:05:00Z',
      acquisitionProgress: {
        aggregateStatus: 'completed',
        currentStage: 'import',
        overallProgress: 1,
      },
    };
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(completed)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, ({ request }) => groupedEntryPage(request, [archivedEntry])),
    );

    renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    expect(await screen.findByText('订阅已完成')).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: `${subscription.name}总进度` })).toHaveAttribute('aria-valuenow', '100');
    expect(screen.getByText('完成时间')).toBeInTheDocument();
    expect(screen.getAllByText('已完成').length).toBeGreaterThan(0);
    expect(screen.getAllByRole('link', { name: archivedEntry.title })).toHaveLength(2);
    for (const progress of screen.getAllByRole('progressbar', { name: `${archivedEntry.title}处理进度` })) {
      expect(progress).toHaveAttribute('aria-valuenow', '100');
    }
    expect(screen.queryByRole('button', { name: '立即检查' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '启用' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '开启自动审核' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '编辑' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '删除' })).toBeInTheDocument();
  });

  it('shows enqueue failures as actionable errors instead of RSS rule rejections', async () => {
    const failedEntry: RssEntry = {
      ...entry,
      status: 'enqueue_failed',
      classification: 'enqueue_failed',
      acquisitionProgress: {
        aggregateStatus: 'failed',
        currentStage: 'download',
        overallProgress: 0.02,
      },
      errorCode: 'download_no_main_video',
      errorMessage: 'the torrent contains no selectable main video',
      // A stale API may still send the old overloaded field. Classification
      // and status remain authoritative for presentation.
      rejectReason: 'the torrent contains no selectable main video',
    };
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(subscription)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, ({ request }) => groupedEntryPage(request, [failedEntry])),
    );

    renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    expect(await screen.findAllByText('资源中没有找到可处理的正片视频。')).toHaveLength(2);
    expect(screen.getByText('已跳过的 RSS 更新')).toBeInTheDocument();
    expect(screen.getByText('暂无已跳过更新')).toBeInTheDocument();
    expect(screen.queryByText('不符合自动获取规则')).not.toBeInTheDocument();
  });

  it('enables persistent automatic review from the subscription actions', async () => {
    let current = subscription;
    let updateBody: Record<string, unknown> | undefined;
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(current)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, () => HttpResponse.json({ items: [] })),
      http.put(`*/api/v1/rss/subscriptions/${subscriptionId}`, async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        current = { ...current, autoReview: true, version: current.version + 1 };
        return HttpResponse.json(current);
      }),
    );

    renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    expect((await screen.findAllByText('已关闭')).length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole('button', { name: '开启自动审核' }));
    await waitFor(() => expect(updateBody).toMatchObject({
      expectedVersion: subscription.version,
      includeKeywords: subscription.includeKeywords,
      excludeKeywords: subscription.excludeKeywords,
      enabled: true,
      autoEpisodeMapping: false,
      autoReview: true,
    }));
    expect(await screen.findByRole('button', { name: '关闭自动审核' })).toBeInTheDocument();
    expect(screen.getByText('已开启')).toBeInTheDocument();
  });

  it('enables persistent automatic episode Mapping from the subscription actions', async () => {
    let current = subscription;
    let updateBody: Record<string, unknown> | undefined;
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(current)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, () => HttpResponse.json({ items: [] })),
      http.put(`*/api/v1/rss/subscriptions/${subscriptionId}`, async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        current = { ...current, autoEpisodeMapping: true, version: current.version + 1 };
        return HttpResponse.json(current);
      }),
    );

    renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    await screen.findByText(subscription.name);
    await userEvent.click(screen.getByRole('button', { name: '开启自动映射' }));
    await waitFor(() => expect(updateBody).toMatchObject({
      expectedVersion: subscription.version,
      autoEpisodeMapping: true,
      autoReview: false,
    }));
    expect(await screen.findByRole('button', { name: '关闭自动映射' })).toBeInTheDocument();
  });

  it('edits include and exclude keywords on an existing subscription', async () => {
    let current = subscription;
    let updateBody: Record<string, unknown> | undefined;
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(current)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, () => HttpResponse.json({ items: [] })),
      http.put(`*/api/v1/rss/subscriptions/${subscriptionId}`, async ({ request }) => {
        updateBody = (await request.json()) as Record<string, unknown>;
        current = {
          ...current,
          includeKeywords: updateBody.includeKeywords as string[],
          excludeKeywords: updateBody.excludeKeywords as string[],
          version: current.version + 1,
        };
        return HttpResponse.json(current);
      }),
    );

    renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    await screen.findByText(subscription.name);
    await userEvent.click(screen.getByRole('button', { name: '编辑' }));
    const includes = screen.getByLabelText('包含词');
    const excludes = screen.getByLabelText('不包含词');
    await userEvent.clear(includes);
    await userEvent.type(includes, 'CHS, 2160p, chs');
    await userEvent.clear(excludes);
    await userEvent.type(excludes, '720p，合集');
    await userEvent.click(screen.getByRole('checkbox', { name: '下载文件确认后自动完成剧集映射，无法唯一判断时使用已启用的 Agent' }));
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(updateBody).toMatchObject({
      includeKeywords: ['CHS', '2160p'],
      excludeKeywords: ['720p', '合集'],
      autoEpisodeMapping: true,
    }));
    expect(await screen.findByText('CHS、2160p')).toBeInTheDocument();
    expect(screen.getByText('720p、合集')).toBeInTheDocument();
  });

  it('explicitly requests deletion of imported media from the destructive confirmation', async () => {
    let deleteImported: string | null = null;
    const operationId = '30000000-0000-0000-0000-000000000010';
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(subscription)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, () => HttpResponse.json({ items: [] })),
      http.delete(`*/api/v1/rss/subscriptions/${subscriptionId}`, ({ request }) => {
        deleteImported = new URL(request.url).searchParams.get('deleteImported');
        return HttpResponse.json({ operationId, status: 'queued' }, { status: 202 });
      }),
      http.get(`*/api/v1/operations/${operationId}`, () => HttpResponse.json({
        id: operationId,
        resourceType: 'rss_subscription',
        resourceId: subscriptionId,
        kind: 'rss.subscription.delete',
        status: 'running',
        attempt: 1,
        maxAttempts: 3,
        createdAt: '2026-07-26T03:00:00Z',
        updatedAt: '2026-07-26T03:00:01Z',
      })),
    );

    renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    await screen.findByText(subscription.name);
    await userEvent.click(screen.getByRole('button', { name: '删除' }));
    await userEvent.click(screen.getByRole('checkbox', { name: '同时删除已经入库到 Emby 的视频和 ASS 字幕' }));
    expect(screen.getByText('删除本订阅已经入库的视频和 ASS 字幕，并刷新 Emby 媒体库。')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: '全部删除' }));

    await waitFor(() => expect(deleteImported).toBe('true'));
  });

  it('reports a backend contract mismatch when an acquisition entry omits lifecycle progress', async () => {
    server.use(
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}`, () => HttpResponse.json(subscription)),
      http.get(`*/api/v1/rss/subscriptions/${subscriptionId}/entries`, ({ request }) => groupedEntryPage(request, [
        { ...entry, acquisitionProgress: undefined },
      ])),
    );

    renderWithProviders(<RssDetailPage subscriptionId={subscriptionId} />, {
      routePath: '/rss/$subscriptionId',
      initialEntry: `/rss/${subscriptionId}`,
    });

    expect(await screen.findByText('后端服务版本与当前页面不匹配，请重启 API 和 Worker 后重试。')).toBeInTheDocument();
  });
});
