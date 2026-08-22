import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClientProvider } from '@tanstack/react-query';
import { http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { SearchesPage } from '@/features/searches/searches-page';
import { SearchAcquisitionsSection } from '@/features/searches/search-acquisitions-section';
import { CandidateTable } from '@/features/searches/candidate-selection';
import { server } from '@/test/msw-server';
import { createTestQueryClient, renderWithProviders } from '@/test/render';
import type { Acquisition, ReleaseCandidate } from '@/api/generated/types.gen';

const SEARCH_ID = 'aaaaaaaa-0000-4000-8000-000000000001';
const CANDIDATE_NEW = 'bbbbbbbb-0000-4000-8000-000000000010';
const CANDIDATE_OLD = 'bbbbbbbb-0000-4000-8000-000000000011';
const ACQ_ID = 'cccccccc-0000-4000-8000-000000000020';

function candidateFixture(overrides: Partial<ReleaseCandidate> & { id: string; title: string }): ReleaseCandidate {
  return {
    id: overrides.id,
    searchRunId: overrides.searchRunId ?? SEARCH_ID,
    provider: 'dmhy',
    title: overrides.title,
    downloadable: overrides.downloadable ?? true,
    publishedAt: overrides.publishedAt ?? '2026-08-22T08:00:00Z',
    sizeBytes: overrides.sizeBytes ?? 123456789,
    createdAt: '2026-08-22T08:01:00Z',
    downloadUri: overrides.downloadable === false ? undefined : 'magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01',
    unavailableReason: overrides.unavailableReason,
    seeders: undefined,
  } as ReleaseCandidate;
}

function acquisitionFixture(): Acquisition {
  return {
    id: ACQ_ID,
    mediaType: 'episode',
    seriesId: 'dddddddd-0000-4000-8000-000000000030',
    seriesTitle: '搜索番剧标题',
    sourceKind: 'search',
    sourceSeason: 1,
    sourceEpisode: 2,
    downloadId: 'eeeeeeee-0000-4000-8000-000000000040',
    download: {
      id: 'eeeeeeee-0000-4000-8000-000000000040',
      attempt: 1,
      status: 'downloading',
      progress: 0.42,
      updatedAt: '2026-08-22T09:00:00Z',
    },
    tasks: [],
    mapping: { selectedVideoCount: 0, mappedVideoCount: 0, complete: false },
    aggregateStatus: 'downloading',
    currentStage: 'download',
    overallProgress: 0.42,
    stages: [
      { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
      { key: 'download', status: 'running', progress: 0.42, completedItems: 0, totalItems: 1 },
      { key: 'mapping', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'transcode', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'subtitle', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'rename', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'organize', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'review', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
      { key: 'import', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
    ],
    createdAt: '2026-08-22T08:30:00Z',
    updatedAt: '2026-08-22T09:00:00Z',
  };
}

function renderCandidateTable(candidates: ReleaseCandidate[], onAcquired?: () => void) {
  const qc = createTestQueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <CandidateTable candidates={candidates} emptyLabel="暂无候选" onAcquired={onAcquired} />
    </QueryClientProvider>,
  );
}

async function flushMicrotasks() {
  for (let i = 0; i < 4; i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
      await Promise.resolve();
      await Promise.resolve();
    });
  }
}

async function advancePoll(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
  for (let i = 0; i < 8; i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
      await Promise.resolve();
      await Promise.resolve();
    });
  }
}

afterEach(() => {
  vi.useRealTimers();
});

