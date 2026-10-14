import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';

import type { RssFeedLookup } from '@/api/generated/types.gen';
import { CreateSubscriptionForm } from '@/features/rss/create-subscription-form';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const feedUrl = 'https://feeds.example.test/show.xml';

function mockTmdbSearch(items: Array<{ tmdbSeriesId: number; name: string; originalName?: string; firstAirDate?: string }>) {
  const calls: string[] = [];
  server.use(
    http.get('*/api/v1/tmdb/series/search', ({ request }) => {
      calls.push(new URL(request.url).searchParams.get('query') ?? '');
      return HttpResponse.json({ items });
    }),
  );
  return calls;
}

function mockLookup(response: () => Response | Promise<Response>) {
  server.use(http.post('*/api/v1/rss/feed-lookup', response));
}

describe('CreateSubscriptionForm', () => {
  it('matches the TMDb series automatically from the feed URL and creates the subscription', async () => {
    mockLookup(() =>
      HttpResponse.json({
        feedUrl,
        feedTitle: 'Fixture Show',
        suggestedQuery: 'Fixture Show',
        suggestedQueries: ['Fixture Show'],
        sampleTitles: ['[Fixture] Fixture Show - S01E01'],
        catalogMatchSource: 'deterministic',
        candidates: [
          { tmdbSeriesId: 100, name: 'Fixture Show', originalName: 'Fixture Show', firstAirDate: '2026-01-01' },
          { tmdbSeriesId: 101, name: 'Fixture Show: Specials' },
        ],
      }),
    );
    const searchCalls = mockTmdbSearch([]);
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.post('*/api/v1/rss/subscriptions', async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({}, { status: 201 });
      }),
    );
    const onDone = vi.fn();
    renderWithProviders(<CreateSubscriptionForm onDone={onDone} />);

    await userEvent.type(await screen.findByLabelText('RSS 地址'), feedUrl);
    await userEvent.click(screen.getByRole('button', { name: /识别作品/ }));

    // 后端已经执行完整 query plan，页面直接展示合并后的候选。
    const candidate = await screen.findByRole('button', { name: /Fixture Show: Specials/ });
    expect(searchCalls).toEqual([]);
    await userEvent.click(candidate);

    expect(await screen.findByText(/ID 101/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('包含词'), '简日, 1080p, 简日');
    await userEvent.type(screen.getByLabelText('不包含词'), '720p，合集');
    await userEvent.click(screen.getByRole('checkbox', { name: '下载文件确认后自动完成剧集映射，无法唯一判断时使用已启用的 Agent' }));
    await userEvent.click(screen.getByRole('checkbox', { name: '媒体处理完成后自动审核并入库到 Emby' }));
    await userEvent.click(screen.getByRole('checkbox', { name: '最终集入库后，删除对应的 qBittorrent 种子和缓存文件' }));
    const submit = screen.getByRole('button', { name: '创建订阅' });
    expect(submit).toBeEnabled();
    await userEvent.click(submit);

    await waitFor(() => expect(captured).not.toBeNull());
    expect(captured).toMatchObject({
      tmdbSeriesId: 101,
      seriesTitle: 'Fixture Show: Specials',
      name: 'Fixture Show',
      feedUrl,
      includeKeywords: ['简日', '1080p'],
      excludeKeywords: ['720p', '合集'],
      autoEpisodeMapping: true,
      autoReview: true,
      cleanupSourceOnCompletion: true,
      sourceSeason: 1,
    });
    await waitFor(() => expect(onDone).toHaveBeenCalled());
  });

  it('uses the structured subtitle group extracted from the feed', async () => {
    mockLookup(() => HttpResponse.json({
      feedUrl,
      feedTitle: 'Fixture Show RSS',
      subtitleGroup: 'LoliHouse',
      subtitleGroupCandidates: ['LoliHouse', 'Other Group'],
      suggestedQuery: 'Fixture Show',
      suggestedQueries: ['Fixture Show'],
      sampleTitles: ['【LoliHouse】 Fixture Show - 01'],
      catalogMatchSource: 'deterministic',
      candidates: [{ tmdbSeriesId: 100, name: 'Fixture Show' }],
    }));
    renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);

    await userEvent.type(await screen.findByLabelText('RSS 地址'), feedUrl);
    await userEvent.click(screen.getByRole('button', { name: /识别作品/ }));

    expect(await screen.findByRole('button', { name: /Fixture Show/ })).toBeInTheDocument();
    expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('LoliHouse');
    expect(screen.getByDisplayValue('LoliHouse')).toHaveAttribute('list', 'rss-subtitle-group-options');
  });

  it('shows a loading state while identifying the feed', async () => {
    let release!: () => void;
    mockLookup(
      () =>
        new Promise<Response>((resolve) => {
          release = () => resolve(HttpResponse.json({
            feedUrl,
            suggestedQuery: 'Fixture Show',
            suggestedQueries: ['Fixture Show'],
            sampleTitles: [],
            catalogMatchSource: 'deterministic',
            candidates: [{ tmdbSeriesId: 100, name: 'Fixture Show' }],
          }));
        }),
    );
    mockTmdbSearch([{ tmdbSeriesId: 100, name: 'Fixture Show' }]);
    renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);

    await userEvent.type(await screen.findByLabelText('RSS 地址'), feedUrl);
    await userEvent.click(screen.getByRole('button', { name: /识别作品/ }));
    expect(await screen.findByText('正在识别 RSS 内容…')).toBeInTheDocument();
    release();
    expect(await screen.findByRole('button', { name: /Fixture Show/ })).toBeInTheDocument();
  });

  it('falls back to manual keyword search when the feed cannot be identified', async () => {
    mockLookup(() => HttpResponse.json({ code: 'rss_fetch_failed', message: 'fetch failed', details: {}, requestId: 'r1' }, { status: 503 }));
    const searchCalls = mockTmdbSearch([{ tmdbSeriesId: 200, name: '手动匹配作品' }]);
    renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);

    await userEvent.type(await screen.findByLabelText('RSS 地址'), feedUrl);
    await userEvent.click(screen.getByRole('button', { name: /识别作品/ }));

    expect(await screen.findByText('无法自动识别 RSS 内容，请手动搜索作品。')).toBeInTheDocument();
    const keyword = screen.getByLabelText('作品搜索关键词');
    await userEvent.type(keyword, '手动匹配作品');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));

    const candidate = await screen.findByRole('button', { name: /手动匹配作品/ });
    expect(searchCalls).toContain('手动匹配作品');
    await userEvent.click(candidate);
    expect(await screen.findByText(/ID 200/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '创建订阅' })).toBeEnabled();
  });

  it('loads only the scoped TMDb candidates proposed by Agent fallback', async () => {
    const resolutionId = '10000000-0000-0000-0000-000000000001';
    mockLookup(() => HttpResponse.json({
      feedUrl,
      feedTitle: 'Group Unknown Words',
      suggestedQuery: 'Group Unknown Words',
      suggestedQueries: ['Group Unknown Words', 'Unknown'],
      sampleTitles: ['[Group] Unknown Words - 01'],
      catalogMatchSource: 'agent_pending',
      candidates: [],
      agentResolutionId: resolutionId,
    }));
    server.use(
      http.get(`*/api/v1/agent/resolutions/${resolutionId}`, () => HttpResponse.json({
        id: resolutionId,
        status: 'review_required',
        validation: { verdict: 'review_required', reasonCodes: ['catalog_candidate_requires_user_confirmation'] },
        proposal: {
          capability: 'catalog_candidate',
          query: 'Agent Chosen Title',
          candidateIds: [401],
          evidenceCodes: ['title_match'],
          decision: 'resolved',
        },
      })),
    );
    const searchCalls = mockTmdbSearch([
      { tmdbSeriesId: 401, name: 'Agent Chosen Title' },
      { tmdbSeriesId: 402, name: 'Unscoped Search Result' },
    ]);
    renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);

    await userEvent.type(await screen.findByLabelText('RSS 地址'), feedUrl);
    await userEvent.click(screen.getByRole('button', { name: /识别作品/ }));

    expect(await screen.findByRole('button', { name: /Agent Chosen Title/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Unscoped Search Result/ })).not.toBeInTheDocument();
    expect(searchCalls).toEqual(['Agent Chosen Title']);
  });

  it('rejects an unvalidated proposal left by a failed Agent resolution', async () => {
    const resolutionId = '10000000-0000-0000-0000-000000000002';
    mockLookup(() => HttpResponse.json({
      feedUrl,
      suggestedQuery: 'Unknown',
      suggestedQueries: ['Unknown'],
      sampleTitles: ['Unknown - 01'],
      catalogMatchSource: 'agent_pending',
      candidates: [],
      agentResolutionId: resolutionId,
    }));
    server.use(
      http.get(`*/api/v1/agent/resolutions/${resolutionId}`, () => HttpResponse.json({
        id: resolutionId,
        status: 'failed',
        validation: { verdict: 'invalid', reasonCodes: ['agent_tool_scope_violation'] },
        proposal: {
          capability: 'catalog_candidate',
          query: 'Unvalidated Title',
          candidateIds: [999],
          evidenceCodes: [],
          decision: 'resolved',
        },
      })),
    );
    const searchCalls = mockTmdbSearch([]);
    renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);

    await userEvent.type(await screen.findByLabelText('RSS 地址'), feedUrl);
    await userEvent.click(screen.getByRole('button', { name: /识别作品/ }));

    expect(await screen.findByText('Agent 未能确认 TMDb 作品，请手动搜索。')).toBeInTheDocument();
    expect(screen.getByLabelText('作品搜索关键词')).toBeInTheDocument();
    expect(searchCalls).toEqual([]);
  });

  it('lets the user correct the keyword when the automatic match finds nothing', async () => {
    mockLookup(() => HttpResponse.json({
      feedUrl,
      feedTitle: 'Unknown Feed',
      suggestedQuery: 'Unknown Feed',
      suggestedQueries: ['Unknown Feed'],
      sampleTitles: [],
      catalogMatchSource: 'none',
      candidates: [],
    }));
    const searchCalls: string[] = [];
    server.use(
      http.get('*/api/v1/tmdb/series/search', ({ request }) => {
        const query = new URL(request.url).searchParams.get('query') ?? '';
        searchCalls.push(query);
        return HttpResponse.json({ items: query === '修正后的名字' ? [{ tmdbSeriesId: 300, name: '修正后的名字' }] : [] });
      }),
    );
    renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);

    await userEvent.type(await screen.findByLabelText('RSS 地址'), feedUrl);
    await userEvent.click(screen.getByRole('button', { name: /识别作品/ }));

    expect(await screen.findByText('未找到匹配作品，请修正关键词后重试。')).toBeInTheDocument();
    const keyword = screen.getByLabelText('作品搜索关键词');
    await userEvent.clear(keyword);
    await userEvent.type(keyword, '修正后的名字');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));

    const candidate = await screen.findByRole('button', { name: /修正后的名字/ });
    expect(searchCalls).toEqual(['修正后的名字']);
    await userEvent.click(candidate);
    expect(screen.getByRole('button', { name: '创建订阅' })).toBeEnabled();
  });
});

