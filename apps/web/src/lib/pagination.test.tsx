import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
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
});
