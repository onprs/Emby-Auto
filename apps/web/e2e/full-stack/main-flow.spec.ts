import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

type Manifest = {
  fixtureURL: string;
  username: string;
  password: string;
  paths: {
    downloadRoot: string;
    workRoot: string;
    stagingRoot: string;
    animeLibraryRoot: string;
    movieLibraryRoot: string;
    ffmpegPath: string;
    ffprobePath: string;
  };
};

type Acquisition = { id: string; mediaType: 'episode' | 'movie'; downloadId?: string; tasks: Array<{ id: string }> };
type Download = { id: string; status: string; version: number; failureStage?: string; errorCode?: string };
type Task = {
  id: string;
  mediaType: 'episode' | 'movie';
  movieTitle?: string;
  releaseYear?: number;
  state: string;
  version: number;
  failureStage?: string;
  errorCode?: string;
  artifacts?: { basename: string; video: { id: string }; subtitle: { id: string } };
  cleanup?: { status: string };
  operations: Array<{ id: string; kind: string; status: string }>;
};
type Operation = { id: string; kind: string; status: string; attemptCount: number; errorCode?: string };
type OperationPage = { items: Operation[] };
type CommandAccepted = { operationId: string; status: string };
type EmbyScanAccepted = { scan: { id: string }; operationId: string; status: string };
type RssSubscription = { id: string; name: string; feedUrl: string; enabled: boolean; completedAt?: string };
type RssSubscriptionPage = { items: RssSubscription[] };
type RssEntryPage = { items: Array<{ acquisitionId?: string; downloadId?: string; status: string }> };

const manifest = JSON.parse(
  fs.readFileSync(path.resolve(process.cwd(), '../../runtime/e2e/manifest.json'), 'utf8'),
) as Manifest;

async function login(page: Page) {
  await page.goto('/');
  const loginHeading = page.getByRole('heading', { name: '管理员登录' });
  const dashboardHeading = page.getByRole('heading', { name: '仪表盘' });
  await expect(loginHeading.or(dashboardHeading)).toBeVisible();
  if (await loginHeading.isVisible()) {
    await page.getByLabel('用户名').fill(manifest.username);
    await page.getByLabel('密码').fill(manifest.password);
    await page.getByRole('button', { name: '登录', exact: true }).click();
  }
  await expect(dashboardHeading).toBeVisible();
}

async function configure(page: Page) {
  await page.goto('/settings/services');
  await expect(page.getByRole('heading', { name: '外部服务' })).toBeVisible();
  const currentURL = await page.locator('#qb-url').inputValue();
  if (currentURL !== manifest.fixtureURL) {
    await page.locator('#qb-url').fill(manifest.fixtureURL);
    await page.locator('#qb-username').fill('fixture');
    await page.locator('[id="secret-密码"]').fill('fixture-password');

    await page.locator('#emby-url').fill(`${manifest.fixtureURL}/emby`);
    await page.locator('[id="secret-API key"]').fill('fixture-key');
    await page.locator('[id="secret-API Read Access Token"]').fill('fixture-token');
    await page.getByRole('button', { name: '保存外部服务配置' }).click();
    await expect(page.getByText('配置已保存', { exact: false })).toBeVisible();
  }

  for (const title of ['qBittorrent', 'Emby', 'TMDb']) {
    const card = page.getByRole('heading', { name: title, exact: true }).locator('xpath=../..');
    await card.getByRole('button', { name: '测试连接' }).click();
    await expect(card.getByText('连接成功', { exact: false })).toBeVisible();
  }

  await page.goto('/settings/storage');
  await expect(page.getByRole('heading', { name: '存储与媒体工具' })).toBeVisible();
  const currentDownloadRoot = await page.locator('#downloadRoot').inputValue();
  if (currentDownloadRoot !== manifest.paths.downloadRoot) {
    await page.locator('#downloadRoot').fill(manifest.paths.downloadRoot);
    await page.locator('#workRoot').fill(manifest.paths.workRoot);
    await page.locator('#stagingRoot').fill(manifest.paths.stagingRoot);
    await page.locator('#animeLibraryRoot').fill(manifest.paths.animeLibraryRoot);
    await page.locator('#movieLibraryRoot').fill(manifest.paths.movieLibraryRoot);
    await page.locator('#ffmpeg').fill(manifest.paths.ffmpegPath);
    await page.locator('#ffprobe').fill(manifest.paths.ffprobePath);
    await page.getByRole('button', { name: '保存存储配置' }).click();
    await expect(page.getByText('配置已保存', { exact: false })).toBeVisible();
  }
  const mediaTools = page.getByRole('heading', { name: '媒体工具', exact: true }).locator('xpath=../..');
  await mediaTools.getByRole('button', { name: '测试连接' }).click();
  await expect(mediaTools.getByText('连接成功', { exact: false })).toBeVisible();
}

