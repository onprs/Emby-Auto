import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ChevronUp, CircleOff, ListPlus } from 'lucide-react';
import { useState } from 'react';
import { useLocation, useNavigate } from '@tanstack/react-router';

import { ApiFailure } from '@/api/app-client';
import type { ReleaseCandidate } from '@/api/generated/types.gen';
import { friendlyError } from '@/lib/presentation';
import { appNavigationState, currentAppLocation } from '@/app/navigation-context';
import { selectCandidate } from '@/features/searches/api';
import { TMDbMoviePicker, type MovieSelection } from '@/features/tmdb/movie-picker';
import { TMDbSeriesPicker, type SeriesSelection } from '@/features/tmdb/series-picker';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { Button } from '@/components/ui/button';
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
    <div className="overflow-hidden rounded-md border border-zinc-200 bg-white shadow-card">
      <ul className="divide-y divide-zinc-100">
        {candidates.map((candidate) => (
          <CandidateItem key={candidate.id} candidate={candidate} onAcquired={onAcquired} />
        ))}
      </ul>
    </div>
  );
}

function CandidateItem({ candidate, onAcquired }: { candidate: ReleaseCandidate; onAcquired?: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <li>
      <div className="grid min-h-16 items-center gap-x-4 gap-y-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_7rem_10.5rem_5rem] sm:px-4">
        <p className="min-w-0 whitespace-normal break-words text-sm font-medium leading-5 text-zinc-900">{candidate.title}</p>
        <p className="text-xs text-zinc-500 sm:text-sm">{formatBytes(candidate.sizeBytes)}</p>
        <p className="text-xs text-zinc-500 sm:text-sm">{formatDateTime(candidate.publishedAt)}</p>
        <div className="flex justify-start sm:justify-end">
          <CandidateActions candidate={candidate} open={open} setOpen={setOpen} />
        </div>
      </div>
      {!candidate.downloadable ? (
        <p className="border-t border-zinc-100 bg-zinc-50 px-3 py-2 text-xs text-zinc-600 sm:px-4">
          {candidate.unavailableReason === 'download_uri_missing' ? '不可下载：缺少下载地址' : '不可下载'}
        </p>
      ) : null}
      {open ? (
        <div className="animate-fade-in border-t border-zinc-200 bg-zinc-50 px-3 py-4 sm:px-4">
          <CandidateForm candidate={candidate} onAcquired={onAcquired} />
        </div>
      ) : null}
    </li>
  );
}

function CandidateActions({ candidate, open, setOpen }: { candidate: ReleaseCandidate; open: boolean; setOpen: (v: boolean | ((prev: boolean) => boolean)) => void }) {
  if (!candidate.downloadable) {
    const reason = candidate.unavailableReason === 'download_uri_missing' ? '缺少下载地址' : '不可下载';
    return (
      <Button type="button" variant="ghost" size="sm" className="h-10 sm:h-8" disabled title={reason}>
        <CircleOff aria-hidden="true" />
        选择
      </Button>
    );
  }
  return (
    <Button type="button" variant="ghost" size="sm" className="h-10 sm:h-8" onClick={() => setOpen((value) => !value)}>
      {open ? <ChevronUp aria-hidden="true" /> : <ListPlus aria-hidden="true" />}
      {open ? '收起' : '选择'}
    </Button>
  );
}

function isLikelySeasonPack(title: string): boolean {
  const normalized = title.toLowerCase();
  return /([0-9]{1,3}\s*[-~+]\s*[0-9]{1,3}|全[集話话]|合[集輯]|complete|season pack|\bpack\b)/i.test(normalized);
}

function guessSeasonFromTitle(title: string): string {
  const match = title.match(/(?:season|s)\s*([0-9]{1,2})|第\s*([0-9]{1,2})\s*季/i);
  const parsedSeason = Number.parseInt(match?.[1] ?? match?.[2] ?? '', 10);
  return parsedSeason > 0 ? String(parsedSeason) : '1';
}

function guessEpisodeFromTitle(title: string): string {
  const match = title.match(/(?:ep|episode|e)\s*([0-9]{1,3})|第\s*([0-9]{1,3})\s*(?:话|話|集)/i);
  const parsedEpisode = Number.parseInt(match?.[1] ?? match?.[2] ?? '', 10);
  return parsedEpisode > 0 ? String(parsedEpisode) : '1';
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
  const [sourceSeason, setSourceSeason] = useState(() => guessSeasonFromTitle(candidate.title));
  const [sourceEpisode, setSourceEpisode] = useState(() => guessEpisodeFromTitle(candidate.title));
  const [singleEpisode, setSingleEpisode] = useState(() => !isLikelySeasonPack(candidate.title));
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
        ...(singleEpisode ? { sourceEpisode: Number(sourceEpisode) } : {}),
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
      if (cause instanceof ApiFailure) {
        setError(friendlyError(cause.code, cause.message));
        return;
      }
      setError(cause instanceof Error ? cause.message : '创建获取失败');
    },
  });

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <div className="text-sm font-medium text-zinc-900">创建获取</div>
        <p className="text-xs text-zinc-500">{mediaType === 'movie' ? '绑定 TMDb 电影元数据' : '绑定 TMDb 番剧与源季集信息'}</p>
      </div>
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
        <div className={`grid gap-4 ${singleEpisode ? 'sm:grid-cols-3' : 'sm:grid-cols-2'}`}>
          <div className="space-y-2">
            <Label htmlFor={`season-${candidate.id}`}>资源对应第几季</Label>
            <Input id={`season-${candidate.id}`} type="number" min={1} value={sourceSeason} onChange={(event) => setSourceSeason(event.target.value)} />
          </div>
          {singleEpisode ? (
            <div className="space-y-2">
              <Label htmlFor={`episode-${candidate.id}`}>资源对应第几集</Label>
              <Input id={`episode-${candidate.id}`} type="number" min={1} value={sourceEpisode} onChange={(event) => setSourceEpisode(event.target.value)} />
            </div>
          ) : null}
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
    </div>
  );
}
