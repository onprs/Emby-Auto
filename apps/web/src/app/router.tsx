import { QueryCache, QueryClient, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  redirect,
  useParams,
  type ErrorComponentProps,
  type RouterHistory,
} from '@tanstack/react-router';
import { LoaderCircle, RefreshCw } from 'lucide-react';
import { useEffect } from 'react';

import { ApiFailure, fetchSession, fetchSetupStatus } from '@/api/app-client';
import type { Session, SetupStatus } from '@/api/generated/types.gen';
import { AppLayout } from '@/app/layout';
import { normalizeInternalLocation } from '@/app/navigation';
import { EventStream } from '@/app/sse';
import { registerSessionLossHandler, reportSessionLoss } from '@/app/session-runtime';
import { Button } from '@/components/ui/button';

const AcquisitionsPage = lazyRouteComponent(
  () => import('@/features/acquisitions/acquisitions-page'),
  'AcquisitionsPage',
);
const AcquisitionDetailPage = lazyRouteComponent(
  () => import('@/features/acquisitions/acquisition-detail-page'),
  'AcquisitionDetailPage',
);
const LoginPage = lazyRouteComponent(() => import('@/features/auth/login-page'), 'LoginPage');
const DashboardPage = lazyRouteComponent(() => import('@/features/dashboard/dashboard-page'), 'DashboardPage');
const DownloadDetailPage = lazyRouteComponent(
  () => import('@/features/downloads/download-detail-page'),
  'DownloadDetailPage',
);
const EmbyPage = lazyRouteComponent(() => import('@/features/emby/emby-page'), 'EmbyPage');
const EmbyScanDetailPage = lazyRouteComponent(
  () => import('@/features/emby/emby-scan-detail-page'),
  'EmbyScanDetailPage',
);
const EmbyLibraryDetailPage = lazyRouteComponent(
  () => import('@/features/emby/emby-library-detail-page'),
  'EmbyLibraryDetailPage',
);
const MappingPage = lazyRouteComponent(() => import('@/features/mapping/mapping-page'), 'MappingPage');
const OperationDetailPage = lazyRouteComponent(
  () => import('@/features/operations/operation-detail-page'),
  'OperationDetailPage',
);
const OperationsPage = lazyRouteComponent(() => import('@/features/operations/operations-page'), 'OperationsPage');
const RssPage = lazyRouteComponent(() => import('@/features/rss/rss-page'), 'RssPage');
const RssDetailPage = lazyRouteComponent(() => import('@/features/rss/rss-detail-page'), 'RssDetailPage');
const SearchDetailPage = lazyRouteComponent(
  () => import('@/features/searches/search-detail-page'),
  'SearchDetailPage',
);
const SearchesPage = lazyRouteComponent(() => import('@/features/searches/searches-page'), 'SearchesPage');
const SettingsAgentPage = lazyRouteComponent(
  () => import('@/features/configuration/settings-agent-page'),
  'SettingsAgentPage',
);
const SettingsHubPage = lazyRouteComponent(
  () => import('@/features/configuration/settings-hub-page'),
  'SettingsHubPage',
);
const SettingsServicesPage = lazyRouteComponent(
  () => import('@/features/configuration/settings-services-page'),
  'SettingsServicesPage',
);
const SettingsStoragePage = lazyRouteComponent(
  () => import('@/features/configuration/settings-storage-page'),
  'SettingsStoragePage',
);
const SettingsTranscodePage = lazyRouteComponent(
  () => import('@/features/configuration/settings-transcode-page'),
  'SettingsTranscodePage',
);
const SetupWizard = lazyRouteComponent(() => import('@/features/setup/setup-wizard'), 'SetupWizard');
const TaskDetailPage = lazyRouteComponent(() => import('@/features/tasks/task-detail-page'), 'TaskDetailPage');
const AccessDeniedPage = lazyRouteComponent(() => import('@/app/access-denied'), 'AccessDeniedPage');
const NotFoundPage = lazyRouteComponent(() => import('@/app/not-found'), 'NotFoundPage');

export const queryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (error) => {
      // FE-AUTH-003: any 401 clears the session so the application gate
      // falls back to the login flow.
      if (error instanceof ApiFailure && error.isUnauthorized) {
        handleSessionLoss();
      }
    },
  }),
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      staleTime: 15_000,
      retry: (failureCount, error) => {
        if (error instanceof Error && 'status' in error) {
          const status = (error as { status?: number }).status;
          if (status === 401 || status === 404) {
            return false;
          }
        }
        return failureCount < 2;
      },
    },
  },
});

export const eventStream = new EventStream(queryClient);
registerSessionLossHandler(handleSessionLoss);

