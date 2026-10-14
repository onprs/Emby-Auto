import { expect, test, type Page } from '@playwright/test';

import type { RssFeedLookup, RssSubtitleGroup } from '../src/api/generated/types.gen';
import { stubAuthenticatedApp } from './fixtures';

const group: RssSubtitleGroup = {
  id: '62000000-0000-4000-8000-000000000001',
  name: 'ReleaseTeam',
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
};
const secondGroup: RssSubtitleGroup = { ...group, id: '62000000-0000-4000-8000-000000000002', name: 'TranslationTeam' };

function gate() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

function feedLookup(feedUrl: string, name: string): RssFeedLookup {
  return {
    feedUrl,
    feedTitle: '动画订阅',
    subtitleGroup: name,
    subtitleGroupCandidates: [name],
    suggestedQuery: '动画作品',
    suggestedQueries: ['动画作品'],
    sampleTitles: [],
    candidates: [{ tmdbSeriesId: 100, name: `${name} 动画` }],
    catalogMatchSource: 'deterministic',
  };
}

async function openSubscriptionForm(page: Page) {
  await stubAuthenticatedApp(page);
  await page.route('**/api/v1/rss/subscriptions**', (route) => route.fulfill({ json: { items: [] } }));
  await page.goto('/rss');
  await page.getByRole('button', { name: '新建订阅', exact: true }).click();
}

test('字幕组添加、重命名和无正文204删除可完成完整维护流程', async ({ page }) => {
  await stubAuthenticatedApp(page);
  let items = [group];
  await page.route('**/api/v1/rss/subtitle-groups**', async (route) => {
    const method = route.request().method();
    const id = new URL(route.request().url()).pathname.split('/').at(-1);
    if (method === 'POST') {
      const body = route.request().postDataJSON() as { name: string };
      const created = { ...secondGroup, name: body.name };
      items = [...items, created];
      await route.fulfill({ status: 201, json: created });
    } else if (method === 'PUT') {
      const body = route.request().postDataJSON() as { name: string };
      items = items.map((item) => item.id === id ? { ...item, name: body.name } : item);
      await route.fulfill({ json: items.find((item) => item.id === id) });
    } else if (method === 'DELETE') {
      items = items.filter((item) => item.id !== id);
      await route.fulfill({ status: 204 });
    } else {
      await route.fulfill({ json: { items } });
    }
  });
  await page.goto('/settings/subtitle-groups');
  await page.getByLabel('字幕组名称', { exact: true }).fill('TranslationTeam');
  await page.getByRole('button', { name: '添加字幕组' }).click();
  await expect(page.getByRole('button', { name: '编辑 TranslationTeam' })).toBeVisible();
  await expect(page.getByLabel('字幕组名称', { exact: true })).toHaveValue('');

  await page.getByRole('button', { name: '编辑 TranslationTeam' }).click();
  await page.getByLabel('字幕组名称', { exact: true }).fill('TranslationCollective');
  await page.getByRole('button', { name: '保存名称' }).click();
  await expect(page.getByRole('button', { name: '编辑 TranslationCollective' })).toBeVisible();
  await page.getByRole('button', { name: '编辑 TranslationCollective' }).click();
  const deletedResponse = page.waitForResponse((response) => response.request().method() === 'DELETE');
  await page.getByRole('button', { name: '删除 TranslationCollective' }).click();
  expect((await deletedResponse).status()).toBe(204);
  await expect(page.getByRole('button', { name: '编辑 TranslationCollective' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: '添加字幕组' })).toBeVisible();
  await expect(page.getByText('删除字幕组失败')).toHaveCount(0);
  await expect(page.getByLabel('字幕组名称', { exact: true })).toHaveValue('');
});

