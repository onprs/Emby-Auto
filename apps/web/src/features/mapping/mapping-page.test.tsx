import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import type { Acquisition, Download, EpisodeMappingAnchor, EpisodeMappingPreview, TmDbSeriesCatalog } from '@/api/generated/types.gen';
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
  seasons: [{
    seasonNumber: 1,
    name: 'Season 1',
    episodeCount: 2,
    special: false,
    episodes: [
      { episodeNumber: 1, title: '第一集' },
      { episodeNumber: 2, title: '第二集' },
    ],
  }],
};

function previewFor(anchor: EpisodeMappingAnchor): EpisodeMappingPreview {
  return {
    acquisitionId,
    seriesId,
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

describe('MappingPage', () => {
  it('saves the current source anchor immediately when a TMDb episode is selected', async () => {
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
        return HttpResponse.json({ profileId: '91000000-0000-4000-8000-000000000005', version: 1, preview: previewFor(anchor) });
      }),
    );
    const { router } = renderPage();

    await screen.findByRole('heading', { name: '选择对应的 TMDb 剧集' });
    expect(screen.queryByLabelText('资源集')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '生成预览' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '确认并继续' })).not.toBeInTheDocument();

    await userEvent.click(await screen.findByRole('button', { name: '映射到 S01E01：第一集' }));

    await waitFor(() => expect(router.state.location.pathname).toBe(`/acquisitions/${acquisitionId}`));
    expect(router.state.location.search).toMatchObject({ from: '/acquisitions?phase=attention' });
    expect(saveBody).toEqual({ anchor: { sourceFileId, targetSeason: 1, targetEpisode: 1 } });
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

    await screen.findByRole('heading', { name: '选择对应的 TMDb 剧集' });
    expect(screen.queryByText('Agent 建议')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '请求建议' })).not.toBeInTheDocument();
    expect(agentResolutionRequests).toBe(0);
  });

  it('automatically anchors the source file matching the current RSS episode', async () => {
    mockBaseRequests({ ...acquisition, sourceEpisode: 2 });
    let saveBody: unknown;
    server.use(
      http.put(`*/api/v1/acquisitions/${acquisitionId}/episode-mapping`, async ({ request }) => {
        saveBody = await request.json();
        const anchor = (saveBody as { anchor: EpisodeMappingAnchor }).anchor;
        return HttpResponse.json({ profileId: '91000000-0000-4000-8000-000000000005', version: 1, preview: previewFor(anchor) });
      }),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '映射到 S01E02：第二集' }));

    await waitFor(() => expect(saveBody).toEqual({
      anchor: { sourceFileId: secondSourceFileId, targetSeason: 1, targetEpisode: 2 },
    }));
  });
});
