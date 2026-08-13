import { expect, test, type Page, type Route } from '@playwright/test';

import { stubAuthenticatedApp } from './fixtures';

const id = '71000000-0000-4000-8000-000000000001';

const configuration = {
  version: 1,
  qBittorrent: { url: '', username: '', password: { configured: false, masked: '' } },
  emby: { url: '', apiKey: { configured: false, masked: '' } },
  tmdb: { apiToken: { configured: false, masked: '' } },
  networkProxy: { enabled: false, url: '' },
  paths: { downloadRoot: '', workRoot: '', stagingRoot: '', animeLibraryRoot: '', movieLibraryRoot: '', ffmpegPath: '', ffprobePath: '' },
  transcode: {
    name: 'default', videoCodec: 'h264', encoder: 'libx264', container: 'matroska', fileExtension: 'mkv',
    qualityMode: 'crf', qualityValue: 20, audioPolicy: 'copy', preset: 'medium', pixelFormat: 'yuv420p',
    threadCount: 0, maxConcurrency: 1,
  },
};

function json(route: Route, body: unknown, status = 200) {
  return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
}

async function stubNavigationApi(page: Page) {
  await stubAuthenticatedApp(page);
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    if (pathname === '/api/v1/setup/status' || pathname === '/api/v1/auth/session' || pathname === '/api/v1/dashboard/summary' || pathname === '/api/v1/dashboard/system-metrics' || pathname === '/api/v1/events') {
      await route.fallback();
      return;
    }
    if (pathname === '/api/v1/config') {
      await json(route, configuration);
      return;
    }
    if (
      pathname === '/api/v1/searches' ||
      pathname === '/api/v1/rss/subscriptions' ||
      pathname === '/api/v1/acquisitions' ||
      pathname === '/api/v1/tasks' ||
      pathname === '/api/v1/downloads' ||
      pathname === '/api/v1/emby/scans' ||
      pathname === '/api/v1/emby/libraries' ||
      pathname === '/api/v1/operations'
    ) {
      await json(route, { items: [] });
      return;
    }
    if (pathname.includes('/items')) {
      await json(route, { items: [] });
      return;
    }
    await json(route, { code: 'not_found', message: 'resource not found', requestId: 'navigation-e2e' }, 404);
  });
}

async function openMainNavigation(page: Page, mobile: boolean) {
  if (mobile) {
    await page.getByRole('button', { name: '打开主导航' }).click();
  }
  return page.getByRole('navigation', { name: '主导航' });
}

