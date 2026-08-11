import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocation, useNavigate, useSearch } from '@tanstack/react-router';
import { Link2, RefreshCw, ShieldCheck } from 'lucide-react';
import { useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import type { RssEntry, RssSubscription } from '@/api/generated/types.gen';
import { appNavigationState, currentAppLocation, useListScrollRestoration } from '@/app/navigation-context';
import { ContextLink } from '@/components/context-link';
import { TaskProgress } from '@/features/acquisitions/task-progress';
import { archiveSubscription, fetchEntries, fetchSubscription, pollSubscription, updateSubscription, type RssEntrySortBy, type SortOrder } from '@/features/rss/api';
import { formatKeywordInput, parseKeywordInput } from '@/features/rss/keyword-input';
import { SubscriptionProgress } from '@/features/rss/subscription-progress';
import { DeletionFeedback, type DeletionSubmission } from '@/features/deletions/deletion-feedback';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { DataTable, DetailErrorState, DetailGrid, DetailLoadingState, PageBody, PageHeader, PaginationControls } from '@/components/resource';
import { SortableColumnHeader } from '@/components/sortable-column-header';
import { StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ConfirmDialog } from '@/components/ui/confirm-dialog';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { formatDateTime } from '@/lib/format';
import { decisionSourceLabel, episodeLabel, friendlyError, reasonLabel } from '@/lib/presentation';
import { useCursorPagination } from '@/lib/pagination';

export function RssDetailPage({ subscriptionId }: { subscriptionId: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const listSource = currentAppLocation(location.href);
  const search = useSearch({ strict: false }) as {
    status?: 'discovered' | 'enqueueing' | 'enqueued' | 'enqueue_failed';
    sortBy?: RssEntrySortBy;
    sortOrder?: SortOrder;
  };
  const sortBy = search.sortBy ?? 'discovered_at';
  const sortOrder = search.sortOrder ?? 'desc';
  const subscription = useQuery({
    queryKey: ['rss', subscriptionId],
    queryFn: () => fetchSubscription(subscriptionId),
  });
  const entriesPagination = useCursorPagination();
  const skippedPagination = useLocalCursorPagination();
  const entries = useQuery({
    queryKey: ['rss', subscriptionId, 'entries', 'confirmed', entriesPagination.cursor, search.status, sortBy, sortOrder],
    queryFn: () => fetchEntries(subscriptionId, entriesPagination.cursor, search.status, 'confirmed', sortBy, sortOrder),
  });
  const skippedEntries = useQuery({
    queryKey: ['rss', subscriptionId, 'entries', 'skipped', skippedPagination.cursor],
    queryFn: () => fetchEntries(subscriptionId, skippedPagination.cursor, undefined, 'skipped', 'discovered_at', 'desc'),
  });

  useListScrollRestoration(listSource, Boolean(entries.data));

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['rss', subscriptionId] });
  };
  const changeSort = (field: RssEntrySortBy) => {
    const nextOrder: SortOrder = sortBy === field && sortOrder === 'asc' ? 'desc' : 'asc';
    void navigate({
      to: '/rss/$subscriptionId',
      params: { subscriptionId },
      search: { ...search, sortBy: field, sortOrder: nextOrder, cursor: undefined, cursorStack: undefined },
    });
  };
  const sortHeader = (label: string, field: RssEntrySortBy) => (
    <SortableColumnHeader key={field} label={label} field={field} activeField={sortBy} order={sortOrder} onSort={changeSort} />
  );

  if (subscription.isPending) {
    return <DetailLoadingState title="订阅详情" label="正在读取订阅" />;
  }
  if (!subscription.data) {
    return <DetailErrorState title="订阅详情" message={subscription.error?.message ?? '无法读取订阅'} onRetry={() => subscription.refetch()} />;
  }
  const value = subscription.data;

  return (
    <PageBody>
      <PageHeader title={value.name} description={value.seriesTitle} actions={<StatusBadge value={value.completedAt ? 'completed' : value.enabled ? 'running' : 'download_paused'} />} />

      <section className="border-y border-zinc-200 py-5" aria-labelledby="rss-overall-progress-heading">
        <h2 id="rss-overall-progress-heading" className="mb-3 text-base font-semibold text-zinc-950">订阅总进度</h2>
        <SubscriptionProgress subscription={value} />
      </section>

      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>订阅信息</CardTitle>
          </CardHeader>
          <CardContent>
            <DetailGrid
              items={[
                { label: 'RSS 地址', value: value.feedUrl },
                { label: '包含词', value: value.includeKeywords.length > 0 ? value.includeKeywords.join('、') : '不限' },
                { label: '不包含词', value: value.excludeKeywords.length > 0 ? value.excludeKeywords.join('、') : '无' },
                { label: '自动剧集映射', value: value.autoEpisodeMapping ? '已开启' : '已关闭' },
                { label: '自动审核', value: value.autoReview ? '已开启' : '已关闭' },
                { label: '完结后源文件', value: value.cleanupSourceOnCompletion ? '删除种子和缓存' : '保留' },
                { label: '对应季', value: `第 ${value.sourceSeason} 季` },
                { label: '检查频率', value: `每 ${Math.max(1, Math.round(value.pollIntervalSeconds / 60))} 分钟` },
                { label: '上次检查', value: formatDateTime(value.lastPolledAt) },
                ...(value.completedAt ? [{ label: '完成时间', value: formatDateTime(value.completedAt) }] : []),
                { label: '下次检查', value: value.completedAt ? '已完成' : value.enabled ? formatDateTime(value.nextPollAt) : '已暂停' },
              ]}
            />
          </CardContent>
        </Card>

        <SubscriptionCommands subscription={value} onChanged={refresh} />
      </div>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>已确认的 RSS 任务</CardTitle>
          <CardDescription>已通过订阅规则并进入任务确认或处理流程</CardDescription>
        </CardHeader>
        <CardContent>
          {entries.isPending ? (
            <LoadingState label="正在读取条目" />
          ) : entries.error ? (
            <ErrorState message={entries.error.message} onRetry={() => entries.refetch()} />
          ) : entries.data.items.length === 0 ? (
            <EmptyState title="暂无已确认任务" description="等待 RSS 轮询确认新任务" />
          ) : (
            <>
              <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 border-y border-zinc-200 px-1 py-1 sm:hidden" role="group" aria-label="RSS 条目排序">
                {sortHeader('内容', 'title')}
                {sortHeader('集数', 'episode')}
                {sortHeader('处理进度', 'progress')}
                {sortHeader('发现时间', 'discovered_at')}
              </div>
              <div className="divide-y divide-zinc-200 border-y border-zinc-200 sm:hidden">
                {entries.data.items.map((entry) => (
                  <div key={entry.id} className="py-4">
                    <EntryTitle entry={entry} className="block break-words font-medium text-zinc-900" />
                    <p className="mt-1 text-sm text-zinc-500">{episodeLabel(entry.sourceSeason, entry.sourceEpisode)} · {formatDateTime(entry.createdAt)}</p>
                    {entry.coordinateSource ? <p className="mt-1 text-xs text-zinc-500">{decisionSourceLabel(entry.coordinateSource)}</p> : null}
                    {entryWasSkipped(entry) ? (
                      <p className="mt-2 text-sm text-zinc-500">已跳过{entry.rejectReason ? `：${reasonLabel(entry.rejectReason)}` : ''}</p>
                    ) : (
                      <div className="mt-3"><EntryProgress entry={entry} /></div>
                    )}
                  </div>
                ))}
              </div>
              <div className="hidden sm:block">
              <DataTable head={[
                sortHeader('内容', 'title'),
                sortHeader('集数', 'episode'),
                sortHeader('处理进度', 'progress'),
                sortHeader('发现时间', 'discovered_at'),
              ]}>
                {entries.data.items.map((entry) => (
                  <tr key={entry.id}>
                    <td className="max-w-0 px-4 py-3">
                      <EntryTitle entry={entry} className="block truncate font-medium text-zinc-900" />
                      {entryWasSkipped(entry) && entry.rejectReason ? <p className="mt-0.5 truncate text-xs text-zinc-500">已跳过：{reasonLabel(entry.rejectReason)}</p> : null}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-zinc-600">{episodeLabel(entry.sourceSeason, entry.sourceEpisode)}</td>
                    <td className="w-64 px-4 py-3">
                      <EntryProgress entry={entry} />
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-zinc-600">{formatDateTime(entry.createdAt)}</td>
                  </tr>
                ))}
              </DataTable>
              </div>
              <PaginationControls
                canGoBack={entriesPagination.canGoBack}
                hasNext={Boolean(entries.data.nextCursor)}
                onPrevious={entriesPagination.goPrevious}
                onNext={() => entriesPagination.goNext(entries.data.nextCursor)}
                isFetching={entries.isFetching}
              />
            </>
          )}
        </CardContent>
      </Card>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>已跳过的 RSS 更新</CardTitle>
          <CardDescription>未通过过滤词或其他订阅规则，不会创建下载任务</CardDescription>
        </CardHeader>
        <CardContent>
          {skippedEntries.isPending ? (
            <LoadingState label="正在读取已跳过更新" />
          ) : skippedEntries.error ? (
            <ErrorState message={skippedEntries.error.message} onRetry={() => skippedEntries.refetch()} />
          ) : skippedEntries.data.items.length === 0 ? (
            <EmptyState title="暂无已跳过更新" description="当前 RSS 更新均已通过订阅规则" />
          ) : (
            <>
              <div className="divide-y divide-zinc-200 border-y border-zinc-200 sm:hidden">
                {skippedEntries.data.items.map((entry) => (
                  <div key={entry.id} className="py-4">
                    <EntryTitle entry={entry} className="block break-words font-medium text-zinc-900" />
                    <p className="mt-1 text-sm text-zinc-500">{episodeLabel(entry.sourceSeason, entry.sourceEpisode)} · {formatDateTime(entry.createdAt)}</p>
                    {entry.coordinateSource ? <p className="mt-1 text-xs text-zinc-500">{decisionSourceLabel(entry.coordinateSource)}</p> : null}
                    <p className="mt-2 text-sm text-zinc-600">{reasonLabel(entry.rejectReason) || '不符合自动获取规则'}</p>
                  </div>
                ))}
              </div>
              <div className="hidden sm:block">
                <DataTable head={['内容', '集数', '跳过原因', '发现时间']}>
                  {skippedEntries.data.items.map((entry) => (
                    <tr key={entry.id}>
                      <td className="max-w-0 px-4 py-3"><EntryTitle entry={entry} className="block truncate font-medium text-zinc-900" /></td>
                      <td className="whitespace-nowrap px-4 py-3 text-zinc-600">{episodeLabel(entry.sourceSeason, entry.sourceEpisode)}</td>
                      <td className="px-4 py-3 text-zinc-600">
                        {reasonLabel(entry.rejectReason) || '不符合自动获取规则'}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-zinc-600">{formatDateTime(entry.createdAt)}</td>
                    </tr>
                  ))}
                </DataTable>
              </div>
              <PaginationControls
                canGoBack={skippedPagination.canGoBack}
                hasNext={Boolean(skippedEntries.data.nextCursor)}
                onPrevious={skippedPagination.goPrevious}
                onNext={() => skippedPagination.goNext(skippedEntries.data.nextCursor)}
                isFetching={skippedEntries.isFetching}
              />
            </>
          )}
        </CardContent>
      </Card>
    </PageBody>
  );
}

