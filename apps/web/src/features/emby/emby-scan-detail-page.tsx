import { useQuery } from '@tanstack/react-query';

import { fetchScan } from '@/features/emby/api';
import { DetailErrorState, DetailGrid, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { ErrorState } from '@/components/ui/feedback';
import { formatDateTime } from '@/lib/format';
import { friendlyError } from '@/lib/presentation';

export function EmbyScanDetailPage({ scanId }: { scanId: string }) {
  const scan = useQuery({
    queryKey: ['emby-scan', scanId],
    queryFn: () => fetchScan(scanId),
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status === 'queued' || status === 'running' ? 3_000 : false;
    },
  });

  if (scan.isPending) {
    return <DetailLoadingState title="目录更新详情" label="正在读取目录更新结果" />;
  }
  if (scan.error || !scan.data) {
    return <DetailErrorState title="目录更新详情" message={scan.error?.message ?? '无法读取目录更新结果'} onRetry={() => scan.refetch()} />;
  }
  const value = scan.data;

  return (
    <PageBody>
      <PageHeader title="从 Emby 更新目录" description={`开始于 ${formatDateTime(value.createdAt)}`} actions={<StatusBadge value={value.status} />} />

      {value.errorMessage ? <ErrorState className="mb-6" message={friendlyError(value.errorCode, value.errorMessage)} /> : null}

      <Card>
        <CardHeader>
          <CardTitle>目录更新结果</CardTitle>
        </CardHeader>
        <CardContent>
          <DetailGrid
            items={[
              { label: '状态', value: <StatusBadge value={value.status} /> },
              { label: '媒体库', value: `${value.libraryCount} 个` },
              { label: '媒体条目', value: `${value.itemCount} 个` },
              { label: '开始时间', value: formatDateTime(value.startedAt) },
              { label: '完成时间', value: formatDateTime(value.completedAt) },
              { label: '更新时间', value: formatDateTime(value.updatedAt) },
            ]}
          />
        </CardContent>
      </Card>
    </PageBody>
  );
}
