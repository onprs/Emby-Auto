import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';

import { expect, test, type Page, type TestInfo } from '@playwright/test';

import { stubAuthenticatedApp } from './fixtures';

async function captureScreenshot(page: Page, testInfo: TestInfo, name: string) {
  const screenshot = await page.screenshot({ fullPage: true });
  await testInfo.attach(name, { body: screenshot, contentType: 'image/png' });
  if (process.env.EMBY_AUTO_SCREENSHOT_DIR) {
    await mkdir(process.env.EMBY_AUTO_SCREENSHOT_DIR, { recursive: true });
    await writeFile(path.join(process.env.EMBY_AUTO_SCREENSHOT_DIR, `${name}-${testInfo.project.name}.png`), screenshot);
  }
}

test.describe('application shell', () => {
  test('shows the dashboard and main navigation for an authenticated admin', async ({ page }, testInfo) => {
    await stubAuthenticatedApp(page);
    await page.goto('/');

    const mobile = testInfo.project.name === 'mobile';
    await expect(page.getByRole('heading', { name: '仪表盘' })).toBeVisible();
    const resources = page.getByLabel('系统资源');
    await expect(resources.getByLabel('CPU 使用率图表')).toContainText('42%');
    await expect(resources.getByLabel('内存使用率图表')).toContainText('8.0 GiB / 16.0 GiB');
    const networkCard = resources.getByLabel('网络速度图表');
    const diskCard = resources.getByLabel('磁盘', { exact: true });
    await expect(networkCard).toContainText('4.0 MiB/s');
    await expect(diskCard).toContainText('nvme0n1');
    await expect(diskCard).toContainText('sda, sdb');
    await expect(diskCard).not.toContainText('/data/video/video1');
    await expect(diskCard).not.toContainText('不同颜色代表不同磁盘设备');
    await expect(diskCard).not.toContainText('容量与 I/O 实时负载');
    await expect(resources.getByLabel('磁盘资源图表')).toContainText('读取 8.0 MiB/s');
    await expect(resources.getByLabel('磁盘资源图表')).toContainText('写入 4.0 MiB/s');
    if (!mobile) {
      const [networkBox, diskBox] = await Promise.all([networkCard.boundingBox(), diskCard.boundingBox()]);
      expect(networkBox).not.toBeNull();
      expect(diskBox).not.toBeNull();
      expect(Math.abs(diskBox!.y - networkBox!.y)).toBeLessThan(1);
      expect(diskBox!.x).toBeGreaterThan(networkBox!.x);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    if (mobile) {
      await page.getByRole('button', { name: '打开主导航' }).click();
    }
    const navigation = page.getByRole('navigation', { name: '主导航' });
    await expect(navigation.getByRole('link', { name: '仪表盘', exact: true })).toBeVisible();
    await expect(navigation.getByRole('link', { name: '搜索添加', exact: true })).toBeVisible();
    await expect(navigation.getByRole('link', { name: 'RSS 订阅', exact: true })).toBeVisible();
    await expect(navigation.getByRole('link', { name: '任务', exact: true })).toBeVisible();
    await expect(navigation.getByRole('link', { name: '媒体库', exact: true })).toBeVisible();
    await expect(navigation.getByRole('link', { name: '运行记录', exact: true })).toBeVisible();
    await expect(navigation.getByRole('link', { name: '系统设置', exact: true })).toBeVisible();
  });

  test('keeps the half-width disk card free of internal overflow at the sm breakpoint', async ({ page }, testInfo) => {
    test.skip(testInfo.project.name !== 'desktop', '桌面项目覆盖中间宽度布局');
    await page.setViewportSize({ width: 640, height: 900 });
    await stubAuthenticatedApp(page);
    await page.goto('/');

    const resources = page.getByLabel('系统资源');
    const networkCard = resources.getByLabel('网络速度图表');
    const diskCard = resources.getByLabel('磁盘', { exact: true });
    const diskList = diskCard.getByLabel('磁盘容量').locator('ul');
    await expect(diskList).toBeVisible();

    const [networkBox, diskBox] = await Promise.all([networkCard.boundingBox(), diskCard.boundingBox()]);
    expect(networkBox).not.toBeNull();
    expect(diskBox).not.toBeNull();
    expect(Math.abs(diskBox!.y - networkBox!.y)).toBeLessThan(1);
    expect(diskBox!.x).toBeGreaterThan(networkBox!.x);
    expect(await diskCard.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    expect(await diskList.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  });

  test('runs Emby file scanning and catalog updates in place', async ({ page }) => {
    const pageErrors: string[] = [];
    page.on('pageerror', (error) => pageErrors.push(error.message));
    await stubAuthenticatedApp(page);

    const scanId = '24000000-0000-4000-8000-000000000001';
    const scanOperationId = '24000000-0000-4000-8000-000000000002';
    const refreshOperationId = '24000000-0000-4000-8000-000000000003';
    const now = '2026-07-26T08:00:00Z';
    let refreshSucceeded = false;
    let scanCreated = false;
    let scanSucceeded = false;

    const scan = () => ({
      id: scanId,
      operationId: scanOperationId,
      status: scanSucceeded ? 'succeeded' : 'running',
      libraryCount: scanSucceeded ? 1 : 0,
      itemCount: scanSucceeded ? 24 : 0,
      createdAt: now,
      updatedAt: now,
      ...(scanSucceeded ? { startedAt: now, completedAt: now } : { startedAt: now }),
    });

    await page.route(/\/api\/v1\/emby\/refresh(?:\?.*)?$/, (route) => route.fulfill({
      status: 202,
      contentType: 'application/json',
      body: JSON.stringify({ operationId: refreshOperationId, status: 'queued' }),
    }));
    await page.route(new RegExp(`/api/v1/operations/${refreshOperationId}(?:\\?.*)?$`), (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: refreshOperationId,
        kind: 'emby.refresh',
        status: refreshSucceeded ? 'succeeded' : 'running',
        idempotencyKey: 'refresh-fixture',
        maxAttempts: 5,
        attemptCount: 1,
        createdAt: now,
        updatedAt: now,
        attempts: [],
      }),
    }));
    await page.route(/\/api\/v1\/emby\/scans(?:\?.*)?$/, (route) => {
      if (route.request().method() === 'POST') {
        scanCreated = true;
        return route.fulfill({
          status: 202,
          contentType: 'application/json',
          body: JSON.stringify({ scan: { ...scan(), status: 'queued' }, operationId: scanOperationId, status: 'queued' }),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: scanCreated ? [scan()] : [] }),
      });
    });
    await page.route(new RegExp(`/api/v1/emby/scans/${scanId}(?:\\?.*)?$`), (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(scan()),
    }));
    await page.route(/\/api\/v1\/emby\/libraries(?:\?.*)?$/, (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(scanSucceeded ? [{
        id: '24000000-0000-4000-8000-000000000004',
        embyId: 'fixture-library',
        name: 'Fixture Library',
        collectionType: 'tvshows',
        locations: ['/media/anime'],
        present: true,
        lastSeenAt: now,
      }] : []),
    }));

    await page.goto('/emby');
    await page.getByRole('button', { name: '请求 Emby 扫描文件' }).click();
    await expect(page.getByText('正在等待后台向 Emby 发送扫描请求。')).toBeVisible();
    await expect(page).toHaveURL(/\/emby$/);
    refreshSucceeded = true;
    await expect(page.getByText('Emby 已接受媒体文件扫描请求。')).toBeVisible({ timeout: 5_000 });

    await page.getByRole('button', { name: '从 Emby 更新目录' }).click();
    await expect(page.getByText('正在读取 Emby 当前的媒体库和条目。')).toBeVisible();
    await expect(page).toHaveURL(/\/emby$/);
    scanSucceeded = true;
    await expect(page.getByText('目录已更新：1 个媒体库，24 个媒体条目。')).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole('link', { name: 'Fixture Library' })).toBeVisible();
    await expect(page).toHaveURL(/\/emby$/);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    expect(pageErrors).toEqual([]);
  });

  test('separates actionable tasks from failed operation history on the dashboard', async ({ page }) => {
    const pageErrors: string[] = [];
    page.on('pageerror', (error) => pageErrors.push(error.message));
    await stubAuthenticatedApp(page);
    await page.goto('/');

    const attention = page.getByLabel('需要处理的任务');
    await expect(attention.getByText('浏览器待处理示例')).toBeVisible();
    await expect(attention.getByText('需要确认剧集对应关系')).toBeVisible();
    await expect(attention.getByText('1 / 3 个视频已确认集数，其余文件无法继续处理。')).toBeVisible();
    await expect(attention.getByRole('link', { name: '设置剧集映射：浏览器待处理示例' })).toHaveAttribute('href', /\/acquisitions\/01000000-0000-0000-0000-000000000001/);
    await expect(attention.getByText(/下载失败/)).toHaveCount(0);
    await expect(attention.getByText('添加下载')).toHaveCount(0);

    const recent = page.getByLabel('最近运行记录');
    await expect(recent.getByText('添加下载')).toBeVisible();
    await expect(recent.getByText('这个资源已经在下载列表中。')).toBeVisible();
    expect(pageErrors).toEqual([]);
  });

  test('shows fine-grained task and RSS aggregate progress', async ({ page }, testInfo) => {
    const mobile = testInfo.project.name === 'mobile';
    const pageErrors: string[] = [];
    page.on('pageerror', (error) => pageErrors.push(error.message));
    await stubAuthenticatedApp(page);
    const now = '2026-07-26T08:30:00Z';
    const acquisitionId = '23000000-0000-0000-0000-000000000001';
    const subscriptionId = '23000000-0000-0000-0000-000000000002';
    const entryTitle = '细粒度进度作品 - S01E01';
    let rssListURL = '';
    let rssEntriesURL = '';

    const rssSubscription = {
      id: subscriptionId,
      seriesId: '23000000-0000-0000-0000-000000000003',
      tmdbSeriesId: 230,
      seriesTitle: '细粒度进度作品',
      name: '细粒度 RSS 订阅',
      feedUrl: 'https://example.test/fine-progress.xml',
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
      nextPollAt: now,
      version: 1,
      createdAt: now,
      updatedAt: now,
    };
    await page.route('**/api/v1/rss/subscriptions**', (route) => {
      const url = new URL(route.request().url());
      if (url.pathname.endsWith(`/${subscriptionId}/entries`)) {
        rssEntriesURL = url.toString();
        // 详情页分别请求 confirmed 与 skipped 两个分组，mock 需按 group 区分，
        // 否则同一条目会在两个分组各渲染一次。
        const group = url.searchParams.get('group');
        const items = group === 'skipped' ? [] : [{
          id: '23000000-0000-0000-0000-000000000004',
          subscriptionId,
          acquisitionId,
          acquisitionProgress: { aggregateStatus: 'pending', currentStage: 'download', overallProgress: 0.02 },
          title: entryTitle,
          status: 'enqueued',
          classification: 'enqueued',
          duplicateCount: 0,
          downloadUriAvailable: true,
          sourceSeason: 1,
          sourceEpisode: 1,
          createdAt: now,
          updatedAt: now,
        }];
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ items }),
        });
      }
      if (url.pathname.endsWith(`/${subscriptionId}`)) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(rssSubscription) });
      }
      rssListURL = url.toString();
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [rssSubscription] }) });
    });
    const acquisition = {
      id: acquisitionId,
      mediaType: 'episode',
      seriesId: '23000000-0000-0000-0000-000000000003',
      seriesTitle: '细粒度进度作品',
      sourceKind: 'rss',
      sourceSeason: 1,
      sourceEpisode: 1,
      tasks: [],
      mapping: { selectedVideoCount: 0, mappedVideoCount: 0, complete: false },
      aggregateStatus: 'pending',
      currentStage: 'download',
      overallProgress: 0.02,
      stages: [
        { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'download', status: 'pending', progress: 0, completedItems: 0, totalItems: 1 },
        { key: 'mapping', status: 'pending', progress: 0, completedItems: 0, totalItems: 0 },
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
    await page.route('**/api/v1/acquisitions**', (route) => {
      const pathname = new URL(route.request().url()).pathname;
      const body = pathname.endsWith(`/${acquisitionId}`) ? acquisition : { items: [acquisition] };
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });

    await page.goto('/rss');
    const rssProgress = page.getByRole('progressbar', { name: '细粒度 RSS 订阅总进度' });
    await expect(rssProgress).toHaveAttribute('aria-valuenow', '36.2');
    const completedCounts = page.getByText('1 / 3 个任务已完成');
    await expect(mobile ? completedCounts.first() : completedCounts.last()).toBeVisible();
    await page.getByRole('button', { name: '总进度，点击按正序排列' }).click();
    await expect(page).toHaveURL(/sortBy=progress/);
    await expect.poll(() => new URL(rssListURL).searchParams.get('sortBy')).toBe('progress');

    await page.goto('/acquisitions');
    const titles = page.getByText('细粒度进度作品');
    const taskProgress = page.getByRole('progressbar', { name: '任务整体进度' });
    const percentages = page.getByText('2%');
    await expect(mobile ? titles.first() : titles.last()).toBeVisible();
    await expect(mobile ? taskProgress.first() : taskProgress.last()).toHaveAttribute('aria-valuenow', '2');
    await expect(mobile ? percentages.first() : percentages.last()).toBeVisible();

    await page.goto(`/rss/${subscriptionId}`);
    const entryProgress = page.getByRole('progressbar', { name: `${entryTitle}处理进度` });
    await expect(entryProgress).toHaveAttribute('aria-valuenow', '2');
    await expect(page.getByText('查看处理进度')).toHaveCount(0);
    await page.getByRole('button', { name: '处理进度，点击按正序排列' }).first().click();
    await expect(page).toHaveURL(/sortBy=progress/);
    await expect.poll(() => new URL(rssEntriesURL).searchParams.get('sortBy')).toBe('progress');
    await page.getByRole('link', { name: entryTitle }).click();
    await expect(page).toHaveURL(new RegExp(`/acquisitions/${acquisitionId}`));
    await expect(page.getByRole('heading', { name: '细粒度进度作品' })).toBeVisible();
    expect(pageErrors).toEqual([]);
  });

  test('deletes a download lifecycle in place and reports backend cleanup completion', async ({ page }) => {
    const pageErrors: string[] = [];
    page.on('pageerror', (error) => pageErrors.push(error.message));
    await stubAuthenticatedApp(page);
    const downloadId = '20000000-0000-0000-0000-000000000001';
    const acquisitionId = '20000000-0000-0000-0000-000000000002';
    const now = new Date().toISOString();
    const download = {
      id: downloadId,
      acquisitionId,
      attempt: 1,
      clientName: 'qbittorrent',
      clientState: 'uploading',
      torrentHash: '0123456789abcdef0123456789abcdef01234567',
      status: 'materialized',
      progress: 1,
      savePath: '/downloads/example',
      version: 3,
      createdAt: now,
      updatedAt: now,
      files: [{ id: '20000000-0000-0000-0000-000000000003', fileIndex: 0, relativePath: 'Example.S01E01.mkv', sizeBytes: 1024, mediaKind: 'video', selected: true }],
      actions: { canRetry: false, canCancel: false, canDelete: true, canEditFileSelection: false },
    };
    let deleteRequested = false;
    await page.route(`**/api/v1/downloads/${downloadId}**`, (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(download) }),
    );
    await page.route('**/api/v1/acquisitions**', (route) => {
      const pathname = new URL(route.request().url()).pathname;
      if (route.request().method() === 'DELETE' && pathname.endsWith(`/${acquisitionId}`)) {
        deleteRequested = true;
        return route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify({ operationId: '20000000-0000-0000-0000-000000000004', status: 'queued' }) });
      }
      const body = pathname.endsWith(`/${acquisitionId}`)
        ? {
            id: acquisitionId,
            mediaType: 'episode',
            seriesId: '20000000-0000-0000-0000-000000000005',
            seriesTitle: '下载删除测试',
            sourceKind: 'manual',
            sourceSeason: 1,
            sourceEpisode: 1,
            downloadId,
            tasks: [],
            mapping: { selectedVideoCount: 1, mappedVideoCount: 1, complete: true },
            aggregateStatus: 'processing',
            createdAt: now,
            updatedAt: now,
          }
        : { items: [] };
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.route('**/api/v1/operations**', (route) => {
      const pathname = new URL(route.request().url()).pathname;
      const body = pathname.endsWith('/20000000-0000-0000-0000-000000000004')
        ? {
            id: '20000000-0000-0000-0000-000000000004',
            resourceType: 'acquisition',
            resourceId: acquisitionId,
            kind: 'acquisition.delete',
            status: 'succeeded',
            attempt: 1,
            maxAttempts: 5,
            createdAt: now,
            updatedAt: now,
          }
        : { items: [] };
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });

    await page.goto(`/downloads/${downloadId}`);
    await expect(page.getByRole('heading', { name: '下载进度' })).toBeVisible();
    await page.getByRole('button', { name: '删除下载' }).click();
    const dialog = page.getByRole('alertdialog', { name: '确认删除下载' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText('将删除这条下载所属的完整任务流程。')).toBeVisible();
    await expect(dialog.getByText('未被其他内容使用的 qBittorrent 任务、源文件和临时文件也会删除。')).toBeVisible();
    await expect(dialog.getByText('已经成功入库到 Emby 的正式资源不会被删除。')).toBeVisible();
    await dialog.getByRole('button', { name: '确认删除' }).click();

    await expect.poll(() => deleteRequested).toBe(true);
    await expect(page).toHaveURL(new RegExp(`/downloads/${downloadId}$`));
    await expect(page.getByText('已成功删除 1 项')).toBeVisible();
    expect(pageErrors).toEqual([]);
  });

  test('sorts lifecycle tasks from column headers and selects the current page', async ({ page }, testInfo) => {
    const pageErrors: string[] = [];
    page.on('pageerror', (error) => pageErrors.push(error.message));
    await stubAuthenticatedApp(page);
    const now = new Date().toISOString();
    const item = {
      id: '21000000-0000-0000-0000-000000000001',
      mediaType: 'episode',
      seriesId: '21000000-0000-0000-0000-000000000002',
      seriesTitle: '列排序测试',
      sourceKind: 'rss',
      sourceSeason: 1,
      sourceEpisode: 2,
      tasks: [],
      mapping: { selectedVideoCount: 0, mappedVideoCount: 0, complete: false },
      aggregateStatus: 'failed',
      currentStage: 'download',
      overallProgress: 0.15,
      stages: [
        { key: 'source', status: 'completed', progress: 1, completedItems: 1, totalItems: 1 },
        { key: 'download', status: 'failed', progress: 0.35, completedItems: 0, totalItems: 1 },
        { key: 'mapping', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'transcode', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'subtitle', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'rename', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'organize', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'review', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
        { key: 'import', status: 'blocked', progress: 0, completedItems: 0, totalItems: 0 },
      ],
      createdAt: now,
      updatedAt: now,
    };
    let lastListURL = '';
    await page.route('**/api/v1/acquisitions**', (route) => {
      if (route.request().method() === 'GET') lastListURL = route.request().url();
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [item] }) });
    });

    await page.goto('/acquisitions');
    const taskTitles = page.getByText('列排序测试');
    await expect(testInfo.project.name === 'mobile' ? taskTitles.first() : taskTitles.last()).toBeVisible();
    await expect(page.locator('#acquisition-sort')).toHaveCount(0);
    const progressHeaders = page.getByRole('button', { name: '整体进度，点击按正序排列' });
    await (testInfo.project.name === 'mobile' ? progressHeaders.first() : progressHeaders.last()).click();
    await expect(page).toHaveURL(/sortBy=progress/);
    await expect(page).toHaveURL(/sortOrder=asc/);
    await expect.poll(() => new URL(lastListURL).searchParams.get('sortBy')).toBe('progress');

    const selectAll = page.getByRole('checkbox', { name: '全选当前页任务' });
    await (testInfo.project.name === 'mobile' ? selectAll.first() : selectAll.last()).check();
    await expect(page.getByText('已选择 1 项')).toBeVisible();
    await expect(page.getByRole('button', { name: '批量删除' })).toBeVisible();
    expect(pageErrors).toEqual([]);
  });

  test('navigates to the settings route', async ({ page }, testInfo) => {
    await stubAuthenticatedApp(page);
    await page.route('**/api/v1/config', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          version: 1,
          qBittorrent: { url: '', username: '', password: { configured: false, masked: '' }, downloadRateLimitKibPerSecond: 0, uploadRateLimitKibPerSecond: 0 },
          emby: { url: '', apiKey: { configured: false, masked: '' } },
          tmdb: { apiToken: { configured: false, masked: '' } },
          networkProxy: { enabled: false, url: '' },
          agent: {
            enabled: false,
            protocol: 'openai_chat_completions',
            baseUrl: '',
            model: '',
            apiKey: { configured: false, masked: '' },
            useNetworkProxy: false,
            requestTimeoutSeconds: 120,
            rssCoordinateMode: 'off',
            downloadFileSelectionMode: 'off',
            catalogMatchEnabled: false,
            episodeMappingEnabled: false,
            allowAutomaticEpisodeMapping: false,
            subtitleVideoMatchMode: 'off',
          },
          paths: { downloadRoot: '', workRoot: '', stagingRoot: '', animeLibraryRoot: '', movieLibraryRoot: '', ffmpegPath: '', ffprobePath: '' },
          transcode: {
            name: 'default', videoCodec: 'h264', encoder: 'libx264', container: 'matroska', fileExtension: 'mkv',
            qualityMode: 'crf', qualityValue: 20, audioPolicy: 'copy', preset: 'medium', pixelFormat: 'yuv420p',
            threadCount: 0, maxConcurrency: 2,
          },
          events: { retentionDays: 30 },
        }),
      }),
    );
    await page.goto('/');
    if (testInfo.project.name === 'mobile') {
      await page.getByRole('button', { name: '打开主导航' }).click();
    }
    await page.getByRole('link', { name: '系统设置', exact: true }).click();
    await expect(page.getByRole('heading', { name: '设置' })).toBeVisible();
  });
});

