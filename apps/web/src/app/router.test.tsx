import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, Outlet, RouterProvider } from '@tanstack/react-router';
import { act, render, screen, waitFor } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { reportSessionLoss } from '@/app/session-runtime';
import { server } from '@/test/msw-server';

const stream = vi.hoisted(() => ({
  start: vi.fn(),
  stop: vi.fn(),
  onStatus: vi.fn(() => () => undefined),
}));

vi.mock('@/app/sse', () => ({
  EventStream: class {
    start = stream.start;
    stop = stream.stop;
    onStatus = stream.onStatus;
  },
}));

vi.mock('@/app/layout', () => ({
  AppLayout: ({ session }: { session: { user: { username: string } } }) => (
    <main data-testid="app-shell">
      <span>{session.user.username}</span>
      <Outlet />
    </main>
  ),
}));
vi.mock('@/features/dashboard/dashboard-page', () => ({
  DashboardPage: () => <h1>lazy dashboard</h1>,
}));
vi.mock('@/features/acquisitions/acquisition-detail-page', () => ({
  AcquisitionDetailPage: ({ acquisitionId }: { acquisitionId: string }) => <h1>acquisition {acquisitionId}</h1>,
}));
vi.mock('@/features/setup/setup-wizard', () => ({
  SetupWizard: () => <h1>lazy setup</h1>,
}));
vi.mock('@/features/auth/login-page', () => ({
  LoginPage: () => <h1>lazy login</h1>,
}));
vi.mock('@/app/access-denied', () => {
  throw new Error('route chunk failed');
});

import { createAppRouter, eventStream, queryClient } from '@/app/router';

const acquisitionId = '71000000-0000-4000-8000-000000000001';
const completedSetup = {
  state: 'completed' as const,
  databaseConfigured: true,
  databaseManagedExternally: true,
  administratorConfigured: true,
};
const session = {
  user: { id: '71000000-0000-4000-8000-000000000002', username: 'admin' },
  expiresAt: '2099-01-01T00:00:00Z',
};

function useSetupResponse(state: 'required' | 'completed' = 'completed') {
  server.use(
    http.get('*/api/v1/setup/status', () => HttpResponse.json(
      state === 'completed'
        ? completedSetup
        : { ...completedSetup, state: 'required', administratorConfigured: false },
    )),
  );
}

function useSessionResponse(authenticated: boolean) {
  server.use(
    http.get('*/api/v1/auth/session', () => authenticated
      ? HttpResponse.json(session)
      : HttpResponse.json(
        { code: 'unauthenticated', message: 'authentication required', details: {}, requestId: 'router-test' },
        { status: 401 },
      )),
  );
}

function renderApp(initialEntry: string) {
  const appRouter = createAppRouter(createMemoryHistory({ initialEntries: [initialEntry] }));
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={appRouter} />
    </QueryClientProvider>,
  );
  return appRouter;
}

beforeEach(() => {
  eventStream.stop();
  queryClient.clear();
  stream.start.mockClear();
  stream.stop.mockClear();
  stream.onStatus.mockClear();
});

describe('application router lazy boundaries', () => {
  it('loads a direct detail route and forwards its path parameter', async () => {
    useSetupResponse();
    useSessionResponse(true);

    renderApp(`/acquisitions/${acquisitionId}`);

    expect(await screen.findByRole('heading', { name: `acquisition ${acquisitionId}` })).toBeVisible();
    expect(screen.getByTestId('app-shell')).toBeVisible();
    expect(stream.start).toHaveBeenCalledTimes(1);
  });

  it('keeps one shell, event stream, and query cache while lazy routes change', async () => {
    useSetupResponse();
    useSessionResponse(true);
    const appRouter = renderApp(`/acquisitions/${acquisitionId}`);
    await screen.findByRole('heading', { name: `acquisition ${acquisitionId}` });
    const sessionQuery = queryClient.getQueryCache().find({ queryKey: ['session'] });

    await act(() => appRouter.navigate({ to: '/' }));

    expect(await screen.findByRole('heading', { name: 'lazy dashboard' })).toBeVisible();
    expect(queryClient.getQueryCache().find({ queryKey: ['session'] })).toBe(sessionQuery);
    expect(stream.start).toHaveBeenCalledTimes(1);
  });

  it('loads setup and login modules only when the application gate selects them', async () => {
    useSetupResponse('required');
    useSessionResponse(false);
    const setupView = renderApp('/');
    expect(await screen.findByRole('heading', { name: 'lazy setup' })).toBeVisible();
    expect(stream.start).not.toHaveBeenCalled();

    await act(() => setupView.navigate({ to: '/searches' }));
    expect(screen.getByRole('heading', { name: 'lazy setup' })).toBeVisible();
  });

  it('shows the login module for a completed but unauthenticated installation', async () => {
    useSetupResponse();
    useSessionResponse(false);

    renderApp('/');

    expect(await screen.findByRole('heading', { name: 'lazy login' })).toBeVisible();
    expect(stream.start).not.toHaveBeenCalled();
  });

  it('renders the stable route error fallback when a lazy import fails', async () => {
    useSetupResponse();
    useSessionResponse(true);
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);

    renderApp('/forbidden');

    expect(await screen.findByRole('heading', { name: '页面加载失败' })).toBeVisible();
    expect(screen.getByText(/error when mocking a module/i)).toBeVisible();
    expect(screen.getByRole('button', { name: '重试' })).toBeVisible();
    expect(consoleError).toHaveBeenCalled();
    consoleError.mockRestore();
  });

  it('stops the shared event stream and returns to login after session loss', async () => {
    useSetupResponse();
    useSessionResponse(true);
    renderApp('/');
    await screen.findByRole('heading', { name: 'lazy dashboard' });

    act(() => reportSessionLoss('unauthorized'));

    await waitFor(() => expect(screen.getByRole('heading', { name: 'lazy login' })).toBeVisible());
    expect(stream.stop).toHaveBeenCalledTimes(2);
    expect(queryClient.getQueryData(['session'])).toBeNull();
  });
});