function subtitleLookup(address: string, name: string): RssFeedLookup {
  return {
    feedUrl: address,
    feedTitle: '动画订阅',
    suggestedQuery: '动画作品',
    suggestedQueries: ['动画作品'],
    sampleTitles: [],
    candidates: [{ tmdbSeriesId: 100, name: `${name} 动画` }],
    catalogMatchSource: 'deterministic',
    subtitleGroup: name,
    subtitleGroupCandidates: [name],
  };
}

function lookupGate() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

describe('字幕组自动填写的请求状态', () => {
  it('更换地址后清理旧自动名称并填写新识别名称', async () => {
    server.use(http.post('*/api/v1/rss/feed-lookup', async ({ request }) => {
      const { feedUrl: address } = await request.json() as { feedUrl: string };
      return HttpResponse.json(subtitleLookup(address, address.endsWith('first.xml') ? 'ReleaseTeam' : 'TranslationTeam'));
    }));
    renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);
    const address = await screen.findByLabelText('RSS 地址');
    await userEvent.type(address, 'https://example.test/first.xml');
    await userEvent.tab();
    await waitFor(() => expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('ReleaseTeam'));
    await userEvent.clear(address);
    expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('');
    await userEvent.type(address, 'https://example.test/second.xml');
    await userEvent.tab();

    await waitFor(() => expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('TranslationTeam'));
    expect(document.querySelector('option[value="TranslationTeam"]')).not.toBeNull();
  });

  it('更换地址后保留用户手动名称', async () => {
    server.use(http.post('*/api/v1/rss/feed-lookup', async ({ request }) => {
      const { feedUrl: address } = await request.json() as { feedUrl: string };
      return HttpResponse.json(subtitleLookup(address, 'ReleaseTeam'));
    }));
    renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);
    await userEvent.type(await screen.findByLabelText('订阅源名称（字幕组）'), '自定义发布版本');
    await userEvent.type(screen.getByLabelText('RSS 地址'), 'https://example.test/first.xml');
    await userEvent.tab();
    await screen.findByRole('button', { name: /ReleaseTeam 动画/ });
    await userEvent.clear(screen.getByLabelText('RSS 地址'));
    await userEvent.type(screen.getByLabelText('RSS 地址'), 'https://example.test/second.xml');
    await userEvent.tab();
    await screen.findByRole('button', { name: /ReleaseTeam 动画/ });

    expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('自定义发布版本');
  });

  it('请求期间手动输入的名称不被完成结果覆盖', async () => {
    const gate = lookupGate();
    let started = false;
    mockLookup(async () => {
      started = true;
      await gate.promise;
      return HttpResponse.json(subtitleLookup(feedUrl, 'ReleaseTeam'));
    });
    const { queryClient } = renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);
    await userEvent.type(await screen.findByLabelText('RSS 地址'), feedUrl);
    await userEvent.tab();
    await waitFor(() => expect(started).toBe(true));
    await userEvent.type(screen.getByLabelText('订阅源名称（字幕组）'), '手动确认版本');
    await act(async () => gate.resolve());
    await waitFor(() => expect(queryClient.isMutating()).toBe(0));

    expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('手动确认版本');
  });

  it('用户主动清空名称后保持留空', async () => {
    server.use(http.post('*/api/v1/rss/feed-lookup', async ({ request }) => {
      const { feedUrl: address } = await request.json() as { feedUrl: string };
      return HttpResponse.json(subtitleLookup(address, 'ReleaseTeam'));
    }));
    renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);
    await userEvent.type(await screen.findByLabelText('RSS 地址'), feedUrl);
    await userEvent.tab();
    await waitFor(() => expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('ReleaseTeam'));
    await userEvent.clear(screen.getByLabelText('订阅源名称（字幕组）'));
    await userEvent.clear(screen.getByLabelText('RSS 地址'));
    await userEvent.type(screen.getByLabelText('RSS 地址'), 'https://example.test/second.xml');
    await userEvent.tab();
    await screen.findByRole('button', { name: /ReleaseTeam 动画/ });

    expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('');
  });

  it('地址已变更而未重新识别时忽略旧地址的完成结果', async () => {
    const gate = lookupGate();
    let started = false;
    mockLookup(async () => {
      started = true;
      await gate.promise;
      return HttpResponse.json(subtitleLookup(feedUrl, 'OldReleaseTeam'));
    });
    const { queryClient } = renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);
    const address = await screen.findByLabelText('RSS 地址');
    await userEvent.type(address, feedUrl);
    await userEvent.tab();
    await waitFor(() => expect(started).toBe(true));
    fireEvent.change(address, { target: { value: 'https://example.test/second.xml' } });
    await act(async () => gate.resolve());
    await waitFor(() => expect(queryClient.isMutating()).toBe(0));

    expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('');
    expect(document.querySelector('option[value="OldReleaseTeam"]')).toBeNull();
    expect(screen.queryByRole('button', { name: /OldReleaseTeam 动画/ })).not.toBeInTheDocument();
  });

  it.each(['成功', '失败'])('新地址先完成后忽略旧请求的%s响应', async (outcome) => {
    const gate = lookupGate();
    let oldStarted = false;
    server.use(http.post('*/api/v1/rss/feed-lookup', async ({ request }) => {
      const { feedUrl: address } = await request.json() as { feedUrl: string };
      if (address === feedUrl) {
        oldStarted = true;
        await gate.promise;
        return outcome === '成功'
          ? HttpResponse.json(subtitleLookup(address, 'OldReleaseTeam'))
          : HttpResponse.json({ code: 'rss_fetch_failed', message: '旧请求失败', details: {} }, { status: 503 });
      }
      return HttpResponse.json(subtitleLookup(address, 'CurrentReleaseTeam'));
    }));
    const { queryClient } = renderWithProviders(<CreateSubscriptionForm onDone={() => {}} />);
    const address = await screen.findByLabelText('RSS 地址');
    await userEvent.type(address, feedUrl);
    await userEvent.tab();
    await waitFor(() => expect(oldStarted).toBe(true));
    await userEvent.clear(address);
    await userEvent.type(address, 'https://example.test/second.xml');
    await userEvent.tab();
    await screen.findByRole('button', { name: /CurrentReleaseTeam 动画/ });
    await act(async () => gate.resolve());
    await waitFor(() => expect(queryClient.isMutating()).toBe(0));

    expect(screen.getByLabelText('订阅源名称（字幕组）')).toHaveValue('CurrentReleaseTeam');
    expect(screen.getByRole('button', { name: /CurrentReleaseTeam 动画/ })).toBeVisible();
    expect(screen.queryByText('无法自动识别 RSS 内容，请手动搜索作品。')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /OldReleaseTeam 动画/ })).not.toBeInTheDocument();
  });
});