describe('SearchesPage live and recent', () => {
  it('keeps path at /searches and polls queued -> running -> completed with fake timers, stops after completed', async () => {
    vi.useFakeTimers();
    let pollCount = 0;
    let recentCalls = 0;
    const candidateTitle = 'Fixture Show S01E01 [1080p] Very Long Title That Should Wrap Across Multiple Lines Without Overflow And Keep Break Words Behavior For Long Titles';
    server.use(
      http.get('*/api/v1/searches/recent-candidates', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('limit') !== '5') {
          return HttpResponse.json({ code: 'bad_request', message: 'limit' }, { status: 400 });
        }
        recentCalls += 1;
        return HttpResponse.json({ items: [] });
      }),
      http.post('*/api/v1/searches', async () =>
        HttpResponse.json(
          { search: { id: SEARCH_ID, query: 'Fixture Show', status: 'queued', createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:00:00Z', candidates: [] }, operationId: 'op-1', status: 'queued' },
          { status: 202 },
        ),
      ),
      http.get(`*/api/v1/searches/${SEARCH_ID}`, () => {
        pollCount += 1;
        if (pollCount === 1) {
          return HttpResponse.json({
            id: SEARCH_ID,
            query: 'Fixture Show',
            status: 'queued',
            candidates: [],
            createdAt: '2026-08-22T08:00:00Z',
            updatedAt: '2026-08-22T08:00:00Z',
          });
        }
        if (pollCount === 2) {
          return HttpResponse.json({
            id: SEARCH_ID,
            query: 'Fixture Show',
            status: 'running',
            candidates: [],
            createdAt: '2026-08-22T08:00:00Z',
            updatedAt: '2026-08-22T08:01:00Z',
          });
        }
        return HttpResponse.json({
          id: SEARCH_ID,
          query: 'Fixture Show',
          status: 'completed',
          candidates: [candidateFixture({ id: CANDIDATE_NEW, title: candidateTitle })],
          createdAt: '2026-08-22T08:00:00Z',
          updatedAt: '2026-08-22T08:02:00Z',
        });
      }),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );

    const { router, queryClient } = renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });

    await flushMicrotasks();
    // 初始最近搜索已加载
    expect(screen.getByText('最近搜索')).toBeInTheDocument();
    const initialRecent = recentCalls;
    expect(initialRecent).toBeGreaterThanOrEqual(1);

    const input = screen.getByLabelText('关键词');
    await act(async () => {
      fireEvent.change(input, { target: { value: 'Fixture Show' } });
    });
    await flushMicrotasks();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '搜索' }));
    });

    await flushMicrotasks();
    await flushMicrotasks();

    expect(screen.getByText(/排队中/)).toBeInTheDocument();
    expect(pollCount).toBe(1);
    expect(router.state.location.pathname).toBe('/searches');

    // 推进 3000ms 进入 running
    await advancePoll(3000);
    expect(screen.getByText(/搜索中/)).toBeInTheDocument();
    expect(pollCount).toBe(2);

    // 再推进 3000ms 进入 completed，候选出现，recent 被精确刷新一次
    await advancePoll(3000);
    expect(screen.getAllByText(candidateTitle)[0]).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/searches');
    expect(pollCount).toBe(3);
    expect(recentCalls).toBe(initialRecent + 1);
    const titleEl = screen.getAllByText(candidateTitle)[0];
    expect(titleEl.className).toMatch(/break-words/);
    expect(titleEl.className).toMatch(/whitespace-normal/);

    // completed 后再推进一个 interval，轮询停止
    await advancePoll(3000);
    expect(pollCount).toBe(3);
    expect(recentCalls).toBe(initialRecent + 1);
    vi.useRealTimers();
  });

  it('renders current and recent without status or seeders columns, and disables non-downloadable with reason', async () => {
    const downloadable = candidateFixture({ id: CANDIDATE_NEW, title: '可下载番剧 S01E01 [1080p]', sizeBytes: 987654321, publishedAt: '2026-08-22T07:00:00Z' });
    const nondl = candidateFixture({ id: CANDIDATE_OLD, title: '不可下载番剧 S01E02', downloadable: false, unavailableReason: 'download_uri_missing' as ReleaseCandidate['unavailableReason'] });
    (nondl as unknown as Record<string, unknown>).downloadable = false;
    (nondl as unknown as Record<string, unknown>).downloadUri = undefined;
    (nondl as unknown as Record<string, unknown>).unavailableReason = 'download_uri_missing';

    server.use(
      http.get('*/api/v1/searches/recent-candidates', () => HttpResponse.json({ items: [downloadable, nondl] })),
      http.get(`*/api/v1/searches/${SEARCH_ID}`, () => HttpResponse.json({ id: SEARCH_ID, query: 'Q', status: 'completed', candidates: [downloadable, nondl], createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:02:00Z' })),
      http.post('*/api/v1/searches', () => HttpResponse.json({ search: { id: SEARCH_ID, query: 'Q', status: 'completed', createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:02:00Z', candidates: [downloadable] }, operationId: 'op-1', status: 'queued' }, { status: 202 })),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );

    renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });

    await userEvent.type(await screen.findByLabelText('关键词'), 'Q');
    await userEvent.click(screen.getByRole('button', { name: '搜索' }));

    await screen.findAllByText('可下载番剧 S01E01 [1080p]');
    expect(screen.getAllByText('不可下载番剧 S01E02').length).toBeGreaterThan(0);
    expect(screen.queryByText('状态')).not.toBeInTheDocument();
    expect(screen.queryByText('做种')).not.toBeInTheDocument();
    expect(screen.queryByText(/做种数/)).not.toBeInTheDocument();
    expect(screen.getAllByText('不可下载：缺少下载地址').length).toBeGreaterThan(0);
    const disabledButtons = screen.getAllByRole('button', { name: '选择' }).filter((b) => (b as HTMLButtonElement).disabled);
    expect(disabledButtons.length).toBeGreaterThan(0);
  });

  it('recent request is fixed limit=5', async () => {
    const urls: string[] = [];
    server.use(
      http.get('*/api/v1/searches/recent-candidates', ({ request }) => {
        urls.push(request.url);
        return HttpResponse.json({ items: [] });
      }),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );
    renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));
    urls.forEach((url) => {
      const parsed = new URL(url);
      expect(parsed.searchParams.get('limit')).toBe('5');
    });
  });

  it('refreshes recent exactly once when search transitions to completed', async () => {
    vi.useFakeTimers();
    let recentCalls = 0;
    let pollCount = 0;
    server.use(
      http.get('*/api/v1/searches/recent-candidates', () => {
        recentCalls += 1;
        return HttpResponse.json({ items: [] });
      }),
      http.post('*/api/v1/searches', () => HttpResponse.json({ search: { id: SEARCH_ID, query: 'Q', status: 'queued', createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:00:00Z', candidates: [] }, operationId: 'op-1', status: 'queued' }, { status: 202 })),
      http.get(`*/api/v1/searches/${SEARCH_ID}`, () => {
        pollCount += 1;
        if (pollCount === 1) {
          return HttpResponse.json({ id: SEARCH_ID, query: 'Q', status: 'queued', candidates: [], createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:00:00Z' });
        }
        return HttpResponse.json({ id: SEARCH_ID, query: 'Q', status: 'completed', candidates: [candidateFixture({ id: CANDIDATE_NEW, title: 'Completed Candidate' })], createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:02:00Z' });
      }),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );
    renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });
    await flushMicrotasks();
    expect(screen.getByText('最近搜索')).toBeInTheDocument();
    const initialCalls = recentCalls;

    await act(async () => {
      fireEvent.change(screen.getByLabelText('关键词'), { target: { value: 'Q' } });
    });
    await flushMicrotasks();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '搜索' }));
    });
    await flushMicrotasks();
    await flushMicrotasks();
    await flushMicrotasks();
    expect(screen.getByText(/排队中/)).toBeInTheDocument();
    expect(pollCount).toBe(1);

    await advancePoll(3000);
    expect(screen.getAllByText('Completed Candidate')[0]).toBeInTheDocument();
    expect(pollCount).toBe(2);
    expect(recentCalls).toBe(initialCalls + 1);
    const afterCompleted = recentCalls;
    await advancePoll(3000);
    expect(recentCalls).toBe(afterCompleted);
    expect(pollCount).toBe(2);
    vi.useRealTimers();
  });

  it('does not refresh recent and shows single friendly error when search fails', async () => {
    vi.useFakeTimers();
    let recentCalls = 0;
    let pollCount = 0;
    const failedCandidateTitle = 'Should Not Appear';
    server.use(
      http.get('*/api/v1/searches/recent-candidates', () => {
        recentCalls += 1;
        return HttpResponse.json({ items: [] });
      }),
      http.post('*/api/v1/searches', () => HttpResponse.json({ search: { id: SEARCH_ID, query: 'Q', status: 'queued', createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:00:00Z', candidates: [] }, operationId: 'op-1', status: 'queued' }, { status: 202 })),
      http.get(`*/api/v1/searches/${SEARCH_ID}`, () => {
        pollCount += 1;
        if (pollCount === 1) {
          return HttpResponse.json({ id: SEARCH_ID, query: 'Q', status: 'queued', candidates: [], createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:00:00Z' });
        }
        return HttpResponse.json({
          id: SEARCH_ID,
          query: 'Q',
          status: 'failed',
          errorCode: 'search_provider_failed',
          errorMessage: 'upstream search failed',
          candidates: [],
          createdAt: '2026-08-22T08:00:00Z',
          updatedAt: '2026-08-22T08:02:00Z',
        });
      }),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );
    renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });
    await flushMicrotasks();
    expect(screen.getByText('最近搜索')).toBeInTheDocument();
    const initialCalls = recentCalls;

    await act(async () => {
      fireEvent.change(screen.getByLabelText('关键词'), { target: { value: 'Q' } });
    });
    await flushMicrotasks();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '搜索' }));
    });
    await flushMicrotasks();
    await flushMicrotasks();
    await flushMicrotasks();
    expect(screen.getByText(/排队中/)).toBeInTheDocument();
    expect(pollCount).toBe(1);

    await advancePoll(3000);
    expect(screen.getByText('搜索失败')).toBeInTheDocument();
    const friendly = '搜索来源暂时不可用，请稍后重试。';
    const matches = screen.getAllByText(friendly);
    expect(matches).toHaveLength(1);
    expect(screen.queryByText(failedCandidateTitle)).not.toBeInTheDocument();
    expect(recentCalls).toBe(initialCalls);
    // failed 后轮询停止
    await advancePoll(3000);
    expect(pollCount).toBe(2);
    expect(recentCalls).toBe(initialCalls);
    vi.useRealTimers();
  });

  it('shows loading, empty and error states for recent', async () => {
    server.use(
      http.get('*/api/v1/searches/recent-candidates', () => HttpResponse.json({ items: [] })),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );
    renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });
    expect(await screen.findByText('暂无最近结果')).toBeInTheDocument();
  });

  it('shows error for recent when request fails', async () => {
    server.use(
      http.get('*/api/v1/searches/recent-candidates', () => HttpResponse.json({ code: 'internal', message: 'boom' }, { status: 500 })),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );
    renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });
    await waitFor(() => expect(screen.getByText(/boom|无法读取最近搜索/)).toBeInTheDocument());
  });
});

