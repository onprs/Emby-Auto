import { useQuery } from '@tanstack/react-query';
import { useLocation, useSearch } from '@tanstack/react-router';

import type { Operation } from '@/api/generated/types.gen';
import { currentAppLocation, useListScrollRestoration } from '@/app/navigation-context';
import { ContextLink } from '@/components/context-link';
import { fetchOperations } from '@/features/operations/api';
import { DataTable, PageBody, PageHeader, PaginationControls } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { FilterChip } from '@/components/ui/filter-chip';
import { formatDateTime, formatDuration } from '@/lib/format';
import { friendlyError, operationLabel, resourceTypeLabel } from '@/lib/presentation';
import { useCursorPagination } from '@/lib/pagination';
import { sanitizeTechnicalDetails } from '@/lib/sanitize';

const statusFilters = [
  { value: '', label: '全部' },
  { value: 'running', label: '运行中' },
  { value: 'succeeded', label: '成功' },
  { value: 'failed', label: '失败' },
] as const;

export function OperationsPage() {
  const location = useLocation();
  const listSource = currentAppLocation(location.href);
  const search = useSearch({ strict: false }) as { status?: string; resourceType?: string; resourceId?: string };
  const status = search.status ?? '';
  const pagination = useCursorPagination();

  const operations = useQuery({
    queryKey: ['operations', 'list', status, search.resourceType, search.resourceId, pagination.cursor],
    queryFn: () => fetchOperations(pagination.cursor, { status, resourceType: search.resourceType, resourceId: search.resourceId }),
  });

  useListScrollRestoration(listSource, Boolean(operations.data));

  return (
    <PageBody>
      <PageHeader title="运行记录" description="最近的处理步骤与执行结果" />

      <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label="状态筛选">
        {statusFilters.map((filter) => (
          <FilterChip
            key={filter.label}
            to="/operations"
            search={{ status: filter.value || undefined, resourceType: search.resourceType, resourceId: search.resourceId }}
            active={status === filter.value}
            label={filter.label}
          />
        ))}
      </div>

      {operations.isPending ? (
        <LoadingState label="正在读取运行记录" />
      ) : operations.error ? (
        <ErrorState message={operations.error.message} onRetry={() => operations.refetch()} />
      ) : operations.data.items.length === 0 ? (
        <EmptyState title="暂无运行记录" />
      ) : (
        <>
          <div className="space-y-2 sm:hidden">
            {operations.data.items.map((operation) => (
              <ContextLink rememberList key={operation.id} to="/operations/$operationId" params={{ operationId: operation.id }} className="block rounded-xl border border-zinc-200/90 bg-white p-4 shadow-card transition-all duration-200 hover:-translate-y-0.5 hover:shadow-card-hover">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <p className="truncate font-medium text-zinc-900">{operationLabel(operation.kind)}</p>
                    <p className="mt-1 text-sm text-zinc-500">{resourceTypeLabel(operation.resourceType)}</p>
                  </div>
                  <StatusBadge value={operation.status} />
                </div>
                <p className="mt-2 text-xs text-zinc-500">
                  {formatDateTime(operation.startedAt ?? operation.createdAt)} · 耗时 {operationDuration(operation)}
                </p>
                {operation.status === 'failed' ? <p className="mt-2 text-sm text-red-700">{friendlyError(operation.errorCode, operation.errorMessage)}</p> : null}
              </ContextLink>
            ))}
          </div>
          <div className="hidden sm:block">
            <DataTable head={['任务类型', '关联内容', '状态', '开始时间', '耗时', '执行结果']}>
              {operations.data.items.map((operation) => (
                <tr key={operation.id}>
                  <td className="max-w-0 px-4 py-3">
                    <ContextLink rememberList to="/operations/$operationId" params={{ operationId: operation.id }} className="block truncate font-medium text-zinc-900 hover:underline">
                      {operationLabel(operation.kind)}
                    </ContextLink>
                  </td>
                  <td className="px-4 py-3 text-zinc-600">
                    {operation.resourceHref ? (
                      <ContextLink rememberList to={operation.resourceHref} className="hover:underline">
                        {resourceTypeLabel(operation.resourceType)}
                      </ContextLink>
                    ) : (
                      resourceTypeLabel(operation.resourceType)
                    )}
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge value={operation.status} />
                  </td>
                  <td className="px-4 py-3 text-zinc-600">{formatDateTime(operation.startedAt ?? operation.createdAt)}</td>
                  <td className="px-4 py-3 text-zinc-600">{operationDuration(operation)}</td>
                  <td className="max-w-0 px-4 py-3">
                    <OperationResult operation={operation} />
                  </td>
                </tr>
              ))}
            </DataTable>
          </div>
          <PaginationControls
            canGoBack={pagination.canGoBack}
            hasNext={Boolean(operations.data.nextCursor)}
            onPrevious={pagination.goPrevious}
            onNext={() => pagination.goNext(operations.data.nextCursor)}
            isFetching={operations.isFetching}
          />
        </>
      )}
    </PageBody>
  );
}

function operationDuration(operation: Operation): string {
  if (operation.finishedAt) {
    return formatDuration(operation.startedAt ?? operation.createdAt, operation.finishedAt);
  }
  return operation.status === 'running' ? '进行中' : '—';
}

function OperationResult({ operation }: { operation: Operation }) {
  if (operation.status !== 'failed') {
    return <span className="text-zinc-600">{operation.status === 'succeeded' ? '成功' : '—'}</span>;
  }
  return (
    <div className="max-w-md">
      <p className="text-sm text-red-700">{friendlyError(operation.errorCode, operation.errorMessage)}</p>
      {operation.errorMessage ? (
        <details className="mt-1">
          <summary className="cursor-pointer text-xs text-zinc-500 hover:text-zinc-700">查看详细错误</summary>
          <p className="mt-1 break-words text-xs text-zinc-500">{sanitizeTechnicalDetails(operation.errorMessage)}</p>
        </details>
      ) : null}
    </div>
  );
}
