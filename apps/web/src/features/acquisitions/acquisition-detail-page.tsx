import { useQuery } from '@tanstack/react-query';
import { Archive, Download, Waypoints } from 'lucide-react';

import { ContextLink } from '@/components/context-link';
import { formatOverallProgress } from '@/components/overall-progress';
import { DetailErrorState, DetailGrid, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { AcquisitionStageBadge, StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { fetchAcquisition } from '@/features/acquisitions/api';
import { AcquisitionMediaItem, mediaItemTitle } from '@/features/acquisitions/acquisition-media-item';
import { TaskProgress, TaskStageTimeline } from '@/features/acquisitions/task-progress';
import { downloadDisplayStatus, downloadFollowupLabel } from '@/features/downloads/download-presentation';
import { formatDateTime, formatPercent } from '@/lib/format';
import { clientStateLabel, decisionSourceLabel, episodeLabel, sourceKindLabel } from '@/lib/presentation';

export function AcquisitionDetailPage({ acquisitionId }: { acquisitionId: string }) {
  const acquisition = useQuery({
    queryKey: ['acquisition', acquisitionId],
    queryFn: () => fetchAcquisition(acquisitionId),
    refetchInterval: (query) => isLifecycleActive(query.state.data?.aggregateStatus) ? 4_000 : false,
  });

  if (acquisition.isPending) {
    return <DetailLoadingState title="任务详情" label="正在读取任务生命周期" />;
  }
  if (acquisition.error || !acquisition.data) {
    return <DetailErrorState title="任务详情" message={acquisition.error?.message ?? '无法读取任务'} onRetry={() => acquisition.refetch()} />;
  }

  const task = acquisition.data;
  const isMovie = task.mediaType === 'movie';
  const downloadStatus = task.download ? downloadDisplayStatus(task.download) : undefined;
  const downloadFollowup = task.download ? downloadFollowupLabel(task.download) : null;
  const title = isMovie
    ? `${task.movieTitle ?? '未命名电影'}${task.releaseYear ? ` (${task.releaseYear})` : ''}`
    : (task.seriesTitle ?? '未命名番剧');

  return (
    <PageBody>
      <PageHeader
        title={title}
        description={`${sourceKindLabel(task.sourceKind)} · 创建于 ${formatDateTime(task.createdAt)}${task.archivedAt ? ` · 归档于 ${formatDateTime(task.archivedAt)}` : ''}`}
        actions={(
          <div className="flex flex-wrap items-center gap-2">
            {task.archived ? (
              <span className="inline-flex items-center gap-1.5 border border-zinc-300 bg-zinc-50 px-2.5 py-1 text-xs font-medium text-zinc-700">
                <Archive className="size-3.5" aria-hidden="true" />已归档
              </span>
            ) : null}
            <AcquisitionStageBadge value={task.aggregateStatus} />
          </div>
        )}
      />

      <section className="border-y border-zinc-200 py-5" aria-labelledby="task-overall-heading">
        <div className="flex flex-col gap-5 lg:flex-row lg:items-start lg:justify-between">
          <div className="min-w-0 flex-1">
            <h2 id="task-overall-heading" className="text-base font-semibold text-zinc-950">整体进度</h2>
            <div className="mt-3 max-w-3xl"><TaskProgress task={task} /></div>
          </div>
          <div className="flex flex-wrap gap-2">
            {!task.archived && task.downloadId ? (
              <Button asChild variant="outline">
                <ContextLink to="/downloads/$downloadId" params={{ downloadId: task.downloadId }}>
                  <Download aria-hidden="true" />下载文件
                </ContextLink>
              </Button>
            ) : null}
            {!task.archived && !isMovie && !task.mapping.complete ? (
              <Button asChild>
                <ContextLink to="/acquisitions/$acquisitionId/mapping" params={{ acquisitionId: task.id }}>
                  <Waypoints aria-hidden="true" />确认剧集映射
                </ContextLink>
              </Button>
            ) : null}
          </div>
        </div>

        <div className="mt-5">
          <DetailGrid items={[
            { label: '任务来源', value: sourceKindLabel(task.sourceKind) },
            ...(task.sourceTitle ? [{ label: '原始资源标题', value: task.sourceTitle }] : []),
            { label: '内容类型', value: isMovie ? '电影' : '番剧' },
            ...(!isMovie ? [{ label: '源集数', value: episodeLabel(task.sourceSeason, task.sourceEpisode, task.sourceEpisodeFractionHundredths) }] : []),
            ...(task.mappingDecisionSource ? [{ label: '剧集映射来源', value: decisionSourceLabel(task.mappingDecisionSource) }] : []),
            { label: '处理项', value: task.tasks.length > 0 ? `${task.tasks.length} 项` : '等待生成' },
            { label: '最近更新', value: formatDateTime(task.updatedAt) },
          ]} />
        </div>
      </section>

      {task.download ? (
        <section className="mt-7" aria-labelledby="download-stage-heading">
          <h2 id="download-stage-heading" className="text-base font-semibold text-zinc-950">下载结果</h2>
          <div className="mt-3 border-l-2 border-zinc-300 pl-4">
            <div className="flex flex-wrap items-center gap-2">
              <StatusBadge value={downloadStatus ?? task.download.status} />
              <span className="text-sm font-medium tabular-nums text-zinc-800">{formatPercent(task.download.progress)}</span>
            </div>
            <p className="mt-1 text-sm text-zinc-600">
              {clientStateLabel(task.download.clientState)} · 第 {task.download.attempt} 次下载{downloadFollowup ? ` · ${downloadFollowup}` : ''}
            </p>
          </div>
        </section>
      ) : null}

      <section className="mt-8" aria-labelledby="task-stages-heading">
        <div className="mb-3 flex items-center justify-between gap-3">
          <h2 id="task-stages-heading" className="text-base font-semibold text-zinc-950">阶段明细</h2>
          <span className="text-sm tabular-nums text-zinc-500">{formatOverallProgress(task.overallProgress)}</span>
        </div>
        <TaskStageTimeline task={task} />
      </section>

      <section className="mt-8" aria-labelledby="media-items-heading">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 id="media-items-heading" className="text-base font-semibold text-zinc-950">
            {task.tasks.length > 1 ? `整季各集处理进度（共 ${task.tasks.length} 集）` : '任务处理项'}
          </h2>
          {task.tasks.length > 1 ? (
            <div className="flex flex-wrap gap-2 text-xs">
              <span className="rounded bg-emerald-50 px-2 py-0.5 font-medium text-emerald-800 border border-emerald-200/60">
                已入库 {task.tasks.filter((t) => t.state === 'imported').length}
              </span>
              {task.tasks.some((t) => t.state === 'processing' || t.state === 'importing' || t.state === 'finalizing') ? (
                <span className="rounded bg-sky-50 px-2 py-0.5 font-medium text-sky-800 border border-sky-200/60">
                  处理中 {task.tasks.filter((t) => t.state === 'processing' || t.state === 'importing' || t.state === 'finalizing').length}
                </span>
              ) : null}
              {task.tasks.some((t) => t.state === 'awaiting_review') ? (
                <span className="rounded bg-amber-50 px-2 py-0.5 font-medium text-amber-800 border border-amber-200/60">
                  待审核 {task.tasks.filter((t) => t.state === 'awaiting_review').length}
                </span>
              ) : null}
              {task.tasks.some((t) => t.state === 'failed') ? (
                <span className="rounded bg-red-50 px-2 py-0.5 font-medium text-red-800 border border-red-200/60">
                  失败 {task.tasks.filter((t) => t.state === 'failed').length}
                </span>
              ) : null}
            </div>
          ) : null}
        </div>
        {task.archived ? (
          <div className="mt-3 border-y border-zinc-200 py-4">
            <div className="flex flex-wrap items-center gap-2">
              <StatusBadge value="completed" />
              <span className="text-sm font-medium text-zinc-800">已完成 Emby 入库</span>
            </div>
            <p className="mt-2 text-sm text-zinc-600">源下载、实时任务与临时文件已按订阅完成策略清理；上方阶段记录和已入库媒体继续保留。</p>
          </div>
        ) : task.tasks.length === 0 ? (
          <p className="mt-3 text-sm text-zinc-500">当前阶段尚未生成媒体处理项。</p>
        ) : (
          <div className="mt-3 space-y-4">
            {task.tasks.map((item, index) => (
              <AcquisitionMediaItem
                key={item.id}
                acquisitionId={task.id}
                summary={item}
                fallbackTitle={mediaItemTitle(item, title, index)}
              />
            ))}
          </div>
        )}
      </section>

      <details className="mt-8 border-y border-zinc-200 py-4">
        <summary className="cursor-pointer text-sm font-medium text-zinc-700">诊断信息</summary>
        <DetailGrid items={[
          { label: '任务 ID', value: task.id },
          { label: 'TMDb ID', value: String(task.tmdbMovieId ?? task.tmdbSeriesId ?? '—') },
          { label: '下载 ID', value: task.downloadId ?? '—' },
          {
            label: '映射结果',
            value: task.mediaType === 'movie'
              ? '无需映射'
              : `${task.mapping.mappedVideoCount} / ${task.mapping.selectedVideoCount}`,
          },
        ]} />
      </details>
    </PageBody>
  );
}

function isLifecycleActive(status?: string): boolean {
  return Boolean(status && !['completed', 'failed', 'rejected', 'cancelled'].includes(status));
}
