import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useLocation, useNavigate, useSearch } from '@tanstack/react-router';
import { useState } from 'react';

import type { Acquisition } from '@/api/generated/types.gen';
import { fetchAcquisitions, type AcquisitionPhase, type AcquisitionSortBy, type SortOrder } from '@/features/acquisitions/api';
import { TaskProgress } from '@/features/acquisitions/task-progress';
import { acquisitionCanDelete, acquisitionHasActiveWork, cancelAcquisitionWork, deleteAcquisition, retryAcquisition } from '@/features/acquisitions/acquisition-actions';
import { DeletionBatchBar } from '@/features/deletions/deletion-batch-bar';
import { DeletionFeedback, type DeletionSubmission } from '@/features/deletions/deletion-feedback';
import { acquisitionFailureInfo } from '@/features/tasks/task-failure';
import { TaskFailureSummary } from '@/features/tasks/task-failure-summary';
import { appNavigationState, currentAppLocation, rememberListPosition, useListScrollRestoration } from '@/app/navigation-context';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { DataTable, PageBody, PageHeader, PaginationControls } from '@/components/resource';
import { SortableColumnHeader } from '@/components/sortable-column-header';
import { RecordActions, type RecordAction } from '@/components/record-actions';
import { AcquisitionStageBadge } from '@/components/status-badge';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { FilterChip } from '@/components/ui/filter-chip';
import { formatDateTime } from '@/lib/format';
import { acquisitionStageFilters, episodeLabel, sourceKindLabel } from '@/lib/presentation';
import { useCursorPagination } from '@/lib/pagination';

