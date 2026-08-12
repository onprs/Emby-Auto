import { useQuery } from '@tanstack/react-query';

import { ContextLink } from '@/components/context-link';
import { fetchOperation } from '@/features/operations/api';
import { EventHistory } from '@/features/events/event-history';
import { DataTable, DetailErrorState, DetailGrid, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { ErrorState } from '@/components/ui/feedback';
import { formatDateTime } from '@/lib/format';
import { friendlyError, operationLabel } from '@/lib/presentation';
import { sanitizeTechnicalDetails } from '@/lib/sanitize';

export function OperationDetailPage({ operationId }: { operationId: string }) {
  const operation = useQuery({
    queryKey: ['operation', operationId],
    queryFn: () => fetchOperation(operationId),
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status === 'queued' || status === 'running' ? 3_000 : false;
    },
  });

  if (operation.isPending) {
    return <DetailLoadingState title="运行详情" label="正在读取处理进度" />;
  }
  if (operation.error || !operation.data) {
    return <DetailErrorState title="运行详情" message={operation.error?.message ?? '无法读取处理进度'} onRetry={() => operation.refetch()} />;
  }
  const value = operation.data;

  return (
    <PageBody>
      <PageHeader title={operationLabel(value.kind)} description="这项工作由后台自动完成，可以离开本页" actions={<StatusBadge value={value.status} />} />

      {value.errorMessage ? <ErrorState className="mb-6" message={friendlyError(value.errorCode, value.errorMessage)} /> : null}

      <Card>
        <CardHeader>
          <CardTitle>处理进度</CardTitle>
        </CardHeader>
        <CardContent>
          <DetailGrid
            items={[
              { label: '当前状态', value: <StatusBadge value={value.status} /> },
              {
                label: '关联资源',
                value: value.resourceHref ? <ContextLink to={value.resourceHref} className="text-emerald-700 hover:underline">查看相关内容</ContextLink> : '后台维护工作',
              },
              { label: '开始时间', value: formatDateTime(value.startedAt ?? value.createdAt) },
              { label: '完成时间', value: value.finishedAt ? formatDateTime(value.finishedAt) : '处理中' },
            ]}
          />
        </CardContent>
      </Card>

      <details className="mt-6 border-y border-zinc-200 py-4">
        <summary className="cursor-pointer text-sm font-medium text-zinc-700">诊断信息</summary>
        <DetailGrid items={[
          { label: '原始操作类型', value: value.kind },
          { label: '操作 ID', value: value.id },
          { label: '尝试次数', value: `${value.attemptCount}/${value.maxAttempts}` },
          { label: '幂等键', value: value.idempotencyKey },
          { label: '最近心跳', value: formatDateTime(value.heartbeatAt) },
          { label: '原始错误码', value: value.errorCode ?? '—' },
        ]} />
        {value.attempts.length > 0 ? (
          <div className="mt-6">
            <DataTable head={['尝试', '状态', '错误', '开始', '心跳', '完成']}>
              {value.attempts.map((attempt) => {
                const technicalError = attempt.errorMessage ? sanitizeTechnicalDetails(attempt.errorMessage) : '—';
                return (
                <tr key={attempt.id}>
                  <td className="px-4 py-3 text-zinc-600">{attempt.attempt}</td>
                  <td className="px-4 py-3"><StatusBadge value={attempt.status} /></td>
                  <td className="max-w-0 truncate px-4 py-3 text-zinc-600" title={technicalError === '—' ? '' : technicalError}>{technicalError}</td>
                  <td className="px-4 py-3 text-zinc-600">{formatDateTime(attempt.startedAt)}</td>
                  <td className="px-4 py-3 text-zinc-600">{formatDateTime(attempt.heartbeatAt)}</td>
                  <td className="px-4 py-3 text-zinc-600">{formatDateTime(attempt.finishedAt)}</td>
                </tr>
                );
              })}
            </DataTable>
          </div>
        ) : null}
        {value.resourceType && value.resourceId ? <div className="mt-6"><EventHistory resourceType={value.resourceType} resourceId={value.resourceId} /></div> : null}
      </details>
    </PageBody>
  );
}
