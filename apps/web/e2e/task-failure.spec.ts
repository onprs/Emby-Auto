import { expect, test } from '@playwright/test';

import { stubAuthenticatedApp } from './fixtures';

const taskId = '61000000-0000-4000-8000-000000000001';
const acquisitionId = '61000000-0000-4000-8000-000000000002';
const downloadId = '61000000-0000-4000-8000-000000000003';
const operationId = '61000000-0000-4000-8000-000000000004';
const cursor = '61000000-0000-4000-8000-000000000099';
const now = '2026-07-25T02:30:00Z';

function acquisitionStages(transcodeStatus: 'running' | 'failed') {
  const downstreamStatus = transcodeStatus === 'failed' ? 'blocked' : 'pending';
  return [
    { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
    { key: 'download', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
    { key: 'mapping', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
    { key: 'transcode', status: transcodeStatus, progress: transcodeStatus === 'failed' ? 0 : 0.4, completedItems: 0, totalItems: 1 },
    { key: 'subtitle', status: downstreamStatus, progress: 0, completedItems: 0, totalItems: 1 },
    { key: 'rename', status: downstreamStatus, progress: 0, completedItems: 0, totalItems: 1 },
    { key: 'organize', status: downstreamStatus, progress: 0, completedItems: 0, totalItems: 1 },
    { key: 'review', status: downstreamStatus, progress: 0, completedItems: 0, totalItems: 1 },
    { key: 'import', status: downstreamStatus, progress: 0, completedItems: 0, totalItems: 1 },
  ];
}

function filler(index: number) {
  const suffix = String(index + 10).padStart(12, '0');
  return {
    id: `61000000-0000-4000-8000-${suffix}`,
    mediaType: 'episode',
    seriesId: `62000000-0000-4000-8000-${suffix}`,
    seriesTitle: `队列内容 ${String(index + 1).padStart(2, '0')}`,
    sourceKind: 'rss',
    sourceSeason: 1,
    sourceEpisode: index + 1,
    tasks: [],
    mapping: { selectedVideoCount: 1, mappedVideoCount: 1, complete: true },
    aggregateStatus: 'processing',
    currentStage: 'transcode',
    overallProgress: 0.38,
    stages: acquisitionStages('running'),
    createdAt: now,
    updatedAt: now,
  };
}

const failedAcquisition = {
  id: acquisitionId,
  mediaType: 'episode',
  seriesId: '62000000-0000-4000-8000-000000000001',
  seriesTitle: '视觉验收任务',
  sourceKind: 'rss',
  sourceSeason: 1,
  sourceEpisode: 21,
  downloadId,
  tasks: [{
    id: taskId,
    mediaType: 'episode',
    downloadId,
    sourceSeason: 1,
    sourceEpisode: 21,
    targetSeason: 1,
    targetEpisode: 21,
    targetEpisodeTitle: '无法转码的源文件',
    state: 'failed',
    failureStage: 'video',
    errorCode: 'ffmpeg_transcode_failed',
    errorMessage: 'Authorization: Bearer top-secret C:\\private\\media\\episode21.mkv encoder exited unexpectedly',
    updatedAt: now,
  }],
  mapping: { selectedVideoCount: 1, mappedVideoCount: 1, complete: true },
  aggregateStatus: 'failed',
  currentStage: 'transcode',
  overallProgress: 0.33,
  stages: acquisitionStages('failed'),
  createdAt: now,
  updatedAt: now,
};

const failedTask = {
  id: taskId,
  acquisitionId,
  downloadId,
  mediaType: 'episode',
  seriesTitle: '视觉验收任务',
  sourceSeason: 1,
  sourceEpisode: 21,
  targetSeason: 1,
  targetEpisode: 21,
  targetEpisodeTitle: '无法转码的源文件',
  state: 'failed',
  videoState: 'failed',
  subtitleState: 'ass_ready',
  version: 7,
  failureStage: 'video',
  errorCode: 'ffmpeg_transcode_failed',
  errorMessage: 'Authorization: Bearer top-secret\nC:\\private\\media\\episode21.mkv\nencoder exited unexpectedly',
  operations: [{
    id: operationId,
    kind: 'transcode.run',
    status: 'failed',
    maxAttempts: 3,
    attemptCount: 2,
    errorCode: 'ffmpeg_transcode_failed',
    errorMessage: 'Cookie=session-secret C:\\private\\work\\ffmpeg.log',
    startedAt: '2026-07-25T02:20:00Z',
    finishedAt: now,
    updatedAt: now,
  }],
  actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
  createdAt: '2026-07-25T01:00:00Z',
  updatedAt: now,
};

test('failure summary, sanitized details, retry, and list restoration', async ({ page }, testInfo) => {
  await stubAuthenticatedApp(page);
  const items = [...Array.from({ length: 20 }, (_, index) => filler(index)), failedAcquisition];
  let acquisitionRequest = '';
  let retryRequest: { expectedVersion?: number; key?: string | null } = {};

  await page.route('**/api/v1/acquisitions**', async (route) => {
    acquisitionRequest = route.request().url();
    const pathname = new URL(route.request().url()).pathname;
    const body = pathname.endsWith(`/${acquisitionId}`) ? failedAcquisition : { items };
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  });
  await page.route(`**/api/v1/tasks/${taskId}**`, async (route) => {
    const request = route.request();
    if (request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/retry')) {
      retryRequest = { expectedVersion: request.postDataJSON().expectedVersion, key: request.headers()['idempotency-key'] };
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ task: { ...failedTask, version: 8 }, operationId, status: 'queued' }),
      });
      return;
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(failedTask) });
  });
  await page.route('**/api/v1/events/history**', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [] }) }));

  const listPath = `/acquisitions?sourceKind=rss&phase=attention&cursor=${cursor}`;
  await page.goto(listPath);
  await expect(page.getByRole('heading', { name: '任务' })).toBeVisible();
  await expect.poll(() => new URL(acquisitionRequest).searchParams.get('sourceKind')).toBe('rss');
  await expect.poll(() => new URL(acquisitionRequest).searchParams.get('phase')).toBe('attention');

  const mobile = testInfo.project.name === 'mobile';
  const record = mobile
    ? page.locator('article').filter({ hasText: '视觉验收任务' })
    : page.locator('tbody tr').filter({ hasText: '视觉验收任务' });
  await record.scrollIntoViewIfNeeded();
  const savedScroll = await page.evaluate(() => window.scrollY);
  expect(savedScroll).toBeGreaterThan(200);
  const failureSummary = record.getByTitle('视频转码失败：FFmpeg 未能完成视频转换');
  await expect(failureSummary).toBeVisible();
  expect(await failureSummary.locator('span').evaluate((node) => node.getBoundingClientRect().height)).toBeLessThanOrEqual(24);
  await expect(record).not.toContainText('top-secret');

  await record.getByRole('button', { name: '更多操作' }).click();
  const menu = page.getByRole('menu');
  await expect(menu.getByRole('menuitem', { name: '查看失败原因' })).toBeVisible();
  await expect(menu.getByRole('menuitem', { name: '重试任务' })).toBeVisible();
  await expect(menu.getByRole('menuitem', { name: '删除', exact: true })).toBeVisible();
  const menuBounds = await menu.evaluate((node) => {
    const bounds = node.getBoundingClientRect();
    return { top: bounds.top, bottom: bounds.bottom, viewportHeight: window.innerHeight };
  });
  expect(menuBounds.top).toBeGreaterThanOrEqual(8);
  expect(menuBounds.bottom).toBeLessThanOrEqual(menuBounds.viewportHeight - 8);
  const menuScreenshotPath = testInfo.outputPath('failure-list-menu.png');
  await page.screenshot({ path: menuScreenshotPath, fullPage: true });
  await testInfo.attach('failure-list-menu', { path: menuScreenshotPath, contentType: 'image/png' });
  await menu.getByRole('menuitem', { name: '查看失败原因' }).click();
  await expect(page).toHaveURL(new RegExp(`/acquisitions/${acquisitionId}`));
  const acquisitionPath = `${new URL(page.url()).pathname}${new URL(page.url()).search}`;
  await expect(page.getByRole('heading', { name: '视觉验收任务' })).toBeVisible();
  await expect(page.getByText('视频转码失败：FFmpeg 未能完成视频转换')).toBeVisible();
  await page.getByRole('link', { name: '处理项诊断' }).click();

  await expect(page).toHaveURL(new RegExp(`/tasks/${taskId}`));
  const detailUrl = new URL(page.url());
  expect(detailUrl.searchParams.get('from')).toBe(acquisitionPath);
  await expect(page.getByRole('heading', { name: '失败信息' })).toBeVisible();
  await expect(page.getByText('视频转码失败：FFmpeg 未能完成视频转换')).toBeVisible();
  await expect(page.getByText('第 1 次执行 · 最近一次尝试 2/3')).toBeVisible();
  await page.getByText('查看技术详情', { exact: true }).click();
  const technical = page.locator('pre').filter({ hasText: 'ffmpeg_transcode_failed' });
  await expect(technical).toContainText('[已隐藏]');
  await expect(technical).toContainText('[服务器路径]');
  await expect(technical).not.toContainText('top-secret');
  await expect(technical).not.toContainText('session-secret');
  await expect(technical).not.toContainText('C:\\private');
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  const screenshotPath = testInfo.outputPath('failure-detail.png');
  await page.screenshot({ path: screenshotPath, fullPage: true });
  await testInfo.attach('failure-detail', { path: screenshotPath, contentType: 'image/png' });

  await page.getByRole('button', { name: '重试任务' }).click();
  await expect.poll(() => retryRequest.expectedVersion).toBe(7);
  expect(retryRequest.key).toBeTruthy();
  await expect(page.getByRole('status').filter({ hasText: '重试请求已提交' })).toBeVisible();

  await page.getByRole('button', { name: '返回' }).click();
  await expect(page).toHaveURL(acquisitionPath);
  await expect(page.getByRole('heading', { name: '视觉验收任务' })).toBeVisible();
  await page.getByRole('button', { name: '返回' }).click();
  await expect(page).toHaveURL(listPath);
  await expect(page.getByRole('heading', { name: '任务' })).toBeVisible();
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(200);
  await expect(record).toBeInViewport();

  await page.goForward();
  await expect(page).toHaveURL(acquisitionPath);
  await page.goForward();
  await expect(page).toHaveURL(new RegExp(`/tasks/${taskId}`));
  await expect(page.getByRole('heading', { name: '失败信息' })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(acquisitionPath);
  await page.goBack();
  await expect(page).toHaveURL(listPath);
  await expect(page.getByRole('heading', { name: '任务' })).toBeVisible();
});
