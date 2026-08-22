import { useQuery, useQueryClient } from '@tanstack/react-query';

import { fetchSearch } from '@/features/searches/api';
import { CandidateTable } from '@/features/searches/candidate-selection';
import { DetailErrorState, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { ErrorState } from '@/components/ui/feedback';
import { formatDateTime } from '@/lib/format';
import { friendlyError } from '@/lib/presentation';
import { ResourceOperationHistory } from '@/features/operations/resource-operation-history';

export function SearchDetailPage({ searchId }: { searchId: string }) {
  const queryClient = useQueryClient();
  const search = useQuery({
    queryKey: ['search', searchId],
    queryFn: () => fetchSearch(searchId),
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status === 'queued' || status === 'running' ? 3_000 : false;
    },
  });

  if (search.isPending) {
    return <DetailLoadingState title="搜索详情" label="正在读取搜索" />;
  }
  if (search.error || !search.data) {
    return <DetailErrorState title="搜索详情" message={search.error?.message ?? '无法读取搜索'} onRetry={() => search.refetch()} />;
  }
  const run = search.data;

  const handleAcquired = () => {
    void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
    void queryClient.invalidateQueries({ queryKey: ['recent-candidates'] });
  };

  return (
    <PageBody>
      <PageHeader title={`搜索：${run.query}`} description={`创建于 ${formatDateTime(run.createdAt)}`} actions={<StatusBadge value={run.status} />} />

      {run.errorMessage ? <ErrorState className="mb-6" message={friendlyError(run.errorCode, run.errorMessage)} /> : null}

      {run.candidates.length === 0 ? (
        <ErrorState message={run.status === 'completed' ? '未找到匹配的发布候选' : '搜索仍在进行中'} />
      ) : (
        <CandidateTable candidates={run.candidates} emptyLabel={run.status === 'completed' ? '未找到匹配的发布候选' : '搜索仍在进行中'} onAcquired={handleAcquired} />
      )}

      <div className="mt-6"><ResourceOperationHistory resourceType="search_run" resourceId={searchId} /></div>
    </PageBody>
  );
}
