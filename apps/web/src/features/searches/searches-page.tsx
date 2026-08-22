import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Search as SearchIcon } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import { fetchRecentCandidates, fetchSearch, startSearch } from '@/features/searches/api';
import { CandidateTable } from '@/features/searches/candidate-selection';
import { SearchAcquisitionsSection } from '@/features/searches/search-acquisitions-section';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { PageBody, PageHeader } from '@/components/resource';
import { Button } from '@/components/ui/button';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { friendlyError } from '@/lib/presentation';

export function SearchesPage() {
  const queryClient = useQueryClient();
  const [query, setQuery] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [liveSearchId, setLiveSearchId] = useState<string | null>(null);
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const recent = useQuery({
    queryKey: ['recent-candidates'],
    queryFn: () => fetchRecentCandidates(5),
  });

  const live = useQuery({
    queryKey: ['search', liveSearchId],
    queryFn: () => fetchSearch(liveSearchId!),
    enabled: Boolean(liveSearchId),
    refetchInterval: (q) => {
      const status = q.state.data?.status;
      return status === 'queued' || status === 'running' ? 3_000 : false;
    },
  });

  const completedSyncRef = useRef<string | null>(null);

  useEffect(() => {
    if (liveSearchId) {
      completedSyncRef.current = null;
    }
  }, [liveSearchId]);

  useEffect(() => {
    if (live.data?.status === 'completed' && live.data.id !== completedSyncRef.current) {
      completedSyncRef.current = live.data.id;
      void queryClient.invalidateQueries({ queryKey: ['recent-candidates'] });
    }
  }, [live.data?.id, live.data?.status, queryClient]);

  const start = useMutation({
    mutationFn: (keywords: string) => startSearch(holder.get(), keywords),
    onSuccess: (result) => {
      holder.reset();
      setError(null);
      const searchId = result.search?.id;
      if (searchId) {
        setLiveSearchId(searchId);
      }
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        holder.reset();
      }
      setError(cause instanceof Error ? cause.message : '创建搜索失败');
    },
  });

  const handleAcquired = () => {
    void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
    void queryClient.invalidateQueries({ queryKey: ['recent-candidates'] });
  };

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

      <section className="mb-8 space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold text-zinc-950">当前搜索结果</h2>
          {live.data ? <span className="text-xs text-zinc-500">关键词：{live.data.query} · {friendlyStatus(live.data.status)}</span> : null}
        </div>
        {!liveSearchId ? (
          <EmptyState title="尚未搜索" description="输入关键词并执行搜索，结果将在此实时显示" />
        ) : live.isPending ? (
          <LoadingState label="正在搜索" />
        ) : live.error ? (
          <ErrorState message={live.error.message} onRetry={() => live.refetch()} />
        ) : live.data.candidates.length === 0 ? (
          <EmptyState
            title={live.data.status === 'completed' ? '未找到匹配的发布候选' : live.data.status === 'failed' ? '搜索失败' : '搜索仍在进行中'}
            description={live.data.errorMessage ? friendlyError(live.data.errorCode, live.data.errorMessage) : '等待搜索返回具体资源'}
          />
        ) : (
          <CandidateTable candidates={live.data.candidates} emptyLabel="未找到匹配的发布候选" onAcquired={handleAcquired} />
        )}
        {live.data?.errorMessage ? <ErrorState message={friendlyError(live.data.errorCode, live.data.errorMessage)} /> : null}
      </section>

      <section className="mb-8 space-y-3">
        <h2 className="text-base font-semibold text-zinc-950">最近搜索</h2>
        {recent.isPending ? (
          <LoadingState label="正在读取最近搜索" />
        ) : recent.error ? (
          <ErrorState message={recent.error.message} onRetry={() => recent.refetch()} />
        ) : recent.data.items.length === 0 ? (
          <EmptyState title="暂无最近结果" description="完成搜索后，最近 5 条具体资源将在此显示" />
        ) : (
          <CandidateTable candidates={recent.data.items} emptyLabel="暂无最近结果" onAcquired={handleAcquired} />
        )}
      </section>

      <SearchAcquisitionsSection />
    </PageBody>
  );
}

function friendlyStatus(status: string): string {
  switch (status) {
    case 'queued':
      return '排队中';
    case 'running':
      return '搜索中';
    case 'completed':
      return '已完成';
    case 'failed':
      return '失败';
    case 'cancelled':
      return '已取消';
    default:
      return status;
  }
}
