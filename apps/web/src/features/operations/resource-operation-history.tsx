import { useQuery } from '@tanstack/react-query';

import { ContextLink } from '@/components/context-link';
import { DataTable } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { fetchOperations } from '@/features/operations/api';
import { formatDateTime } from '@/lib/format';
import { friendlyError, operationLabel } from '@/lib/presentation';

export function ResourceOperationHistory({ resourceType, resourceId }: { resourceType: string; resourceId: string }) {
  const operations = useQuery({
    queryKey: ['operations', 'resource', resourceType, resourceId],
    queryFn: () => fetchOperations(undefined, { resourceType, resourceId }),
  });

  return (
    <Card>
      <CardHeader><CardTitle>运行记录</CardTitle></CardHeader>
      <CardContent>
        {operations.isPending ? (
          <LoadingState label="正在读取运行记录" />
        ) : operations.error ? (
          <ErrorState message={operations.error.message} onRetry={() => operations.refetch()} />
        ) : operations.data.items.length === 0 ? (
          <EmptyState title="暂无运行记录" />
        ) : (
          <DataTable head={['类型', '状态', '尝试', '错误', '更新时间']}>
            {operations.data.items.map((operation) => (
              <tr key={operation.id}>
                <td className="px-4 py-3"><ContextLink to="/operations/$operationId" params={{ operationId: operation.id }} className="font-medium text-zinc-900 hover:underline">{operationLabel(operation.kind)}</ContextLink></td>
                <td className="px-4 py-3"><StatusBadge value={operation.status} /></td>
                <td className="px-4 py-3 text-zinc-600">{operation.attemptCount}/{operation.maxAttempts}</td>
                <td className="max-w-64 truncate px-4 py-3 text-zinc-600" title={operation.errorMessage ?? ''}>
                  {operation.status === 'failed' ? friendlyError(operation.errorCode, operation.errorMessage) : '—'}
                </td>
                <td className="px-4 py-3 text-zinc-600">{formatDateTime(operation.updatedAt)}</td>
              </tr>
            ))}
          </DataTable>
        )}
      </CardContent>
    </Card>
  );
}
