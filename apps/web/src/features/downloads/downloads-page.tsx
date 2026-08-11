import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useLocation, useNavigate, useSearch } from '@tanstack/react-router';
import { Search as SearchIcon } from 'lucide-react';
import { useEffect, useState } from 'react';

import type { Download } from '@/api/generated/types.gen';
import { currentAppLocation, useListScrollRestoration } from '@/app/navigation-context';
import { ContextLink } from '@/components/context-link';
import { DataTable, PageBody, PageHeader, PaginationControls } from '@/components/resource';
import { SortableColumnHeader, type SortOrder } from '@/components/sortable-column-header';
import { RecordActions, type RecordAction } from '@/components/record-actions';
import { StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { deleteAcquisitionCommand } from '@/features/acquisitions/api';
import { DeletionFeedback, type DeletionSubmission } from '@/features/deletions/deletion-feedback';
import { cancelDownloadCommand, fetchDownloads, retryDownloadCommand } from '@/features/downloads/api';
import { downloadDisplayStatus, downloadFollowupLabel, downloadRetryLabel } from '@/features/downloads/download-presentation';
import { formatDateTime, formatPercent } from '@/lib/format';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { useCursorPagination } from '@/lib/pagination';
import { clientStateLabel } from '@/lib/presentation';

type DownloadPhase = 'active' | 'waiting' | 'downloading' | 'paused' | 'completed' | 'failed';
type DownloadListSearch = {
  cursor?: string;
  cursorStack?: string;
  status?: Download['status'];
  phase?: DownloadPhase;
  query?: string;
  sortBy?: DownloadSortBy;
  sortOrder?: SortOrder;
};
type DownloadSortBy = 'attempt' | 'status' | 'client_state' | 'progress' | 'updated_at';

const phaseFilters: { value: DownloadPhase | ''; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'waiting', label: '等待中' },
  { value: 'downloading', label: '下载中' },
  { value: 'paused', label: '已暂停' },
  { value: 'completed', label: '已完成' },
  { value: 'failed', label: '失败' },
];