function handleSessionLoss(): void {
  eventStream.stop();
  queryClient.setQueryData(['session'], null);
  queryClient.removeQueries({
    predicate: (query) => query.queryKey[0] !== 'setup-status' && query.queryKey[0] !== 'session',
  });
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function stringSearch(value: unknown, maxLength = 256): string | undefined {
  return typeof value === 'string' && value.length > 0 && value.length <= maxLength ? value : undefined;
}

function enumSearch<const T extends readonly string[]>(value: unknown, allowed: T): T[number] | undefined {
  return typeof value === 'string' && allowed.includes(value as T[number]) ? (value as T[number]) : undefined;
}

type CursorRouteSearch = { cursor?: string; cursorStack?: string };
type SearchListSearch = CursorRouteSearch & { status?: 'queued' | 'running' | 'completed' | 'failed' | 'cancelled'; query?: string };
type AcquisitionListSearch = CursorRouteSearch & {
  sourceKind?: 'search' | 'rss' | 'manual';
  phase?: 'mapping_pending' | 'downloading' | 'processing' | 'awaiting_review' | 'importing' | 'completed' | 'attention';
  sortBy?: 'content' | 'source_kind' | 'progress' | 'updated_at';
  sortOrder?: 'asc' | 'desc';
};
type DetailSourceSearch = { from?: string };
type RssListSearch = CursorRouteSearch & { sortBy?: 'name' | 'series_title' | 'source_season' | 'enabled' | 'progress' | 'next_poll_at'; sortOrder?: 'asc' | 'desc' };
type RssEntrySearch = CursorRouteSearch & DetailSourceSearch & {
  status?: 'discovered' | 'enqueueing' | 'enqueued' | 'enqueue_failed';
  sortBy?: 'title' | 'episode' | 'progress' | 'discovered_at';
  sortOrder?: 'asc' | 'desc';
};
type EmbyItemSearch = CursorRouteSearch & DetailSourceSearch & { itemType?: 'Series' | 'Season' | 'Episode' | 'Movie'; name?: string; providerId?: string; present?: 'true' | 'false' };
type OperationListSearch = CursorRouteSearch & { status?: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'; resourceType?: string; resourceId?: string };

function detailSourceSearch(search: Record<string, unknown>): DetailSourceSearch {
  return { from: normalizeInternalLocation(stringSearch(search.from, 8_192)) };
}

function cursorSearch(search: Record<string, unknown>): CursorRouteSearch {
  const cursor = typeof search.cursor === 'string' && uuidPattern.test(search.cursor) ? search.cursor : undefined;
  const cursorStack = typeof search.cursorStack === 'string' && search.cursorStack.length <= 4096 &&
    search.cursorStack.split(',').every((value) => value === '_' || uuidPattern.test(value))
    ? search.cursorStack
    : undefined;
  return { cursor, cursorStack };
}

const rootRoute = createRootRoute({ component: ApplicationGate });

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  component: AppShell,
});

