import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocation, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import type { ReleaseCandidate } from '@/api/generated/types.gen';
import { appNavigationState, currentAppLocation } from '@/app/navigation-context';
import { fetchSearch, selectCandidate } from '@/features/searches/api';
import { TMDbMoviePicker, type MovieSelection } from '@/features/tmdb/movie-picker';
import { TMDbSeriesPicker, type SeriesSelection } from '@/features/tmdb/series-picker';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { DataTable, DetailErrorState, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select } from '@/components/ui/select';
import { formatBytes, formatDateTime } from '@/lib/format';
import { friendlyError } from '@/lib/presentation';
import { ResourceOperationHistory } from '@/features/operations/resource-operation-history';

export function SearchDetailPage({ searchId }: { searchId: string }) {
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

  return (
    <PageBody>
      <PageHeader title={`搜索：${run.query}`} description={`创建于 ${formatDateTime(run.createdAt)}`} actions={<StatusBadge value={run.status} />} />

      {run.errorMessage ? <ErrorState className="mb-6" message={friendlyError(run.errorCode, run.errorMessage)} /> : null}

      {run.candidates.length === 0 ? (
        <EmptyState title="暂无候选" description={run.status === 'completed' ? '未找到匹配的发布候选' : '搜索仍在进行中'} />
      ) : (
        <DataTable head={['标题', '提供者', '大小', '做种', '发布时间', '状态', '操作']}>
          {run.candidates.map((candidate) => (
            <CandidateRow key={candidate.id} candidate={candidate} />
          ))}
        </DataTable>
      )}

      <div className="mt-6"><ResourceOperationHistory resourceType="search_run" resourceId={searchId} /></div>
    </PageBody>
  );
}

function CandidateRow({ candidate }: { candidate: ReleaseCandidate }) {
  const navigate = useNavigate();
  const location = useLocation();
  const source = currentAppLocation(location.href);
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [mediaType, setMediaType] = useState<'episode' | 'movie'>('episode');
  const [series, setSeries] = useState<SeriesSelection | null>(null);
  const [movie, setMovie] = useState<MovieSelection | null>(null);
  const [sourceSeason, setSourceSeason] = useState('1');
  const [sourceEpisode, setSourceEpisode] = useState('1');
  const [singleEpisode, setSingleEpisode] = useState(true);
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const select = useMutation({
    mutationFn: () => {
      if (mediaType === 'movie') {
        if (!movie) throw new Error('请先选择 TMDb 电影');
        return selectCandidate(holder.get(), {
          candidateId: candidate.id,
          mediaType: 'movie',
          tmdbMovieId: movie.tmdbMovieId,
          movieTitle: movie.title,
          releaseYear: movie.releaseYear,
        });
      }
      if (!series) throw new Error('请先选择 TMDb 番剧');
      return selectCandidate(holder.get(), {
        candidateId: candidate.id,
        mediaType: 'episode',
        tmdbSeriesId: series.tmdbSeriesId,
        seriesTitle: series.title,
        sourceSeason: Number(sourceSeason),
        sourceEpisode: Number(sourceEpisode),
        singleEpisode,
      });
    },
    onSuccess: (result) => {
      holder.reset();
      void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
      void navigate({
        to: '/acquisitions/$acquisitionId',
        params: { acquisitionId: result.acquisitionId },
        search: { from: source },
        state: appNavigationState(source),
      });
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        holder.reset();
      }
      setError(cause instanceof Error ? cause.message : '创建获取失败');
    },
  });

  return (
    <>
      <tr>
        <td className="max-w-0 px-4 py-3">
          <span className="block truncate font-medium text-zinc-900" title={candidate.title}>
            {candidate.title}
          </span>
        </td>
        <td className="px-4 py-3 text-zinc-600">{candidate.provider}</td>
        <td className="px-4 py-3 text-zinc-600">{formatBytes(candidate.sizeBytes)}</td>
        <td className="px-4 py-3 text-zinc-600">{candidate.seeders ?? '—'}</td>
        <td className="px-4 py-3 text-zinc-600">{formatDateTime(candidate.publishedAt)}</td>
        <td className="px-4 py-3">
          {candidate.downloadable ? (
            <StatusBadge value="pending" />
          ) : (
            <span className="text-sm text-zinc-600">
              {candidate.unavailableReason === 'download_uri_missing' ? '不可下载：缺少下载地址' : '不可下载'}
            </span>
          )}
        </td>
        <td className="px-4 py-3">
          <Button type="button" variant="outline" disabled={!candidate.downloadable} onClick={() => setOpen((value) => !value)}>
            {open ? '收起' : '选择'}
          </Button>
        </td>
      </tr>
      {open ? (
        <tr>
          <td colSpan={7} className="bg-zinc-50 px-4 py-4">
            <Card>
              <CardHeader>
                <CardTitle>创建获取</CardTitle>
                <CardDescription>{mediaType === 'movie' ? '绑定 TMDb 电影元数据' : '绑定 TMDb 番剧与源季集信息'}</CardDescription>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor={`media-type-${candidate.id}`}>内容类型</Label>
                  <Select
                    id={`media-type-${candidate.id}`}
                    value={mediaType}
                    onChange={(value) => {
                      setMediaType(value as 'episode' | 'movie');
                      setError(null);
                    }}
                    options={[
                      { value: 'episode', label: '番剧' },
                      { value: 'movie', label: '电影' },
                    ]}
                  />
                </div>
                {mediaType === 'movie' ? <TMDbMoviePicker value={movie} onChange={setMovie} /> : <TMDbSeriesPicker value={series} onChange={setSeries} />}
                {mediaType === 'episode' ? <div className="grid gap-4 sm:grid-cols-3">
                  <div className="space-y-2">
                    <Label htmlFor={`season-${candidate.id}`}>资源对应第几季</Label>
                    <Input id={`season-${candidate.id}`} type="number" min={1} value={sourceSeason} onChange={(event) => setSourceSeason(event.target.value)} />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor={`episode-${candidate.id}`}>资源对应第几集</Label>
                    <Input id={`episode-${candidate.id}`} type="number" min={0} value={sourceEpisode} onChange={(event) => setSourceEpisode(event.target.value)} />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor={`single-${candidate.id}`}>类型</Label>
                    <Select
                      id={`single-${candidate.id}`}
                      value={singleEpisode ? 'single' : 'pack'}
                      onChange={(value) => setSingleEpisode(value === 'single')}
                      options={[
                        { value: 'single', label: '单集' },
                        { value: 'pack', label: '季度包' },
                      ]}
                    />
                  </div>
                </div> : null}
                {error ? <ErrorState message={error} /> : null}
                <Button type="button" onClick={() => select.mutate()} disabled={select.isPending || (mediaType === 'movie' ? !movie : !series)}>
                  创建获取并下载
                </Button>
              </CardContent>
            </Card>
          </td>
        </tr>
      ) : null}
    </>
  );
}
