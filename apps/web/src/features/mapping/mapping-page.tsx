import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocation, useNavigate } from '@tanstack/react-router';
import { LoaderCircle, RefreshCw } from 'lucide-react';
import { useRef, useState } from 'react';

import { appNavigationState, currentAppLocation } from '@/app/navigation-context';
import { ApiFailure } from '@/api/app-client';
import type { EpisodeMappingAnchor } from '@/api/generated/types.gen';
import { DetailErrorState, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { Button } from '@/components/ui/button';
import { ErrorState, LoadingState } from '@/components/ui/feedback';
import { fetchAcquisition } from '@/features/acquisitions/api';
import { fetchDownload } from '@/features/downloads/api';
import { saveMapping } from '@/features/mapping/api';
import { fetchSeriesCatalog, syncSeries } from '@/features/tmdb/api';
import { formatDateTime } from '@/lib/format';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { friendlyError } from '@/lib/presentation';

/** Saves the current source episode as soon as its TMDb episode is selected. */
export function MappingPage({ acquisitionId }: { acquisitionId: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const source = currentAppLocation(location.href);
  const [selectedTarget, setSelectedTarget] = useState('');
  const [error, setError] = useState<string | null>(null);
  const holder = useState(() => new IdempotencyKeyHolder())[0];
  const commandFingerprint = useRef('');
  const syncHolder = useState(() => new IdempotencyKeyHolder())[0];
  const acquisition = useQuery({ queryKey: ['acquisition', acquisitionId], queryFn: () => fetchAcquisition(acquisitionId) });
  const tmdbSeriesId = acquisition.data?.mediaType === 'episode' ? acquisition.data.tmdbSeriesId : undefined;
  const downloadId = acquisition.data?.downloadId;
  const download = useQuery({
    queryKey: ['download', downloadId],
    queryFn: () => {
      if (!downloadId) throw new Error('剧集获取缺少下载记录');
      return fetchDownload(downloadId);
    },
    enabled: Boolean(downloadId),
    retry: false,
  });
  const catalog = useQuery({
    queryKey: ['tmdb-catalog', tmdbSeriesId],
    queryFn: () => {
      if (!tmdbSeriesId) throw new Error('番剧获取缺少 TMDb ID');
      return fetchSeriesCatalog(tmdbSeriesId);
    },
    enabled: Boolean(tmdbSeriesId),
    retry: false,
  });

  const sourceOptions = download.data?.files.filter((file) => file.selected && file.mediaKind === 'video') ?? [];
  const anchorSource = findAnchorSource(sourceOptions, acquisition.data?.sourceSeason, acquisition.data?.sourceEpisode);

  const syncCatalog = useMutation({
    mutationFn: () => {
      if (!tmdbSeriesId || !acquisition.data?.seriesTitle) throw new Error('番剧获取缺少 TMDb 信息');
      return syncSeries(syncHolder.get(), tmdbSeriesId, acquisition.data.seriesTitle);
    },
    onSuccess: (result) => {
      syncHolder.reset();
      void navigate({
        to: '/operations/$operationId',
        params: { operationId: result.operationId },
        search: { from: source },
        state: appNavigationState(source),
      });
      void queryClient.invalidateQueries({ queryKey: ['tmdb-catalog', acquisition.data?.tmdbSeriesId] });
    },
    onError: (cause) => setError(cause instanceof Error ? cause.message : 'TMDb 同步失败'),
  });

  const saveMutation = useMutation({
    mutationFn: (target: { season: number; episode: number }) => {
      setError(null);
      if (!anchorSource) throw new Error('无法确定当前资源集，请检查下载文件的季集识别结果');
      const anchor: EpisodeMappingAnchor = {
        sourceFileId: anchorSource.id,
        targetSeason: target.season,
        targetEpisode: target.episode,
      };
      const fingerprint = JSON.stringify(anchor);
      if (commandFingerprint.current !== fingerprint) {
        holder.reset();
        commandFingerprint.current = fingerprint;
      }
      setSelectedTarget(`${target.season}:${target.episode}`);
      return saveMapping(acquisitionId, holder.get(), { anchor });
    },
    onSuccess: () => {
      holder.reset();
      commandFingerprint.current = '';
      void queryClient.invalidateQueries({ queryKey: ['acquisition', acquisitionId] });
      void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
      void queryClient.invalidateQueries({ queryKey: ['rss-subscriptions'] });
      void navigate({
        to: '/acquisitions/$acquisitionId',
        params: { acquisitionId },
        search: { from: '/acquisitions?phase=attention' },
        state: appNavigationState('/acquisitions?phase=attention'),
        replace: true,
      });
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        holder.reset();
        commandFingerprint.current = '';
      }
      setError(cause instanceof ApiFailure ? friendlyError(cause.code, cause.message) : cause instanceof Error ? cause.message : '保存映射失败');
    },
  });

  if (acquisition.isPending) return <DetailLoadingState title="剧集对应关系" label="正在读取剧集信息" />;
  if (acquisition.error || !acquisition.data) return <DetailErrorState title="剧集对应关系" message={acquisition.error?.message ?? '无法读取剧集信息'} onRetry={() => acquisition.refetch()} />;
  if (acquisition.data.mediaType === 'movie') {
    return (
      <PageBody>
        <PageHeader title="无需确认集数" description={`${acquisition.data.movieTitle ?? '电影'}会直接按电影信息处理`} />
      </PageBody>
    );
  }
  if (!downloadId) return <DetailErrorState title="剧集对应关系" message="当前内容没有可用于确认集数的下载记录" />;

  const sourceCoordinate = episodeCoordinate(acquisition.data.sourceSeason, acquisition.data.sourceEpisode);

  return (
    <PageBody>
      <PageHeader
        title="选择对应的 TMDb 剧集"
        description={`${sourceCoordinate} · 选择后立即应用到同一 RSS 的其他集`}
        actions={
          <Button type="button" variant="outline" onClick={() => syncCatalog.mutate()} disabled={syncCatalog.isPending || !tmdbSeriesId}>
            <RefreshCw />更新剧集信息
          </Button>
        }
      />
      {catalog.error ? <ErrorState className="mb-4" message="暂时没有剧集信息，请更新后重试。" onRetry={() => catalog.refetch()} /> : null}
      {download.error ? <ErrorState className="mb-4" message="暂时无法读取资源文件。" onRetry={() => download.refetch()} /> : null}
      {(catalog.isPending || download.isPending) && !catalog.error && !download.error ? <LoadingState label="正在读取资源与剧集信息" /> : null}
      {!download.isPending && !download.error && !anchorSource ? (
        <ErrorState className="mb-4" message="无法从已选视频中确定当前资源集，请先检查文件选择和季集识别。" />
      ) : null}
      {error ? <ErrorState className="mb-4" message={error} /> : null}

      {catalog.data ? (
        <section className="rounded-xl border border-zinc-200/90 bg-white px-5 py-5 shadow-card" aria-labelledby="catalog-title">
          <div className="mb-5 flex flex-wrap items-baseline justify-between gap-2">
            <div>
              <h2 id="catalog-title" className="text-base font-semibold text-zinc-950">{catalog.data.title}</h2>
              {anchorSource ? <p className="mt-1 break-all text-xs text-zinc-500">当前资源：{sourceCoordinate} · {anchorSource.relativePath}</p> : null}
            </div>
            <p className="text-xs text-zinc-500">更新于 {formatDateTime(catalog.data.lastSyncedAt)}</p>
          </div>
          <div className="space-y-6">
            {catalog.data.seasons.filter((season) => !season.special).map((season) => (
              <section key={season.seasonNumber} aria-labelledby={`tmdb-season-${season.seasonNumber}`}>
                <div className="mb-2 flex items-baseline justify-between gap-3 border-b border-zinc-200 pb-2">
                  <h3 id={`tmdb-season-${season.seasonNumber}`} className="text-sm font-semibold text-zinc-900">
                    S{String(season.seasonNumber).padStart(2, '0')} · {season.name}
                  </h3>
                  <span className="shrink-0 text-xs text-zinc-500">{season.episodeCount} 集</span>
                </div>
                <ul className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                  {season.episodes.map((episode) => {
                    const value = `${season.seasonNumber}:${episode.episodeNumber}`;
                    const pending = saveMutation.isPending && selectedTarget === value;
                    const label = `S${String(season.seasonNumber).padStart(2, '0')}E${String(episode.episodeNumber).padStart(2, '0')}`;
                    return (
                      <li key={episode.episodeNumber}>
                        <button
                          type="button"
                          className="flex min-h-16 w-full items-center gap-3 rounded-lg border border-zinc-200 bg-white px-3 py-2 text-left shadow-sm transition-all duration-150 hover:border-emerald-400 hover:bg-emerald-50 hover:shadow disabled:cursor-not-allowed disabled:opacity-60"
                          aria-label={`映射到 ${label}：${episode.title}`}
                          aria-pressed={selectedTarget === value}
                          disabled={saveMutation.isPending || !anchorSource}
                          onClick={() => saveMutation.mutate({ season: season.seasonNumber, episode: episode.episodeNumber })}
                        >
                          <span className="w-16 shrink-0 font-mono text-xs font-semibold text-emerald-700">{label}</span>
                          <span className="min-w-0 flex-1">
                            <span className="block break-words text-sm font-medium text-zinc-900">{episode.title}</span>
                            {episode.airDate ? <span className="mt-0.5 block text-xs text-zinc-500">{episode.airDate}</span> : null}
                          </span>
                          {pending ? <LoaderCircle className="size-4 shrink-0 animate-spin text-emerald-700" aria-hidden="true" /> : null}
                        </button>
                      </li>
                    );
                  })}
                </ul>
              </section>
            ))}
          </div>
        </section>
      ) : null}
    </PageBody>
  );
}

function findAnchorSource<T extends { id: string; sourceSeason?: number; sourceEpisode?: number }>(
  files: T[],
  sourceSeason?: number,
  sourceEpisode?: number,
): T | undefined {
  const matching = files.filter((file) => file.sourceSeason === sourceSeason && file.sourceEpisode === sourceEpisode);
  if (matching.length > 0) return matching[0];
  return files.length === 1 ? files[0] : undefined;
}

function episodeCoordinate(season?: number, episode?: number): string {
  if (!season || !episode) return '当前资源集';
  return `S${String(season).padStart(2, '0')}E${String(episode).padStart(2, '0')}`;
}
