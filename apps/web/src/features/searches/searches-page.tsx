import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useLocation, useNavigate, useSearch } from '@tanstack/react-router';
import { Search as SearchIcon } from 'lucide-react';
import { useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import { appNavigationState, currentAppLocation, rememberListPosition, useListScrollRestoration } from '@/app/navigation-context';
import { ContextLink } from '@/components/context-link';
import { fetchSearches, startSearch } from '@/features/searches/api';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { DataTable, PageBody, PageHeader, PaginationControls } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { FilterChip } from '@/components/ui/filter-chip';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { formatDateTime } from '@/lib/format';
import { friendlyError } from '@/lib/presentation';
import { useCursorPagination } from '@/lib/pagination';

const searchStatusFilters = [
  { value: '', label: '全部' },
  { value: 'running', label: '进行中' },
  { value: 'completed', label: '已完成' },
  { value: 'failed', label: '失败' },
] as const;

export function SearchesPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const listSource = currentAppLocation(location.href);
  const listSearch = useSearch({ strict: false }) as { status?: 'queued' | 'running' | 'completed' | 'failed' | 'cancelled'; query?: string };
  const queryClient = useQueryClient();
  const [query, setQuery] = useState('');
  const [error, setError] = useState<string | null>(null);
  const holder = useState(() => new IdempotencyKeyHolder())[0];
  const pagination = useCursorPagination();

  const searches = useQuery({
    queryKey: ['searches', 'list', pagination.cursor, listSearch.status, listSearch.query],
    queryFn: () => fetchSearches(pagination.cursor, listSearch.status ?? '', listSearch.query),
  });

  useListScrollRestoration(listSource, Boolean(searches.data));

  const start = useMutation({
    mutationFn: (keywords: string) => startSearch(holder.get(), keywords),
    onSuccess: (result) => {
      holder.reset();
      setQuery('');
      void queryClient.invalidateQueries({ queryKey: ['searches'] });
      const searchId = result.search?.id;
      if (searchId) {
        rememberListPosition(listSource);
        void navigate({
          to: '/searches/$searchId',
          params: { searchId },
          search: { from: listSource },
          state: appNavigationState(listSource),
        });
      }
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        holder.reset();
      }
      setError(cause instanceof Error ? cause.message : '创建搜索失败');
    },
  });

  return (
    <PageBody>
      <PageHeader title="搜索添加" description="搜索资源，选择后加入任务流程" />

      <form
        className="mb-6 flex max-w-xl items-end gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          setError(null);
          if (query.trim()) {
            start.mutate(query.trim());
          }
        }}
      >
        <div className="flex-1 space-y-2">
          <Label htmlFor="search-query">关键词</Label>
          <Input
            id="search-query"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="输入番剧名称或发布关键词"
          />
        </div>
        <Button type="submit" disabled={start.isPending || !query.trim()}>
          <SearchIcon />
          搜索
        </Button>
      </form>
      {error ? <ErrorState className="mb-4" message={error} /> : null}

      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-base font-semibold text-zinc-950">最近搜索</h2>
        <div className="flex flex-wrap gap-2" role="group" aria-label="搜索状态筛选">
          {searchStatusFilters.map((filter) => (
            <FilterChip
              key={filter.label}
              to="/searches"
              search={{
                ...listSearch,
                status: filter.value || undefined,
                cursor: undefined,
                cursorStack: undefined,
              }}
              active={listSearch.status === (filter.value || undefined)}
              label={filter.label}
            />
          ))}
        </div>
      </div>

      {searches.isPending ? (
        <LoadingState label="正在读取搜索历史" />
      ) : searches.error ? (
        <ErrorState message={searches.error.message} onRetry={() => searches.refetch()} />
      ) : searches.data.items.length === 0 ? (
        <EmptyState title="暂无搜索" description="提交一次关键词搜索以开始" />
      ) : (
        <>
          <DataTable head={['关键词', '状态', '结果', '搜索时间']}>
            {searches.data.items.map((run) => (
              <tr key={run.id}>
                <td className="max-w-0 px-4 py-3">
                  <ContextLink rememberList to="/searches/$searchId" params={{ searchId: run.id }} className="block truncate font-medium text-zinc-900 hover:underline">
                    {run.query}
                  </ContextLink>
                </td>
                <td className="px-4 py-3">
                  <StatusBadge value={run.status} />
                </td>
                <td className="max-w-0 truncate px-4 py-3 text-zinc-600">{run.errorMessage ? friendlyError(run.errorCode, run.errorMessage) : '—'}</td>
                <td className="px-4 py-3 text-zinc-600">{formatDateTime(run.createdAt)}</td>
              </tr>
            ))}
          </DataTable>
          <PaginationControls
            canGoBack={pagination.canGoBack}
            hasNext={Boolean(searches.data.nextCursor)}
            onPrevious={pagination.goPrevious}
            onNext={() => pagination.goNext(searches.data.nextCursor)}
            isFetching={searches.isFetching}
          />
        </>
      )}
    </PageBody>
  );
}
