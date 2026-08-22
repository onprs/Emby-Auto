import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useLocation } from '@tanstack/react-router';

import { appNavigationState, currentAppLocation, rememberListPosition } from '@/app/navigation-context';
import { fetchAcquisitions } from '@/features/acquisitions/api';
import { TaskProgress } from '@/features/acquisitions/task-progress';
import { DataTable, PageHeader, PaginationControls } from '@/components/resource';
import { AcquisitionStageBadge } from '@/components/status-badge';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { formatDateTime } from '@/lib/format';
import { episodeLabel, sourceKindLabel } from '@/lib/presentation';
import { useCursorPagination } from '@/lib/pagination';
import { RecordActions, type RecordAction } from '@/components/record-actions';
import { acquisitionCanDelete, acquisitionHasActiveWork, cancelAcquisitionWork, deleteAcquisition, retryAcquisition } from '@/features/acquisitions/acquisition-actions';
import { acquisitionFailureInfo } from '@/features/tasks/task-failure';
import { TaskFailureSummary } from '@/features/tasks/task-failure-summary';
import { useState } from 'react';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import type { Acquisition } from '@/api/generated/types.gen';

export function SearchAcquisitionsSection() {
  const queryClient = useQueryClient();
  const location = useLocation();
  const listSource = currentAppLocation(location.href);
  const pagination = useCursorPagination();
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const acquisitions = useQuery({
    queryKey: ['acquisitions', 'search-tasks', pagination.cursor],
    queryFn: () => fetchAcquisitions(pagination.cursor, 'search', undefined, 'updated_at', 'desc'),
  });

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
  };

  const rowActions = (item: Acquisition): RecordAction[] => {
    const failure = acquisitionFailureInfo(item);
    const actions: RecordAction[] = [
      {
        key: 'detail',
        label: failure ? '查看失败原因' : '查看详情',
        run: async (): Promise<string | null> => {
          rememberListPosition(listSource);
          return null;
        },
      },
    ];
    if (failure?.canRetry) {
      actions.push({
        key: 'retry',
        label: failure.retryLabel ?? '重试任务',
        run: async (): Promise<string | null> => {
          const result = await retryAcquisition(item, holder.get());
          holder.reset();
          if (result.ok) { refresh(); return null; }
          return result.error ?? '重试失败';
        },
      });
    }
    if (acquisitionHasActiveWork(item)) {
      actions.push({
        key: 'cancel',
        label: '停止处理',
        confirmLines: ['将请求停止这个内容正在进行的下载、转码或处理；已产生的产物不会自动删除。'],
        confirmLabel: '确认停止',
        run: async (): Promise<string | null> => {
          const result = await cancelAcquisitionWork(item, holder.get());
          holder.reset();
          if (result.ok) { refresh(); return null; }
          return result.error ?? '停止失败';
        },
      });
    }
    if (acquisitionCanDelete(item)) {
      actions.push({
        key: 'delete',
        label: '删除',
        danger: true,
        confirmLines: [
          '停止这个内容正在进行的下载、转码或处理。',
          '删除尚未入库的下载源文件、种子任务和临时缓存。',
          '已经成功入库到 Emby 的正式资源不会被删除。',
        ],
        confirmLabel: '确认删除',
        run: async (): Promise<string | null> => {
          const result = await deleteAcquisition(item, holder.get());
          holder.reset();
          if (result.ok) refresh();
          return null;
        },
      });
    }
    return actions;
  };

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-base font-semibold text-zinc-950">搜索添加任务</h2>
        <span className="text-xs text-zinc-500">仅显示通过搜索添加的任务 · 按更新时间倒序</span>
      </div>

      {acquisitions.isPending ? (
        <LoadingState label="正在读取搜索任务" />
      ) : acquisitions.error ? (
        <ErrorState message={acquisitions.error.message} onRetry={() => acquisitions.refetch()} />
      ) : acquisitions.data.items.length === 0 ? (
        <EmptyState title="暂无搜索任务" description="选择候选并创建获取后，任务将在此显示" />
      ) : (
        <>
          <div className="space-y-2 sm:hidden">
            {acquisitions.data.items.map((item) => {
              const failure = acquisitionFailureInfo(item);
              return (
                <article key={item.id} className="rounded-xl border border-zinc-200 bg-white p-4 shadow-card">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <Link
                        to="/acquisitions/$acquisitionId"
                        params={{ acquisitionId: item.id }}
                        search={{ from: listSource }}
                        state={appNavigationState(listSource)}
                        onClick={() => rememberListPosition(listSource)}
                        className="block whitespace-normal break-words font-medium text-zinc-900 hover:underline"
                      >
                        {item.mediaType === 'movie' ? `${item.movieTitle ?? '未命名电影'}${item.releaseYear ? ` (${item.releaseYear})` : ''}` : (item.seriesTitle ?? '未命名番剧')}
                      </Link>
                      <p className="mt-1 text-sm text-zinc-500">{item.mediaType === 'episode' ? episodeLabel(item.sourceSeason, item.sourceEpisode) : sourceKindLabel(item.sourceKind)}</p>
                    </div>
                    <AcquisitionStageBadge value={item.aggregateStatus} />
                  </div>
                  <div className="mt-3"><TaskProgress task={item} /></div>
                  <p className="mt-2 text-xs text-zinc-500">{formatDateTime(item.updatedAt)}</p>
                  {failure ? <TaskFailureSummary info={failure} className="mt-2 text-sm" /> : null}
                  <div className="mt-2 flex justify-end">
                    <Link to="/acquisitions/$acquisitionId" params={{ acquisitionId: item.id }} search={{ from: listSource }} state={appNavigationState(listSource)} onClick={() => rememberListPosition(listSource)} className="text-sm font-medium text-emerald-700 hover:underline">
                      查看详情
                    </Link>
                    <span className="ml-3"><RecordActions actions={rowActions(item)} onChanged={refresh} /></span>
                  </div>
                </article>
              );
            })}
          </div>
          <div className="hidden sm:block">
            <DataTable head={['内容', '进度', '更新时间', '操作']}>
              {acquisitions.data.items.map((item) => {
                const failure = acquisitionFailureInfo(item);
                return (
                  <tr key={item.id}>
                    <td className="max-w-0 px-4 py-3">
                      <Link
                        to="/acquisitions/$acquisitionId"
                        params={{ acquisitionId: item.id }}
                        search={{ from: listSource }}
                        state={appNavigationState(listSource)}
                        onClick={() => rememberListPosition(listSource)}
                        className="block whitespace-normal break-words font-medium text-zinc-900 hover:underline"
                      >
                        {item.mediaType === 'movie' ? `${item.movieTitle ?? '未命名电影'}${item.releaseYear ? ` (${item.releaseYear})` : ''}` : (item.seriesTitle ?? '未命名番剧')}
                      </Link>
                      {item.mediaType === 'episode' ? <span className="mt-0.5 block text-xs text-zinc-500">{episodeLabel(item.sourceSeason, item.sourceEpisode)}</span> : null}
                    </td>
                    <td className="min-w-52 px-4 py-3">
                      <div className="mb-2"><AcquisitionStageBadge value={item.aggregateStatus} /></div>
                      <TaskProgress task={item} compact />
                      {failure ? <TaskFailureSummary info={failure} className="mt-1 max-w-md text-xs" /> : null}
                    </td>
                    <td className="px-4 py-3 text-sm text-zinc-600">{formatDateTime(item.updatedAt)}</td>
                    <td className="px-4 py-3 text-right">
                      <div className="flex items-center justify-end gap-2">
                        <Link to="/acquisitions/$acquisitionId" params={{ acquisitionId: item.id }} search={{ from: listSource }} state={appNavigationState(listSource)} onClick={() => rememberListPosition(listSource)} className="text-sm font-medium text-emerald-700 hover:underline">
                          详情
                        </Link>
                        <RecordActions actions={rowActions(item)} onChanged={refresh} />
                      </div>
                    </td>
                  </tr>
                );
              })}
            </DataTable>
          </div>
          <PaginationControls
            canGoBack={pagination.canGoBack}
            hasNext={Boolean(acquisitions.data.nextCursor)}
            onPrevious={pagination.goPrevious}
            onNext={() => pagination.goNext(acquisitions.data.nextCursor)}
            isFetching={acquisitions.isFetching}
          />
        </>
      )}
    </section>
  );
}
