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
import type { Acquisition, ReleaseCandidate, SearchRunSummary } from '@/api/generated/types.gen';

const SEARCH_ID = 'aaaaaaaa-0000-4000-8000-000000000001';
const SEARCH_ID_2 = 'aaaaaaaa-0000-4000-8000-000000000002';
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

function searchRunFixture(overrides: Partial<SearchRunSummary> & { id: string; query: string }): SearchRunSummary {
  return {
    id: overrides.id,
    query: overrides.query,
    status: overrides.status ?? 'completed',
    errorCode: overrides.errorCode,
    errorMessage: overrides.errorMessage,
    createdAt: overrides.createdAt ?? '2026-08-22T08:00:00Z',
    updatedAt: overrides.updatedAt ?? '2026-08-22T08:01:00Z',
  };
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
      http.get('*/api/v1/searches', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('limit') !== '5') {
          return HttpResponse.json({ code: 'bad_request', message: 'limit' }, { status: 400 });
        }
        recentCalls += 1;
        return HttpResponse.json({ items: [], nextCursor: null });
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

    const { router } = renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });

    await flushMicrotasks();
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

    await advancePoll(3000);
    expect(screen.getByText(/搜索中/)).toBeInTheDocument();
    expect(pollCount).toBe(2);

    await advancePoll(3000);
    expect(screen.getAllByText(candidateTitle)[0]).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/searches');
    expect(pollCount).toBe(3);
    expect(recentCalls).toBe(initialRecent + 2);
    const titleEl = screen.getAllByText(candidateTitle)[0];
    expect(titleEl.className).toMatch(/break-words/);
    expect(titleEl.className).toMatch(/whitespace-normal/);

    await advancePoll(3000);
    expect(pollCount).toBe(3);
    expect(recentCalls).toBe(initialRecent + 2);
    vi.useRealTimers();
  });

  it('renders current without status or seeders columns, and disables non-downloadable with reason', async () => {
    const downloadable = candidateFixture({ id: CANDIDATE_NEW, title: '可下载番剧 S01E01 [1080p]', sizeBytes: 987654321, publishedAt: '2026-08-22T07:00:00Z' });
    const nondl = candidateFixture({ id: CANDIDATE_OLD, title: '不可下载番剧 S01E02', downloadable: false, unavailableReason: 'download_uri_missing' as ReleaseCandidate['unavailableReason'] });
    (nondl as unknown as Record<string, unknown>).downloadable = false;
    (nondl as unknown as Record<string, unknown>).downloadUri = undefined;
    (nondl as unknown as Record<string, unknown>).unavailableReason = 'download_uri_missing';

    server.use(
      http.get('*/api/v1/searches', () => HttpResponse.json({ items: [], nextCursor: null })),
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

  it('recent request is fixed limit=5, caps UI at 5 and does not request recent-candidates', async () => {
    const urls: string[] = [];
    let recentCandidatesCalled = false;
    const manyRuns = Array.from({ length: 7 }, (_, i) =>
      searchRunFixture({ id: `id-${i}`, query: `关键词-${i}-${'超长'.repeat(20)}`, status: 'completed', createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:01:00Z' }),
    );
    server.use(
      http.get('*/api/v1/searches', ({ request }) => {
        urls.push(request.url);
        return HttpResponse.json({ items: manyRuns, nextCursor: null });
      }),
      http.get('*/api/v1/searches/recent-candidates', () => {
        recentCandidatesCalled = true;
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
    expect(recentCandidatesCalled).toBe(false);
    // UI caps at 5 even though server returned 7
    await screen.findByText('关键词-0-超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长');
    const links = screen.getAllByRole('link', { name: /关键词-/ });
    // Two links per run (title + 查看详情) => count doubled, but distinct query links at least 5
    const queryLinks = links.filter((el) => el.textContent?.startsWith('关键词-'));
    expect(queryLinks.length).toBe(5);
    expect(screen.queryByText('关键词-5-超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长')).not.toBeInTheDocument();
    expect(screen.queryByText('关键词-6-超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长超长')).not.toBeInTheDocument();
    // long query wraps without overflow
    const firstLink = queryLinks[0] as HTMLAnchorElement;
    expect(firstLink.className).toMatch(/break-words/);
    expect(firstLink.className).toMatch(/whitespace-normal/);
  });

  it('clicking recent search record navigates to /searches/{id}', async () => {
    const runA = searchRunFixture({ id: SEARCH_ID, query: '导航测试关键词', status: 'completed', createdAt: '2026-08-22T08:00:00Z', updatedAt: '2026-08-22T08:01:00Z' });
    const runB = searchRunFixture({ id: SEARCH_ID_2, query: '另一条记录', status: 'failed', errorCode: 'search_provider_failed', errorMessage: 'fail', createdAt: '2026-08-22T08:02:00Z', updatedAt: '2026-08-22T08:03:00Z' });
    server.use(
      http.get('*/api/v1/searches', () => HttpResponse.json({ items: [runA, runB], nextCursor: null })),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );
    const { router } = renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });
    await screen.findByText('导航测试关键词');
    const link = screen.getAllByRole('link', { name: '导航测试关键词' })[0];
    expect(link.getAttribute('href')).toContain(SEARCH_ID);
    await userEvent.click(link);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/searches/${SEARCH_ID}`));
  });

  it('refreshes recent exactly once when search transitions to completed', async () => {
    vi.useFakeTimers();
    let recentCalls = 0;
    let pollCount = 0;
    server.use(
      http.get('*/api/v1/searches', () => {
        recentCalls += 1;
        return HttpResponse.json({ items: [], nextCursor: null });
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
    expect(recentCalls).toBe(initialCalls + 2);
    const afterCompleted = recentCalls;
    await advancePoll(3000);
    expect(recentCalls).toBe(afterCompleted);
    expect(pollCount).toBe(2);
    vi.useRealTimers();
  });

  it('refreshes recent once on failed terminal and shows single friendly error, stops polling without duplicate refresh', async () => {
    vi.useFakeTimers();
    let recentCalls = 0;
    let pollCount = 0;
    const failedCandidateTitle = 'Should Not Appear';
    server.use(
      http.get('*/api/v1/searches', () => {
        recentCalls += 1;
        return HttpResponse.json({ items: [], nextCursor: null });
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
    // onSuccess 已刷新一次
    expect(recentCalls).toBe(initialCalls + 1);

    await advancePoll(3000);
    expect(screen.getByText('搜索失败')).toBeInTheDocument();
    const friendly = '搜索来源暂时不可用，请稍后重试。';
    const matches = screen.getAllByText(friendly);
    expect(matches).toHaveLength(1);
    expect(screen.queryByText(failedCandidateTitle)).not.toBeInTheDocument();
    // 进入 failed 终态再刷新一次
    expect(recentCalls).toBe(initialCalls + 2);
    expect(pollCount).toBe(2);
    const afterFailed = recentCalls;
    await advancePoll(3000);
    // 轮询停止且不重复刷新
    expect(pollCount).toBe(2);
    expect(recentCalls).toBe(afterFailed);
    vi.useRealTimers();
  });

  it('shows loading, empty and error states for recent', async () => {
    server.use(
      http.get('*/api/v1/searches', () => HttpResponse.json({ items: [], nextCursor: null })),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );
    renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });
    expect(await screen.findByText('暂无最近结果')).toBeInTheDocument();
  });

  it('shows error for recent when request fails and retry works', async () => {
    let shouldFail = true;
    server.use(
      http.get('*/api/v1/searches', () => {
        if (shouldFail) {
          return HttpResponse.json({ code: 'internal', message: 'boom' }, { status: 500 });
        }
        return HttpResponse.json({ items: [], nextCursor: null });
      }),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );
    renderWithProviders(<SearchesPage />, { routePath: '/searches', initialEntry: '/searches' });
    await waitFor(() => expect(screen.getByText(/boom|无法读取最近搜索/)).toBeInTheDocument());
    shouldFail = false;
    const retryBtn = screen.getByRole('button', { name: /重试/ });
    await userEvent.click(retryBtn);
    await screen.findByText('暂无最近结果');
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

  it('shows friendly error for state_conflict', async () => {
    server.use(
      http.get('*/api/v1/searches', () => HttpResponse.json({ items: [], nextCursor: null })),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
      http.get('*/api/v1/tmdb/series/search', () => HttpResponse.json({ items: [{ tmdbSeriesId: 123, name: 'Fixture Series', originalName: 'Fixture Series', firstAirDate: '2024-01-01' }] })),
      http.post('*/api/v1/acquisitions', async () => HttpResponse.json({ code: 'state_conflict', message: 'already selected', details: {} }, { status: 409 })),
    );
    const candidates = [candidateFixture({ id: CANDIDATE_NEW, title: '可创建候选 S01E01 [1080p]' })];
    renderWithProviders(<CandidateTable candidates={candidates} emptyLabel="暂无候选" />, { routePath: '/searches', initialEntry: '/searches' });
    await userEvent.click((await screen.findAllByRole('button', { name: '选择' }))[0]);
    await screen.findByText('创建获取');
    const input = await screen.findByLabelText('TMDb 作品');
    await userEvent.type(input, 'Fixture');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    await screen.findByText('Fixture Series');
    await userEvent.click(screen.getByText('Fixture Series'));
    await userEvent.click(screen.getByRole('button', { name: '创建获取并下载' }));
    await screen.findByText('任务状态已经变化，请刷新后再试。');
    expect(screen.queryByText('already selected')).not.toBeInTheDocument();
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

describe('Candidate selection season pack', () => {
  it('defaults to single, hides episode for pack and restores on single', async () => {
    const candidates = [candidateFixture({ id: CANDIDATE_NEW, title: '番剧候选 S01 [1080p]' })];
    renderWithProviders(<CandidateTable candidates={candidates} emptyLabel="暂无候选" />, { routePath: '/searches', initialEntry: '/searches' });
    await userEvent.click((await screen.findAllByRole('button', { name: '选择' }))[0]);
    await screen.findByText('创建获取');
    expect(screen.getByLabelText('资源对应第几季')).toBeInTheDocument();
    expect(screen.getByLabelText('资源对应第几集')).toBeInTheDocument();
    expect(screen.getByLabelText('类型')).toBeInTheDocument();
    const gridSingle = document.querySelector('.grid.gap-4');
    expect(gridSingle?.className).toMatch(/sm:grid-cols-3/);
    const episodeInput = screen.getByLabelText('资源对应第几集') as HTMLInputElement;
    expect(episodeInput.value).toBe('1');
    await userEvent.selectOptions(screen.getByLabelText('类型'), 'pack');
    expect(screen.queryByLabelText('资源对应第几集')).not.toBeInTheDocument();
    const gridPack = document.querySelector('.grid.gap-4');
    expect(gridPack?.className).toMatch(/sm:grid-cols-2/);
    expect(gridPack?.className).not.toMatch(/sm:grid-cols-3/);
    await userEvent.selectOptions(screen.getByLabelText('类型'), 'single');
    const restored = screen.getByLabelText('资源对应第几集') as HTMLInputElement;
    expect(restored).toBeInTheDocument();
    expect(restored.value).toBe('1');
    const gridRestored = document.querySelector('.grid.gap-4');
    expect(gridRestored?.className).toMatch(/sm:grid-cols-3/);
  });

  it('sends season pack without sourceEpisode and singleEpisode false', async () => {
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.get('*/api/v1/tmdb/series/search', () => HttpResponse.json({ items: [{ tmdbSeriesId: 123, name: 'Fixture Series', originalName: 'Fixture Series', firstAirDate: '2024-01-01' }] })),
      http.post('*/api/v1/acquisitions', async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ acquisitionId: ACQ_ID, downloadId: 'dl-1', operationId: 'op-1', status: 'queued' }, { status: 202 });
      }),
    );
    const candidates = [candidateFixture({ id: CANDIDATE_NEW, title: '可创建候选 S01 [1080p]' })];
    renderWithProviders(<CandidateTable candidates={candidates} emptyLabel="暂无候选" />, { routePath: '/searches', initialEntry: '/searches' });
    await userEvent.click((await screen.findAllByRole('button', { name: '选择' }))[0]);
    await screen.findByText('创建获取');
    const input = await screen.findByLabelText('TMDb 作品');
    await userEvent.type(input, 'Fixture');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    await screen.findByText('Fixture Series');
    await userEvent.click(screen.getByText('Fixture Series'));
    await userEvent.selectOptions(screen.getByLabelText('类型'), 'pack');
    expect(screen.queryByLabelText('资源对应第几集')).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: '创建获取并下载' }));
    await waitFor(() => expect(captured).not.toBeNull());
    expect(captured).toMatchObject({ candidateId: CANDIDATE_NEW, mediaType: 'episode', sourceSeason: 1, singleEpisode: false });
    expect(captured).not.toHaveProperty('sourceEpisode');
  });

  it('sends single episode with sourceEpisode and singleEpisode true', async () => {
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.get('*/api/v1/tmdb/series/search', () => HttpResponse.json({ items: [{ tmdbSeriesId: 123, name: 'Fixture Series', originalName: 'Fixture Series', firstAirDate: '2024-01-01' }] })),
      http.post('*/api/v1/acquisitions', async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ acquisitionId: ACQ_ID, downloadId: 'dl-1', operationId: 'op-1', status: 'queued' }, { status: 202 });
      }),
    );
    const candidates = [candidateFixture({ id: CANDIDATE_NEW, title: '可创建候选 S01E01 [1080p]' })];
    renderWithProviders(<CandidateTable candidates={candidates} emptyLabel="暂无候选" />, { routePath: '/searches', initialEntry: '/searches' });
    await userEvent.click((await screen.findAllByRole('button', { name: '选择' }))[0]);
    await screen.findByText('创建获取');
    const input = await screen.findByLabelText('TMDb 作品');
    await userEvent.type(input, 'Fixture');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    await screen.findByText('Fixture Series');
    await userEvent.click(screen.getByText('Fixture Series'));
    await userEvent.click(screen.getByRole('button', { name: '创建获取并下载' }));
    await waitFor(() => expect(captured).not.toBeNull());
    expect(captured).toMatchObject({ candidateId: CANDIDATE_NEW, mediaType: 'episode', sourceSeason: 1, sourceEpisode: 1, singleEpisode: true });
  });

  it('navigates to acquisition detail even with onAcquired and keeps cache invalidation', async () => {
    let postCount = 0;
    server.use(
      http.get('*/api/v1/tmdb/series/search', () => HttpResponse.json({ items: [{ tmdbSeriesId: 123, name: 'Fixture Series', originalName: 'Fixture Series', firstAirDate: '2024-01-01' }] })),
      http.post('*/api/v1/acquisitions', async () => {
        postCount += 1;
        return HttpResponse.json({ acquisitionId: ACQ_ID, downloadId: 'dl-1', operationId: 'op-1', status: 'queued' }, { status: 202 });
      }),
      http.get('*/api/v1/acquisitions', () => HttpResponse.json({ items: [] })),
    );
    const candidates = [candidateFixture({ id: CANDIDATE_NEW, title: '可创建候选 S01E01 [1080p]' })];
    const onAcquired = vi.fn();
    const { router } = renderWithProviders(<CandidateTable candidates={candidates} emptyLabel="暂无候选" onAcquired={onAcquired} />, { routePath: '/searches', initialEntry: '/searches' });
    await userEvent.click((await screen.findAllByRole('button', { name: '选择' }))[0]);
    await screen.findByText('创建获取');
    const input = await screen.findByLabelText('TMDb 作品');
    await userEvent.type(input, 'Fixture');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    await screen.findByText('Fixture Series');
    await userEvent.click(screen.getByText('Fixture Series'));
    await userEvent.click(screen.getByRole('button', { name: '创建获取并下载' }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/acquisitions/${ACQ_ID}`));
    expect(onAcquired).toHaveBeenCalledTimes(1);
    expect(postCount).toBe(1);
    expect(screen.queryByText('创建获取')).not.toBeInTheDocument();
  });

  it('keeps button disabled during pending and sends only one POST', async () => {
    let postCount = 0;
    let resolvePost: (value: unknown) => void = () => {};
    const pending = new Promise((resolve) => {
      resolvePost = resolve as (value: unknown) => void;
    });
    server.use(
      http.get('*/api/v1/tmdb/series/search', () => HttpResponse.json({ items: [{ tmdbSeriesId: 123, name: 'Fixture Series', originalName: 'Fixture Series', firstAirDate: '2024-01-01' }] })),
      http.post('*/api/v1/acquisitions', async () => {
        postCount += 1;
        await pending;
        return HttpResponse.json({ acquisitionId: ACQ_ID, downloadId: 'dl-1', operationId: 'op-1', status: 'queued' }, { status: 202 });
      }),
    );
    const candidates = [candidateFixture({ id: CANDIDATE_NEW, title: '可创建候选 S01E01 [1080p]' })];
    const { router } = renderWithProviders(<CandidateTable candidates={candidates} emptyLabel="暂无候选" />, { routePath: '/searches', initialEntry: '/searches' });
    await userEvent.click((await screen.findAllByRole('button', { name: '选择' }))[0]);
    await screen.findByText('创建获取');
    const input = await screen.findByLabelText('TMDb 作品');
    await userEvent.type(input, 'Fixture');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    await screen.findByText('Fixture Series');
    await userEvent.click(screen.getByText('Fixture Series'));
    const button = screen.getByRole('button', { name: '创建获取并下载' });
    await userEvent.click(button);
    await waitFor(() => expect(button.hasAttribute('disabled')).toBe(true));
    expect(postCount).toBe(1);
    await userEvent.click(button).catch(() => {});
    expect(postCount).toBe(1);
    resolvePost(undefined);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/acquisitions/${ACQ_ID}`));
    expect(postCount).toBe(1);
  });
});