test('慢保存完成后保留另一条字幕组的编辑草稿', async ({ page }) => {
  await stubAuthenticatedApp(page);
  const saved = gate();
  const started = gate();
  await page.route('**/api/v1/rss/subtitle-groups**', async (route) => {
    if (route.request().method() === 'PUT') {
      started.resolve();
      await saved.promise;
      await route.fulfill({ json: group });
    } else {
      await route.fulfill({ json: { items: [group, secondGroup] } });
    }
  });
  await page.goto('/settings/subtitle-groups');
  await page.getByRole('button', { name: '编辑 ReleaseTeam' }).click();
  await page.getByRole('button', { name: '保存名称' }).click();
  await started.promise;
  await page.getByRole('button', { name: '编辑 TranslationTeam' }).click();
  await page.getByLabel('字幕组名称', { exact: true }).fill('UnsubmittedTranslationTeam');
  const refreshed = page.waitForResponse((response) => response.url().endsWith('/rss/subtitle-groups') && response.request().method() === 'GET');
  saved.resolve();
  await refreshed;

  await expect(page.getByLabel('字幕组名称', { exact: true })).toHaveValue('UnsubmittedTranslationTeam');
  await expect(page.getByRole('button', { name: '保存名称' })).toBeVisible();
});

test('RSS地址变更会更新自动名称并保留后续手动名称', async ({ page }) => {
  await page.route('**/api/v1/rss/feed-lookup', async (route) => {
    const { feedUrl } = route.request().postDataJSON() as { feedUrl: string };
    await route.fulfill({ json: feedLookup(feedUrl, feedUrl.endsWith('first.xml') ? 'ReleaseTeam' : 'TranslationTeam') });
  });
  await openSubscriptionForm(page);
  const address = page.getByLabel('RSS 地址', { exact: true });
  const name = page.getByLabel('订阅源名称（字幕组）', { exact: true });
  await address.fill('https://example.test/first.xml');
  await page.getByRole('button', { name: '识别作品' }).click();
  await expect(name).toHaveValue('ReleaseTeam');
  await address.fill('https://example.test/second.xml');
  await page.getByRole('button', { name: '识别作品' }).click();
  await expect(name).toHaveValue('TranslationTeam');
  await name.fill('手动确认版本');
  await address.fill('https://example.test/third.xml');
  await page.getByRole('button', { name: '识别作品' }).click();
  await expect(page.getByRole('button', { name: /TranslationTeam 动画/ })).toBeVisible();
  await expect(name).toHaveValue('手动确认版本');
});

test('旧地址迟到的识别结果不会覆盖新地址的名称和作品候选', async ({ page }) => {
  const oldResult = gate();
  const started = gate();
  await page.route('**/api/v1/rss/feed-lookup', async (route) => {
    const { feedUrl } = route.request().postDataJSON() as { feedUrl: string };
    if (feedUrl.endsWith('first.xml')) {
      started.resolve();
      await oldResult.promise;
      await route.fulfill({ json: feedLookup(feedUrl, 'OldReleaseTeam') });
    } else {
      await route.fulfill({ json: feedLookup(feedUrl, 'CurrentReleaseTeam') });
    }
  });
  await openSubscriptionForm(page);
  const address = page.getByLabel('RSS 地址', { exact: true });
  await address.fill('https://example.test/first.xml');
  await address.press('Tab');
  await started.promise;
  await address.fill('https://example.test/second.xml');
  await address.press('Tab');
  await expect(page.getByLabel('订阅源名称（字幕组）', { exact: true })).toHaveValue('CurrentReleaseTeam');
  const completed = page.waitForResponse((response) => response.url().endsWith('/rss/feed-lookup') && (response.request().postDataJSON() as { feedUrl: string }).feedUrl.endsWith('first.xml'));
  oldResult.resolve();
  await completed;

  await expect(page.getByLabel('订阅源名称（字幕组）', { exact: true })).toHaveValue('CurrentReleaseTeam');
  await expect(page.getByRole('button', { name: /CurrentReleaseTeam 动画/ })).toBeVisible();
  await expect(page.getByRole('button', { name: /OldReleaseTeam 动画/ })).toHaveCount(0);
});