function useLocalCursorPagination() {
  const [cursor, setCursor] = useState<string | undefined>();
  const [stack, setStack] = useState<Array<string | undefined>>([]);

  return {
    cursor,
    canGoBack: stack.length > 0,
    goNext(nextCursor: string | null | undefined) {
      if (!nextCursor) return;
      setStack((current) => [...current, cursor]);
      setCursor(nextCursor);
    },
    goPrevious() {
      setCursor(stack.at(-1));
      setStack(stack.slice(0, -1));
    },
  };
}

function EntryTitle({ entry, className }: { entry: RssEntry; className: string }) {
  if (entry.acquisitionId) {
    return (
      <ContextLink
        rememberList
        to="/acquisitions/$acquisitionId"
        params={{ acquisitionId: entry.acquisitionId }}
        className={`${className} hover:underline`}
        title={entry.title}
      >
        {entry.title}
      </ContextLink>
    );
  }
  return <span className={className} title={entry.title}>{entry.title}</span>;
}

function entryWasSkipped(entry: RssEntry): boolean {
  return entry.classification === 'rejected' || entry.classification === 'unconsumable';
}

function EntryProgress({ entry }: { entry: RssEntry }) {
  if (entry.status === 'enqueue_failed') {
    return <span className="text-sm text-red-700">{friendlyError(entry.errorCode, entry.errorMessage)}</span>;
  }
  if (entryWasSkipped(entry)) {
    return <span className="text-sm text-zinc-500">已跳过</span>;
  }
  if (entry.acquisitionProgress) {
    return <TaskProgress task={entry.acquisitionProgress} compact ariaLabel={`${entry.title}处理进度`} />;
  }
  return <StatusBadge value={entry.status} />;
}

