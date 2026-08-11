import { useQuery } from '@tanstack/react-query';
import { useLocation, useNavigate, useSearch } from '@tanstack/react-router';
import { useEffect, useState } from 'react';

import { currentAppLocation, useListScrollRestoration } from '@/app/navigation-context';
import { ContextLink } from '@/components/context-link';
import { fetchLibraryItems } from '@/features/emby/api';
import { DataTable, PageBody, PageHeader, PaginationControls } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { FilterChip } from '@/components/ui/filter-chip';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { formatDateTime } from '@/lib/format';
import { useCursorPagination } from '@/lib/pagination';
import { embyItemTypeLabel } from '@/lib/presentation';

const typeFilters = [
  { value: '', label: '全部' },
  { value: 'Series', label: '剧集' },
  { value: 'Season', label: '季' },
  { value: 'Episode', label: '集' },
  { value: 'Movie', label: '电影' },
] as const;

export function EmbyLibraryDetailPage({ libraryId }: { libraryId: string }) {
  const navigate = useNavigate();
  const location = useLocation();
  const listSource = currentAppLocation(location.href);
  const search = useSearch({ strict: false }) as {
    itemType?: 'Series' | 'Season' | 'Episode' | 'Movie';
    name?: string;
    providerId?: string;
    present?: 'true' | 'false';
    from?: string;
  };
  const [name, setName] = useState(search.name ?? '');
  const [providerId, setProviderId] = useState(search.providerId ?? '');
  const pagination = useCursorPagination();

  useEffect(() => setName(search.name ?? ''), [search.name]);
  useEffect(() => setProviderId(search.providerId ?? ''), [search.providerId]);

  const items = useQuery({
    queryKey: ['emby-library', libraryId, 'items', search, pagination.cursor],
    queryFn: () => fetchLibraryItems(libraryId, pagination.cursor, {
      itemType: search.itemType,
      name: search.name,
      providerId: search.providerId,
      present: search.present === undefined ? undefined : search.present === 'true',
    }),
  });

  useListScrollRestoration(listSource, Boolean(items.data));

  return (
    <PageBody>
      <PageHeader title="媒体库条目" description="按类型筛选条目" />

      <form
        className="mb-4 grid gap-2 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]"
        onSubmit={(event) => {
          event.preventDefault();
          void navigate({
            to: '/emby/libraries/$libraryId',
            params: { libraryId },
            search: {
              ...search,
              name: name.trim() || undefined,
              providerId: providerId.trim() || undefined,
              cursor: undefined,
              cursorStack: undefined,
            },
          });
        }}
      >
        <Input aria-label="名称筛选" value={name} onChange={(event) => setName(event.target.value)} placeholder="名称" />
        <Input aria-label="Provider ID 筛选" value={providerId} onChange={(event) => setProviderId(event.target.value)} placeholder="TMDb / TVDb / IMDb ID" />
        <Button type="submit" variant="outline">筛选</Button>
      </form>
      <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label="类型与存在状态筛选">
        {typeFilters.map((filter) => (
          <FilterChip
            key={filter.label}
            to="/emby/libraries/$libraryId"
            params={{ libraryId }}
            search={{ ...search, itemType: filter.value || undefined, cursor: undefined, cursorStack: undefined }}
            active={search.itemType === (filter.value || undefined)}
            label={filter.label}
          />
        ))}
        <FilterChip
          to="/emby/libraries/$libraryId"
          params={{ libraryId }}
          search={{ ...search, present: search.present === 'true' ? undefined : 'true', cursor: undefined, cursorStack: undefined }}
          active={search.present === 'true'}
          label="当前存在"
        />
        <FilterChip
          to="/emby/libraries/$libraryId"
          params={{ libraryId }}
          search={{ ...search, present: search.present === 'false' ? undefined : 'false', cursor: undefined, cursorStack: undefined }}
          active={search.present === 'false'}
          label="已移除"
        />
      </div>

      {items.isPending ? (
        <LoadingState label="正在读取条目" />
      ) : items.error ? (
        <ErrorState message={items.error.message} onRetry={() => items.refetch()} />
      ) : items.data.items.length === 0 ? (
        <EmptyState title="暂无条目" />
      ) : (
        <>
          <DataTable head={['名称', '类型', '季/集', 'Provider IDs', '状态', '关联任务', '最近出现']}>
            {items.data.items.map((item) => (
              <tr key={item.id}>
                <td className="max-w-0 truncate px-4 py-3 font-medium text-zinc-900" title={item.name}>
                  {item.name}
                </td>
                <td className="px-4 py-3 text-zinc-600">{embyItemTypeLabel(item.itemType)}</td>
                <td className="px-4 py-3 text-zinc-600">
                  {item.seasonNumber !== undefined && item.episodeNumber !== undefined ? `S${item.seasonNumber}E${item.episodeNumber}` : item.seasonNumber !== undefined ? `S${item.seasonNumber}` : '—'}
                </td>
                <td className="max-w-48 truncate px-4 py-3 font-mono text-xs text-zinc-600">{Object.values(item.providerIds).join(' · ') || '—'}</td>
                <td className="px-4 py-3">{item.present ? <StatusBadge value="mapped" /> : <StatusBadge value="cancelled" />}</td>
                <td className="px-4 py-3">{item.importedTaskId ? <ContextLink rememberList to="/tasks/$taskId" params={{ taskId: item.importedTaskId }} className="text-emerald-700 hover:underline">查看任务</ContextLink> : '—'}</td>
                <td className="px-4 py-3 text-zinc-600">{formatDateTime(item.lastSeenAt)}</td>
              </tr>
            ))}
          </DataTable>
          <PaginationControls
            canGoBack={pagination.canGoBack}
            hasNext={Boolean(items.data.nextCursor)}
            onPrevious={pagination.goPrevious}
            onNext={() => pagination.goNext(items.data.nextCursor)}
            isFetching={items.isFetching}
          />
        </>
      )}
    </PageBody>
  );
}