test('uses one route-aware shell and accessible mobile or desktop navigation', async ({ page }, testInfo) => {
  await stubNavigationApi(page);
  const mobile = testInfo.project.name === 'mobile';
  await page.goto('/');
  await expect(page.getByRole('heading', { name: '仪表盘' })).toBeVisible();

  const navigation = await openMainNavigation(page, mobile);
  const expected = ['仪表盘', '搜索添加', 'RSS 订阅', '任务', '媒体库', '运行记录', '系统设置'];
  for (const label of expected) {
    await expect(navigation.getByRole('link', { name: label, exact: true })).toBeVisible();
  }
  await expect(navigation.getByRole('link', { name: '下载', exact: true })).toHaveCount(0);
  await expect(navigation.getByRole('link', { name: '仪表盘', exact: true })).toHaveAttribute('aria-current', 'page');

  if (mobile) {
    await expect(page.getByRole('button', { name: '关闭主导航', exact: true })).toBeFocused();
    await page.keyboard.press('Shift+Tab');
    await expect(navigation.getByRole('link', { name: '系统设置', exact: true })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog', { name: '主导航抽屉' })).toBeHidden();
    await expect(page.getByRole('button', { name: '打开主导航' })).toBeFocused();

    await page.getByRole('button', { name: '打开主导航' }).click();
    await navigation.getByRole('link', { name: '任务', exact: true }).click();
    await expect(page).toHaveURL(/\/acquisitions$/);
    await expect(page.getByRole('dialog', { name: '主导航抽屉' })).toBeHidden();
  } else {
    await page.getByRole('button', { name: '收起侧边栏' }).click();
    await expect(page.getByRole('button', { name: '展开侧边栏' })).toBeVisible();
    await page.reload();
    await expect(page.getByRole('button', { name: '展开侧边栏' })).toBeVisible();
  }
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test('no longer renders the removed single-child submenu while the operations module is active', async ({ page }, testInfo) => {
  await stubNavigationApi(page);
  const mobile = testInfo.project.name === 'mobile';
  await page.goto('/operations');
  await expect(page.getByRole('heading', { name: '运行记录' })).toBeVisible();

  const navigation = await openMainNavigation(page, mobile);
  await expect(navigation.getByRole('link', { name: '运行记录', exact: true })).toHaveAttribute('aria-current', 'page');
  await expect(navigation.getByRole('link', { name: '任务执行', exact: true })).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test('keeps module highlighting and direct-detail recovery correct for every detail route', async ({ page }, testInfo) => {
  await stubNavigationApi(page);
  const mobile = testInfo.project.name === 'mobile';
  const cases = [
    { path: `/searches/${id}`, module: '搜索添加', fallback: '/searches' },
    { path: `/rss/${id}`, module: 'RSS 订阅', fallback: '/rss' },
    { path: `/acquisitions/${id}`, module: '任务', fallback: '/acquisitions' },
    { path: `/acquisitions/${id}/mapping`, module: '任务', fallback: `/acquisitions/${id}` },
    { path: `/tasks/${id}`, module: '任务', fallback: '/acquisitions' },
    { path: `/downloads/${id}`, module: '任务', fallback: '/acquisitions' },
    { path: `/emby/scans/${id}`, module: '媒体库', fallback: '/emby' },
    { path: `/emby/libraries/${id}`, module: '媒体库', fallback: '/emby' },
    { path: `/operations/${id}`, module: '运行记录', fallback: '/operations' },
  ];

  for (const item of cases) {
    await page.goto(item.path);
    await expect(page.getByRole('button', { name: '返回' })).toBeVisible();
    const navigation = await openMainNavigation(page, mobile);
    await expect(navigation.getByRole('link', { name: item.module, exact: true })).toHaveAttribute('aria-current', 'page');
    if (mobile) {
      await page.getByRole('button', { name: '关闭主导航', exact: true }).click();
    }
    await page.getByRole('button', { name: '返回' }).click();
    await expect(page).toHaveURL(item.fallback);
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  }
});

test('redirects legacy download and media-processing lists into the unified task list', async ({ page }) => {
  await stubNavigationApi(page);

  await page.goto('/downloads?phase=failed');
  await expect(page).toHaveURL(/\/acquisitions$/);
  await expect(page.getByRole('heading', { name: '任务' })).toBeVisible();

  await page.goto('/tasks?state=failed');
  await expect(page).toHaveURL(/\/acquisitions$/);
  await expect(page.getByRole('heading', { name: '任务' })).toBeVisible();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test('offers stable recovery from unknown and forbidden routes', async ({ page }) => {
  await stubNavigationApi(page);
  const source = '/acquisitions?phase=attention';
  await page.goto(source);
  await expect(page.getByRole('heading', { name: '任务' })).toBeVisible();
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('emby-auto:navigation:last-valid'))).toBe(source);

  await page.goto('/this-route-does-not-exist');
  await expect(page.getByRole('heading', { name: '页面不存在' })).toBeVisible();
  await expect(page.getByRole('link', { name: '返回上一有效页面' })).toHaveAttribute('href', source);
  await page.getByRole('link', { name: '返回上一有效页面' }).click();
  await expect(page).toHaveURL(source);

  await page.goto('/forbidden');
  await expect(page.getByRole('heading', { name: '无权访问' })).toBeVisible();
  await expect(page.getByRole('link', { name: '返回上一有效页面' })).toHaveAttribute('href', source);
  await expect(page.getByRole('link', { name: '返回仪表盘' })).toBeVisible();
});
