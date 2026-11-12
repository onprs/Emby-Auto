import { expect, test, type Page, type Route } from '@playwright/test';

import { stubAuthenticatedApp } from './fixtures';

const downloadId = '31000000-0000-0000-0000-000000000001';
const acquisitionId = '31000000-0000-0000-0000-000000000002';
const now = '2026-11-12T10:00:00Z';

function download(version: number, id = downloadId, acquisition = acquisitionId, attempt = 1) {
  return {
    id,
    acquisitionId: acquisition,
    attempt,
    clientName: 'qBittorrent',
    clientState: 'downloading',
    status: 'downloading',
    progress: 0.42,
    version,
    createdAt: now,
    updatedAt: now,
    files: [],
    actions: {
      canRetry: false,
      canCancel: true,
      canDelete: false,
      canEditFileSelection: false,
      canResolveFiles: false,
      canRequestAgent: false,
    },
  };
}

function json(route: Route, body: unknown, status = 200) {
  return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
}

function isDownloadsList(route: Route): boolean {
  const request = route.request();
  return request.method() === 'GET' && new URL(request.url()).pathname === '/api/v1/downloads';
}

const cursorFor = (index: number) => `00000000-0000-4000-8000-${String(index).padStart(12, '0')}`;

async function stubDownloadsList(page: Page, onList: () => void) {
  await page.route('**/api/v1/downloads**', async (route) => {
    if (!isDownloadsList(route)) {
      await route.fallback();
      return;
    }
    const cursor = new URL(route.request().url()).searchParams.get('cursor');
    const next = cursor ? Number(cursor.slice(-12)) + 1 : 1;
    await json(route, { items: [download(next)], nextCursor: cursorFor(next) });
    onList();
  });
}