async function json<T>(request: APIRequestContext, url: string): Promise<T> {
  const response = await request.get(url);
  expect(response.ok(), `${url}: ${response.status()} ${await response.text()}`).toBeTruthy();
  return response.json() as Promise<T>;
}

function currentResourceID(page: Page): string {
  const id = new URL(page.url()).pathname.split('/').filter(Boolean).at(-1);
  expect(id).toBeTruthy();
  return id!;
}

async function poll<T>(read: () => Promise<T>, done: (value: T) => boolean, timeout = 60_000): Promise<T> {
  const started = Date.now();
  let latest = await read();
  while (!done(latest)) {
    if (Date.now() - started > timeout) {
      throw new Error(`poll timed out with ${JSON.stringify(latest)}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 300));
    latest = await read();
  }
  return latest;
}

async function selectFixtureSeries(page: Page) {
  await page.locator('#tmdb-query').fill('Fixture Show');
  await page.getByRole('button', { name: '查询', exact: true }).click();
  await page.getByRole('button', { name: /Fixture Show/ }).click();
}

async function setFixtureControl(page: Page, target: string, enabled: boolean) {
  const response = await page.request.post(`${manifest.fixtureURL}/control/failure?target=${encodeURIComponent(target)}&enabled=${enabled}`);
  expect(response.ok(), `fixture control ${target}=${enabled}`).toBeTruthy();
}

async function retryFailedTaskFromPage(page: Page, taskId: string) {
  for (let attempt = 0; attempt < 2; attempt += 1) {
    const responsePromise = page.waitForResponse((response) =>
      new URL(response.url()).pathname === `/api/v1/tasks/${taskId}/retry` && response.request().method() === 'POST');
    await page.getByRole('button', { name: '重试任务', exact: true }).click();
    const response = await responsePromise;
    if (response.ok()) return;
    expect(response.status()).toBe(409);
    await page.reload();
    await expect(page.getByRole('button', { name: '重试任务', exact: true })).toBeVisible();
  }
  throw new Error('task retry remained stale after reloading the latest task version');
}

async function waitForFixtureStatus(page: Page, target: string, enabled: boolean) {
  await poll(
    async () => {
      const response = await page.request.get(`${manifest.fixtureURL}/control/status?target=${encodeURIComponent(target)}`);
      expect(response.ok()).toBeTruthy();
      return response.json() as Promise<{ enabled: boolean }>;
    },
    (value) => value.enabled === enabled,
  );
}

async function setQBPassword(page: Page, action: 'set' | 'clear') {
  await page.goto('/settings/services');
  const password = page.locator('[id="secret-密码"]');
  await expect(password).toBeVisible();
  if (action === 'clear') {
    await expect(password).toHaveValue('fixture-password');
  }
  await password.fill(action === 'set' ? 'fixture-password' : '');
  const saved = page.waitForResponse((response) => response.url().includes('/api/v1/config') && response.request().method() === 'PUT');
  await page.getByRole('button', { name: '保存外部服务配置' }).click();
  expect((await saved).ok()).toBeTruthy();
  await expect(page.getByText(action === 'set' ? '密码已配置' : '密码未配置', { exact: false })).toBeVisible();
}

async function createSearchAcquisition(page: Page, query: string) {
  await page.goto('/searches');
  await page.locator('#search-query').fill(query);
  await page.getByRole('button', { name: '搜索', exact: true }).click();
  await expect(page.getByText('[Fixture] Fixture Show - S01E01')).toBeVisible();
  await page.getByRole('button', { name: '选择' }).click();
  await selectFixtureSeries(page);
  await page.getByRole('button', { name: '创建获取并下载' }).click();
  await expect(page).toHaveURL(/\/acquisitions\/[0-9a-f-]+(?:\?.*)?$/);
  const acquisitionId = currentResourceID(page);
  const acquisition = await poll(
    () => json<Acquisition>(page.request, `/api/v1/acquisitions/${acquisitionId}`),
    (value) => Boolean(value.downloadId),
  );
  return { acquisitionId, downloadId: acquisition.downloadId! };
}

async function createMovieAcquisition(page: Page, query: string) {
  await page.goto('/searches');
  await page.locator('#search-query').fill(query);
  await page.getByRole('button', { name: '搜索', exact: true }).click();
  await expect(page.getByText('[Fixture] Fixture Show - S01E01')).toBeVisible();
  await page.getByRole('button', { name: '选择' }).click();
  await page.getByLabel('内容类型').selectOption('movie');
  await page.locator('#tmdb-movie-query').fill('Fixture Movie');
  await page.getByRole('button', { name: '查询', exact: true }).click();
  await page.getByRole('button', { name: /Fixture Movie/ }).click();
  await page.getByRole('button', { name: '创建获取并下载' }).click();
  await expect(page).toHaveURL(/\/acquisitions\/[0-9a-f-]+(?:\?.*)?$/);
  const acquisitionId = currentResourceID(page);
  const acquisition = await poll(
    () => json<Acquisition>(page.request, `/api/v1/acquisitions/${acquisitionId}`),
    (value) => value.mediaType === 'movie' && Boolean(value.downloadId),
  );
  return { acquisitionId, downloadId: acquisition.downloadId! };
}

async function createRSSSubscription(
  page: Page,
  name: string,
  cleanupSourceOnCompletion = false,
  seriesID = 100,
  seriesTitle = 'Fixture Show',
) {
  await page.goto('/rss');
  await page.getByRole('button', { name: '新建订阅' }).click();
  const feedUrl = `${manifest.fixtureURL}/rss.xml?run=${encodeURIComponent(name)}&series=${seriesID}`;
  await page.locator('#rss-url').fill(feedUrl);
  await page.getByRole('button', { name: /识别作品/ }).click();
  // 自动识别成功时直接出现 TMDb 候选；识别失败（如夹具故障）时回退到手动关键词搜索。
  const keyword = page.locator('#rss-tmdb-keyword');
  await expect(page.getByRole('button', { name: new RegExp(seriesTitle) }).or(keyword).first()).toBeVisible();
  if (await keyword.isVisible()) {
    await keyword.fill(seriesTitle);
    await page.getByRole('button', { name: '查询', exact: true }).click();
  }
  await page.getByRole('button', { name: new RegExp(seriesTitle) }).click();
  await page.locator('#rss-season').fill('1');
  if (cleanupSourceOnCompletion) {
    await page.getByRole('checkbox', { name: '最终集入库后，删除对应的 qBittorrent 种子和缓存文件' }).check();
  }
  const createResponse = page.waitForResponse((response) => response.url().includes('/api/v1/rss/subscriptions') && response.request().method() === 'POST');
  await page.getByRole('button', { name: '创建订阅' }).click();
  expect((await createResponse).ok()).toBeTruthy();
  await expect(page.getByRole('link', { name: seriesTitle }).first()).toBeVisible();
  const subscriptions = await json<RssSubscriptionPage>(page.request, '/api/v1/rss/subscriptions?limit=100');
  const subscriptionId = subscriptions.items.find((item) => item.feedUrl === feedUrl)?.id;
  expect(subscriptionId).toBeTruthy();
  return subscriptionId!;
}

async function mapAndMaterialize(page: Page, acquisitionId: string, downloadId: string) {
  const mappingBoundary = await poll(
    () => json<Download>(page.request, `/api/v1/downloads/${downloadId}`),
    (download) => download.status === 'failed' || download.status === 'materialized',
  );
  if (mappingBoundary.status === 'materialized') {
    return;
  }
  expect(mappingBoundary.failureStage).toBe('materialize');
  expect(mappingBoundary.errorCode).toBe('mapping_profile_required');
  await poll(
    () => page.request.get('/api/v1/tmdb/series/100/catalog'),
    (response) => response.ok(),
  );

  await page.goto(`/acquisitions/${acquisitionId}/mapping`);
  await expect(page.getByText('Season 1', { exact: false }).first()).toBeVisible();
  const mappingSaved = page.waitForResponse((response) => response.url().includes(`/api/v1/acquisitions/${acquisitionId}/episode-mapping`) && response.request().method() === 'PUT');
  await page.getByRole('button', { name: '映射到 S01E01：Pilot' }).click();
  expect((await mappingSaved).ok()).toBeTruthy();

  await expect(page).toHaveURL(new RegExp(`/acquisitions/${acquisitionId}(?:\\?.*)?$`));
  const download = await poll(
    () => json<Download>(page.request, `/api/v1/downloads/${downloadId}`),
    (value) => value.status === 'materialized',
  );
  expect(download.status).toBe('materialized');
}

async function requestEmbyRefreshAndUpdateCatalog(page: Page) {
  await page.goto('/emby');

  const refreshResponsePromise = page.waitForResponse((response) =>
    new URL(response.url()).pathname === '/api/v1/emby/refresh' && response.request().method() === 'POST');
  await page.getByRole('button', { name: '请求 Emby 扫描文件' }).click();
  const refreshResponse = await refreshResponsePromise;
  expect(refreshResponse.status()).toBe(202);
  const refreshAccepted = await refreshResponse.json() as CommandAccepted;
  await expect(page).toHaveURL(/\/emby$/);
  await poll(
    () => json<{ status: string }>(page.request, `/api/v1/operations/${refreshAccepted.operationId}`),
    (value) => value.status === 'succeeded',
  );
  await expect(page.getByText('Emby 已接受媒体文件扫描请求。')).toBeVisible({ timeout: 15_000 });
  await expect(page).toHaveURL(/\/emby$/);

  const scanResponsePromise = page.waitForResponse((response) =>
    new URL(response.url()).pathname === '/api/v1/emby/scans' && response.request().method() === 'POST');
  await page.getByRole('button', { name: '从 Emby 更新目录' }).click();
  const scanResponse = await scanResponsePromise;
  expect(scanResponse.status()).toBe(202);
  const scanAccepted = await scanResponse.json() as EmbyScanAccepted;
  await expect(page).toHaveURL(/\/emby$/);
  const completedScan = await poll(
    () => json<{ status: string; libraryCount: number; itemCount: number }>(page.request, `/api/v1/emby/scans/${scanAccepted.scan.id}`),
    (value) => value.status === 'succeeded',
  );
  await expect(page.getByText(`目录已更新：${completedScan.libraryCount} 个媒体库，${completedScan.itemCount} 个媒体条目。`)).toBeVisible({ timeout: 15_000 });
  await expect(page).toHaveURL(/\/emby$/);
}

async function reviewTask(page: Page, acquisitionId: string) {
  const acquisition = await poll(
    () => json<Acquisition>(page.request, `/api/v1/acquisitions/${acquisitionId}`),
    (value) => value.tasks.length > 0,
  );
  const taskId = acquisition.tasks[0].id;
  const task = await poll(
    () => json<Task>(page.request, `/api/v1/tasks/${taskId}`),
    (value) => value.state === 'awaiting_review' && Boolean(value.artifacts),
    90_000,
  );

  await page.goto(`/acquisitions/${acquisitionId}`);
  await expect(page.getByRole('button', { name: '审核通过并入库' })).toBeVisible();
  await page.getByRole('button', { name: '查看字幕' }).click();
  await expect(page.getByText('Fixture subtitle', { exact: false })).toBeVisible();

  const videoResponse = await page.request.get(`/api/v1/tasks/${taskId}/artifacts/${task.artifacts!.video.id}/content`, {
    headers: { Range: 'bytes=0-7' },
  });
  expect(videoResponse.status()).toBe(206);
  expect(videoResponse.headers()['content-range']).toMatch(/^bytes 0-7\//);
  expect((await videoResponse.body()).length).toBe(8);

  const reviewed = page.waitForResponse((response) => response.url().includes(`/api/v1/tasks/${taskId}/review`) && response.request().method() === 'POST');
  await page.getByRole('button', { name: '审核通过并入库' }).click();
  const reviewedTask = await (await reviewed).json() as Task;
  expect(reviewedTask.state).toBe('import_queued');
  const importOperation = reviewedTask.operations.find((operation) => operation.kind === 'emby.import');
  expect(importOperation).toBeTruthy();
  return { taskId, importOperationId: importOperation!.id };
}

async function reviewImportAndScan(page: Page, acquisitionId: string) {
  const { taskId } = await reviewTask(page, acquisitionId);
  await poll(
    () => json<Task>(page.request, `/api/v1/tasks/${taskId}`),
    (value) => value.state === 'imported' && value.cleanup?.status === 'completed',
    90_000,
  );

  await requestEmbyRefreshAndUpdateCatalog(page);
  await expect(page.getByRole('link', { name: 'Fixture Library' })).toBeVisible();
  await page.getByRole('link', { name: 'Fixture Library' }).click();
  await expect(page.getByText('Pilot', { exact: true })).toBeVisible();
  const pilotRow = page.getByRole('row').filter({ has: page.getByText('Pilot', { exact: true }) });
  await expect(pilotRow.getByRole('link', { name: '查看任务' })).toBeVisible();
}

test('@cross-browser real search pipeline reaches reviewed import and Emby catalog', async ({ page }, testInfo) => {
  await login(page);
  await configure(page);

  const { acquisitionId, downloadId } = await createSearchAcquisition(page, `Fixture Show ${testInfo.project.name}`);
  await expect(page.getByRole('heading', { name: 'Fixture Show' })).toBeVisible();
  await mapAndMaterialize(page, acquisitionId, downloadId);
  await reviewImportAndScan(page, acquisitionId);
});

test('@movie real movie pipeline reaches the movie library and Emby catalog', async ({ page }, testInfo) => {
  await login(page);
  await configure(page);

  const { acquisitionId, downloadId } = await createMovieAcquisition(page, `Fixture Movie ${testInfo.project.name}`);
  await expect(page.getByRole('heading', { name: 'Fixture Movie (2024)', level: 1 })).toBeVisible();
  const download = await poll(
    () => json<Download>(page.request, `/api/v1/downloads/${downloadId}`),
    (value) => value.status === 'materialized' || value.status === 'failed',
  );
  expect(download.status, JSON.stringify(download)).toBe('materialized');

  const acquisition = await poll(
    () => json<Acquisition>(page.request, `/api/v1/acquisitions/${acquisitionId}`),
    (value) => value.tasks.length === 1,
  );
  const taskId = acquisition.tasks[0].id;
  const task = await poll(
    () => json<Task>(page.request, `/api/v1/tasks/${taskId}`),
    (value) => value.state === 'awaiting_review' && Boolean(value.artifacts),
    90_000,
  );
  expect(task.mediaType).toBe('movie');
  expect(task.movieTitle).toBe('Fixture Movie');
  expect(task.releaseYear).toBe(2024);
  expect(task.artifacts?.basename).toBe('Fixture Movie(2024)');

  await page.goto(`/acquisitions/${acquisitionId}`);
  await expect(page.getByRole('heading', { name: 'Fixture Movie (2024)', level: 1 })).toBeVisible();
  await expect(page.getByText('电影', { exact: true }).first()).toBeVisible();
  await page.getByRole('button', { name: '查看字幕' }).click();
  await expect(page.getByText('Fixture subtitle', { exact: false })).toBeVisible();
  const reviewed = page.waitForResponse((response) => response.url().includes(`/api/v1/tasks/${taskId}/review`) && response.request().method() === 'POST');
  await page.getByRole('button', { name: '审核通过并入库' }).click();
  const reviewedTask = await (await reviewed).json() as Task;
  expect(reviewedTask.state).toBe('import_queued');
  await poll(
    () => json<Task>(page.request, `/api/v1/tasks/${taskId}`),
    (value) => value.state === 'imported' && value.cleanup?.status === 'completed',
    90_000,
  );

  const movieDirectory = path.join(manifest.paths.movieLibraryRoot, 'Fixture Movie(2024)');
  expect(fs.existsSync(path.join(movieDirectory, 'Fixture Movie(2024).mp4'))).toBeTruthy();
  expect(fs.existsSync(path.join(movieDirectory, 'Fixture Movie(2024).ass'))).toBeTruthy();
  expect(fs.existsSync(path.join(manifest.paths.animeLibraryRoot, 'Fixture Movie(2024)'))).toBeFalsy();

  await requestEmbyRefreshAndUpdateCatalog(page);
  await page.getByRole('link', { name: 'Fixture Movies' }).click();
  await expect(page.getByText('Fixture Movie', { exact: true })).toBeVisible();
});

test('@cross-browser real RSS pipeline reaches acquisition, task and Emby catalog', async ({ page }, testInfo) => {
  await login(page);
  await configure(page);

  const rssSeries = testInfo.project.name === 'firefox'
    ? { id: 103, title: 'Fixture RSS Firefox', episodeTitle: 'Firefox Premiere' }
    : testInfo.project.name === 'edge'
      ? { id: 104, title: 'Fixture RSS Edge', episodeTitle: 'Edge Premiere' }
      : { id: 102, title: 'Fixture RSS Chromium', episodeTitle: 'Chromium Premiere' };
  const subscriptionName = `Fixture RSS ${testInfo.project.name}`;
  const subscriptionId = await createRSSSubscription(page, subscriptionName, true, rssSeries.id, rssSeries.title);

  const entries = await poll(
    () => json<RssEntryPage>(page.request, `/api/v1/rss/subscriptions/${subscriptionId}/entries?limit=50`),
    (value) => value.items.some((item) => Boolean(item.acquisitionId && item.downloadId)),
  );
  const entry = entries.items.find((item) => item.acquisitionId && item.downloadId)!;
  const acquisitionId = entry.acquisitionId!;
  await mapAndMaterialize(page, acquisitionId, entry.downloadId!);
  const { taskId, importOperationId } = await reviewTask(page, acquisitionId);

  const importOperation = await poll(
    () => json<Operation>(page.request, `/api/v1/operations/${importOperationId}`),
    (value) => ['succeeded', 'failed', 'cancelled'].includes(value.status),
    90_000,
  );
  expect(importOperation.status).toBe('succeeded');
  await poll(
    () => json<Task>(page.request, `/api/v1/tasks/${taskId}`),
    (value) => value.state === 'imported',
    90_000,
  );
  await poll(
    () => json<RssSubscription>(page.request, `/api/v1/rss/subscriptions/${subscriptionId}`),
    (value) => !value.enabled && Boolean(value.completedAt),
    90_000,
  );
  await poll(
    () => json<Task>(page.request, `/api/v1/tasks/${taskId}`),
    (value) => value.cleanup?.status === 'completed',
    90_000,
  );
  const subscriptionOperations = await json<OperationPage>(page.request, `/api/v1/operations?limit=100&resourceType=rss_subscription&resourceId=${subscriptionId}`);
  expect(subscriptionOperations.items.find((operation) => operation.kind === 'rss.subscription.delete')).toBeUndefined();

  const [subscriptionResponse, acquisitionResponse, taskResponse] = await Promise.all([
    page.request.get(`/api/v1/rss/subscriptions/${subscriptionId}`),
    page.request.get(`/api/v1/acquisitions/${acquisitionId}`),
    page.request.get(`/api/v1/tasks/${taskId}`),
  ]);
  expect(subscriptionResponse.status()).toBe(200);
  expect(acquisitionResponse.status()).toBe(200);
  expect(taskResponse.status()).toBe(200);
  const retainedSubscription = await subscriptionResponse.json() as RssSubscription;
  expect(retainedSubscription.enabled).toBeFalsy();
  expect(retainedSubscription.completedAt).toBeTruthy();

  const animeDirectory = path.join(manifest.paths.animeLibraryRoot, rssSeries.title, 'Season1');
  expect(fs.existsSync(path.join(animeDirectory, `${rssSeries.title} - S01E01 - ${rssSeries.episodeTitle}.mp4`))).toBeTruthy();
  expect(fs.existsSync(path.join(animeDirectory, `${rssSeries.title} - S01E01 - ${rssSeries.episodeTitle}.ass`))).toBeTruthy();

  await requestEmbyRefreshAndUpdateCatalog(page);
  await page.getByRole('link', { name: 'Fixture Library' }).click();
  await expect(page.getByText(rssSeries.episodeTitle, { exact: true })).toBeVisible();
});

test('@recovery configuration connectivity is persisted and stale tabs conflict', async ({ page, context }) => {
  await login(page);
  await configure(page);
  await page.goto('/');
  const dependency = page.getByText('qBittorrent', { exact: true }).locator('xpath=..');
  await expect(dependency.getByText('可用', { exact: true })).toBeVisible();

  await page.goto('/settings/services');
  const stalePage = await context.newPage();
  await stalePage.route('**/api/v1/events**', (route) => route.abort());
  await stalePage.goto('/settings/services');
  await page.locator('#qb-username').fill('fixture-primary');
  const firstSave = page.waitForResponse((response) => response.url().includes('/api/v1/config') && response.request().method() === 'PUT');
  await page.getByRole('button', { name: '保存外部服务配置' }).click();
  expect((await firstSave).ok()).toBeTruthy();

  await stalePage.locator('#qb-username').fill('fixture-stale');
  const staleSave = stalePage.waitForResponse((response) => response.url().includes('/api/v1/config') && response.request().method() === 'PUT');
  await stalePage.getByRole('button', { name: '保存外部服务配置' }).click();
  expect((await staleSave).status()).toBe(409);
  await expect(stalePage.getByText('配置已被其他操作修改', { exact: false })).toBeVisible();
  await stalePage.close();

  await page.locator('#qb-username').fill('fixture');
  const restored = page.waitForResponse((response) => response.url().includes('/api/v1/config') && response.request().method() === 'PUT');
  await page.getByRole('button', { name: '保存外部服务配置' }).click();
  expect((await restored).ok()).toBeTruthy();
});

test('@recovery download enqueue failure can retry and active download can cancel', async ({ page }) => {
  await login(page);
  await configure(page);

  await setQBPassword(page, 'clear');
  const retryFlow = await createSearchAcquisition(page, 'Fixture download retry');
  await poll(
    () => json<Download>(page.request, `/api/v1/downloads/${retryFlow.downloadId}`),
    (value) => value.status === 'failed' && value.failureStage === 'enqueue',
  );
  await setQBPassword(page, 'set');
  await page.goto(`/downloads/${retryFlow.downloadId}`);
  await page.getByRole('button', { name: '重试下载', exact: true }).click();
  await expect(page).toHaveURL(/\/operations\/[0-9a-f-]+(?:\?.*)?$/);
  const retryOperationId = currentResourceID(page);
  await poll(
    () => json<Operation>(page.request, `/api/v1/operations/${retryOperationId}`),
    (value) => value.status === 'succeeded',
  );
  const recovered = await poll(
    () => json<Download>(page.request, `/api/v1/downloads/${retryFlow.downloadId}`),
    (value) => value.status === 'materialized' || (value.status === 'failed' && value.failureStage === 'materialize'),
  );
  if (recovered.status === 'failed') {
    expect(recovered.errorCode).toBe('mapping_profile_required');
  }

  await setFixtureControl(page, 'qbittorrent', true);
  try {
    const cancelFlow = await createSearchAcquisition(page, 'Fixture download cancel');
    await page.goto(`/downloads/${cancelFlow.downloadId}`);
    await page.getByRole('button', { name: '停止下载' }).click();
    await page.getByRole('button', { name: '确认停止' }).click();
    await poll(
      () => json<Download>(page.request, `/api/v1/downloads/${cancelFlow.downloadId}`),
      (value) => value.status === 'cancelled',
    );
  } finally {
    await setFixtureControl(page, 'qbittorrent', false);
  }
});

test('@recovery task media failure retries, stale review conflicts, and processing cancels', async ({ page, context }) => {
  test.setTimeout(180_000);
  await login(page);
  await configure(page);
  await setFixtureControl(page, 'media_invalid', true);
  try {
    const retryFlow = await createSearchAcquisition(page, 'Fixture task retry');
    await mapAndMaterialize(page, retryFlow.acquisitionId, retryFlow.downloadId);
    const retryAcquisition = await poll(
      () => json<Acquisition>(page.request, `/api/v1/acquisitions/${retryFlow.acquisitionId}`),
      (value) => value.tasks.length > 0,
    );
    const retryTaskId = retryAcquisition.tasks[0].id;
    await poll(
      () => json<Task>(page.request, `/api/v1/tasks/${retryTaskId}`),
      (value) => value.state === 'failed' && value.failureStage === 'video' && value.errorCode === 'transcode_output_invalid',
      90_000,
    );
    await setFixtureControl(page, 'media_invalid', false);
    await page.goto(`/tasks/${retryTaskId}`);
    await retryFailedTaskFromPage(page, retryTaskId);
    await poll(
      () => json<Task>(page.request, `/api/v1/tasks/${retryTaskId}`),
      (value) => value.state === 'awaiting_review',
      120_000,
    );

    await page.goto(`/tasks/${retryTaskId}`);
    const stalePage = await context.newPage();
    await stalePage.route('**/api/v1/events**', (route) => route.abort());
    await stalePage.goto(`/tasks/${retryTaskId}`);
    const approvedResponse = page.waitForResponse((response) => response.url().includes(`/api/v1/tasks/${retryTaskId}/review`) && response.request().method() === 'POST');
    await page.getByRole('button', { name: '审核通过并入库' }).click();
    expect(((await (await approvedResponse).json()) as Task).state).toBe('import_queued');
    await stalePage.getByRole('button', { name: '拒绝' }).click();
    const staleReview = stalePage.waitForResponse((response) => response.url().includes(`/api/v1/tasks/${retryTaskId}/review`) && response.request().method() === 'POST');
    await stalePage.getByRole('button', { name: '确认', exact: true }).click();
    expect((await staleReview).status()).toBe(409);
    await expect(stalePage.getByRole('alert').last()).toBeVisible();
    await stalePage.close();

    await setFixtureControl(page, 'media_slow', true);
    const cancelFlow = await createSearchAcquisition(page, 'Fixture task cancel');
    await mapAndMaterialize(page, cancelFlow.acquisitionId, cancelFlow.downloadId);
    const cancelAcquisition = await poll(
      () => json<Acquisition>(page.request, `/api/v1/acquisitions/${cancelFlow.acquisitionId}`),
      (value) => value.tasks.length > 0,
    );
    const cancelTaskId = cancelAcquisition.tasks[0].id;
    await poll(
      () => json<Task>(page.request, `/api/v1/tasks/${cancelTaskId}`),
      (value) => value.state === 'processing',
    );
    await page.goto(`/tasks/${cancelTaskId}`);
    await page.getByRole('button', { name: '取消任务' }).click();
    await page.getByRole('button', { name: '确认', exact: true }).click();
    await poll(
      () => json<Task>(page.request, `/api/v1/tasks/${cancelTaskId}`),
      (value) => value.state === 'cancelled',
    );
  } finally {
    await setFixtureControl(page, 'media_invalid', false);
    await setFixtureControl(page, 'media_slow', false);
  }
});

test('@recovery RSS stale edit reports a version conflict', async ({ page, context }) => {
  await login(page);
  await configure(page);
  const subscriptionId = await createRSSSubscription(page, 'Fixture RSS conflict');
  await page.goto(`/rss/${subscriptionId}`);
  const stalePage = await context.newPage();
  await stalePage.route('**/api/v1/events**', (route) => route.abort());
  await stalePage.goto(`/rss/${subscriptionId}`);
  await page.getByRole('button', { name: '编辑', exact: true }).click();
  await stalePage.getByRole('button', { name: '编辑', exact: true }).click();
  await page.locator('#edit-rss-name').fill('Fixture RSS primary');
  const firstSave = page.waitForResponse((response) => response.url().includes(`/api/v1/rss/subscriptions/${subscriptionId}`) && response.request().method() === 'PUT');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  expect((await firstSave).ok()).toBeTruthy();

  await stalePage.locator('#edit-rss-name').fill('Fixture RSS stale');
  const staleSave = stalePage.waitForResponse((response) => response.url().includes(`/api/v1/rss/subscriptions/${subscriptionId}`) && response.request().method() === 'PUT');
  await stalePage.getByRole('button', { name: '保存', exact: true }).click();
  expect((await staleSave).status()).toBe(409);
  await expect(stalePage.getByRole('alert').last()).toBeVisible();
  await stalePage.close();
});

test('@recovery RSS upstream failure is audited and succeeds on retry', async ({ page }) => {
  await login(page);
  await configure(page);
  await setFixtureControl(page, 'rss', true);
  try {
    const subscriptionId = await createRSSSubscription(page, 'Fixture RSS retry');
    await page.goto(`/rss/${subscriptionId}`);
    await page.getByRole('button', { name: '立即检查' }).click();
    await expect(page).toHaveURL(/\/operations\/[0-9a-f-]+(?:\?.*)?$/);
    const operationId = currentResourceID(page);
    const failedAttempt = await poll(
      () => json<Operation>(page.request, `/api/v1/operations/${operationId}`),
      (value) => value.attemptCount >= 1 && value.errorCode === 'rss_fetch_failed',
    );
    expect(failedAttempt.status).toBe('queued');
    await setFixtureControl(page, 'rss', false);
    await poll(
      () => json<Operation>(page.request, `/api/v1/operations/${operationId}`),
      (value) => value.status === 'succeeded',
      90_000,
    );
    const entries = await poll(
      () => json<RssEntryPage>(page.request, `/api/v1/rss/subscriptions/${subscriptionId}/entries?limit=50`),
      (value) => value.items.length > 0,
      90_000,
    );
    expect(entries.items.length).toBeGreaterThan(0);
  } finally {
    await setFixtureControl(page, 'rss', false);
  }
});

test('@recovery SSE replays events produced while the API restarts', async ({ page, context }) => {
  await login(page);
  await configure(page);
  await page.goto('/searches');
  const producer = await context.newPage();
  await producer.goto('/searches');
  await setFixtureControl(page, 'search_slow', true);
  const query = 'Fixture SSE restart';
  await producer.locator('#search-query').fill(query);
  const createSearchResponse = producer.waitForResponse((response) =>
    response.url().endsWith('/api/v1/searches') && response.request().method() === 'POST',
  );
  await producer.getByRole('button', { name: '搜索', exact: true }).click();
  const created = await (await createSearchResponse).json() as { search: { id: string } };
  const searchId = created.search.id;
  await expect(producer).toHaveURL(/\/searches(?:\?.*)?$/);
  await setFixtureControl(page, 'api_restart', true);
  await waitForFixtureStatus(page, 'api_restarted', true);
  await setFixtureControl(page, 'search_slow', false);
  await poll(
    () => json<{ status: string }>(page.request, `/api/v1/searches/${searchId}`),
    (value) => value.status === 'completed',
  );

  const recent = page.getByRole('region', { name: '最近搜索' });
  const row = recent.getByRole('listitem').filter({ hasText: query });
  await expect(row).toBeVisible({ timeout: 70_000 });
  await expect(row.getByText('已完成', { exact: true })).toBeVisible();
  await producer.close();
});

test('@recovery invalidated session clears protected UI and does not persist a write', async ({ page, context }) => {
  await login(page);
  await configure(page);
  await page.goto('/settings/services');
  const originalUsername = await page.locator('#qb-username').inputValue();
  await page.locator('#qb-username').fill('must-not-persist');
  await context.clearCookies();
  await page.getByRole('button', { name: '保存外部服务配置' }).click();
  await expect(page.getByRole('heading', { name: '管理员登录' })).toBeVisible();

  await page.getByLabel('用户名').fill(manifest.username);
  await page.getByLabel('密码').fill(manifest.password);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '外部服务' })).toBeVisible();
  await expect(page.locator('#qb-username')).toHaveValue(originalUsername);
});

test('@narrow narrow viewport keeps primary navigation and URL filters usable', async ({ page }) => {
  await login(page);
  await configure(page);
  await page.goto('/downloads?phase=failed');
  await expect(page).toHaveURL(/\/acquisitions$/);
  await expect(page.getByRole('heading', { name: '任务' })).toBeVisible();
  await page.getByRole('button', { name: '打开主导航' }).click();
  await expect(page.getByRole('navigation', { name: '主导航' }).getByRole('link', { name: '任务', exact: true })).toBeVisible();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});