test.describe('first-run setup', () => {
  test('collects complete runtime configuration before entering the application', async ({ page }) => {
    let requestBody: Record<string, unknown> | null = null;
    await page.route('**/api/v1/setup/status', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        state: 'required',
        databaseConfigured: true,
        databaseManagedExternally: true,
        administratorConfigured: false,
      }),
    }));
    await page.route('**/api/v1/setup/initialize', async (route) => {
      requestBody = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          state: 'completed',
          databaseConfigured: true,
          databaseManagedExternally: true,
          administratorConfigured: true,
        }),
      });
    });
    await page.route('**/api/v1/auth/session', (route) => route.fulfill({ status: 401, contentType: 'application/json', body: '{}' }));

    await page.goto('/');
    await expect(page.getByRole('heading', { name: '系统初始化' })).toBeVisible();
    await page.getByLabel('密码', { exact: true }).fill('administrator-password');
    await page.getByLabel('确认密码').fill('administrator-password');
    await page.getByRole('button', { name: '继续' }).click();

    await page.getByLabel('密码', { exact: true }).fill('qb-password');
    await page.getByLabel('Emby API key').fill('emby-key');
    await page.getByLabel('TMDb API Read Access Token').fill('tmdb-token');
    await page.getByRole('button', { name: '继续' }).click();
    await expect(page.getByLabel('下载根目录')).toHaveValue('/srv/emby-auto/downloads');
    await expect(page.getByLabel('番剧媒体库目录')).toHaveValue('/srv/emby-auto/media/anime');
    await expect(page.getByLabel('电影媒体库目录')).toHaveValue('/srv/emby-auto/media/movies');
    await page.getByRole('button', { name: '继续' }).click();
    await expect(page.getByLabel('配置名称')).toHaveValue('default-h264');
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.getByRole('button', { name: '继续', exact: true }).click();
    const initializeButton = page.getByRole('button', { name: '开始初始化' });
    await initializeButton.waitFor({ state: 'visible', timeout: 1_000 }).catch(() => undefined);
    if (requestBody === null) {
      await initializeButton.click();
    }

    await expect.poll(() => requestBody).not.toBeNull();
    const body = requestBody as unknown as {
      database?: object;
      configuration: {
        qBittorrent: { password: string };
        emby: { apiKey: string };
        tmdb: { apiToken: string };
        paths: { ffmpegPath: string; animeLibraryRoot: string; movieLibraryRoot: string };
        transcode: { encoder: string; maxConcurrency: number };
      };
    };
    expect(body.database).toBeUndefined();
    expect(body.configuration.qBittorrent.password).toBe('qb-password');
    expect(body.configuration.emby.apiKey).toBe('emby-key');
    expect(body.configuration.tmdb.apiToken).toBe('tmdb-token');
    expect(body.configuration.paths).toEqual(expect.objectContaining({
      ffmpegPath: '/usr/bin/ffmpeg',
      animeLibraryRoot: '/srv/emby-auto/media/anime',
      movieLibraryRoot: '/srv/emby-auto/media/movies',
    }));
    expect(body.configuration.transcode).toEqual(expect.objectContaining({ encoder: 'libx264', maxConcurrency: 1 }));
  });
});