test.describe('列表反馈与实时订阅', () => {
  test('危险操作失败后关闭确认框并让失败原因可读', async ({ page }) => {
    await stubAuthenticatedApp(page);
    await stubDownloadsList(page, () => undefined);
    let cancelCalls = 0;
    await page.route('**/api/v1/downloads/*/cancel', async (route) => {
      cancelCalls += 1;
      await json(
        route,
        { code: 'version_conflict', message: '下载已被其他操作修改，请刷新后重试', details: {}, requestId: 'e2e-cancel' },
        409,
      );
    });

    await page.goto('/downloads');
    await expect(page.getByRole('link', { name: '下载记录 1' }).first()).toBeVisible();

    await page.getByRole('button', { name: '更多操作' }).click();
    await page.getByRole('menuitem', { name: '停止' }).click();
    const dialog = page.getByRole('alertdialog', { name: '确认停止' });
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: '确认停止' }).click();

    // 失败后确认框关闭，原因显示在遮罩之上；重复确认不会再发出命令。
    const alert = page.getByRole('alert').filter({ hasText: '下载已被其他操作修改，请刷新后重试' });
    await expect(alert).toBeVisible();
    await expect(page.getByRole('alertdialog')).toHaveCount(0);
    expect(cancelCalls).toBe(1);
    expect(
      await alert.evaluate((node) => {
        const rect = node.getBoundingClientRect();
        const top = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
        return top !== null && node.contains(top);
      }),
    ).toBe(true);
  });

  test('两行失败提示同时可见、可分别关闭且不互相遮挡', async ({ page }) => {
    await stubAuthenticatedApp(page);
    await page.route('**/api/v1/downloads**', async (route) => {
      if (!isDownloadsList(route)) {
        await route.fallback();
        return;
      }
      await json(route, {
        items: [
          download(1, '41000000-0000-4000-8000-000000000001', '41000000-0000-4000-8000-000000000011', 1),
          download(2, '41000000-0000-4000-8000-000000000002', '41000000-0000-4000-8000-000000000012', 2),
        ],
      });
    });
    let cancelCalls = 0;
    await page.route('**/api/v1/downloads/*/cancel', async (route) => {
      cancelCalls += 1;
      await json(
        route,
        { code: 'version_conflict', message: `第 ${cancelCalls} 行失败原因`, details: {}, requestId: 'e2e-multi' },
        409,
      );
    });

    await page.goto('/downloads');
    await expect(page.getByRole('link', { name: '下载记录 1' }).first()).toBeVisible();
    await expect(page.getByRole('link', { name: '下载记录 2' }).first()).toBeVisible();

    for (let index = 0; index < 2; index += 1) {
      await page.getByRole('button', { name: '更多操作' }).nth(index).click();
      await page.getByRole('menuitem', { name: '停止' }).click();
      await page.getByRole('alertdialog').getByRole('button', { name: '确认停止' }).click();
      await expect(page.getByRole('alert')).toHaveCount(index + 1);
    }

    const alerts = page.getByRole('alert');
    await expect(alerts).toHaveCount(2);
    await expect(alerts.nth(0)).toHaveText(/第 1 行失败原因/);
    await expect(alerts.nth(1)).toHaveText(/第 2 行失败原因/);
    const visibility = await alerts.evaluateAll((nodes) => nodes.map((node) => {
      const rect = node.getBoundingClientRect();
      const top = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
      return !(top !== null && node.contains(top));
    }));
    expect(visibility).toEqual([false, false]);

    // 关闭第二条不会影响第一条，剩余提示仍在可视层。
    await alerts.nth(1).getByRole('button', { name: '关闭' }).click();
    await expect(alerts).toHaveCount(1);
    await expect(alerts.nth(0)).toHaveText(/第 1 行失败原因/);
    expect(
      await alerts.nth(0).evaluate((node) => {
        const rect = node.getBoundingClientRect();
        const top = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
        return top !== null && node.contains(top);
      }),
    ).toBe(true);
    expect(cancelCalls).toBe(2);
  });

  test('深分页截断后提供明确的第一页入口，不再静默跳页', async ({ page }, testInfo) => {
    test.skip(testInfo.project.name !== 'desktop', '分页交互由桌面项目覆盖');
    test.setTimeout(120_000);
    await stubAuthenticatedApp(page);
    await stubDownloadsList(page, () => undefined);

    await page.goto('/downloads');
    await expect(page.getByRole('button', { name: '下一页' })).toBeEnabled();

    for (let pageIndex = 1; pageIndex <= 115; pageIndex += 1) {
      await page.getByRole('button', { name: '下一页' }).click();
      await expect
        .poll(() => new URL(page.url()).searchParams.get('cursor'))
        .toBe(cursorFor(pageIndex));
      // 游标栈一旦被路由整段丢弃，“上一页”会永久禁用；深页必须保持可返回。
      await expect(page.getByRole('button', { name: '上一页' })).toBeEnabled();
    }

    const cursorStack = new URL(page.url()).searchParams.get('cursorStack') ?? '';
    expect(cursorStack.length).toBeLessThanOrEqual(4096);
    const initialEntries = cursorStack.split(',');
    const oldestRetained = initialEntries[1];
    await expect(page.getByRole('button', { name: '上一页' })).toBeEnabled();
    await page.getByRole('button', { name: '上一页' }).click();
    await expect
      .poll(() => new URL(page.url()).searchParams.get('cursor'))
      .toBe(cursorFor(114));

    // 继续回退到最老保留页：上一页禁用，历史截断提示与独立“回到第一页”入口可见。
    for (let step = 0; step < 60; step += 1) {
      if (!(await page.getByRole('button', { name: '上一页' }).isEnabled())) {
        break;
      }
      const before = new URL(page.url()).searchParams.get('cursor');
      await page.getByRole('button', { name: '上一页' }).click();
      await expect.poll(() => new URL(page.url()).searchParams.get('cursor')).not.toBe(before);
    }
    await expect(page.getByRole('button', { name: '上一页' })).toBeDisabled();
    await expect(page.getByText('更早的分页记录已省略')).toBeVisible();
    expect(new URL(page.url()).searchParams.get('cursor')).toBe(oldestRetained);
    const firstPage = page.getByRole('button', { name: '回到第一页' });
    await expect(firstPage).toBeEnabled();

    await firstPage.click();
    await expect.poll(() => new URL(page.url()).searchParams.get('cursor')).toBeNull();
    expect(new URL(page.url()).searchParams.get('cursorStack')).toBeNull();
    expect(new URL(page.url()).searchParams.get('cursorTruncated')).toBeNull();
    await expect(page.getByText('更早的分页记录已省略')).toHaveCount(0);
  });

  test('新会话先收到订阅边界，随后从该水位继续', async ({ page }) => {
    await stubAuthenticatedApp(page);
    const head = cursorFor(900);
    const eventHeaders: Array<string | null> = [];
    let listCalls = 0;
    let releaseBoundary!: () => void;
    const initialList = new Promise<void>((resolve) => {
      releaseBoundary = resolve;
    });
    await stubDownloadsList(page, () => {
      listCalls += 1;
      if (listCalls === 1) {
        releaseBoundary();
      }
    });
    await page.route('**/api/v1/events', async (route) => {
      const lastEventId = route.request().headers()['last-event-id'] ?? null;
      eventHeaders.push(lastEventId);
      if (lastEventId === null) {
        // 等初始化列表读取完成并落入查询缓存后再发边界，模拟“读取与订阅之间”的窗口。
        await initialList;
        await new Promise((resolve) => setTimeout(resolve, 300));
        await route.fulfill({
          status: 200,
          headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' },
          body: `id: ${head}\nevent: stream.started\ndata: {"cursor":"${head}"}\n\n`,
        });
        return;
      }
      await route.fulfill({
        status: 200,
        headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' },
        body: ': connected\n\n',
      });
    });

    await page.goto('/downloads');
    await expect(page.getByRole('link', { name: '下载记录 1' }).first()).toBeVisible();

    // 边界到达后补齐边界前的读取；重连必须携带边界水位而不是重放保留期历史。
    await expect.poll(() => listCalls, { timeout: 15_000 }).toBeGreaterThanOrEqual(2);
    await expect.poll(() => eventHeaders.includes(head), { timeout: 15_000 }).toBe(true);
    expect(eventHeaders.some((value) => value === null)).toBe(true);
    expect(eventHeaders.every((value) => value === null || value === head)).toBe(true);
  });
});