const indexRoute = createRoute({ getParentRoute: () => appRoute, path: '/', component: DashboardPage });
const searchesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/searches',
  validateSearch: (search: Record<string, unknown>): SearchListSearch => ({
    ...cursorSearch(search),
    status: enumSearch(search.status, ['queued', 'running', 'completed', 'failed', 'cancelled'] as const),
    query: stringSearch(search.query, 512),
  }),
  component: SearchesPage,
});
const searchDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/searches/$searchId',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: function SearchDetailRoute() {
    const { searchId } = useParams({ from: searchDetailRoute.id });
    return <SearchDetailPage searchId={searchId} />;
  },
});
const acquisitionsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/acquisitions',
  validateSearch: (search: Record<string, unknown>): AcquisitionListSearch => ({
    ...cursorSearch(search),
    sourceKind: enumSearch(search.sourceKind, ['search', 'rss', 'manual'] as const),
    phase: enumSearch(search.phase, ['mapping_pending', 'downloading', 'processing', 'awaiting_review', 'importing', 'completed', 'attention'] as const),
    sortBy: enumSearch(search.sortBy, ['content', 'source_kind', 'progress', 'updated_at'] as const),
    sortOrder: enumSearch(search.sortOrder, ['asc', 'desc'] as const),
  }),
  component: AcquisitionsPage,
});
const acquisitionDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/acquisitions/$acquisitionId',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: function AcquisitionDetailRoute() {
    const { acquisitionId } = useParams({ from: acquisitionDetailRoute.id });
    return <AcquisitionDetailPage acquisitionId={acquisitionId} />;
  },
});
const acquisitionMappingRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/acquisitions/$acquisitionId/mapping',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: function AcquisitionMappingRoute() {
    const { acquisitionId } = useParams({ from: acquisitionMappingRoute.id });
    return <MappingPage acquisitionId={acquisitionId} />;
  },
});
const downloadsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/downloads',
  beforeLoad: () => {
    throw redirect({ to: '/acquisitions', replace: true });
  },
});
const downloadDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/downloads/$downloadId',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: function DownloadDetailRoute() {
    const { downloadId } = useParams({ from: downloadDetailRoute.id });
    return <DownloadDetailPage downloadId={downloadId} />;
  },
});
const rssRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/rss',
  validateSearch: (search: Record<string, unknown>): RssListSearch => ({
    ...cursorSearch(search),
    sortBy: enumSearch(search.sortBy, ['name', 'series_title', 'source_season', 'enabled', 'progress', 'next_poll_at'] as const),
    sortOrder: enumSearch(search.sortOrder, ['asc', 'desc'] as const),
  }),
  component: RssPage,
});
const rssDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/rss/$subscriptionId',
  validateSearch: (search: Record<string, unknown>): RssEntrySearch => ({
    ...cursorSearch(search),
    ...detailSourceSearch(search),
    status: enumSearch(search.status, ['discovered', 'enqueueing', 'enqueued', 'enqueue_failed'] as const),
    sortBy: enumSearch(search.sortBy, ['title', 'episode', 'progress', 'discovered_at'] as const),
    sortOrder: enumSearch(search.sortOrder, ['asc', 'desc'] as const),
  }),
  component: function RssDetailRoute() {
    const { subscriptionId } = useParams({ from: rssDetailRoute.id });
    return <RssDetailPage subscriptionId={subscriptionId} />;
  },
});
const tasksRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/tasks',
  beforeLoad: () => {
    throw redirect({ to: '/acquisitions', replace: true });
  },
});
const taskDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/tasks/$taskId',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: function TaskDetailRoute() {
    const { taskId } = useParams({ from: taskDetailRoute.id });
    return <TaskDetailPage taskId={taskId} />;
  },
});
const embyRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/emby',
  validateSearch: (search: Record<string, unknown>) => cursorSearch(search),
  component: EmbyPage,
});
const embyScanDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/emby/scans/$scanId',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: function EmbyScanDetailRoute() {
    const { scanId } = useParams({ from: embyScanDetailRoute.id });
    return <EmbyScanDetailPage scanId={scanId} />;
  },
});
const embyLibraryDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/emby/libraries/$libraryId',
  validateSearch: (search: Record<string, unknown>): EmbyItemSearch => ({
    ...cursorSearch(search),
    ...detailSourceSearch(search),
    itemType: enumSearch(search.itemType, ['Series', 'Season', 'Episode', 'Movie'] as const),
    name: stringSearch(search.name),
    providerId: stringSearch(search.providerId),
    present: enumSearch(search.present, ['true', 'false'] as const),
  }),
  component: function EmbyLibraryDetailRoute() {
    const { libraryId } = useParams({ from: embyLibraryDetailRoute.id });
    return <EmbyLibraryDetailPage libraryId={libraryId} />;
  },
});
const operationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/operations',
  validateSearch: (search: Record<string, unknown>): OperationListSearch => ({
    ...cursorSearch(search),
    status: enumSearch(search.status, ['queued', 'running', 'succeeded', 'failed', 'cancelled'] as const),
    resourceType: stringSearch(search.resourceType, 128),
    resourceId: typeof search.resourceId === 'string' && uuidPattern.test(search.resourceId) ? search.resourceId : undefined,
  }),
  component: OperationsPage,
});
const operationDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/operations/$operationId',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: function OperationDetailRoute() {
    const { operationId } = useParams({ from: operationDetailRoute.id });
    return <OperationDetailPage operationId={operationId} />;
  },
});
const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: SettingsHubPage,
});
const settingsServicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings/services',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: SettingsServicesPage,
});
const settingsAgentRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings/agent',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: SettingsAgentPage,
});
const settingsStorageRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings/storage',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: SettingsStoragePage,
});
const settingsTranscodeRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings/transcode',
  validateSearch: (search: Record<string, unknown>) => detailSourceSearch(search),
  component: SettingsTranscodePage,
});
const forbiddenRoute = createRoute({ getParentRoute: () => appRoute, path: '/forbidden', component: AccessDeniedPage });

const routeTree = rootRoute.addChildren([
  appRoute.addChildren([
    indexRoute,
    searchesRoute,
    searchDetailRoute,
    acquisitionsRoute,
    acquisitionDetailRoute,
    acquisitionMappingRoute,
    downloadsRoute,
    downloadDetailRoute,
    rssRoute,
    rssDetailRoute,
    tasksRoute,
    taskDetailRoute,
    embyRoute,
    embyScanDetailRoute,
    embyLibraryDetailRoute,
    operationsRoute,
    operationDetailRoute,
    settingsRoute,
    settingsServicesRoute,
    settingsAgentRoute,
    settingsStorageRoute,
    settingsTranscodeRoute,
    forbiddenRoute,
  ]),
]);