export function DownloadsPage() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const search = useSearch({ strict: false }) as DownloadListSearch;
  const listSource = currentAppLocation(location.href);
  const pagination = useCursorPagination();
  const [queryInput, setQueryInput] = useState(search.query ?? '');
  const [pendingId, setPendingId] = useState<string | null>(null);
  const [deletions, setDeletions] = useState<DeletionSubmission[]>([]);
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  useEffect(() => setQueryInput(search.query ?? ''), [search.query]);

  const downloads = useQuery({
    queryKey: ['downloads', 'list', pagination.cursor, search.status, search.phase, search.query, search.sortBy, search.sortOrder],
    queryFn: () => fetchDownloads(pagination.cursor, {
      status: search.status,
      phase: search.phase,
      query: search.query,
      sortBy: search.sortBy ?? 'updated_at',
      sortOrder: search.sortOrder ?? 'desc',
    }),
  });

  useListScrollRestoration(listSource, Boolean(downloads.data));

  const updateSearch = (patch: Partial<DownloadListSearch>) => {
    void navigate({
      to: '/downloads',
      search: {
        ...search,
        ...patch,
        cursor: undefined,
        cursorStack: undefined,
      },
    });
  };

  const sortBy = search.sortBy ?? 'updated_at';
  const sortOrder = search.sortOrder ?? 'desc';
  const changeSort = (field: DownloadSortBy) => {
    const nextOrder: SortOrder = sortBy === field && sortOrder === 'asc' ? 'desc' : 'asc';
    updateSearch({ sortBy: field, sortOrder: nextOrder });
  };
  const sortHeader = (label: string, field: DownloadSortBy) => (
    <SortableColumnHeader key={field} label={label} field={field} activeField={sortBy} order={sortOrder} onSort={changeSort} />
  );

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['downloads'] });
    void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
  };

  const run = async (download: Download, action: () => Promise<unknown>): Promise<string | null> => {
    setPendingId(download.id);
    try {
      await action();
      holder.reset();
      refresh();
      return null;
    } catch (cause) {
      return cause instanceof Error ? cause.message : '操作失败';
    } finally {
      setPendingId(null);
    }
  };

  const runDelete = async (download: Download): Promise<string | null> => {
    setPendingId(download.id);
    try {
      const result = await deleteAcquisitionCommand(download.acquisitionId, holder.get());
      holder.reset();
      setDeletions((current) => [
        ...current.filter((item) => item.resourceId !== download.acquisitionId),
        { resourceId: download.acquisitionId, label: `下载记录 ${download.attempt}`, operationId: result.operationId },
      ]);
      refresh();
      return null;
    } catch (cause) {
      holder.reset();
      setDeletions((current) => [
        ...current.filter((item) => item.resourceId !== download.acquisitionId),
        { resourceId: download.acquisitionId, label: `下载记录 ${download.attempt}`, error: cause instanceof Error ? cause.message : '删除下载失败' },
      ]);
      return null;
    } finally {
      setPendingId(null);
    }
  };

  const rowActions = (download: Download): RecordAction[] => {
    const actions: RecordAction[] = [];
    if (download.actions.canRetry) {
      const retryLabel = downloadRetryLabel(download);
      actions.push({
        key: 'retry',
        label: retryLabel,
        title: retryLabel === '重试准备处理' ? '重新生成媒体处理任务' : '重新执行这个下载',
        disabled: pendingId === download.id,
        run: () => run(download, () => retryDownloadCommand(download.id, holder.get(), download.version)),
      });
    }
    if (download.actions.canCancel) {
      actions.push({
        key: 'cancel',
        label: '停止',
        title: '停止当前下载，保留已下载的文件',
        disabled: pendingId === download.id,
        confirmLines: ['将停止当前下载，但保留已经下载的文件。'],
        confirmLabel: '确认停止',
        run: () => run(download, () => cancelDownloadCommand(download.id, holder.get(), download.version)),
      });
    }
    if (download.actions.canDelete) {
      actions.push({
        key: 'delete',
        label: '删除',
        danger: true,
        disabled: pendingId === download.id,
        confirmLines: [
          '将删除这条下载所属的完整任务流程。',
          '未被其他内容使用的 qBittorrent 任务、源文件和临时文件也会删除。',
          '已经成功入库到 Emby 的正式资源不会被删除。',
        ],
        confirmLabel: '确认删除',
        run: () => runDelete(download),
      });
    }
    return actions;
  };

  return (
    <PageBody>
      <PageHeader title="下载" description="查看 qBittorrent 下载状态并处理暂停、失败或已完成的记录" />

      <div className="mb-4 space-y-3">
        <div className="overflow-x-auto" aria-label="下载阶段筛选">
          <div className="flex min-w-max border-b border-zinc-200">
            {phaseFilters.map((filter) => {
              const active = filter.value ? search.phase === filter.value : !search.phase && !search.status;
              return (
                <Link
                  key={filter.value || 'all'}
                  to="/downloads"
                  search={{
                    ...search,
                    status: undefined,
                    phase: filter.value || undefined,
                    cursor: undefined,
                    cursorStack: undefined,
                  }}
                  className={active
                    ? 'border-b-2 border-emerald-700 px-3 py-2 text-sm font-medium text-emerald-800'
                    : 'border-b-2 border-transparent px-3 py-2 text-sm text-zinc-600 hover:border-zinc-300 hover:text-zinc-950'}
                >
                  {filter.label}
                </Link>
              );
            })}
          </div>
        </div>

        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <form
            className="flex w-full gap-2 sm:max-w-md"
            role="search"
            onSubmit={(event) => {
              event.preventDefault();
              updateSearch({ query: queryInput.trim() || undefined });
            }}
          >
            <label className="sr-only" htmlFor="download-query">搜索下载</label>
            <Input
              id="download-query"
              value={queryInput}
              maxLength={512}
              placeholder="搜索作品、记录编号或种子"
              onChange={(event) => setQueryInput(event.target.value)}
            />
            <Button type="submit" variant="outline">
              <SearchIcon aria-hidden="true" />
              搜索
            </Button>
          </form>

        </div>
      </div>

      <DeletionFeedback items={deletions} onDismiss={() => setDeletions([])} onSettled={refresh} />

      {downloads.isPending ? (
        <LoadingState label="正在读取下载" />
      ) : downloads.error ? (
        <ErrorState message={downloads.error.message} onRetry={() => downloads.refetch()} />
      ) : downloads.data.items.length === 0 ? (
        <EmptyState title="暂无下载" description="当前筛选条件下没有下载" />
      ) : (
        <>
          <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 border-y border-zinc-200 bg-white px-3 py-1 sm:hidden" role="group" aria-label="下载排序">
            {sortHeader('下载', 'attempt')}
            {sortHeader('状态', 'status')}
            {sortHeader('qBittorrent', 'client_state')}
            {sortHeader('进度', 'progress')}
            {sortHeader('最近更新', 'updated_at')}
          </div>
          <div className="space-y-2 sm:hidden">
            {downloads.data.items.map((download) => (
              <article key={download.id} className="rounded-lg border border-zinc-200 bg-white p-3">
                <div className="flex min-w-0 items-start justify-between gap-2">
                  <div className="min-w-0">
                    <ContextLink rememberList to="/downloads/$downloadId" params={{ downloadId: download.id }} className="block truncate font-medium text-zinc-900 hover:underline">
                      下载记录 {download.attempt}
                    </ContextLink>
                    <ContextLink rememberList to="/acquisitions/$acquisitionId" params={{ acquisitionId: download.acquisitionId }} className="mt-1 block text-xs text-zinc-500 hover:underline">
                      查看关联任务
                    </ContextLink>
                  </div>
                  <RecordActions actions={rowActions(download)} onChanged={refresh} />
                </div>
                <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 text-xs text-zinc-500">
                  <StatusBadge value={downloadDisplayStatus(download)} />
                  {downloadFollowupLabel(download) ? <span className="font-medium text-amber-700">{downloadFollowupLabel(download)}</span> : null}
                  <span>{clientStateLabel(download.clientState)}</span>
                  <span>{formatPercent(download.progress)}</span>
                  <span>{formatDateTime(download.updatedAt)}</span>
                </div>
              </article>
            ))}
          </div>

          <div className="hidden sm:block">
            <DataTable head={[
              sortHeader('下载', 'attempt'),
              sortHeader('状态', 'status'),
              sortHeader('qBittorrent', 'client_state'),
              sortHeader('进度', 'progress'),
              sortHeader('最近更新', 'updated_at'),
              '操作',
            ]}>
              {downloads.data.items.map((download) => (
                <tr key={download.id}>
                  <td className="max-w-0 px-4 py-3">
                    <ContextLink rememberList to="/downloads/$downloadId" params={{ downloadId: download.id }} className="block truncate font-medium text-zinc-900 hover:underline">
                      下载记录 {download.attempt}
                    </ContextLink>
                    <ContextLink rememberList to="/acquisitions/$acquisitionId" params={{ acquisitionId: download.acquisitionId }} className="mt-0.5 block text-xs text-zinc-500 hover:underline">
                      查看关联任务
                    </ContextLink>
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge value={downloadDisplayStatus(download)} />
                    {downloadFollowupLabel(download) ? <p className="mt-1 whitespace-nowrap text-xs font-medium text-amber-700">{downloadFollowupLabel(download)}</p> : null}
                  </td>
                  <td className="px-4 py-3 text-zinc-600">{clientStateLabel(download.clientState)}</td>
                  <td className="px-4 py-3 text-zinc-600">{formatPercent(download.progress)}</td>
                  <td className="px-4 py-3 text-zinc-600">{formatDateTime(download.updatedAt)}</td>
                  <td className="w-12 px-2 py-3 text-right">
                    <RecordActions actions={rowActions(download)} onChanged={refresh} />
                  </td>
                </tr>
              ))}
            </DataTable>
          </div>
          <PaginationControls
            canGoBack={pagination.canGoBack}
            hasNext={Boolean(downloads.data.nextCursor)}
            onPrevious={pagination.goPrevious}
            onNext={() => pagination.goNext(downloads.data.nextCursor)}
            isFetching={downloads.isFetching}
          />
        </>
      )}
    </PageBody>
  );
}