function SubscriptionCommands({ subscription, onChanged }: { subscription: RssSubscription; onChanged: () => void }) {
  const navigate = useNavigate();
  const location = useLocation();
  const source = currentAppLocation(location.href);
  const queryClient = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleteImported, setDeleteImported] = useState(false);
  const [deletions, setDeletions] = useState<DeletionSubmission[]>([]);
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const command = useMutation({
    mutationFn: async (action: 'poll' | 'toggle' | 'auto-mapping' | 'auto-review' | 'delete') => {
      setError(null);
      try {
        if (action === 'poll') {
          return await pollSubscription(subscription.id, holder.get());
        }
        if (action === 'toggle' || action === 'auto-mapping' || action === 'auto-review') {
          return await updateSubscription(subscription.id, {
            expectedVersion: subscription.version,
            mappingProfileId: subscription.mappingProfileId,
            name: subscription.name,
            feedUrl: subscription.feedUrl,
            includeKeywords: subscription.includeKeywords,
            excludeKeywords: subscription.excludeKeywords,
            enabled: action === 'toggle' ? !subscription.enabled : subscription.enabled,
            autoEpisodeMapping: action === 'auto-mapping' ? !subscription.autoEpisodeMapping : subscription.autoEpisodeMapping,
            autoReview: action === 'auto-review' ? !subscription.autoReview : subscription.autoReview,
            cleanupSourceOnCompletion: subscription.cleanupSourceOnCompletion,
            sourceSeason: subscription.sourceSeason,
            pollIntervalSeconds: subscription.pollIntervalSeconds,
          });
        }
        return await archiveSubscription(subscription.id, subscription.version, holder.get(), deleteImported);
      } catch (cause) {
        if (cause instanceof ApiFailure && cause.isConflict) {
          holder.reset();
          onChanged();
        }
        throw cause;
      }
    },
    onSuccess: (result, action) => {
      holder.reset();
      setConfirmDelete(false);
      setDeleteImported(false);
      if (action === 'delete') {
        void queryClient.invalidateQueries({ queryKey: ['rss', 'list'] });
        void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
        void queryClient.invalidateQueries({ queryKey: ['tasks'] });
        void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
        if (result && 'operationId' in result) {
          setDeletions([{ resourceId: subscription.id, label: subscription.name, operationId: result.operationId }]);
        }
        return;
      }
      onChanged();
      if (action === 'poll' && result && 'operationId' in result) {
        void navigate({
          to: '/operations/$operationId',
          params: { operationId: result.operationId },
          search: { from: source },
          state: appNavigationState(source),
        });
      }
    },
    onError: (cause) => setError(cause instanceof ApiFailure ? friendlyError(cause.code, cause.message) : cause instanceof Error ? cause.message : '操作失败'),
  });

  return (
    <Card>
      <CardHeader><CardTitle>操作</CardTitle></CardHeader>
      <CardContent>
        <DeletionFeedback
          items={deletions}
          onDismiss={() => setDeletions([])}
          onSettled={() => {
            void queryClient.invalidateQueries({ queryKey: ['rss', 'list'] });
            void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
          }}
        />
        {error ? <ErrorState className="mb-4" message={error} /> : null}
        <ConfirmDialog
          open={confirmDelete}
          title="确认删除订阅"
          danger
          lines={deleteImported ? [
            '删除该 RSS 订阅及其全部任务资源。',
            '删除 qBittorrent 种子、下载缓存和转码临时文件。',
            '删除本订阅已经入库的视频和 ASS 字幕，并刷新 Emby 媒体库。',
          ] : [
            '删除该 RSS 订阅，不再检查这个地址。',
            '停止并清空正在下载、转码或处理中的相关任务。',
            '删除 qBittorrent 种子、下载缓存和转码临时文件。',
            '已经成功入库到 Emby 的正式资源不会被删除。',
          ]}
          confirmLabel={deleteImported ? '全部删除' : '确认删除'}
          running={command.isPending}
          onConfirm={() => command.mutate('delete')}
          onCancel={() => {
            setConfirmDelete(false);
            setDeleteImported(false);
          }}
        >
          <label className="mb-4 flex items-start gap-2.5 rounded border border-red-200 bg-red-50 px-3 py-2.5 text-sm text-red-900">
            <input
              type="checkbox"
              className="mt-0.5 size-4 shrink-0 accent-red-600"
              checked={deleteImported}
              onChange={(event) => setDeleteImported(event.target.checked)}
            />
            <span>同时删除已经入库到 Emby 的视频和 ASS 字幕</span>
          </label>
        </ConfirmDialog>
        <div className="flex flex-wrap gap-2">
          {!subscription.completedAt ? (
            <>
              <Button type="button" onClick={() => command.mutate('poll')} disabled={command.isPending}><RefreshCw />立即检查</Button>
              <Button type="button" variant="outline" onClick={() => command.mutate('auto-mapping')} disabled={command.isPending}>
                <Link2 />{subscription.autoEpisodeMapping ? '关闭自动映射' : '开启自动映射'}
              </Button>
              <Button type="button" variant="outline" onClick={() => command.mutate('auto-review')} disabled={command.isPending}>
                <ShieldCheck />{subscription.autoReview ? '关闭自动审核' : '开启自动审核'}
              </Button>
              <Button type="button" variant="outline" onClick={() => command.mutate('toggle')} disabled={command.isPending}>{subscription.enabled ? '禁用' : '启用'}</Button>
            </>
          ) : null}
          <Button type="button" variant="outline" onClick={() => setEditing((value) => !value)}>编辑</Button>
          <Button type="button" variant="outline" onClick={() => setConfirmDelete(true)} disabled={command.isPending}>删除</Button>
        </div>
        {editing ? <EditSubscriptionForm subscription={subscription} onDone={() => { setEditing(false); onChanged(); }} /> : null}
      </CardContent>
    </Card>
  );
}

