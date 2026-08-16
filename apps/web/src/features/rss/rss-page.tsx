import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocation, useNavigate, useSearch } from '@tanstack/react-router';
import { Plus, SearchIcon } from 'lucide-react';
import { useEffect, useState } from 'react';

import type { RssSubscription } from '@/api/generated/types.gen';
import { appNavigationState, currentAppLocation, rememberListPosition, useListScrollRestoration } from '@/app/navigation-context';
import { ContextLink } from '@/components/context-link';
import { archiveSubscription, fetchSubscriptions, type RssSubscriptionSortBy, type SortOrder } from '@/features/rss/api';
import { CreateSubscriptionForm } from '@/features/rss/create-subscription-form';
import { SubscriptionProgress } from '@/features/rss/subscription-progress';
import { DeletionBatchBar } from '@/features/deletions/deletion-batch-bar';
import { DeletionFeedback, type DeletionSubmission } from '@/features/deletions/deletion-feedback';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { DataTable, PageBody, PageHeader, PaginationControls } from '@/components/resource';
import { SortableColumnHeader } from '@/components/sortable-column-header';
import { RecordActions, type RecordAction } from '@/components/record-actions';
import { Button } from '@/components/ui/button';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { formatDateTime } from '@/lib/format';
import { useCursorPagination } from '@/lib/pagination';

