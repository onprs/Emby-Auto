import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocation } from '@tanstack/react-router';
import { AlertTriangle, CheckCircle2, FolderSync, LoaderCircle, RefreshCw } from 'lucide-react';
import { useEffect, useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import { currentAppLocation, useListScrollRestoration } from '@/app/navigation-context';
import { ContextLink } from '@/components/context-link';
import { PageBody, PageHeader, PaginationControls } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import {
  fetchLibraries,
  fetchScan,
  fetchScans,
  refreshEmby,
  startScan,
} from '@/features/emby/api';
import { fetchOperation } from '@/features/operations/api';
import { formatDateTime } from '@/lib/format';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { useCursorPagination } from '@/lib/pagination';
import { friendlyError } from '@/lib/presentation';

export function EmbyPage() {
  const location = useLocation();
  const listSource = currentAppLocation(location.href);
  const queryClient = useQueryClient();
  const [scanError, setScanError] = useState<string | null>(null);
  const [refreshError, setRefreshError] = useState<string | null>(null);
  const [activeScanId, setActiveScanId] = useState<string | null>(null);
  const [refreshOperationId, setRefreshOperationId] = useState<string | null>(null);
  const holder = useState(() => new IdempotencyKeyHolder())[0];
  const refreshHolder = useState(() => new IdempotencyKeyHolder())[0];
  const scansPagination = useCursorPagination();

  const scans = useQuery({
    queryKey: ['emby-scans', 'list', scansPagination.cursor],
    queryFn: () => fetchScans(scansPagination.cursor),
    refetchInterval: (query) => query.state.data?.items.some(isActiveStatus) ? 2_000 : false,
  });
  const libraries = useQuery({
    queryKey: ['emby-libraries'],
    queryFn: fetchLibraries,
  });
  const activeScan = useQuery({
    queryKey: ['emby-scan', activeScanId],
    queryFn: () => fetchScan(activeScanId!),
    enabled: Boolean(activeScanId),
    refetchInterval: (query) => !query.state.data || isActiveStatus(query.state.data) ? 2_000 : false,
  });
  const refreshOperation = useQuery({
    queryKey: ['operation', refreshOperationId],
    queryFn: () => fetchOperation(refreshOperationId!),
    enabled: Boolean(refreshOperationId),
    refetchInterval: (query) => !query.state.data || isActiveStatus(query.state.data) ? 2_000 : false,
  });

  useListScrollRestoration(listSource, Boolean(scans.data || libraries.data));

  const scan = useMutation({
    mutationFn: () => {
      setScanError(null);
      setActiveScanId(null);
      return startScan(holder.get());
    },
    onSuccess: (result) => {
      holder.reset();
      setActiveScanId(result.scan.id);
      void queryClient.invalidateQueries({ queryKey: ['emby-scans'] });
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        holder.reset();
        void queryClient.invalidateQueries({ queryKey: ['emby-scans'] });
      }
      setScanError(cause instanceof Error ? cause.message : '从 Emby 更新目录失败');
    },
  });

  const refresh = useMutation({
    mutationFn: () => {
      setRefreshError(null);
      setRefreshOperationId(null);
      return refreshEmby(refreshHolder.get());
    },
    onSuccess: (result) => {
      refreshHolder.reset();
      setRefreshOperationId(result.operationId);
      void queryClient.invalidateQueries({ queryKey: ['operations'] });
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        refreshHolder.reset();
      }
      setRefreshError(cause instanceof Error ? cause.message : '请求 Emby 扫描文件失败');
    },
  });

  const scanStatus = activeScan.data?.status;
  useEffect(() => {
    if (!scanStatus || isActiveStatus(scanStatus)) {
      return;
    }
    void queryClient.invalidateQueries({ queryKey: ['emby-scans'] });
    if (scanStatus === 'succeeded') {
      void queryClient.invalidateQueries({ queryKey: ['emby-libraries'] });
    }
  }, [queryClient, scanStatus]);

  const listedActiveScan = scans.data?.items.find(isActiveStatus);
  const scanFeedback = activeScan.data ?? scan.data?.scan ?? listedActiveScan;
  const scanFeedbackError = scanError ?? activeScan.error?.message ?? null;
  const scanFeedbackStatus = scanFeedbackError ? 'failed' : scanFeedback?.status ?? (scan.isPending ? 'queued' : undefined);
  const refreshStatus = refreshOperation.data?.status;
  const refreshFeedbackError = refreshError ?? refreshOperation.error?.message ?? null;
  const refreshFeedbackStatus = refreshFeedbackError ? 'failed' : refreshStatus ?? (refresh.isPending || refreshOperationId ? 'queued' : undefined);
  const hasActiveScan = Boolean(listedActiveScan) || isActiveStatus(scanFeedback);
  const hasActiveRefresh = refresh.isPending || Boolean(refreshOperationId && (!refreshStatus || isActiveStatus(refreshStatus)));

  return (
    <PageBody>
      <PageHeader
        title="媒体库"
        description="查看本系统从 Emby 获取的媒体库和条目"
        actions={
          <>
            <Button type="button" variant="outline" onClick={() => refresh.mutate()} disabled={hasActiveRefresh}>
              {hasActiveRefresh ? <LoaderCircle className="animate-spin" /> : <RefreshCw />}
              {hasActiveRefresh ? '正在请求扫描' : '请求 Emby 扫描文件'}
            </Button>
            <Button type="button" onClick={() => scan.mutate()} disabled={scan.isPending || hasActiveScan}>
              {scan.isPending || hasActiveScan ? <LoaderCircle className="animate-spin" /> : <FolderSync />}
              {scan.isPending || hasActiveScan ? '正在更新目录' : '从 Emby 更新目录'}
            </Button>
          </>
        }
      />

      {refreshFeedbackStatus || scanFeedbackStatus ? (
        <div className="mb-6 space-y-2" aria-live="polite">
          {refreshFeedbackStatus ? (
            <CommandFeedback
              title="请求 Emby 扫描文件"
              status={refreshFeedbackStatus}
              error={refreshFeedbackError ?? (refreshStatus === 'failed' ? friendlyError(refreshOperation.data?.errorCode, refreshOperation.data?.errorMessage) : null)}
              message={refreshStatus === 'succeeded'
                ? 'Emby 已接受媒体文件扫描请求。'
                : refreshStatus === 'cancelled'
                  ? '扫描请求已取消。'
                  : '正在等待后台向 Emby 发送扫描请求。'}
            />
          ) : null}
          {scanFeedbackStatus ? (
            <CommandFeedback
              title="从 Emby 更新目录"
              status={scanFeedbackStatus}
              error={scanFeedbackError ?? (scanFeedback?.status === 'failed' ? friendlyError(scanFeedback.errorCode, scanFeedback.errorMessage) : null)}
              message={scanFeedback?.status === 'succeeded'
                ? `目录已更新：${scanFeedback.libraryCount} 个媒体库，${scanFeedback.itemCount} 个媒体条目。`
                : scanFeedback?.status === 'cancelled'
                  ? '目录更新已取消。'
                  : '正在读取 Emby 当前的媒体库和条目。'}
            />
          ) : null}
        </div>
      ) : null}

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>目录更新记录</CardTitle>
            <CardDescription>从 Emby 获取媒体库和条目的历史结果</CardDescription>
          </CardHeader>
          <CardContent>
            {scans.isPending ? (
              <LoadingState label="正在加载更新记录" />
            ) : scans.error ? (
              <ErrorState message={scans.error.message} onRetry={() => scans.refetch()} />
            ) : scans.data.items.length === 0 ? (
              <EmptyState title="暂无记录" description="点击“从 Emby 更新目录”开始" />
            ) : (
              <>
                <ul className="divide-y divide-zinc-100">
                  {scans.data.items.map((item) => (
                    <li key={item.id} className="flex items-center justify-between gap-3 py-2.5">
                      <div className="min-w-0">
                        <ContextLink rememberList to="/emby/scans/$scanId" params={{ scanId: item.id }} className="block truncate text-sm font-medium text-zinc-900 hover:underline">
                          {formatDateTime(item.createdAt)}
                        </ContextLink>
                        <p className="mt-0.5 text-xs text-zinc-500">
                          {item.libraryCount} 个媒体库 · {item.itemCount} 个媒体条目
                        </p>
                      </div>
                      <StatusBadge value={item.status} />
                    </li>
                  ))}
                </ul>
                <PaginationControls
                  canGoBack={scansPagination.canGoBack}
                  hasNext={Boolean(scans.data.nextCursor)}
                  onPrevious={scansPagination.goPrevious}
                  onNext={() => scansPagination.goNext(scans.data.nextCursor)}
                  isFetching={scans.isFetching}
                />
              </>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>已同步的媒体库</CardTitle>
            <CardDescription>最近一次从 Emby 获取的媒体库目录</CardDescription>
          </CardHeader>
          <CardContent>
            {libraries.isPending ? (
              <LoadingState label="正在加载媒体库目录" />
            ) : libraries.error ? (
              <ErrorState message={libraries.error.message} onRetry={() => libraries.refetch()} />
            ) : libraries.data.length === 0 ? (
              <EmptyState title="暂无媒体库" description="完成一次目录更新后显示" />
            ) : (
              <ul className="divide-y divide-zinc-100">
                {libraries.data.map((library) => (
                  <li key={library.id} className="flex items-center justify-between gap-3 py-2.5">
                    <div className="min-w-0">
                      <ContextLink rememberList to="/emby/libraries/$libraryId" params={{ libraryId: library.id }} className="block truncate text-sm font-medium text-zinc-900 hover:underline">
                        {library.name}
                      </ContextLink>
                      <p className="mt-0.5 text-xs text-zinc-500">{library.collectionType === 'tvshows' ? '番剧' : library.collectionType === 'movies' ? '电影' : '媒体'}</p>
                    </div>
                    {library.present ? <StatusBadge value="mapped" /> : <StatusBadge value="cancelled" />}
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>
    </PageBody>
  );
}

function isActiveStatus(value: string | { status: string } | null | undefined) {
  const status = typeof value === 'string' ? value : value?.status;
  return status === 'queued' || status === 'running';
}

function CommandFeedback({
  title,
  status,
  message,
  error,
}: {
  title: string;
  status: string;
  message: string;
  error: string | null;
}) {
  const failed = Boolean(error) || status === 'failed';
  const succeeded = status === 'succeeded';
  const cancelled = status === 'cancelled';
  const Icon = failed || cancelled ? AlertTriangle : succeeded ? CheckCircle2 : LoaderCircle;

  return (
    <div
      role={failed ? 'alert' : 'status'}
      className={`flex items-start gap-3 rounded-xl border px-4 py-3 ${
        failed
          ? 'border-red-200 bg-red-50 text-red-950'
          : succeeded
            ? 'border-emerald-200 bg-emerald-50 text-emerald-950'
            : cancelled
              ? 'border-amber-200 bg-amber-50 text-amber-950'
              : 'border-zinc-200 bg-zinc-50 text-zinc-950'
      }`}
    >
      <Icon className={`mt-0.5 size-4 shrink-0 ${!failed && !succeeded && !cancelled ? 'animate-spin' : ''}`} />
      <div className="min-w-0 flex-1">
        <p className="break-words text-sm font-medium">{title}</p>
        <p className="mt-0.5 break-words text-sm opacity-80">{error ?? message}</p>
      </div>
      <StatusBadge value={failed ? 'failed' : status} />
    </div>
  );
}