function EditSubscriptionForm({ subscription, onDone }: { subscription: RssSubscription; onDone: () => void }) {
  const [name, setName] = useState(subscription.name);
  const [feedUrl, setFeedUrl] = useState(subscription.feedUrl);
  const [includeKeywords, setIncludeKeywords] = useState(formatKeywordInput(subscription.includeKeywords));
  const [excludeKeywords, setExcludeKeywords] = useState(formatKeywordInput(subscription.excludeKeywords));
  const [sourceSeason, setSourceSeason] = useState(String(subscription.sourceSeason));
  const [pollIntervalMinutes, setPollIntervalMinutes] = useState(String(Math.max(1, Math.round(subscription.pollIntervalSeconds / 60))));
  const [autoEpisodeMapping, setAutoEpisodeMapping] = useState(subscription.autoEpisodeMapping);
  const [cleanupSourceOnCompletion, setCleanupSourceOnCompletion] = useState(subscription.cleanupSourceOnCompletion);
  const [error, setError] = useState<string | null>(null);

  const update = useMutation({
    mutationFn: () => updateSubscription(subscription.id, {
      expectedVersion: subscription.version,
      mappingProfileId: subscription.mappingProfileId,
      name: name.trim(),
      feedUrl: feedUrl.trim(),
      includeKeywords: parseKeywordInput(includeKeywords),
      excludeKeywords: parseKeywordInput(excludeKeywords),
      enabled: subscription.enabled,
      autoEpisodeMapping,
      autoReview: subscription.autoReview,
      cleanupSourceOnCompletion,
      sourceSeason: Number(sourceSeason),
      pollIntervalSeconds: Number(pollIntervalMinutes) * 60,
    }),
    onSuccess: () => onDone(),
    onError: (cause) => setError(cause instanceof ApiFailure ? friendlyError(cause.code, cause.message) : cause instanceof Error ? cause.message : '更新失败'),
  });

  const invalid = !name.trim() || !feedUrl.trim() || Number(sourceSeason) < 1 || Number(pollIntervalMinutes) < 1;

  return (
    <div className="mt-4 grid gap-4 border-t border-zinc-200 pt-4 sm:grid-cols-2">
      <div className="space-y-2"><Label htmlFor="edit-rss-name">名称</Label><Input id="edit-rss-name" value={name} onChange={(event) => setName(event.target.value)} /></div>
      <div className="space-y-2"><Label htmlFor="edit-rss-feed">RSS 地址</Label><Input id="edit-rss-feed" type="url" value={feedUrl} onChange={(event) => setFeedUrl(event.target.value)} /></div>
      <div className="space-y-2"><Label htmlFor="edit-rss-include-keywords">包含词</Label><Input id="edit-rss-include-keywords" value={includeKeywords} onChange={(event) => setIncludeKeywords(event.target.value)} placeholder="例如：简日, 1080p" /></div>
      <div className="space-y-2"><Label htmlFor="edit-rss-exclude-keywords">不包含词</Label><Input id="edit-rss-exclude-keywords" value={excludeKeywords} onChange={(event) => setExcludeKeywords(event.target.value)} placeholder="例如：720p, 合集" /></div>
      <div className="space-y-2"><Label htmlFor="edit-rss-season">对应第几季</Label><Input id="edit-rss-season" type="number" min={1} value={sourceSeason} onChange={(event) => setSourceSeason(event.target.value)} /></div>
      <div className="space-y-2"><Label htmlFor="edit-rss-interval">每隔多少分钟检查</Label><Input id="edit-rss-interval" type="number" min={1} max={1440} value={pollIntervalMinutes} onChange={(event) => setPollIntervalMinutes(event.target.value)} /></div>
      <label className="flex items-start gap-2.5 border border-zinc-200 bg-zinc-50 px-3 py-2.5 text-sm text-zinc-800 sm:col-span-2">
        <input
          type="checkbox"
          className="mt-0.5 size-4 shrink-0 accent-emerald-700"
          checked={autoEpisodeMapping}
          onChange={(event) => setAutoEpisodeMapping(event.target.checked)}
        />
        <span>下载文件确认后自动完成剧集映射，无法唯一判断时使用已启用的 Agent</span>
      </label>
      <label className="flex items-start gap-2.5 border border-zinc-200 bg-zinc-50 px-3 py-2.5 text-sm text-zinc-800 sm:col-span-2">
        <input
          type="checkbox"
          className="mt-0.5 size-4 shrink-0 accent-emerald-700"
          checked={cleanupSourceOnCompletion}
          onChange={(event) => setCleanupSourceOnCompletion(event.target.checked)}
        />
        <span>最终集入库后，删除对应的 qBittorrent 种子和缓存文件</span>
      </label>
      {error ? <div className="sm:col-span-2"><ErrorState message={error} /></div> : null}
      <div className="flex gap-2 sm:col-span-2">
        <Button type="button" onClick={() => update.mutate()} disabled={update.isPending || invalid}>保存</Button>
        <Button type="button" variant="outline" onClick={onDone}>取消</Button>
      </div>
    </div>
  );
}