describe('SearchAcquisitionsSection', () => {
  it('requests with sourceKind=search sortBy updated_at sortOrder desc and renders link navigation', async () => {
    const requested: string[] = [];
    server.use(
      http.get('*/api/v1/acquisitions', ({ request }) => {
        requested.push(request.url);
        return HttpResponse.json({ items: [acquisitionFixture()], nextCursor: null });
      }),
    );
    const { router } = renderWithProviders(<SearchAcquisitionsSection />, { routePath: '/searches', initialEntry: '/searches' });
    await screen.findAllByText('搜索番剧标题');
    const url = new URL(requested[0]);
    expect(url.searchParams.get('sourceKind')).toBe('search');
    expect(url.searchParams.get('sortBy')).toBe('updated_at');
    expect(url.searchParams.get('sortOrder')).toBe('desc');
    expect(screen.getAllByText('搜索番剧标题').length).toBeGreaterThan(0);
    expect(screen.getAllByText(/09:00|2026/).length).toBeGreaterThan(0);
    const links = screen.getAllByRole('link', { name: /详情|查看详情/ });
    await userEvent.click(links[0]);
    await waitFor(() => expect(router.state.location.pathname).toContain(ACQ_ID));
  });

  it('renders loading and empty for acquisitions', async () => {
    let resolve: (value: unknown) => void = () => {};
    const pending = new Promise((r) => {
      resolve = r;
    });
    server.use(
      http.get('*/api/v1/acquisitions', () => pending.then(() => HttpResponse.json({ items: [] })) as unknown as Response),
    );
    renderWithProviders(<SearchAcquisitionsSection />, { routePath: '/searches', initialEntry: '/searches' });
    expect(await screen.findByText('正在读取搜索任务')).toBeInTheDocument();
    resolve(HttpResponse.json({ items: [] }));
    await screen.findByText('暂无搜索任务');
  });
});

