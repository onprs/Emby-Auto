import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useLocation, useNavigate } from '@tanstack/react-router';

import { ApiFailure } from '@/api/app-client';
import type { ReleaseCandidate } from '@/api/generated/types.gen';
import { appNavigationState, currentAppLocation } from '@/app/navigation-context';
import { selectCandidate } from '@/features/searches/api';
import { TMDbMoviePicker, type MovieSelection } from '@/features/tmdb/movie-picker';
import { TMDbSeriesPicker, type SeriesSelection } from '@/features/tmdb/series-picker';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select } from '@/components/ui/select';
import { formatBytes, formatDateTime } from '@/lib/format';

export function CandidateTable({ candidates, emptyLabel, onAcquired }: { candidates: ReleaseCandidate[]; emptyLabel: string; onAcquired?: () => void }) {
  if (candidates.length === 0) {
    return <EmptyState title="暂无候选" description={emptyLabel} />;
  }
  return (
    <>
      <div className="space-y-2 sm:hidden">
        {candidates.map((candidate) => (
          <CandidateCard key={candidate.id} candidate={candidate} onAcquired={onAcquired} />
        ))}
      </div>
      <div className="hidden sm:block">
        <table className="min-w-full divide-y divide-zinc-200 border border-zinc-200 bg-white">
          <thead className="bg-zinc-50">
            <tr>
              <th className="px-4 py-3 text-left text-xs font-semibold text-zinc-600">标题</th>
              <th className="px-4 py-3 text-left text-xs font-semibold text-zinc-600">大小</th>
              <th className="px-4 py-3 text-left text-xs font-semibold text-zinc-600">发布时间</th>
              <th className="px-4 py-3 text-left text-xs font-semibold text-zinc-600">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-200">
            {candidates.map((candidate) => (
              <CandidateRow key={candidate.id} candidate={candidate} onAcquired={onAcquired} />
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

function CandidateRow({ candidate, onAcquired }: { candidate: ReleaseCandidate; onAcquired?: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <tr>
        <td className="max-w-md px-4 py-3 align-top">
          <span className="block whitespace-normal break-words font-medium text-zinc-900">{candidate.title}</span>
        </td>
        <td className="px-4 py-3 align-top text-sm text-zinc-600">{formatBytes(candidate.sizeBytes)}</td>
        <td className="px-4 py-3 align-top text-sm text-zinc-600">{formatDateTime(candidate.publishedAt)}</td>
        <td className="px-4 py-3 align-top">
          <CandidateActions candidate={candidate} open={open} setOpen={setOpen} onAcquired={onAcquired} />
        </td>
      </tr>
      {open ? (
        <tr>
          <td colSpan={4} className="bg-zinc-50 px-4 py-4">
            <CandidateForm candidate={candidate} onAcquired={onAcquired} />
          </td>
        </tr>
      ) : null}
    </>
  );
}

function CandidateCard({ candidate, onAcquired }: { candidate: ReleaseCandidate; onAcquired?: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <article className="rounded-xl border border-zinc-200 bg-white p-4 shadow-card">
      <h3 className="whitespace-normal break-words text-sm font-medium text-zinc-900">{candidate.title}</h3>
      <p className="mt-2 text-xs text-zinc-600">大小 {formatBytes(candidate.sizeBytes)} · 发布时间 {formatDateTime(candidate.publishedAt)}</p>
      <div className="mt-3">
        <CandidateActions candidate={candidate} open={open} setOpen={setOpen} onAcquired={onAcquired} />
      </div>
      {open ? (
        <div className="mt-3 bg-zinc-50 p-3">
          <CandidateForm candidate={candidate} onAcquired={onAcquired} />
        </div>
      ) : null}
    </article>
  );
}

function CandidateActions({ candidate, open, setOpen, onAcquired }: { candidate: ReleaseCandidate; open: boolean; setOpen: (v: boolean | ((prev: boolean) => boolean)) => void; onAcquired?: () => void }) {
  if (!candidate.downloadable) {
    const reason = candidate.unavailableReason === 'download_uri_missing' ? '不可下载：缺少下载地址' : '不可下载';
    return (
      <div className="space-y-1">
        <Button type="button" variant="outline" disabled>
          选择
        </Button>
        <p className="text-xs text-zinc-600">{reason}</p>
      </div>
    );
  }
  return (
    <Button type="button" variant="outline" onClick={() => setOpen((value) => !value)}>
      {open ? '收起' : '选择'}
    </Button>
  );
}

function CandidateForm({ candidate, onAcquired }: { candidate: ReleaseCandidate; onAcquired?: () => void }) {
  const navigate = useNavigate();
  const location = useLocation();
  const source = currentAppLocation(location.href);
  const queryClient = useQueryClient();
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
      void queryClient.invalidateQueries({ queryKey: ['recent-candidates'] });
      if (onAcquired) {
        onAcquired();
      }
      // 保持在搜索页时不跳转，仅刷新任务；详情页等场景可选跳转，调用方可传入自定义行为
      // 默认行为：若提供 onAcquired 则不跳转，否则跳转到任务详情
      if (!onAcquired) {
        void navigate({
          to: '/acquisitions/$acquisitionId',
          params: { acquisitionId: result.acquisitionId },
          search: { from: source },
          state: appNavigationState(source),
        });
      }
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        holder.reset();
      }
      setError(cause instanceof Error ? cause.message : '创建获取失败');
    },
  });

  return (
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
        {mediaType === 'episode' ? (
          <div className="grid gap-4 sm:grid-cols-3">
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
          </div>
        ) : null}
        {error ? <ErrorState message={error} /> : null}
        <Button type="button" onClick={() => select.mutate()} disabled={select.isPending || (mediaType === 'movie' ? !movie : !series)}>
          创建获取并下载
        </Button>
      </CardContent>
    </Card>
  );
}
