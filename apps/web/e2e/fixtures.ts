import type { Page, Route } from '@playwright/test';

/**
 * Stubs the API at the network layer so E2E exercises the real frontend
 * without requiring PostgreSQL, qBittorrent, TMDb or Emby. Each handler
 * mirrors the contract shape from contracts/openapi.yaml.
 */

const completedSetup = {
  state: 'completed',
  databaseConfigured: true,
  databaseManagedExternally: true,
  administratorConfigured: true,
};

const session = {
  user: { id: '00000000-0000-0000-0000-000000000001', username: 'admin' },
  expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
};

function json(route: Route, body: unknown, status = 200) {
  return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
}

export async function stubAuthenticatedApp(page: Page) {
  await page.route('**/api/v1/setup/status', (route) => json(route, completedSetup));
  await page.route('**/api/v1/auth/session', (route) => json(route, session));
  await page.route('**/api/v1/dashboard/summary', (route) =>
    json(route, {
      counts: { downloading: 1, processing: 2, awaitingReview: 1, importing: 0, attention: 1, failed: 0, cleanupFailed: 0, mappingPending: 1 },
      attentionItems: [{
        reason: 'mapping_required',
        acquisition: {
          id: '01000000-0000-0000-0000-000000000001',
          mediaType: 'episode',
          seriesId: '01000000-0000-0000-0000-000000000002',
          seriesTitle: '浏览器待处理示例',
          sourceKind: 'rss',
          sourceSeason: 1,
          sourceEpisode: 7,
          downloadId: '01000000-0000-0000-0000-000000000004',
          download: {
            id: '01000000-0000-0000-0000-000000000004',
            attempt: 1,
            status: 'failed',
            progress: 1,
            clientState: 'uploading',
            failureStage: 'materialize',
            errorCode: 'mapping_profile_required',
            errorMessage: 'the episode acquisition requires a mapping profile before materialization',
            updatedAt: '2026-07-26T07:30:00Z',
          },
          tasks: [],
          mapping: { selectedVideoCount: 3, mappedVideoCount: 1, complete: false },
          aggregateStatus: 'mapping_pending',
          currentStage: 'mapping',
          overallProgress: 0.25,
          stages: [],
          createdAt: '2026-07-26T06:00:00Z',
          updatedAt: '2026-07-26T07:30:00Z',
        },
      }],
      recentOperations: [{
        id: '01000000-0000-0000-0000-000000000003',
        kind: 'download.enqueue',
        status: 'failed',
        errorCode: 'duplicate_torrent',
        updatedAt: '2026-07-26T07:00:00Z',
      }],
      recentImports: [],
      recentScans: [],
      dependencies: {
        qBittorrent: { configured: true },
        tmdb: { configured: true },
        emby: { configured: true },
        mediaTools: { configured: true },
        networkProxy: { configured: false },
      },
      links: {
        downloading: '/acquisitions?phase=downloading',
        processing: '/acquisitions?phase=processing',
        awaitingReview: '/acquisitions?phase=awaiting_review',
        importing: '/acquisitions?phase=importing',
        failed: '/acquisitions?phase=attention',
        cleanupFailed: '/operations?status=failed',
        mappingPending: '/acquisitions?phase=mapping_pending',
      },
    }),
  );
  await page.route('**/api/v1/dashboard/system-metrics', (route) =>
    json(route, {
      sampledAt: '2026-07-26T08:00:04Z',
      sampleIntervalSeconds: 2,
      historyWindowSeconds: 120,
      availability: { cpu: true, memory: true, network: true, diskIO: true, diskCapacity: true },
      memory: { usedBytes: 8589934592, totalBytes: 17179869184 },
      disks: [{ path: 'D:', usedBytes: 644245094400, totalBytes: 1073741824000, usedPercent: 60 }],
      samples: [
        { sampledAt: '2026-07-26T08:00:00Z', cpuUsedPercent: 30, memoryUsedPercent: 48, networkReceiveBytesPerSecond: 1048576, networkSendBytesPerSecond: 524288, diskReadBytesPerSecond: 2097152, diskWriteBytesPerSecond: 1048576 },
        { sampledAt: '2026-07-26T08:00:02Z', cpuUsedPercent: 36, memoryUsedPercent: 49, networkReceiveBytesPerSecond: 2097152, networkSendBytesPerSecond: 1048576, diskReadBytesPerSecond: 4194304, diskWriteBytesPerSecond: 2097152 },
        { sampledAt: '2026-07-26T08:00:04Z', cpuUsedPercent: 42, memoryUsedPercent: 50, networkReceiveBytesPerSecond: 4194304, networkSendBytesPerSecond: 2097152, diskReadBytesPerSecond: 8388608, diskWriteBytesPerSecond: 4194304 },
      ],
    }),
  );
  await page.route('**/api/v1/events**', (route) => route.abort());
}
