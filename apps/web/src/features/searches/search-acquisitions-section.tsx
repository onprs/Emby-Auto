import { useQuery } from '@tanstack/react-query';
import { Link, useLocation } from '@tanstack/react-router';

import { appNavigationState, currentAppLocation, rememberListPosition } from '@/app/navigation-context';
import { fetchAcquisitions } from '@/features/acquisitions/api';
import { TaskProgress } from '@/features/acquisitions/task-progress';
import { DataTable, PaginationControls } from '@/components/resource';
import { AcquisitionStageBadge } from '@/components/status-badge';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { formatDateTime } from '@/lib/format';
import { episodeLabel, sourceKindLabel } from '@/lib/presentation';
import { useCursorPagination } from '@/lib/pagination';

export function SearchAcquisitionsSection() {
  const location = useLocation();
  const listSource = currentAppLocation(location.href);
  const pagination = useCursorPagination();

  const acquisitions = useQuery({
    queryKey: ['acquisitions', 'search-tasks', pagination.cursor],
    queryFn: () => fetchAcquisitions(pagination.cursor, 'search', undefined, 'updated_at', 'desc'),
  });

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
            {acquisitions.data.items.map((item) => (
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
                    <p className="mt-1 text-sm text-zinc-500">{item.mediaType === 'episode' ? episodeLabel(item.sourceSeason, item.sourceEpisode, item.sourceEpisodeFractionHundredths) : sourceKindLabel(item.sourceKind)}</p>
                  </div>
                  <AcquisitionStageBadge value={item.aggregateStatus} />
                </div>
                <div className="mt-3"><TaskProgress task={item} /></div>
                <p className="mt-2 text-xs text-zinc-500">{formatDateTime(item.updatedAt)}</p>
                <div className="mt-3 flex justify-end">
                  <Link
                    to="/acquisitions/$acquisitionId"
                    params={{ acquisitionId: item.id }}
                    search={{ from: listSource }}
                    state={appNavigationState(listSource)}
                    onClick={() => rememberListPosition(listSource)}
                    className="text-sm font-medium text-emerald-700 hover:underline"
                  >
                    查看详情
                  </Link>
                </div>
              </article>
            ))}
          </div>
          <div className="hidden sm:block">
            <DataTable head={['内容', '进度', '更新时间', '操作']}>
              {acquisitions.data.items.map((item) => (
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
                    {item.mediaType === 'episode' ? <span className="mt-0.5 block text-xs text-zinc-500">{episodeLabel(item.sourceSeason, item.sourceEpisode, item.sourceEpisodeFractionHundredths)}</span> : null}
                  </td>
                  <td className="min-w-52 px-4 py-3">
                    <div className="mb-2"><AcquisitionStageBadge value={item.aggregateStatus} /></div>
                    <TaskProgress task={item} compact />
                  </td>
                  <td className="px-4 py-3 text-sm text-zinc-600">{formatDateTime(item.updatedAt)}</td>
                  <td className="px-4 py-3 text-right">
                    <Link
                      to="/acquisitions/$acquisitionId"
                      params={{ acquisitionId: item.id }}
                      search={{ from: listSource }}
                      state={appNavigationState(listSource)}
                      onClick={() => rememberListPosition(listSource)}
                      className="text-sm font-medium text-emerald-700 hover:underline"
                    >
                      详情
                    </Link>
                  </td>
                </tr>
              ))}
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