export function AcquisitionsPage() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const search = useSearch({ strict: false }) as {
    sourceKind?: 'search' | 'rss' | 'manual';
    phase?: AcquisitionPhase;
    sortBy?: AcquisitionSortBy;
    sortOrder?: SortOrder;
  };
  const phase = search.phase ?? '';
  const sourceKind = search.sourceKind ?? '';
  const sortBy = search.sortBy ?? 'updated_at';
  const sortOrder = search.sortOrder ?? 'desc';
  const pagination = useCursorPagination();
  const listSource = currentAppLocation(location.href);
  const holder = useState(() => new IdempotencyKeyHolder())[0];
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [deletions, setDeletions] = useState<DeletionSubmission[]>([]);
  const [batchRunning, setBatchRunning] = useState(false);

  const acquisitions = useQuery({
    queryKey: ['acquisitions', 'list', sourceKind, phase, sortBy, sortOrder, pagination.cursor],
    queryFn: () => fetchAcquisitions(pagination.cursor, sourceKind, phase || undefined, sortBy, sortOrder),
  });

  useListScrollRestoration(listSource, Boolean(acquisitions.data));

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
    void queryClient.invalidateQueries({ queryKey: ['tasks'] });
    void queryClient.invalidateQueries({ queryKey: ['downloads'] });
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
  };

  const items = acquisitions.data?.items ?? [];
  const selectedItems = items.filter((item) => selected.has(item.id));
  const allChecked = items.length > 0 && items.every((item) => selected.has(item.id));

  const toggleOne = (id: string) => setSelected((current) => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  const toggleAll = () => setSelected(allChecked ? new Set() : new Set(items.map((item) => item.id)));
  const changeSort = (field: AcquisitionSortBy) => {
    const nextOrder: SortOrder = sortBy === field && sortOrder === 'asc' ? 'desc' : 'asc';
    void navigate({
      to: '/acquisitions',
      search: { ...search, sortBy: field, sortOrder: nextOrder, cursor: undefined, cursorStack: undefined },
    });
  };
  const sortHeader = (label: string, field: AcquisitionSortBy) => (
    <SortableColumnHeader label={label} field={field} activeField={sortBy} order={sortOrder} onSort={changeSort} />
  );
  const recordDeletion = (item: Acquisition, result: { ok: boolean; operationId?: string; error?: string }) => {
    const submission: DeletionSubmission = {
      resourceId: item.id,
      label: acquisitionTitle(item),
      operationId: result.operationId,
      error: result.ok ? undefined : (result.error ?? '删除失败'),
    };
    setDeletions((current) => [...current.filter((entry) => entry.resourceId !== item.id), submission]);
  };
  const deleteSelected = async () => {
    setBatchRunning(true);
    const key = holder.get();
    try {
      for (const item of selectedItems) {
        const result = await deleteAcquisition(item, `${key}:${item.id}`);
        recordDeletion(item, result);
      }
      holder.reset();
      setSelected(new Set());
      refresh();
    } finally {
      setBatchRunning(false);
    }
  };

  const rowActions = (item: Acquisition): RecordAction[] => {
    const failure = acquisitionFailureInfo(item);
    const actions: RecordAction[] = [
      {
        key: 'detail',
        label: failure ? '查看失败原因' : '查看详情',
        run: async () => {
          rememberListPosition(listSource);
          void navigate({
            to: '/acquisitions/$acquisitionId',
            params: { acquisitionId: item.id },
            search: { from: listSource },
            state: appNavigationState(listSource),
          });
          return null;
        },
      },
    ];
    if (failure?.canRetry) {
      actions.push({
        key: 'retry',
        label: failure.retryLabel ?? '重试任务',
        title: '重新执行失败的任务（复用原配置，创建新的执行记录）',
        run: async () => {
          const result = await retryAcquisition(item, holder.get());
          holder.reset();
          refresh();
          if (result.ok) { return null; }
          return result.error ?? '重试失败';
        },
      });
    }
    if (acquisitionHasActiveWork(item)) {
      actions.push({
        key: 'cancel',
        label: '停止处理',
        title: '请求停止正在进行的下载、转码或处理',
        confirmLines: ['将请求停止这个内容正在进行的下载、转码或处理；已产生的产物不会自动删除。'],
        confirmLabel: '确认停止',
        run: async () => {
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
        run: async () => {
          const result = await deleteAcquisition(item, holder.get());
          holder.reset();
          recordDeletion(item, result);
          if (result.ok) refresh();
          return null;
        },
      });
    }
    return actions;
  };

  return (
    <PageBody>
      <PageHeader
        title={phase === 'mapping_pending' ? '需要设置剧集对应关系' : '任务'}
        description={phase === 'mapping_pending' ? '这些内容需要确认集数后才能继续' : '查看内容从下载到入库的处理进度'}
        actions={phase === 'mapping_pending' ? <Link to="/acquisitions" search={{}} className="text-sm font-medium text-emerald-700 hover:underline">返回全部</Link> : undefined}
      />

      <div className="mb-4">
        <div className="flex flex-wrap gap-2" role="group" aria-label="阶段筛选">
        {acquisitionStageFilters.map((filter) => (
          <FilterChip
            key={filter.label}
            to="/acquisitions"
            search={{
              ...search,
              phase: filter.value || undefined,
              cursor: undefined,
              cursorStack: undefined,
            }}
            active={phase === filter.value || (filter.value === 'attention' && phase === 'mapping_pending')}
            label={filter.label}
          />
        ))}
        </div>
      </div>

      <DeletionFeedback items={deletions} onDismiss={() => setDeletions([])} onSettled={refresh} />
      {selectedItems.length > 0 ? (
        <DeletionBatchBar
          count={selectedItems.length}
          noun="任务"
          running={batchRunning}
          lines={[
            '停止选中内容正在进行的下载、转码或处理。',
            '删除关联的 qBittorrent 项、源文件、临时文件和工作流记录。',
            '已经成功入库到 Emby 的正式资源不会被删除。',
          ]}
          onDelete={deleteSelected}
          onClear={() => setSelected(new Set())}
        />
      ) : null}

      {acquisitions.isPending ? (
        <LoadingState label="正在读取处理进度" />
      ) : acquisitions.error ? (
        <ErrorState message={acquisitions.error.message} onRetry={() => acquisitions.refetch()} />
      ) : acquisitions.data.items.length === 0 ? (
        <EmptyState title="暂无处理内容" description="通过搜索添加或 RSS 订阅开始" />
      ) : (
        <>
          <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 border-y border-zinc-200 bg-white px-3 py-1 sm:hidden" role="group" aria-label="任务列表控制">
            <label className="inline-flex min-h-8 items-center gap-1.5 text-xs font-medium text-zinc-600">
              <input type="checkbox" aria-label="全选当前页任务" checked={allChecked} onChange={toggleAll} className="size-4 accent-emerald-700" />
              全选
            </label>
            {sortHeader('内容', 'content')}
            {sortHeader('添加方式', 'source_kind')}
            {sortHeader('整体进度', 'progress')}
            {sortHeader('最近更新', 'updated_at')}
          </div>
          <div className="space-y-2 sm:hidden">
            {acquisitions.data.items.map((item) => {
              const failure = acquisitionFailureInfo(item);
              return (
              <article key={item.id} className="rounded-xl border border-zinc-200/90 bg-white p-4 shadow-card transition-shadow duration-200 hover:shadow-card-hover">
                <div className="flex items-start gap-3">
                  <input
                    type="checkbox"
                    aria-label={`选择 ${acquisitionTitle(item)}`}
                    checked={selected.has(item.id)}
                    onChange={() => toggleOne(item.id)}
                    className="mt-1 size-4 accent-emerald-700"
                  />
                  <div className="min-w-0 flex-1">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <Link
                      to="/acquisitions/$acquisitionId"
                      params={{ acquisitionId: item.id }}
                      search={{ from: listSource }}
                      state={appNavigationState(listSource)}
                      onClick={() => rememberListPosition(listSource)}
                      className="block truncate font-medium text-zinc-900 hover:underline"
                    >
                      {item.mediaType === 'movie' ? `${item.movieTitle ?? '未命名电影'}${item.releaseYear ? ` (${item.releaseYear})` : ''}` : (item.seriesTitle ?? '未命名番剧')}
                    </Link>
                    <p className="mt-1 text-sm text-zinc-500">{item.mediaType === 'episode' ? episodeLabel(item.sourceSeason, item.sourceEpisode) : sourceKindLabel(item.sourceKind)}</p>
                    {item.sourceTitle ? <p className="mt-1 line-clamp-2 break-words text-xs text-zinc-600">{item.sourceTitle}</p> : null}
                  </div>
                  <AcquisitionStageBadge value={item.aggregateStatus} />
                </div>
                <div className="mt-3"><TaskProgress task={item} /></div>
                <p className="mt-2 text-xs text-zinc-500">{sourceKindLabel(item.sourceKind)} · {formatDateTime(item.updatedAt)}</p>
                {failure ? <TaskFailureSummary info={failure} className="mt-2 text-sm" /> : null}
                <div className="mt-2 flex justify-end">
                  <RecordActions actions={rowActions(item)} onChanged={refresh} />
                </div>
                </div>
                </div>
              </article>
              );
            })}
          </div>
          <div className="hidden sm:block">
          <DataTable head={[
            <input key="all" type="checkbox" aria-label="全选当前页任务" checked={allChecked} onChange={toggleAll} className="size-4 accent-emerald-700" />,
            sortHeader('内容', 'content'),
            sortHeader('添加方式', 'source_kind'),
            sortHeader('整体进度', 'progress'),
            sortHeader('最近更新', 'updated_at'),
            '操作',
          ]}>
            {acquisitions.data.items.map((item) => {
              const failure = acquisitionFailureInfo(item);
              return (
              <tr key={item.id}>
                <td className="px-4 py-3">
                  <input
                    type="checkbox"
                    aria-label={`选择 ${acquisitionTitle(item)}`}
                    checked={selected.has(item.id)}
                    onChange={() => toggleOne(item.id)}
                    className="size-4 accent-emerald-700"
                  />
                </td>
                <td className="max-w-0 px-4 py-3">
                  <Link
                    to="/acquisitions/$acquisitionId"
                    params={{ acquisitionId: item.id }}
                    search={{ from: listSource }}
                    state={appNavigationState(listSource)}
                    onClick={() => rememberListPosition(listSource)}
                    className="block truncate font-medium text-zinc-900 hover:underline"
                  >
                    {item.mediaType === 'movie' ? `${item.movieTitle ?? '未命名电影'}${item.releaseYear ? ` (${item.releaseYear})` : ''}` : (item.seriesTitle ?? '未命名番剧')}
                  </Link>
                  {item.mediaType === 'episode' ? <span className="mt-0.5 block text-xs text-zinc-500">{episodeLabel(item.sourceSeason, item.sourceEpisode)}</span> : null}
                  {item.sourceTitle ? <span className="mt-1 block max-w-xl truncate text-xs text-zinc-600" title={item.sourceTitle}>{item.sourceTitle}</span> : null}
                </td>
                <td className="px-4 py-3 text-zinc-600">{sourceKindLabel(item.sourceKind)}</td>
                <td className="min-w-52 px-4 py-3">
                  <div className="mb-2"><AcquisitionStageBadge value={item.aggregateStatus} /></div>
                  <TaskProgress task={item} compact />
                  {failure ? <TaskFailureSummary info={failure} className="mt-1 max-w-md text-xs" /> : null}
                </td>
                <td className="px-4 py-3 text-zinc-600">{formatDateTime(item.updatedAt)}</td>
                <td className="w-12 px-2 py-3 text-right">
                  <RecordActions actions={rowActions(item)} onChanged={refresh} />
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
    </PageBody>
  );
}

function acquisitionTitle(item: Acquisition): string {
  return item.mediaType === 'movie'
    ? `${item.movieTitle ?? '未命名电影'}${item.releaseYear ? ` (${item.releaseYear})` : ''}`
    : (item.seriesTitle ?? '未命名番剧');
}
