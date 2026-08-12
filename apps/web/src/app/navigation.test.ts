import { describe, expect, it } from 'vitest';

import {
  appNavigation,
  breadcrumbsForLocation,
  defaultReturnForPathname,
  moduleForPathname,
  normalizeInternalLocation,
  routeForPathname,
} from '@/app/navigation';

describe('application navigation manifest', () => {
  it('contains every implemented top-level module in one stable order', () => {
    expect(appNavigation.map((item) => [item.id, item.label, item.to])).toEqual([
      ['dashboard', '仪表盘', '/'],
      ['searches', '搜索添加', '/searches'],
      ['rss', 'RSS 订阅', '/rss'],
      ['tasks', '任务', '/acquisitions'],
      ['emby', '媒体库', '/emby'],
      ['operations', '运行记录', '/operations'],
      ['settings', '系统设置', '/settings'],
    ]);
    expect(appNavigation.find((item) => item.id === 'tasks')?.children).toBeUndefined();
    expect(appNavigation.find((item) => item.id === 'operations')?.children).toEqual([
      { label: '任务执行', to: '/operations' },
    ]);
  });

  it.each([
    ['/', 'dashboard'],
    ['/acquisitions', 'tasks'],
    ['/acquisitions/11111111-1111-4111-8111-111111111111/mapping', 'tasks'],
    ['/tasks', 'tasks'],
    ['/tasks/11111111-1111-4111-8111-111111111111', 'tasks'],
    ['/downloads/11111111-1111-4111-8111-111111111111', 'tasks'],
    ['/rss/11111111-1111-4111-8111-111111111111', 'rss'],
    ['/emby/scans/11111111-1111-4111-8111-111111111111', 'emby'],
    ['/operations/11111111-1111-4111-8111-111111111111', 'operations'],
  ])('maps %s to the %s module', (pathname, moduleId) => {
    expect(moduleForPathname(pathname)).toBe(moduleId);
  });

  it('defines a reliable list fallback for every detail route', () => {
    expect(defaultReturnForPathname('/searches/id')).toBe('/searches');
    expect(defaultReturnForPathname('/rss/id')).toBe('/rss');
    expect(defaultReturnForPathname('/tasks/id')).toBe('/acquisitions');
    expect(defaultReturnForPathname('/downloads/id')).toBe('/acquisitions');
    expect(defaultReturnForPathname('/emby/scans/id')).toBe('/emby');
    expect(defaultReturnForPathname('/operations/id')).toBe('/operations');
    expect(defaultReturnForPathname('/acquisitions/id/mapping')).toBe('/acquisitions/id');
  });

  it('only accepts known same-origin application locations', () => {
    const origin = 'http://app.test';
    expect(normalizeInternalLocation('/downloads?phase=paused&cursor=11111111-1111-4111-8111-111111111111', origin))
      .toBe('/downloads?phase=paused&cursor=11111111-1111-4111-8111-111111111111');
    expect(normalizeInternalLocation('http://app.test/tasks/id?state=failed', origin)).toBe('/tasks/id?state=failed');
    expect(normalizeInternalLocation('https://evil.test/tasks/id', origin)).toBeUndefined();
    expect(normalizeInternalLocation('//evil.test/tasks/id', origin)).toBeUndefined();
    expect(normalizeInternalLocation('/not-a-route', origin)).toBeUndefined();
    expect(normalizeInternalLocation('/agent/resolutions', origin)).toBeUndefined();
    expect(normalizeInternalLocation('javascript:alert(1)', origin)).toBeUndefined();
  });

  it('keeps the unified task source in diagnostic breadcrumbs', () => {
    expect(breadcrumbsForLocation('/downloads/download-id', '/acquisitions?phase=attention')).toEqual([
      { label: '任务', to: '/acquisitions?phase=attention' },
      { label: '下载诊断' },
    ]);
  });

  it('builds real cross-module breadcrumbs from the immediate source', () => {
    expect(breadcrumbsForLocation(
      '/downloads/22222222-2222-4222-8222-222222222222',
      '/tasks/11111111-1111-4111-8111-111111111111?from=%2Ftasks',
    )).toEqual([
      { label: '任务', to: '/acquisitions' },
      { label: '媒体处理诊断', to: '/tasks/11111111-1111-4111-8111-111111111111?from=%2Ftasks' },
      { label: '下载诊断' },
    ]);
  });

  it('uses the owning module breadcrumb when a detail is opened directly', () => {
    expect(breadcrumbsForLocation('/downloads/id')).toEqual([
      { label: '任务', to: '/acquisitions' },
      { label: '下载诊断' },
    ]);
    expect(breadcrumbsForLocation('/acquisitions/id/mapping')).toEqual([
      { label: '任务', to: '/acquisitions' },
      { label: '内容任务详情', to: '/acquisitions/id' },
      { label: '剧集对应关系' },
    ]);
    expect(routeForPathname('/missing')).toBeUndefined();
  });
});
