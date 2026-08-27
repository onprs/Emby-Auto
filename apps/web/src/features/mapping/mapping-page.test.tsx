import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import type {
  Acquisition,
  Download,
  EpisodeMappingAnchor,
  EpisodeMappingExplicitDisposition,
  EpisodeMappingPreview,
  TmDbSeriesCatalog,
} from '@/api/generated/types.gen';
import { MappingPage } from '@/features/mapping/mapping-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const acquisitionId = '91000000-0000-4000-8000-000000000001';
const seriesId = '91000000-0000-4000-8000-000000000002';
const sourceFileId = '91000000-0000-4000-8000-000000000003';
const secondSourceFileId = '91000000-0000-4000-8000-000000000006';
const downloadId = '91000000-0000-4000-8000-000000000004';
const now = '2026-07-26T01:00:00Z';

const acquisition: Acquisition = {
  id: acquisitionId,
  mediaType: 'episode',
  seriesId,
  tmdbSeriesId: 42,
  seriesTitle: '映射页面测试番剧',
  sourceKind: 'rss',
  sourceTitle: 'Fixture release season pack',
  sourceSeason: 1,
  sourceEpisode: 1,
  downloadId,
  download: {
    id: downloadId,
    attempt: 1,
    status: 'failed',
    progress: 1,
    failureStage: 'materialize',
    errorCode: 'mapping_profile_required',
    updatedAt: now,
  },
  tasks: [],
  mapping: { selectedVideoCount: 2, mappedVideoCount: 0, complete: false },
  aggregateStatus: 'mapping_pending',
  currentStage: 'mapping',
  overallProgress: 0.3,
  stages: [
    { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
    { key: 'download', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
    { key: 'mapping', status: 'waiting', progress: 0, completedItems: 0, totalItems: 2 },
    { key: 'transcode', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
    { key: 'subtitle', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
    { key: 'rename', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
    { key: 'organize', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
    { key: 'review', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
    { key: 'import', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
  ],
  createdAt: now,
  updatedAt: now,
};

const download: Download = {
  id: downloadId,
  acquisitionId,
  attempt: 1,
  clientName: 'qbittorrent',
  status: 'failed',
  progress: 1,
  version: 2,
  failureStage: 'materialize',
  errorCode: 'mapping_profile_required',
  createdAt: now,
  updatedAt: now,
  files: [
    { id: sourceFileId, fileIndex: 0, relativePath: 'Show - 01.mkv', sizeBytes: 1000, mediaKind: 'video', selected: true, sourceSeason: 1, sourceEpisode: 1 },
    { id: secondSourceFileId, fileIndex: 1, relativePath: 'Show - 02.mkv', sizeBytes: 1000, mediaKind: 'video', selected: true, sourceSeason: 1, sourceEpisode: 2 },
  ],
  actions: { canRetry: false, canCancel: false, canDelete: true, canEditFileSelection: false, canResolveFiles: false, canRequestAgent: false },
};

const catalog: TmDbSeriesCatalog = {
  seriesId,
  tmdbSeriesId: 42,
  title: '映射页面测试番剧',
  synced: true,
  lastSyncedAt: now,
  seasons: [
    {
      seasonNumber: 0,
      name: 'Specials',
      episodeCount: 1,
      special: true,
      episodes: [{ episodeNumber: 1, title: '特别篇' }],
    },
    {
      seasonNumber: 1,
      name: 'Season 1',
      episodeCount: 2,
      special: false,
      episodes: [
        { episodeNumber: 1, title: '第一集' },
        { episodeNumber: 2, title: '第二集' },
      ],
    },
  ],
};

function anchorPreview(anchor: EpisodeMappingAnchor): EpisodeMappingPreview {
  return {
    acquisitionId,
    seriesId,
    mode: 'anchor',
    anchor,
    rows: download.files.map((file, index) => ({
      sourceFileId: file.id,
      relativePath: file.relativePath,
      sourceSeason: 1,
      sourceEpisode: index + 1,
      absoluteEpisode: index + 1,
      status: 'mapped',
      targetSeason: 1,
      targetEpisode: index + 1,
      targetTitle: index === 0 ? '第一集' : '第二集',
      matchSource: 'anchor',
    })),
  };
}

function explicitPreview(assignments: EpisodeMappingExplicitDisposition[]): EpisodeMappingPreview {
  return {
    acquisitionId,
    seriesId,
    mode: 'explicit',
    rows: assignments.map((assignment, index) => {
      const mapped = assignment.action === 'map';
      return {
        sourceFileId: assignment.sourceFileId,
        relativePath: download.files[index].relativePath,
        sourceSeason: 1,
        sourceEpisode: index + 1,
        status: mapped ? 'mapped' : 'excluded',
        ...(mapped
          ? {
              targetSeason: assignment.targetSeason,
              targetEpisode: assignment.targetEpisode,
              targetTitle: assignment.targetSeason === 0 ? '特别篇' : '第一集',
            }
          : {}),
        matchSource: mapped ? 'explicit' : 'pending',
      };
    }),
  };
}

function mockBaseRequests(value: Acquisition = acquisition) {
  server.use(
    http.get(`*/api/v1/acquisitions/${acquisitionId}`, () => HttpResponse.json(value)),
    http.get(`*/api/v1/downloads/${downloadId}`, () => HttpResponse.json(download)),
    http.get('*/api/v1/tmdb/series/42/catalog', () => HttpResponse.json(catalog)),
  );
}

function renderPage() {
  return renderWithProviders(<MappingPage acquisitionId={acquisitionId} />, {
    routePath: '/acquisitions/$acquisitionId/mapping',
    initialEntry: `/acquisitions/${acquisitionId}/mapping`,
  });
}

function createPreviewGate() {
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { pending, release };
}

async function chooseExplicitTarget(fileName: string, optionName: string) {
  const actionGroup = screen.getByRole('group', { name: `${fileName} 的处置` });
  await userEvent.click(within(actionGroup).getByRole('button', { name: '映射' }));
  await userEvent.click(screen.getByRole('button', { name: `${fileName} 的 TMDb 剧集` }));
  await userEvent.click(screen.getByRole('option', { name: optionName }));
}

describe('MappingPage', () => {
  it('saves a continuous anchor immediately when a TMDb episode is selected', async () => {
    mockBaseRequests();
    let saveBody: unknown;
    let previewCalls = 0;
    server.use(
      http.post(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping/preview`, () => {
        previewCalls++;
        return HttpResponse.json({}, { status: 500 });
      }),
      http.put(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping`, async ({ request }) => {
        saveBody = await request.json();
        const anchor = (saveBody as { anchor: EpisodeMappingAnchor }).anchor;
        return HttpResponse.json({ profileId: '91000000-0000-4000-8000-000000000005', version: 1, preview: anchorPreview(anchor) });
      }),
    );
    const { router } = renderPage();

    await screen.findByRole('heading', { name: '剧集映射' });
    expect(await screen.findByText('Fixture release season pack')).toBeInTheDocument();
    expect(await screen.findByText('Show - 01.mkv')).toBeInTheDocument();
    expect(screen.getByText('Show - 02.mkv')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '生成预览' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '确认并继续' })).not.toBeInTheDocument();

    await userEvent.click(await screen.findByRole('button', { name: '映射到 S01E01：第一集' }));

    await waitFor(() => expect(router.state.location.pathname).toBe(`/acquisitions/${acquisitionId}`));
    expect(router.state.location.search).toMatchObject({ from: '/acquisitions?phase=attention' });
    expect(saveBody).toEqual({ mode: 'anchor', anchor: { sourceFileId, targetSeason: 1, targetEpisode: 1 } });
    expect(previewCalls).toBe(0);
  });

  it('does not expose or request a separate Agent suggestion flow', async () => {
    mockBaseRequests();
    let agentResolutionRequests = 0;
    server.use(
      http.get('*/api/v1/agent/resolutions', () => {
        agentResolutionRequests++;
        return HttpResponse.json({ items: [] });
      }),
    );

    renderPage();

    await screen.findByText('Show - 01.mkv');
    expect(screen.queryByText('Agent 建议')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '请求建议' })).not.toBeInTheDocument();
    expect(agentResolutionRequests).toBe(0);
  });

  it('automatically anchors the source file matching the current source episode', async () => {
    mockBaseRequests({ ...acquisition, sourceEpisode: 2 });
    let saveBody: unknown;
    server.use(
      http.put(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping`, async ({ request }) => {
        saveBody = await request.json();
        const anchor = (saveBody as { anchor: EpisodeMappingAnchor }).anchor;
        return HttpResponse.json({ profileId: '91000000-0000-4000-8000-000000000005', version: 1, preview: anchorPreview(anchor) });
      }),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '映射到 S01E02：第二集' }));

    await waitFor(() => expect(saveBody).toEqual({
      mode: 'anchor',
      anchor: { sourceFileId: secondSourceFileId, targetSeason: 1, targetEpisode: 2 },
    }));
  });

  it('forces fractional source episodes into explicit mapping without colliding with integers', async () => {
    mockBaseRequests();
    server.use(
      http.get(`*/api/v1/downloads/${downloadId}`, () => HttpResponse.json({
        ...download,
        files: [
          { ...download.files[0], fileIndex: 2, relativePath: 'Show - 12.5.mkv', sourceEpisode: 12, sourceEpisodeFractionHundredths: 50 },
          { ...download.files[1], fileIndex: 1, relativePath: 'Show - 125.mkv', sourceEpisode: 125, sourceEpisodeFractionHundredths: 0 },
          {
            ...download.files[1],
            id: '91000000-0000-4000-8000-000000000008',
            fileIndex: 0,
            relativePath: 'Show - 12.mkv',
            sourceEpisode: 12,
            sourceEpisodeFractionHundredths: 0,
          },
        ],
      })),
    );
    renderPage();

    await waitFor(() => expect(screen.getByRole('button', { name: '单点连续' })).toBeDisabled());
    expect(screen.getByRole('button', { name: '逐个文件' })).toHaveAttribute('aria-pressed', 'true');
    const integerTwelve = screen.getByText('S01E12');
    const fractionalTwelve = screen.getByText('S01E12.5');
    const integerOneTwentyFive = screen.getByText('S01E125');
    expect(integerTwelve.compareDocumentPosition(fractionalTwelve) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(fractionalTwelve.compareDocumentPosition(integerOneTwentyFive) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(screen.queryByRole('button', { name: /映射到 S01E01/ })).not.toBeInTheDocument();
  });

  it('previews and saves complete explicit mappings including Season 0', async () => {
    mockBaseRequests();
    let previewBody: { mode: 'explicit'; assignments: EpisodeMappingExplicitDisposition[] } | undefined;
    let saveBody: unknown;
    server.use(
      http.post(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping/preview`, async ({ request }) => {
        previewBody = await request.json() as typeof previewBody;
        return HttpResponse.json(explicitPreview(previewBody!.assignments));
      }),
      http.put(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping`, async ({ request }) => {
        saveBody = await request.json();
        return HttpResponse.json({ profileId: '91000000-0000-4000-8000-000000000005', version: 1, preview: explicitPreview((saveBody as { assignments: EpisodeMappingExplicitDisposition[] }).assignments) });
      }),
    );
    const { router } = renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '逐个文件' }));
    await chooseExplicitTarget('Show - 01.mkv', 'S01E01 · 第一集');
    await chooseExplicitTarget('Show - 02.mkv', 'S00E01 · 特别篇');
    await userEvent.click(screen.getByRole('button', { name: '生成预览' }));

    await screen.findByRole('heading', { name: '映射预览' });
    expect(previewBody).toEqual({
      mode: 'explicit',
      assignments: [
        { sourceFileId, action: 'map', targetSeason: 1, targetEpisode: 1 },
        { sourceFileId: secondSourceFileId, action: 'map', targetSeason: 0, targetEpisode: 1 },
      ],
    });

    await userEvent.click(screen.getByRole('button', { name: '确认并继续' }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/acquisitions/${acquisitionId}`));
    expect(saveBody).toEqual(previewBody);
  });

  it('clears the explicit preview and locks every mapping control while catalog sync is pending', async () => {
    mockBaseRequests();
    const syncGate = createPreviewGate();
    const operationId = '91000000-0000-4000-8000-000000000007';
    let syncCalls = 0;
    server.use(
      http.post('*/api/v1/tmdb/series/42/sync', async () => {
        syncCalls++;
        await syncGate.pending;
        return HttpResponse.json({ operationId, status: 'queued' }, { status: 202 });
      }),
      http.post(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping/preview`, async ({ request }) => {
        const body = await request.json() as { assignments: EpisodeMappingExplicitDisposition[] };
        return HttpResponse.json(explicitPreview(body.assignments));
      }),
    );
    const { router } = renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '逐个文件' }));
    await chooseExplicitTarget('Show - 01.mkv', 'S01E01 · 第一集');
    await chooseExplicitTarget('Show - 02.mkv', 'S00E01 · 特别篇');
    await userEvent.click(screen.getByRole('button', { name: '生成预览' }));
    await screen.findByRole('heading', { name: '映射预览' });

    const syncButton = screen.getByRole('button', { name: '更新剧集信息' });
    await userEvent.click(syncButton);

    await waitFor(() => expect(syncCalls).toBe(1));
    expect(syncButton).toBeDisabled();
    expect(screen.getByRole('button', { name: '单点连续' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '逐个文件' })).toBeDisabled();
    const actionGroup = screen.getByRole('group', { name: 'Show - 01.mkv 的处置' });
    expect(within(actionGroup).getByRole('button', { name: '映射' })).toBeDisabled();
    expect(within(actionGroup).getByRole('button', { name: '排除' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Show - 01.mkv 的 TMDb 剧集' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '生成预览' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '确认并继续' })).toBeDisabled();
    expect(screen.queryByRole('heading', { name: '映射预览' })).not.toBeInTheDocument();

    await userEvent.click(syncButton);
    expect(syncCalls).toBe(1);
    syncGate.release();

    await waitFor(() => expect(router.state.location.pathname).toBe(`/operations/${operationId}`));
  });

  it('discards a delayed preview when the source plan changes and locks mapping controls while pending', async () => {
    mockBaseRequests();
    const previewGate = createPreviewGate();
    let previewCalls = 0;
    server.use(
      http.post(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping/preview`, async ({ request }) => {
        previewCalls++;
        const body = await request.json() as { assignments: EpisodeMappingExplicitDisposition[] };
        await previewGate.pending;
        return HttpResponse.json(explicitPreview(body.assignments));
      }),
    );
    const { queryClient } = renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '逐个文件' }));
    await chooseExplicitTarget('Show - 01.mkv', 'S01E01 · 第一集');
    await chooseExplicitTarget('Show - 02.mkv', 'S00E01 · 特别篇');
    await userEvent.click(screen.getByRole('button', { name: '生成预览' }));

    await waitFor(() => expect(previewCalls).toBe(1));
    expect(screen.getByRole('button', { name: '单点连续' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '逐个文件' })).toBeDisabled();
    expect(within(screen.getByRole('group', { name: 'Show - 01.mkv 的处置' })).getByRole('button', { name: '排除' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Show - 01.mkv 的 TMDb 剧集' })).toBeDisabled();

    act(() => {
      queryClient.setQueryData<Download>(['download', downloadId], { ...download, files: [download.files[0]] });
    });
    previewGate.release();

    await waitFor(() => expect(screen.getByRole('button', { name: '生成预览' })).toBeEnabled());
    expect(screen.queryByRole('heading', { name: '映射预览' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '确认并继续' })).toBeDisabled();
  });

  it('clears an existing preview before a failed retry', async () => {
    mockBaseRequests();
    let previewCalls = 0;
    server.use(
      http.post(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping/preview`, async ({ request }) => {
        previewCalls++;
        const body = await request.json() as { assignments: EpisodeMappingExplicitDisposition[] };
        if (previewCalls === 1) return HttpResponse.json(explicitPreview(body.assignments));
        return HttpResponse.json({ code: 'mapping_incomplete', message: 'preview failed', details: {}, requestId: 'mapping-preview-retry' }, { status: 400 });
      }),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '逐个文件' }));
    await chooseExplicitTarget('Show - 01.mkv', 'S01E01 · 第一集');
    await chooseExplicitTarget('Show - 02.mkv', 'S00E01 · 特别篇');
    await userEvent.click(screen.getByRole('button', { name: '生成预览' }));
    await screen.findByRole('heading', { name: '映射预览' });
    expect(screen.getByRole('button', { name: '确认并继续' })).toBeEnabled();

    await userEvent.click(screen.getByRole('button', { name: '生成预览' }));

    await screen.findByRole('alert');
    expect(screen.queryByRole('heading', { name: '映射预览' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '确认并继续' })).toBeDisabled();
  });

  it('shows a non-interactive state when the TMDb catalog is not synchronized', async () => {
    mockBaseRequests();
    server.use(
      http.get('*/api/v1/tmdb/series/42/catalog', () => HttpResponse.json({ ...catalog, synced: false, lastSyncedAt: undefined, seasons: [] })),
    );
    renderPage();

    expect(await screen.findByText('TMDb 剧集信息尚未同步')).toBeInTheDocument();
    expect(screen.getByText('Show - 01.mkv')).toBeInTheDocument();
    expect(screen.getByText('Show - 02.mkv')).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: '逐个文件' }));
    expect(screen.getByText('TMDb 剧集信息尚未同步')).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Show - 01.mkv 的处置' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '生成预览' })).not.toBeInTheDocument();
  });

  it('keeps Season 0 available for explicit mapping when no regular episodes exist', async () => {
    mockBaseRequests();
    server.use(
      http.get('*/api/v1/tmdb/series/42/catalog', () => HttpResponse.json({ ...catalog, seasons: [catalog.seasons[0]] })),
    );
    renderPage();

    expect(await screen.findByText('没有可用的常规剧集')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: '逐个文件' }));
    await userEvent.click(within(screen.getByRole('group', { name: 'Show - 01.mkv 的处置' })).getByRole('button', { name: '映射' }));
    await userEvent.click(screen.getByRole('button', { name: 'Show - 01.mkv 的 TMDb 剧集' }));
    expect(screen.getByRole('option', { name: 'S00E01 · 特别篇' })).toBeInTheDocument();
  });

  it('shows selected files without mapping controls when catalog seasons contain no episodes', async () => {
    mockBaseRequests();
    server.use(
      http.get('*/api/v1/tmdb/series/42/catalog', () => HttpResponse.json({
        ...catalog,
        seasons: [{ ...catalog.seasons[1], episodeCount: 0, episodes: [] }],
      })),
    );
    renderPage();

    expect(await screen.findByText('没有可用的常规剧集')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: '逐个文件' }));
    expect(screen.getByText('没有可用的 TMDb 剧集')).toBeInTheDocument();
    expect(screen.getByText('Show - 01.mkv')).toBeInTheDocument();
    expect(screen.getByText('Show - 02.mkv')).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Show - 01.mkv 的处置' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '生成预览' })).not.toBeInTheDocument();
  });

  it('submits explicit exclusions without target fields', async () => {
    mockBaseRequests();
    let previewBody: { mode: 'explicit'; assignments: EpisodeMappingExplicitDisposition[] } | undefined;
    server.use(
      http.post(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping/preview`, async ({ request }) => {
        previewBody = await request.json() as typeof previewBody;
        return HttpResponse.json(explicitPreview(previewBody!.assignments));
      }),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '逐个文件' }));
    await chooseExplicitTarget('Show - 01.mkv', 'S01E01 · 第一集');
    const secondGroup = screen.getByRole('group', { name: 'Show - 02.mkv 的处置' });
    await userEvent.click(within(secondGroup).getByRole('button', { name: '排除' }));
    await userEvent.click(screen.getByRole('button', { name: '生成预览' }));

    await waitFor(() => expect(previewBody).toEqual({
      mode: 'explicit',
      assignments: [
        { sourceFileId, action: 'map', targetSeason: 1, targetEpisode: 1 },
        { sourceFileId: secondSourceFileId, action: 'exclude' },
      ],
    }));
  });
});