describe('Candidate selection', () => {
  it('candidate form has no nested card structure and is operable', async () => {
    const candidates = [candidateFixture({ id: CANDIDATE_NEW, title: '候选标题' })];
    renderWithProviders(<CandidateTable candidates={candidates} emptyLabel="暂无候选" />, { routePath: '/searches', initialEntry: '/searches' });
    await userEvent.click((await screen.findAllByRole('button', { name: '选择' }))[0]);
    const form = await screen.findByText('创建获取');
    expect(form).toBeInTheDocument();
    const cardElements = document.querySelectorAll('.shadow-card');
    expect(cardElements.length).toBe(1);
    expect(form.className).not.toMatch(/shadow-card/);
    const innerCards = document.querySelectorAll('.shadow-card .shadow-card');
    expect(innerCards.length).toBe(0);
  });

  it('creates acquisition and shows conflict error, with bounded invalidation', async () => {
    const recentSpy = vi.fn();
    const acqSpy = vi.fn();
    let createShouldConflict = false;
    server.use(
      http.get('*/api/v1/searches/recent-candidates', ({ request }) => {
        recentSpy(String(request.url));
        return HttpResponse.json({ items: [] });
      }),
      http.get('*/api/v1/acquisitions', ({ request }) => {
        acqSpy(String(request.url));
        return HttpResponse.json({ items: [] });
      }),
      http.get('*/api/v1/tmdb/series/search', () => HttpResponse.json({ items: [{ tmdbSeriesId: 123, name: 'Fixture Series', originalName: 'Fixture Series', firstAirDate: '2024-01-01' }] })),
      http.post('*/api/v1/acquisitions', async () => {
        if (createShouldConflict) {
          return HttpResponse.json({ code: 'conflict', message: 'already selected' }, { status: 409 });
        }
        return HttpResponse.json({ acquisitionId: ACQ_ID, downloadId: 'dl-1', operationId: 'op-1', status: 'queued' }, { status: 202 });
      }),
    );

    const candidates = [candidateFixture({ id: CANDIDATE_NEW, title: '可创建候选 S01E01 [1080p]' })];
    const onAcquired = vi.fn();
    renderWithProviders(<CandidateTable candidates={candidates} emptyLabel="暂无候选" onAcquired={onAcquired} />, { routePath: '/searches', initialEntry: '/searches' });

    await userEvent.click((await screen.findAllByRole('button', { name: '选择' }))[0]);
    await screen.findByText('创建获取');

    // pick TMDb series
    const input = await screen.findByLabelText('TMDb 作品');
    await userEvent.type(input, 'Fixture');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    await screen.findByText('Fixture Series');
    await userEvent.click(screen.getByText('Fixture Series'));

    recentSpy.mockClear();
    acqSpy.mockClear();
    await userEvent.click(screen.getByRole('button', { name: '创建获取并下载' }));
    await waitFor(() => expect(onAcquired).toHaveBeenCalled());

    createShouldConflict = true;
    await userEvent.click(screen.getByRole('button', { name: '创建获取并下载' }));
    await screen.findByText(/already selected|创建获取失败|冲突/);
  });

  it('shows downloadable reason and disables button for non-downloadable', async () => {
    const dl = candidateFixture({ id: CANDIDATE_NEW, title: '可下载' });
    const nondl = candidateFixture({ id: CANDIDATE_OLD, title: '不可下载', downloadable: false, unavailableReason: 'download_uri_missing' as ReleaseCandidate['unavailableReason'] });
    (nondl as unknown as Record<string, unknown>).downloadable = false;
    (nondl as unknown as Record<string, unknown>).unavailableReason = 'download_uri_missing';
    renderCandidateTable([dl, nondl]);
    expect(screen.getAllByText('可下载').length).toBeGreaterThan(0);
    expect(screen.getAllByText('不可下载').length).toBeGreaterThan(0);
    expect(screen.getAllByText('不可下载：缺少下载地址').length).toBeGreaterThan(0);
    const disabled = screen.getAllByRole('button', { name: '选择' }).filter((b) => (b as HTMLButtonElement).disabled);
    expect(disabled.length).toBeGreaterThan(0);
    expect(screen.queryByText('状态')).not.toBeInTheDocument();
    expect(screen.queryByText('做种')).not.toBeInTheDocument();
  });
});