export function RssPage() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const listSource = currentAppLocation(location.href);
  const search = useSearch({ strict: false }) as { query?: string; sortBy?: RssSubscriptionSortBy; sortOrder?: SortOrder };
  const sortBy = search.sortBy ?? 'name';
  const sortOrder = search.sortOrder ?? 'asc';
  const [queryInput, setQueryInput] = useState(search.query ?? '');
  const [creating, setCreating] = useState(false);
  useEffect(() => setQueryInput(search.query ?? ''), [search.query]);
  const [pendingId, setPendingId] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [deletions, setDeletions] = useState<DeletionSubmission[]>([]);
  const [batchRunning, setBatchRunning] = useState(false);
  const pagination = useCursorPagination();
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const subscriptions = useQuery({
    queryKey: ['rss', 'list', search.query, sortBy, sortOrder, pagination.cursor],
    queryFn: () => fetchSubscriptions(pagination.cursor, search.query, sortBy, sortOrder),
  });

  useListScrollRestoration(listSource, Boolean(subscriptions.data));

  const openDetail = (subscriptionId: string) => {
    rememberListPosition(listSource);
    void navigate({
      to: '/rss/$subscriptionId',
      params: { subscriptionId },
      search: { from: listSource },
      state: appNavigationState(listSource),
    });
  };

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['rss'] });
    void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
    void queryClient.invalidateQueries({ queryKey: ['tasks'] });
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
  };

  const items = subscriptions.data?.items ?? [];
  const selectedItems = items.filter((item) => selected.has(item.id));
  const allChecked = items.length > 0 && items.every((item) => selected.has(item.id));
  const toggleOne = (id: string) => setSelected((current) => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  const toggleAll = () => setSelected(allChecked ? new Set() : new Set(items.map((item) => item.id)));
  const changeSort = (field: RssSubscriptionSortBy) => {
    const nextOrder: SortOrder = sortBy === field && sortOrder === 'asc' ? 'desc' : 'asc';
    void navigate({
      to: '/rss',
      search: { ...search, sortBy: field, sortOrder: nextOrder, cursor: undefined, cursorStack: undefined },
    });
  };
  const submitQuery = () => {
    void navigate({
      to: '/rss',
      search: { ...search, query: queryInput.trim() || undefined, cursor: undefined, cursorStack: undefined },
    });
  };
  const sortHeader = (label: string, field: RssSubscriptionSortBy) => (
    <SortableColumnHeader key={field} label={label} field={field} activeField={sortBy} order={sortOrder} onSort={changeSort} />
  );
  const recordDeletion = (item: RssSubscription, operationId?: string, error?: string) => {
    setDeletions((current) => [
      ...current.filter((entry) => entry.resourceId !== item.id),
      { resourceId: item.id, label: item.name, operationId, error },
    ]);
  };
  const deleteSelected = async () => {
    setBatchRunning(true);
    const key = holder.get();
    try {
      for (const item of selectedItems) {
        try {
          const result = await archiveSubscription(item.id, item.version, `${key}:${item.id}`);
          recordDeletion(item, result.operationId);
        } catch (cause) {
          recordDeletion(item, undefined, cause instanceof Error ? cause.message : '删除订阅失败');
        }
      }
      holder.reset();
      setSelected(new Set());
      refresh();
    } finally {
      setBatchRunning(false);
    }
  };

  const rowActions = (item: RssSubscription): RecordAction[] => {
    const actions: RecordAction[] = [
      {
        key: 'detail',
        label: '查看详情',
        run: async () => {
          openDetail(item.id);
          return null;
        },
      },
    ];
    if ((item.retryableTaskCount ?? 0) > 0) {
      actions.push({
        key: 'retry-failed',
        label: `重试失败任务 (${item.retryableTaskCount})`,
        title: '前往订阅详情重试失败的任务',
        run: async () => {
          openDetail(item.id);
          return null;
        },
      });
    }
    const submitDeletion = async (deleteImported: boolean) => {
      setPendingId(item.id);
      try {
        const result = await archiveSubscription(item.id, item.version, holder.get(), deleteImported);
        holder.reset();
        recordDeletion(item, result.operationId);
        refresh();
        return null;
      } catch (cause) {
        holder.reset();
        recordDeletion(item, undefined, cause instanceof Error ? cause.message : '删除订阅失败');
        return null;
      } finally {
        setPendingId(null);
      }
    };
    actions.push({
      key: 'delete',
      label: pendingId === item.id ? '删除中…' : '删除订阅',
      danger: true,
      confirmLines: [
        '删除该 RSS 订阅，不再检查这个地址。',
        '停止并清空正在下载、转码或处理中的相关任务。',
        '删除 qBittorrent 种子、下载缓存和转码临时文件。',
        '已经成功入库到 Emby 的正式资源不会被删除。',
      ],
      confirmLabel: '确认删除',
      run: () => submitDeletion(false),
    });
    actions.push({
      key: 'delete-imported',
      label: pendingId === item.id ? '删除中…' : '删除订阅及入库资源',
      danger: true,
      confirmLines: [
        '删除该 RSS 订阅及其全部任务资源。',
        '删除 qBittorrent 种子、下载缓存和转码临时文件。',
        '删除本订阅已经入库的视频和 ASS 字幕，并刷新 Emby 媒体库。',
        '此操作无法恢复。',
      ],
      confirmLabel: '全部删除',
      run: () => submitDeletion(true),
    });
    return actions;
  };

  return (
    <PageBody>
      <PageHeader
        title="RSS 订阅"
        description="管理订阅源；发现新内容后自动下载并进入任务流程"
        actions={
          <Button type="button" onClick={() => setCreating((value) => !value)}>
            <Plus />
            新建订阅
          </Button>
        }
      />

      {creating ? <CreateSubscriptionForm onDone={() => setCreating(false)} /> : null}

      <form
        className="mb-3 flex w-full gap-2 sm:max-w-md"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
          submitQuery();
        }}
      >
        <label className="sr-only" htmlFor="rss-query">搜索订阅</label>
        <Input
          id="rss-query"
          value={queryInput}
          maxLength={256}
          placeholder="搜索订阅名称或作品"
          onChange={(event) => setQueryInput(event.target.value)}
        />
        <Button type="submit" variant="outline">
          <SearchIcon aria-hidden="true" />
          搜索
        </Button>
      </form>

      <DeletionFeedback items={deletions} onDismiss={() => setDeletions([])} onSettled={refresh} />
      {selectedItems.length > 0 ? (
        <DeletionBatchBar
          count={selectedItems.length}
          noun="订阅"
          running={batchRunning}
          lines={[
            '停止选中订阅及其正在运行的下载、转码和处理。',
            '删除关联任务、qBittorrent 项、源文件、临时文件和订阅记录。',
            '已经成功入库到 Emby 的正式资源不会被删除。',
          ]}
          onDelete={deleteSelected}
          onClear={() => setSelected(new Set())}
        />
      ) : null}

      {subscriptions.isPending ? (
        <LoadingState label="正在读取订阅" />
      ) : subscriptions.error ? (
        <ErrorState message={subscriptions.error.message} onRetry={() => subscriptions.refetch()} />
      ) : subscriptions.data.items.length === 0 ? (
        <EmptyState title="暂无订阅" description="创建 RSS 订阅以自动获取" />
      ) : (
        <>
          <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 border-y border-zinc-200 bg-white px-3 py-1 sm:hidden" role="group" aria-label="RSS 订阅列表控制">
            <label className="inline-flex min-h-8 items-center gap-1.5 text-xs font-medium text-zinc-600">
              <input type="checkbox" aria-label="全选当前页订阅" checked={allChecked} onChange={toggleAll} className="size-4 accent-emerald-700" />
              全选
            </label>
            {sortHeader('订阅', 'name')}
            {sortHeader('作品', 'series_title')}
            {sortHeader('对应季', 'source_season')}
            {sortHeader('总进度', 'progress')}
            {sortHeader('下次检查', 'next_poll_at')}
          </div>
          <div className="space-y-2 sm:hidden">
            {subscriptions.data.items.map((item) => (
              <article key={item.id} className="rounded-xl border border-zinc-200/90 bg-white p-4 shadow-card transition-shadow duration-200 hover:shadow-card-hover">
                <div className="flex items-start gap-3">
                  <input
                    type="checkbox"
                    aria-label={`选择 ${item.name}`}
                    checked={selected.has(item.id)}
                    onChange={() => toggleOne(item.id)}
                    className="mt-1 size-4 accent-emerald-700"
                  />
                  <div className="min-w-0 flex-1">
                    <ContextLink rememberList to="/rss/$subscriptionId" params={{ subscriptionId: item.id }} className="block break-words font-medium text-zinc-900 hover:underline">
                      {item.name}
                    </ContextLink>
                    <p className="mt-1 break-words text-sm text-zinc-500">{item.seriesTitle} · 第 {item.sourceSeason} 季</p>
                    <div className="mt-3"><SubscriptionProgress subscription={item} /></div>
                    <div className="mt-3 flex items-end justify-between gap-3">
                      <p className="min-w-0 text-xs text-zinc-500">下次检查 {item.completedAt ? '已完成' : item.enabled ? formatDateTime(item.nextPollAt) : '已暂停'}</p>
                      <RecordActions actions={rowActions(item)} onChanged={refresh} />
                    </div>
                  </div>
                </div>
              </article>
            ))}
          </div>
          <div className="hidden sm:block">
            <DataTable head={[
              <input key="all" type="checkbox" aria-label="全选当前页订阅" checked={allChecked} onChange={toggleAll} className="size-4 accent-emerald-700" />,
              sortHeader('订阅', 'name'),
              sortHeader('作品', 'series_title'),
              sortHeader('对应季', 'source_season'),
              sortHeader('总进度', 'progress'),
              sortHeader('下次检查', 'next_poll_at'),
              '操作',
            ]}>
              {subscriptions.data.items.map((item) => (
                <tr key={item.id}>
                  <td className="px-4 py-3">
                    <input
                      type="checkbox"
                      aria-label={`选择 ${item.name}`}
                      checked={selected.has(item.id)}
                      onChange={() => toggleOne(item.id)}
                      className="size-4 accent-emerald-700"
                    />
                  </td>
                  <td className="max-w-0 px-4 py-3">
                    <ContextLink rememberList to="/rss/$subscriptionId" params={{ subscriptionId: item.id }} className="block truncate font-medium text-zinc-900 hover:underline">
                      {item.name}
                    </ContextLink>
                  </td>
                  <td className="px-4 py-3 text-zinc-600">{item.seriesTitle}</td>
                  <td className="px-4 py-3 text-zinc-600">第 {item.sourceSeason} 季</td>
                  <td className="w-64 px-4 py-3"><SubscriptionProgress subscription={item} compact /></td>
                  <td className="px-4 py-3 text-zinc-600">{item.completedAt ? '已完成' : item.enabled ? formatDateTime(item.nextPollAt) : '已暂停'}</td>
                  <td className="w-12 px-2 py-3 text-right">
                    <RecordActions actions={rowActions(item)} onChanged={refresh} />
                  </td>
                </tr>
              ))}
            </DataTable>
          </div>
          <PaginationControls
            canGoBack={pagination.canGoBack}
            hasNext={Boolean(subscriptions.data.nextCursor)}
            onPrevious={pagination.goPrevious}
            onNext={() => pagination.goNext(subscriptions.data.nextCursor)}
            isFetching={subscriptions.isFetching}
          />
        </>
      )}
    </PageBody>
  );
}
