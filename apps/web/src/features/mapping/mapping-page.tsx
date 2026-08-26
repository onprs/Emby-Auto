import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocation, useNavigate } from '@tanstack/react-router';
import { Check, ListTree, LoaderCircle, RefreshCw, Save, Waypoints } from 'lucide-react';
import { useRef, useState } from 'react';

import { appNavigationState, currentAppLocation } from '@/app/navigation-context';
import { ApiFailure } from '@/api/app-client';
import type {
  DownloadFile,
  EpisodeMappingExplicitDisposition,
  EpisodeMappingPlanRequest,
  EpisodeMappingPreview,
  TmDbEpisodeCatalog,
  TmDbSeasonCatalog,
} from '@/api/generated/types.gen';
import { DetailErrorState, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { Button } from '@/components/ui/button';
import { ErrorState, LoadingState } from '@/components/ui/feedback';
import { Select } from '@/components/ui/select';
import { fetchAcquisition } from '@/features/acquisitions/api';
import { fetchDownload } from '@/features/downloads/api';
import { previewMapping, saveMapping } from '@/features/mapping/api';
import { fetchSeriesCatalog, syncSeries } from '@/features/tmdb/api';
import { formatDateTime } from '@/lib/format';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { friendlyError } from '@/lib/presentation';
import { cn } from '@/lib/utils';

type MappingMode = 'anchor' | 'explicit';
type ExplicitAction = '' | 'map' | 'exclude';
interface ExplicitDraft {
  action: ExplicitAction;
  target: string;
}
interface ExplicitPreviewResult {
  plan: EpisodeMappingPlanRequest;
  fingerprint: string;
  preview: EpisodeMappingPreview;
}

export function MappingPage({ acquisitionId }: { acquisitionId: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const source = currentAppLocation(location.href);
  const [mode, setMode] = useState<MappingMode>('anchor');
  const [selectedAnchorSource, setSelectedAnchorSource] = useState('');
  const [selectedTarget, setSelectedTarget] = useState('');
  const [explicitDrafts, setExplicitDrafts] = useState<Record<string, ExplicitDraft>>({});
  const [previewResult, setPreviewResult] = useState<ExplicitPreviewResult | null>(null);
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
  const hasFractionalSource = sourceOptions.some((file) => (file.sourceEpisodeFractionHundredths ?? 0) > 0);
  const activeMode: MappingMode = hasFractionalSource ? 'explicit' : mode;
  const preferredAnchor = findAnchorSource(sourceOptions, acquisition.data?.sourceSeason, acquisition.data?.sourceEpisode);
  const anchorSourceId = selectedAnchorSource || preferredAnchor?.id || sourceOptions[0]?.id || '';
  const anchorSource = sourceOptions.find((file) => file.id === anchorSourceId);
  const regularSeasons = catalog.data?.seasons.filter((season) => !season.special) ?? [];
  const targetOptions = catalog.data?.seasons.flatMap((season) => season.episodes.map((episode) => ({
    value: targetValue(season.seasonNumber, episode.episodeNumber),
    label: `${targetCoordinate(season.seasonNumber, episode.episodeNumber)} · ${episode.title}`,
  }))) ?? [];
  const explicitPlan = buildExplicitPlan(sourceOptions, explicitDrafts);
  const explicitPlanFingerprint = explicitPlan ? mappingPlanFingerprint(explicitPlan) : '';
  const verifiedPreview = previewResult?.fingerprint === explicitPlanFingerprint ? previewResult : null;

  const syncCatalog = useMutation({
    mutationFn: () => {
      if (!tmdbSeriesId || !acquisition.data?.seriesTitle) throw new Error('番剧获取缺少 TMDb 信息');
      return syncSeries(syncHolder.get(), tmdbSeriesId, acquisition.data.seriesTitle);
    },
    onMutate: () => {
      setPreviewResult(null);
      setError(null);
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

  const previewMutation = useMutation({
    mutationFn: async (plan: EpisodeMappingPlanRequest): Promise<ExplicitPreviewResult> => ({
      plan,
      fingerprint: mappingPlanFingerprint(plan),
      preview: await previewMapping(acquisitionId, plan),
    }),
    onMutate: () => {
      setPreviewResult(null);
      setError(null);
    },
    onSuccess: setPreviewResult,
    onError: (cause) => {
      setPreviewResult(null);
      setError(cause instanceof ApiFailure ? friendlyError(cause.code, cause.message) : cause instanceof Error ? cause.message : '预览映射失败');
    },
  });

  const saveMutation = useMutation({
    mutationFn: (plan: EpisodeMappingPlanRequest) => {
      setError(null);
      const fingerprint = JSON.stringify(plan);
      if (commandFingerprint.current !== fingerprint) {
        holder.reset();
        commandFingerprint.current = fingerprint;
      }
      return saveMapping(acquisitionId, holder.get(), plan);
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

  const commandPending = syncCatalog.isPending || previewMutation.isPending || saveMutation.isPending;

  const saveAnchor = (season: number, episode: number) => {
    if (commandPending || hasFractionalSource) return;
    if (!anchorSource) {
      setError('请选择一个源视频作为连续映射锚点');
      return;
    }
    setSelectedTarget(targetValue(season, episode));
    saveMutation.mutate({
      mode: 'anchor',
      anchor: { sourceFileId: anchorSource.id, targetSeason: season, targetEpisode: episode },
    });
  };

  const changeMode = (nextMode: MappingMode) => {
    if (commandPending || (nextMode === 'anchor' && hasFractionalSource)) return;
    setMode(nextMode);
    setPreviewResult(null);
    setError(null);
  };

  const changeExplicitDraft = (sourceFileId: string, next: ExplicitDraft) => {
    if (commandPending) return;
    setExplicitDrafts((current) => ({ ...current, [sourceFileId]: next }));
    setPreviewResult(null);
    setError(null);
  };

  if (acquisition.isPending) return <DetailLoadingState title="剧集映射" label="正在读取剧集信息" />;
  if (acquisition.error || !acquisition.data) return <DetailErrorState title="剧集映射" message={acquisition.error?.message ?? '无法读取剧集信息'} onRetry={() => acquisition.refetch()} />;
  if (acquisition.data.mediaType === 'movie') {
    return (
      <PageBody>
        <PageHeader title="无需确认集数" description={`${acquisition.data.movieTitle ?? '电影'}会直接按电影信息处理`} />
      </PageBody>
    );
  }
  if (!downloadId) return <DetailErrorState title="剧集映射" message="当前内容没有可用于确认集数的下载记录" />;

  return (
    <PageBody>
      <PageHeader
        title="剧集映射"
        description={`${acquisition.data.seriesTitle ?? '未命名番剧'} · ${download.data ? sourceOptions.length : acquisition.data.mapping.selectedVideoCount} 个已选视频`}
        actions={(
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              if (!commandPending) syncCatalog.mutate();
            }}
            disabled={commandPending || !tmdbSeriesId}
          >
            <RefreshCw />更新剧集信息
          </Button>
        )}
      />

      {acquisition.data.sourceTitle ? (
        <section className="mb-5 border-y border-zinc-200 bg-white px-4 py-3" aria-labelledby="source-release-title">
          <p className="text-xs font-medium text-zinc-500">原始资源标题</p>
          <h2 id="source-release-title" className="mt-1 break-words text-sm font-medium text-zinc-900">{acquisition.data.sourceTitle}</h2>
        </section>
      ) : null}

      <div className="mb-5 inline-flex max-w-full rounded-lg border border-zinc-300 bg-zinc-100 p-1" role="group" aria-label="映射模式">
        <button
          type="button"
          aria-pressed={activeMode === 'anchor'}
          className={modeButtonClass(activeMode === 'anchor')}
          disabled={commandPending || hasFractionalSource}
          onClick={() => changeMode('anchor')}
        >
          <Waypoints className="size-4" aria-hidden="true" />单点连续
        </button>
        <button
          type="button"
          aria-pressed={activeMode === 'explicit'}
          className={modeButtonClass(activeMode === 'explicit')}
          disabled={commandPending}
          onClick={() => changeMode('explicit')}
        >
          <ListTree className="size-4" aria-hidden="true" />逐个文件
        </button>
      </div>

      {catalog.error ? <ErrorState className="mb-4" message="暂时没有剧集信息，请更新后重试。" onRetry={() => catalog.refetch()} /> : null}
      {download.error ? <ErrorState className="mb-4" message="暂时无法读取资源文件。" onRetry={() => download.refetch()} /> : null}
      {(catalog.isPending || download.isPending) && !catalog.error && !download.error ? <LoadingState label="正在读取资源与剧集信息" /> : null}
      {!download.isPending && !download.error && sourceOptions.length === 0 ? <ErrorState className="mb-4" message="没有可映射的已选视频。" /> : null}
      {error ? <ErrorState className="mb-4" message={error} /> : null}

      {catalog.data && sourceOptions.length > 0 && activeMode === 'anchor' ? (
        <AnchorMappingPanel
          files={sourceOptions}
          selectedSourceId={anchorSourceId}
          selectedTarget={selectedTarget}
          seasons={regularSeasons}
          emptyMessage={!catalog.data.synced && targetOptions.length === 0 ? 'TMDb 剧集信息尚未同步' : '没有可用的常规剧集'}
          pending={commandPending}
          onSourceChange={(fileId) => {
            if (commandPending) return;
            setSelectedAnchorSource(fileId);
            setSelectedTarget('');
            setError(null);
          }}
          onTarget={saveAnchor}
        />
      ) : null}

      {catalog.data && sourceOptions.length > 0 && activeMode === 'explicit' ? (
        <ExplicitMappingPanel
          files={sourceOptions}
          drafts={explicitDrafts}
          targetOptions={targetOptions}
          targetsAvailable={targetOptions.length > 0}
          targetUnavailableMessage={catalog.data.synced ? '没有可用的 TMDb 剧集' : 'TMDb 剧集信息尚未同步'}
          preview={verifiedPreview?.preview ?? null}
          previewPending={previewMutation.isPending}
          savePending={saveMutation.isPending}
          interactionPending={commandPending}
          onChange={changeExplicitDraft}
          onPreview={() => {
            if (commandPending) return;
            if (explicitPlan) previewMutation.mutate(explicitPlan);
          }}
          onSave={() => {
            if (commandPending) return;
            if (verifiedPreview) saveMutation.mutate(verifiedPreview.plan);
          }}
        />
      ) : null}

      {catalog.data?.lastSyncedAt ? <p className="mt-4 text-right text-xs text-zinc-500">TMDb 更新于 {formatDateTime(catalog.data.lastSyncedAt)}</p> : null}
    </PageBody>
  );
}

function AnchorMappingPanel({
  files,
  selectedSourceId,
  selectedTarget,
  seasons,
  emptyMessage,
  pending,
  onSourceChange,
  onTarget,
}: {
  files: DownloadFile[];
  selectedSourceId: string;
  selectedTarget: string;
  seasons: TmDbSeasonCatalog[];
  emptyMessage: string;
  pending: boolean;
  onSourceChange: (fileId: string) => void;
  onTarget: (season: number, episode: number) => void;
}) {
  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(16rem,0.72fr)_minmax(0,1.6fr)]">
      <section aria-labelledby="anchor-source-heading">
        <div className="mb-2 flex items-baseline justify-between gap-3 border-b border-zinc-200 pb-2">
          <h2 id="anchor-source-heading" className="text-sm font-semibold text-zinc-950">源视频锚点</h2>
          <span className="text-xs text-zinc-500">{files.length} 个视频</span>
        </div>
        <ul className="max-h-[34rem] divide-y divide-zinc-200 overflow-y-auto border-b border-zinc-200">
          {files.map((file) => {
            const selected = file.id === selectedSourceId;
            return (
              <li key={file.id}>
                <button
                  type="button"
                  aria-pressed={selected}
                  className={cn(
                    'flex min-h-14 w-full items-start gap-3 px-2 py-2.5 text-left transition-colors disabled:opacity-60',
                    selected ? 'bg-emerald-50 text-emerald-950' : 'bg-white text-zinc-800 hover:bg-zinc-50',
                  )}
                  disabled={pending}
                  onClick={() => onSourceChange(file.id)}
                >
                  <span className="mt-0.5 w-16 shrink-0 font-mono text-xs font-semibold text-emerald-700">{sourceCoordinate(file)}</span>
                  <span className="min-w-0 flex-1 break-all text-sm leading-5">{file.relativePath}</span>
                  {selected ? <Check className="mt-0.5 size-4 shrink-0 text-emerald-700" aria-hidden="true" /> : null}
                </button>
              </li>
            );
          })}
        </ul>
      </section>

      <section aria-labelledby="anchor-target-heading">
        <div className="mb-3 border-b border-zinc-200 pb-2">
          <h2 id="anchor-target-heading" className="text-sm font-semibold text-zinc-950">TMDb 连续映射起点</h2>
        </div>
        <CatalogEpisodeList seasons={seasons} selectedTarget={selectedTarget} emptyMessage={emptyMessage} pending={pending} onTarget={onTarget} />
      </section>
    </div>
  );
}

function CatalogEpisodeList({
  seasons,
  selectedTarget,
  emptyMessage,
  pending,
  onTarget,
}: {
  seasons: TmDbSeasonCatalog[];
  selectedTarget: string;
  emptyMessage: string;
  pending: boolean;
  onTarget: (season: number, episode: number) => void;
}) {
  const availableSeasons = seasons.filter((season) => season.episodes.length > 0);
  if (availableSeasons.length === 0) {
    return <MappingTargetEmptyState message={emptyMessage} />;
  }
  return (
    <div className="space-y-6">
      {availableSeasons.map((season) => (
        <section key={season.seasonNumber} aria-labelledby={`tmdb-season-${season.seasonNumber}`}>
          <div className="mb-2 flex items-baseline justify-between gap-3">
            <h3 id={`tmdb-season-${season.seasonNumber}`} className="text-sm font-semibold text-zinc-900">
              S{String(season.seasonNumber).padStart(2, '0')} · {season.name}
            </h3>
            <span className="shrink-0 text-xs text-zinc-500">{season.episodeCount} 集</span>
          </div>
          <ul className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
            {season.episodes.map((episode) => (
              <CatalogEpisodeButton
                key={episode.episodeNumber}
                season={season.seasonNumber}
                episode={episode}
                selected={selectedTarget === targetValue(season.seasonNumber, episode.episodeNumber)}
                pending={pending}
                onTarget={onTarget}
              />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function CatalogEpisodeButton({
  season,
  episode,
  selected,
  pending,
  onTarget,
}: {
  season: number;
  episode: TmDbEpisodeCatalog;
  selected: boolean;
  pending: boolean;
  onTarget: (season: number, episode: number) => void;
}) {
  const label = targetCoordinate(season, episode.episodeNumber);
  return (
    <li>
      <button
        type="button"
        className={cn(
          'flex min-h-16 w-full items-center gap-3 rounded-lg border px-3 py-2 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60',
          selected ? 'border-emerald-500 bg-emerald-50' : 'border-zinc-200 bg-white hover:border-emerald-400 hover:bg-emerald-50',
        )}
        aria-label={`映射到 ${label}：${episode.title}`}
        aria-pressed={selected}
        disabled={pending}
        onClick={() => onTarget(season, episode.episodeNumber)}
      >
        <span className="w-16 shrink-0 font-mono text-xs font-semibold text-emerald-700">{label}</span>
        <span className="min-w-0 flex-1">
          <span className="block break-words text-sm font-medium text-zinc-900">{episode.title}</span>
          {episode.airDate ? <span className="mt-0.5 block text-xs text-zinc-500">{episode.airDate}</span> : null}
        </span>
        {pending && selected ? <LoaderCircle className="size-4 shrink-0 animate-spin text-emerald-700" aria-hidden="true" /> : null}
      </button>
    </li>
  );
}

function ExplicitMappingPanel({
  files,
  drafts,
  targetOptions,
  targetsAvailable,
  targetUnavailableMessage,
  preview,
  previewPending,
  savePending,
  interactionPending,
  onChange,
  onPreview,
  onSave,
}: {
  files: DownloadFile[];
  drafts: Record<string, ExplicitDraft>;
  targetOptions: { value: string; label: string }[];
  targetsAvailable: boolean;
  targetUnavailableMessage: string;
  preview: EpisodeMappingPreview | null;
  previewPending: boolean;
  savePending: boolean;
  interactionPending: boolean;
  onChange: (sourceFileId: string, next: ExplicitDraft) => void;
  onPreview: () => void;
  onSave: () => void;
}) {
  const completed = files.filter((file) => explicitDraftComplete(drafts[file.id])).length;
  const mapped = files.filter((file) => drafts[file.id]?.action === 'map').length;
  const complete = completed === files.length && mapped > 0;

  return (
    <section aria-labelledby="explicit-mapping-heading">
      <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2 border-b border-zinc-200 pb-2">
        <h2 id="explicit-mapping-heading" className="text-sm font-semibold text-zinc-950">逐个文件处置</h2>
        <span className="text-xs tabular-nums text-zinc-500">{completed} / {files.length} 已完成</span>
      </div>
      {!targetsAvailable ? <MappingTargetEmptyState message={targetUnavailableMessage} /> : null}
      <div className="divide-y divide-zinc-200 border-b border-zinc-200">
        {files.map((file) => {
          const draft = drafts[file.id] ?? { action: '', target: '' };
          return (
            <div key={file.id} className="grid gap-3 bg-white px-2 py-3 md:grid-cols-[minmax(0,1fr)_auto_minmax(14rem,0.75fr)] md:items-center">
              <div className="flex min-w-0 items-start gap-3">
                <span className="mt-0.5 w-16 shrink-0 font-mono text-xs font-semibold text-emerald-700">{sourceCoordinate(file)}</span>
                <span className="min-w-0 break-all text-sm leading-5 text-zinc-800">{file.relativePath}</span>
              </div>
              {targetsAvailable ? (
                <>
                  <div className="inline-flex w-fit rounded-lg border border-zinc-300 bg-zinc-100 p-0.5" role="group" aria-label={`${file.relativePath} 的处置`}>
                    <button
                      type="button"
                      aria-pressed={draft.action === 'map'}
                      className={actionButtonClass(draft.action === 'map')}
                      disabled={interactionPending}
                      onClick={() => onChange(file.id, { action: 'map', target: draft.target })}
                    >映射</button>
                    <button
                      type="button"
                      aria-pressed={draft.action === 'exclude'}
                      className={actionButtonClass(draft.action === 'exclude')}
                      disabled={interactionPending}
                      onClick={() => onChange(file.id, { action: 'exclude', target: '' })}
                    >排除</button>
                  </div>
                  {draft.action === 'map' ? (
                    <Select
                      value={draft.target}
                      onChange={(target) => onChange(file.id, { action: 'map', target })}
                      options={targetOptions}
                      disabled={interactionPending}
                      placeholder="选择 TMDb 剧集"
                      ariaLabel={`${file.relativePath} 的 TMDb 剧集`}
                    />
                  ) : (
                    <span className="text-sm text-zinc-500">{draft.action === 'exclude' ? '不创建媒体任务' : '尚未处置'}</span>
                  )}
                </>
              ) : (
                <span className="text-sm text-zinc-500 md:col-span-2">暂无可用目标</span>
              )}
            </div>
          );
        })}
      </div>

      {targetsAvailable && preview ? <MappingPreviewSummary preview={preview} /> : null}

      {targetsAvailable ? (
        <div className="mt-4 flex flex-wrap justify-end gap-2">
          <Button type="button" variant="outline" disabled={!complete || interactionPending} onClick={onPreview}>
            {previewPending ? <LoaderCircle className="animate-spin" /> : <ListTree />}生成预览
          </Button>
          <Button type="button" disabled={!preview || interactionPending} onClick={onSave}>
            {savePending ? <LoaderCircle className="animate-spin" /> : <Save />}确认并继续
          </Button>
        </div>
      ) : null}
    </section>
  );
}

function MappingTargetEmptyState({ message }: { message: string }) {
  return (
    <div className="grid min-h-32 place-items-center border-y border-zinc-200 bg-zinc-50 px-4 py-8" role="status">
      <p className="text-sm text-zinc-600">{message}</p>
    </div>
  );
}

function MappingPreviewSummary({ preview }: { preview: EpisodeMappingPreview }) {
  const mapped = preview.rows.filter((row) => row.status === 'mapped').length;
  const excluded = preview.rows.filter((row) => row.status === 'excluded').length;
  return (
    <section className="mt-5 border-y border-zinc-200 bg-zinc-50 px-3 py-3" aria-labelledby="mapping-preview-heading">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 id="mapping-preview-heading" className="text-sm font-semibold text-zinc-900">映射预览</h3>
        <span className="text-xs text-zinc-600">{mapped} 个映射 · {excluded} 个排除</span>
      </div>
      <ul className="mt-2 grid gap-x-5 gap-y-1 sm:grid-cols-2">
        {preview.rows.map((row) => (
          <li key={row.sourceFileId} className="flex min-w-0 items-baseline gap-2 text-xs">
            <span className="min-w-0 flex-1 truncate text-zinc-600" title={row.relativePath}>{row.relativePath}</span>
            <span className="shrink-0 font-mono font-medium text-zinc-900">
              {row.status === 'excluded' ? '排除' : targetCoordinate(row.targetSeason ?? 0, row.targetEpisode ?? 0)}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function buildExplicitPlan(files: DownloadFile[], drafts: Record<string, ExplicitDraft>): EpisodeMappingPlanRequest | null {
  const assignments: EpisodeMappingExplicitDisposition[] = [];
  for (const file of files) {
    const draft = drafts[file.id];
    if (!explicitDraftComplete(draft)) return null;
    if (draft.action === 'exclude') {
      assignments.push({ sourceFileId: file.id, action: 'exclude' });
      continue;
    }
    const [season, episode] = draft.target.split(':').map(Number);
    assignments.push({ sourceFileId: file.id, action: 'map', targetSeason: season, targetEpisode: episode });
  }
  if (!assignments.some((assignment) => assignment.action === 'map')) return null;
  return { mode: 'explicit', assignments };
}

function mappingPlanFingerprint(plan: EpisodeMappingPlanRequest): string {
  return JSON.stringify(plan);
}

function explicitDraftComplete(draft?: ExplicitDraft): boolean {
  return Boolean(draft && (draft.action === 'exclude' || (draft.action === 'map' && draft.target)));
}

function findAnchorSource<T extends { id: string; sourceSeason?: number; sourceEpisode?: number; sourceEpisodeFractionHundredths?: number }>(
  files: T[],
  sourceSeason?: number,
  sourceEpisode?: number,
): T | undefined {
  const matching = files.filter((file) => file.sourceSeason === sourceSeason && file.sourceEpisode === sourceEpisode && (file.sourceEpisodeFractionHundredths ?? 0) === 0);
  if (matching.length > 0) return matching[0];
  return files.length === 1 ? files[0] : undefined;
}

function sourceCoordinate(file: Pick<DownloadFile, 'sourceSeason' | 'sourceEpisode' | 'sourceEpisodeFractionHundredths'>): string {
  if (!file.sourceSeason || !file.sourceEpisode) return '未识别';
  const fraction = file.sourceEpisodeFractionHundredths ?? 0;
  if (fraction === 0) return targetCoordinate(file.sourceSeason, file.sourceEpisode);
  const decimal = String(fraction).padStart(2, '0').replace(/0$/, '');
  return `S${String(file.sourceSeason).padStart(2, '0')}E${file.sourceEpisode}.${decimal}`;
}

function targetCoordinate(season: number, episode: number): string {
  return `S${String(season).padStart(2, '0')}E${String(episode).padStart(2, '0')}`;
}

function targetValue(season: number, episode: number): string {
  return `${season}:${episode}`;
}

function modeButtonClass(active: boolean): string {
  return cn(
    'inline-flex min-h-9 items-center gap-2 rounded-md px-3 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-60',
    active ? 'bg-white text-zinc-950 shadow-sm' : 'text-zinc-600 hover:text-zinc-950',
  );
}

function actionButtonClass(active: boolean): string {
  return cn(
    'min-h-8 rounded-md px-2.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-60',
    active ? 'bg-white text-zinc-950 shadow-sm' : 'text-zinc-600 hover:text-zinc-950',
  );
}