export function createAppRouter(history?: RouterHistory) {
  return createRouter({
    routeTree,
    context: { queryClient },
    defaultNotFoundComponent: NotFoundPage,
    defaultPendingComponent: RouteLoading,
    defaultErrorComponent: RouteError,
    defaultPendingMs: 120,
    defaultPendingMinMs: 250,
    ...(history ? { history } : {}),
  });
}

export const router = createAppRouter();

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}

function ApplicationGate() {
  const queryClient = useQueryClient();
  const setup = useQuery({
    queryKey: ['setup-status'],
    queryFn: fetchSetupStatus,
    retry: 1,
    refetchInterval: (query) => (query.state.data?.state === 'initializing' ? 1_000 : false),
  });
  const session = useQuery({
    queryKey: ['session'],
    queryFn: fetchSession,
    enabled: setup.data?.state === 'completed',
    retry: false,
  });

  if (setup.isPending || setup.data?.state === 'initializing') {
    return <FullPageLoading label="正在读取系统状态" />;
  }
  if (setup.error || !setup.data) {
    return <FullPageError message={setup.error?.message ?? '无法读取系统状态'} onRetry={() => setup.refetch()} />;
  }
  if (setup.data.state !== 'completed') {
    return (
      <SetupWizard
        status={setup.data}
        onCompleted={(status: SetupStatus) => {
          queryClient.setQueryData(['setup-status'], status);
          void queryClient.invalidateQueries({ queryKey: ['session'] });
        }}
      />
    );
  }
  if (session.isPending) {
    return <FullPageLoading label="正在读取登录状态" />;
  }
  if (session.error) {
    return <FullPageError message={session.error.message} onRetry={() => session.refetch()} />;
  }
  if (!session.data) {
    return <LoginPage />;
  }
  return <Outlet />;
}

function AppShell() {
  const session = useQuery<Session | null>({ queryKey: ['session'], queryFn: fetchSession, retry: false });

  if (!session.data) {
    return null;
  }
  return <ConnectedLayout session={session.data} events={eventStream} />;
}

function ConnectedLayout({ session, events }: { session: Session; events: EventStream }) {
  useEffect(() => {
    events.start();
    const expiresIn = Math.max(0, Date.parse(session.expiresAt) - Date.now());
    const expirationTimer = window.setTimeout(() => reportSessionLoss('expired'), Math.min(expiresIn, 2_147_483_647));
    return () => {
      window.clearTimeout(expirationTimer);
      events.stop({ clearCursor: false });
    };
  }, [events, session.expiresAt]);
  return <AppLayout session={session} events={events} />;
}

function RouteLoading() {
  return (
    <section className="grid min-h-[calc(100vh-4rem)] place-items-center px-4" aria-label="页面加载中">
      <div className="flex items-center gap-3 text-sm text-zinc-600" role="status">
        <LoaderCircle className="size-5 animate-spin text-emerald-700" aria-hidden="true" />
        正在加载页面
      </div>
    </section>
  );
}

export function RouteError({ error, reset }: ErrorComponentProps) {
  return (
    <section className="grid min-h-[calc(100vh-4rem)] place-items-center px-4">
      <div className="w-full max-w-md border-t-4 border-red-600 bg-white px-6 py-7 shadow-lg">
        <h1 className="text-lg font-semibold text-zinc-950">页面加载失败</h1>
        <p className="mt-2 break-words text-sm text-zinc-600">{error.message || '无法加载此页面'}</p>
        <Button type="button" className="mt-6" variant="outline" onClick={reset}>
          <RefreshCw aria-hidden="true" />
          重试
        </Button>
      </div>
    </section>
  );
}

function FullPageLoading({ label }: { label: string }) {
  return (
    <main className="grid min-h-screen place-items-center bg-surface px-4">
      <div className="flex items-center gap-3 text-sm text-zinc-600" role="status">
        <LoaderCircle className="size-5 animate-spin text-emerald-700" />
        {label}
      </div>
    </main>
  );
}

function FullPageError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <main className="grid min-h-screen place-items-center bg-surface px-4">
      <section className="w-full max-w-md animate-scale-in rounded-2xl border-t-4 border-red-600 bg-white px-6 py-7 shadow-xl">
        <h1 className="text-lg font-semibold text-zinc-950">连接失败</h1>
        <p className="mt-2 break-words text-sm text-zinc-600">{message}</p>
        <Button type="button" className="mt-6" variant="outline" onClick={onRetry}>
          <RefreshCw />
          重试
        </Button>
      </section>
    </main>
  );
}
