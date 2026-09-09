import { expect, test } from '@playwright/test';

import { stubAuthenticatedApp } from './fixtures';

const setup = {
  state: 'completed', databaseConfigured: true, databaseManagedExternally: true, administratorConfigured: true,
};

test('启动请求并行发送，初始化完成后才展示仪表盘', async ({ page }) => {
  await stubAuthenticatedApp(page);
  let releaseSetup!: () => void;
  const gate = new Promise<void>((resolve) => { releaseSetup = resolve; });
  await page.route('**/api/v1/setup/status', async (route) => {
    await gate;
    await route.fulfill({ json: setup });
  });
  const sessionRead = page.waitForRequest('**/api/v1/auth/session');
  await page.goto('/');
  await sessionRead;
  await expect(page.getByText('正在读取系统状态')).toBeVisible();
  await expect(page.getByRole('heading', { name: '仪表盘' })).toHaveCount(0);
  releaseSetup();
  await expect(page.getByRole('heading', { name: '仪表盘' })).toBeVisible();
});

test('连接停滞后显示超时，重试可以恢复页面', async ({ page }) => {
  test.setTimeout(35_000);
  await stubAuthenticatedApp(page);
  let recovered = false;
  let reads = 0;
  await page.route('**/api/v1/setup/status', async (route) => {
    reads += 1;
    if (recovered) await route.fulfill({ json: setup });
  });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: '连接失败' })).toBeVisible({ timeout: 25_000 });
  await expect(page.getByText('读取超时，请检查网络后重试')).toBeVisible();
  expect(reads).toBe(1);
  recovered = true;
  await page.getByRole('button', { name: '重试', exact: true }).click();
  await expect(page.getByRole('heading', { name: '仪表盘' })).toBeVisible();
  expect(reads).toBe(2);
});
