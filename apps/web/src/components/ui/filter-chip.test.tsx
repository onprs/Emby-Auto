import { render, screen, within } from '@testing-library/react';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { describe, expect, it } from 'vitest';

import { FilterChip, FilterChipGroup } from '@/components/ui/filter-chip';

function StatusFilters() {
  return (
    <FilterChipGroup label="状态筛选" activeKey="running">
      <FilterChip to="/items" search={{ state: 'running' }} active label="处理中" />
      <FilterChip to="/items" search={{ state: 'failed' }} active={false} label="失败" />
    </FilterChipGroup>
  );
}

function renderFilters() {
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/items', component: StatusFilters });
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute]),
    history: createMemoryHistory({ initialEntries: ['/items'] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

describe('FilterChip', () => {
  it('exposes the selected filter through aria-current instead of aria-pressed', async () => {
    renderFilters();
    const group = await screen.findByRole('group', { name: '状态筛选' });

    const active = within(group).getByRole('link', { name: '处理中' });
    // link 角色不支持 aria-pressed；选中态必须用 aria-current 表达。
    expect(active).toHaveAttribute('aria-current', 'true');
    expect(active).not.toHaveAttribute('aria-pressed');

    const inactive = within(group).getByRole('link', { name: '失败' });
    expect(inactive).not.toHaveAttribute('aria-current');
    expect(inactive).not.toHaveAttribute('aria-pressed');
  });
});
