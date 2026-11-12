import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { useState } from 'react';
import { describe, expect, it } from 'vitest';

import { useCursorPagination } from '@/lib/pagination';

function PaginationHarness() {
  const pagination = useCursorPagination();
  return (
    <div>
      <output data-testid="cursor">{pagination.cursor ?? 'root'}</output>
      <output data-testid="page">{pagination.pageIndex}</output>
      <button type="button" onClick={() => pagination.goNext('cursor-2')}>next-2</button>
      <button type="button" onClick={() => pagination.goNext('cursor-3')}>next-3</button>
      <button type="button" onClick={pagination.goPrevious}>previous</button>
      <button type="button" onClick={pagination.reset}>reset</button>
      <button type="button" onClick={() => pagination.goNext(null)}>empty</button>
    </div>
  );
}

function renderPagination(initialEntry = '/items?status=running') {
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/items', component: PaginationHarness });
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

const cursorFor = (index: number) => `00000000-0000-4000-8000-${String(index).padStart(12, '0')}`;

function DeepPaginationHarness() {
  const pagination = useCursorPagination();
  const [index, setIndex] = useState(1);
  return (
    <div>
      <output data-testid="cursor">{pagination.cursor ?? 'root'}</output>
      <output data-testid="can-back">{String(pagination.canGoBack)}</output>
      <output data-testid="can-first">{String(pagination.canGoFirst)}</output>
      <output data-testid="truncated">{String(pagination.historyTruncated)}</output>
      <button
        type="button"
        onClick={() => {
          setIndex((current) => current + 1);
          pagination.goNext(cursorFor(index + 1));
        }}
      >
        next
      </button>
      <button type="button" onClick={pagination.goPrevious}>previous</button>
      <button type="button" onClick={pagination.reset}>first</button>
    </div>
  );
}

function renderDeepPagination() {
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/items', component: DeepPaginationHarness });
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute]),
    history: createMemoryHistory({ initialEntries: ['/items'] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

describe('useCursorPagination', () => {
  it('persists forward and backward cursor state in the URL', async () => {
    const router = renderPagination();
    expect(await screen.findByTestId('cursor')).toHaveTextContent('root');

    fireEvent.click(screen.getByText('next-2'));
    await waitFor(() => expect(screen.getByTestId('cursor')).toHaveTextContent('cursor-2'));
    expect(router.state.location.search).toMatchObject({ status: 'running', cursor: 'cursor-2', cursorStack: '_' });

    fireEvent.click(screen.getByText('next-3'));
    await waitFor(() => expect(screen.getByTestId('cursor')).toHaveTextContent('cursor-3'));
    expect(router.state.location.search).toMatchObject({ cursor: 'cursor-3', cursorStack: '_,cursor-2' });

    fireEvent.click(screen.getByText('previous'));
    await waitFor(() => expect(screen.getByTestId('cursor')).toHaveTextContent('cursor-2'));
    fireEvent.click(screen.getByText('previous'));
    await waitFor(() => expect(screen.getByTestId('cursor')).toHaveTextContent('root'));
    expect(router.state.location.search).toEqual({ status: 'running' });
  });

  it('ignores empty next cursors', async () => {
    const router = renderPagination();
    await screen.findByTestId('cursor');
    fireEvent.click(screen.getByText('empty'));
    expect(router.state.location.search).toEqual({ status: 'running' });
  });

  it('resets pagination while preserving filters', async () => {
    const router = renderPagination('/items?status=failed&cursor=cursor-2&cursorStack=_');
    expect(await screen.findByTestId('cursor')).toHaveTextContent('cursor-2');
    fireEvent.click(screen.getByText('reset'));
    await waitFor(() => expect(screen.getByTestId('cursor')).toHaveTextContent('root'));
    expect(router.state.location.search).toEqual({ status: 'failed' });
  });

  it('marks truncated history and keeps the oldest retained page behind an explicit first-page action', async () => {
    const router = renderDeepPagination();
    expect(await screen.findByTestId('cursor')).toHaveTextContent('root');

    for (let page = 1; page <= 60; page += 1) {
      fireEvent.click(screen.getByText('next'));
      await waitFor(() => expect(screen.getByTestId('cursor')).toHaveTextContent(cursorFor(page + 1)));
    }

    const cursorStack = new URL(router.state.location.href, 'http://localhost').searchParams.get('cursorStack') ?? '';
    const entries = cursorStack.split(',');
    // 路由只接受不超过 4096 字符的游标栈；截断必须显式标记，不能依赖整段丢弃。
    expect(cursorStack.length).toBeLessThanOrEqual(4096);
    expect(entries[0]).toBe('_');
    expect(entries).toHaveLength(50);
    expect(new URL(router.state.location.href, 'http://localhost').searchParams.get('cursorTruncated')).toBe('truncated');
    expect(screen.getByTestId('truncated')).toHaveTextContent('true');
    expect(screen.getByTestId('can-back')).toHaveTextContent('true');

    const oldestRetained = entries[1];
    let previousSteps = 0;
    while (screen.getByTestId('can-back').textContent === 'true' && previousSteps < 60) {
      const before = screen.getByTestId('cursor').textContent;
      fireEvent.click(screen.getByText('previous'));
      previousSteps += 1;
      await waitFor(() => expect(screen.getByTestId('cursor').textContent).not.toBe(before));
    }

    // 最老可返回处：上一页禁用，不能无提示跨段跳到第一页；历史截断状态保持可见。
    expect(screen.getByTestId('can-back')).toHaveTextContent('false');
    expect(screen.getByTestId('truncated')).toHaveTextContent('true');
    expect(screen.getByTestId('cursor')).toHaveTextContent(oldestRetained);
    expect(screen.getByTestId('can-first')).toHaveTextContent('true');

    const beforeBoundaryClick = screen.getByTestId('cursor').textContent;
    fireEvent.click(screen.getByText('previous'));
    await waitFor(() => expect(screen.getByTestId('cursor').textContent).toBe(beforeBoundaryClick));

    fireEvent.click(screen.getByText('first'));
    await waitFor(() => expect(screen.getByTestId('cursor')).toHaveTextContent('root'));
    expect(screen.getByTestId('truncated')).toHaveTextContent('false');
    expect(screen.getByTestId('can-first')).toHaveTextContent('false');
    expect(screen.getByTestId('can-back')).toHaveTextContent('false');
    expect(router.state.location.search).toEqual({});
  });
});
