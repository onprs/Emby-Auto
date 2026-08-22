import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router';
import { render, type RenderOptions } from '@testing-library/react';
import type { ReactElement, ReactNode } from 'react';

export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });
}

type TestRoutePath = '/' | '/searches' | '/tasks' | '/tasks/$taskId' | '/acquisitions' | '/acquisitions/$acquisitionId' | '/acquisitions/$acquisitionId/mapping' | '/rss' | '/rss/$subscriptionId' | '/emby';

function createTestRouter(ui: ReactNode, routePath: TestRoutePath, initialEntry: string) {
  const rootRoute = createRootRoute({ component: Outlet });
  const catchAllRoute = createRoute({ getParentRoute: () => rootRoute, path: '$', component: () => null });

  if (routePath === '/tasks') {
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/tasks', component: () => <>{ui}</> });
    const detailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/tasks/$taskId', component: () => null });
    return createRouter({
      routeTree: rootRoute.addChildren([testRoute, detailRoute, catchAllRoute]),
      history: createMemoryHistory({ initialEntries: [initialEntry] }),
    });
  }

  if (routePath === '/tasks/$taskId') {
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/tasks/$taskId', component: () => <>{ui}</> });
    const acquisitionsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/acquisitions', component: () => null });
    return createRouter({
      routeTree: rootRoute.addChildren([testRoute, acquisitionsRoute, catchAllRoute]),
      history: createMemoryHistory({ initialEntries: [initialEntry] }),
    });
  }

  if (routePath === '/acquisitions') {
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/acquisitions', component: () => <>{ui}</> });
    const detailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/acquisitions/$acquisitionId', component: () => null });
    return createRouter({
      routeTree: rootRoute.addChildren([testRoute, detailRoute, catchAllRoute]),
      history: createMemoryHistory({ initialEntries: [initialEntry] }),
    });
  }

  if (routePath === '/acquisitions/$acquisitionId/mapping') {
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/acquisitions/$acquisitionId/mapping', component: () => <>{ui}</> });
    const detailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/acquisitions/$acquisitionId', component: () => null });
    return createRouter({
      routeTree: rootRoute.addChildren([testRoute, detailRoute, catchAllRoute]),
      history: createMemoryHistory({ initialEntries: [initialEntry] }),
    });
  }

  if (routePath === '/rss') {
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/rss', component: () => <>{ui}</> });
    const detailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/rss/$subscriptionId', component: () => null });
    return createRouter({
      routeTree: rootRoute.addChildren([testRoute, detailRoute, catchAllRoute]),
      history: createMemoryHistory({ initialEntries: [initialEntry] }),
    });
  }

  if (routePath === '/rss/$subscriptionId') {
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/rss/$subscriptionId', component: () => <>{ui}</> });
    const acquisitionRoute = createRoute({ getParentRoute: () => rootRoute, path: '/acquisitions/$acquisitionId', component: () => null });
    return createRouter({
      routeTree: rootRoute.addChildren([testRoute, acquisitionRoute, catchAllRoute]),
      history: createMemoryHistory({ initialEntries: [initialEntry] }),
    });
  }

  if (routePath === '/searches') {
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/searches', component: () => <>{ui}</> });
    const detailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/acquisitions/$acquisitionId', component: () => <div>acquisition detail</div> });
    return createRouter({
      routeTree: rootRoute.addChildren([testRoute, detailRoute, catchAllRoute]),
      history: createMemoryHistory({ initialEntries: [initialEntry] }),
    });
  }

  if (routePath === '/emby') {
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/emby', component: () => <>{ui}</> });
    const scanRoute = createRoute({ getParentRoute: () => rootRoute, path: '/emby/scans/$scanId', component: () => null });
    const libraryRoute = createRoute({ getParentRoute: () => rootRoute, path: '/emby/libraries/$libraryId', component: () => null });
    return createRouter({
      routeTree: rootRoute.addChildren([testRoute, scanRoute, libraryRoute, catchAllRoute]),
      history: createMemoryHistory({ initialEntries: [initialEntry] }),
    });
  }

  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => <>{ui}</> });
  return createRouter({
    routeTree: rootRoute.addChildren([indexRoute, catchAllRoute]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  });
}

type ProviderRenderOptions = RenderOptions & {
  queryClient?: QueryClient;
  routePath?: TestRoutePath;
  initialEntry?: string;
};

export function renderWithProviders(ui: ReactElement, options?: ProviderRenderOptions) {
  const {
    queryClient = createTestQueryClient(),
    routePath = '/',
    initialEntry = '/',
    ...renderOptions
  } = options ?? {};
  const router = createTestRouter(ui, routePath, initialEntry);
  function Wrapper() {
    return (
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    );
  }
  return { queryClient, router, ...render(<Wrapper />, renderOptions) };
}
