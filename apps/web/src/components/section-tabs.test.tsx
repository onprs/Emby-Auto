import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { SectionTabs, sectionIdForPathname, type SectionNavigationDefinition } from '@/components/section-tabs';
import { settingsNavigation } from '@/features/configuration/settings-shell';
import { taskNavigation } from '@/features/tasks/tasks-layout';

async function renderTabs(
  navigation: SectionNavigationDefinition,
  activeId: string,
  pathname: string,
) {
  const rootRoute = createRootRoute({
    component: () => (
      <>
        <SectionTabs {...navigation} activeId={activeId} />
        <Outlet />
      </>
    ),
  });
  const routes = navigation.sections.map((section) =>
    createRoute({ getParentRoute: () => rootRoute, path: section.to, component: () => null }),
  );
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: [pathname] }),
  });
  render(<RouterProvider router={router} />);
  return screen.findByRole('navigation', { name: navigation.ariaLabel });
}

describe('module section navigation', () => {
  it('resolves the active section from list and detail paths', () => {
    expect(sectionIdForPathname(taskNavigation.sections, '/tasks')).toBe('tasks');
    expect(sectionIdForPathname(taskNavigation.sections, '/tasks/11111111-1111-4111-8111-111111111111')).toBe('tasks');
    expect(sectionIdForPathname(taskNavigation.sections, '/acquisitions/11111111-1111-4111-8111-111111111111/mapping')).toBe('acquisitions');
    expect(sectionIdForPathname(taskNavigation.sections, '/downloads/11111111-1111-4111-8111-111111111111')).toBe('downloads');
    expect(sectionIdForPathname(settingsNavigation.sections, '/settings/subtitle-groups')).toBe('subtitle-groups');
    expect(sectionIdForPathname(settingsNavigation.sections, '/not-a-section')).toBeUndefined();
  });

  it('renders the task sections in one header navigation without sidebar submenus', async () => {
    const navigation = await renderTabs(taskNavigation, 'acquisitions', '/acquisitions');

    expect(within(navigation).getAllByRole('link').map((link) => link.textContent)).toEqual([
      '视频任务',
      '获取记录',
      '下载',
    ]);
    expect(within(navigation).getByRole('link', { name: '获取记录' })).toHaveAttribute('aria-current', 'page');
    expect(within(navigation).getByRole('link', { name: '获取记录' })).toHaveAttribute('data-sliding-active', 'true');
    expect(within(navigation).getByRole('link', { name: '下载' })).not.toHaveAttribute('aria-current');
  });

  it('resets list filters when switching task sections', async () => {
    const navigation = await renderTabs(taskNavigation, 'tasks', '/tasks?state=failed');

    expect(within(navigation).getByRole('link', { name: '获取记录' })).toHaveAttribute('href', '/acquisitions');
    expect(within(navigation).getByRole('link', { name: '下载' })).toHaveAttribute('href', '/downloads');
  });

  it('renders every settings section and the sliding indicator', async () => {
    const navigation = await renderTabs(settingsNavigation, 'storage', '/settings/storage');

    expect(within(navigation).getAllByRole('link').map((link) => link.textContent)).toEqual([
      '外部服务',
      'Agent',
      '存储与工具',
      '字幕组',
      '转码',
    ]);
    expect(within(navigation).queryByRole('link', { name: '总览' })).not.toBeInTheDocument();
    expect(within(navigation).getByRole('link', { name: '存储与工具' })).toHaveAttribute('aria-current', 'page');
    expect(navigation.querySelector('[data-sliding-indicator]')).not.toBeNull();
    await waitFor(() => {
      expect((navigation.querySelector('[data-sliding-indicator]') as HTMLElement).style.transition)
        .toContain('transform');
    });
  });
});
