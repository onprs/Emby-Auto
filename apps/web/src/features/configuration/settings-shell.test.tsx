import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router';
import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { SettingsTabs, settingsSections } from '@/features/configuration/settings-shell';

const sectionLabels = ['外部服务', 'Agent', '存储与工具', '字幕组', '转码'];

async function renderSettingsTabs(pathname: string) {
  const rootRoute = createRootRoute({ component: Outlet });
  const routes = settingsSections.map((section) =>
    createRoute({ getParentRoute: () => rootRoute, path: section.to, component: SettingsTabs }),
  );
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: [pathname] }),
  });
  render(<RouterProvider router={router} />);
  return screen.findByRole('navigation', { name: '系统设置' });
}

describe('settings section navigation', () => {
  it('lists every settings section in one navigation row', async () => {
    const navigation = await renderSettingsTabs('/settings/services');

    expect(within(navigation).getAllByRole('link').map((link) => link.textContent)).toEqual(sectionLabels);
    expect(within(navigation).queryByRole('link', { name: '总览' })).not.toBeInTheDocument();
    expect(within(navigation).getByRole('link', { name: '外部服务' })).toHaveAttribute('href', '/settings/services');
    expect(within(navigation).getByRole('link', { name: '转码' })).toHaveAttribute('href', '/settings/transcode');
  });

  it('marks only the active section as the current page', async () => {
    const navigation = await renderSettingsTabs('/settings/storage');

    expect(within(navigation).getByRole('link', { name: '存储与工具' })).toHaveAttribute('aria-current', 'page');
    for (const label of sectionLabels.filter((value) => value !== '存储与工具')) {
      expect(within(navigation).getByRole('link', { name: label })).not.toHaveAttribute('aria-current');
    }
  });
});
