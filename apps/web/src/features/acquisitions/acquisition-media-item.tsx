import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ExternalLink } from 'lucide-react';

import type { AcquisitionTaskSummary, Task } from '@/api/generated/types.gen';
import { ContextLink } from '@/components/context-link';
import { DetailGrid } from '@/components/resource';
import { CleanupStageBadge, StatusBadge } from '@/components/status-badge';
import { ErrorState, LoadingState } from '@/components/ui/feedback';
import { AcquisitionReviewCommands } from '@/features/acquisitions/acquisition-review-commands';
import { ArtifactReview } from '@/features/tasks/artifact-review';
import { fetchTask } from '@/features/tasks/api';
import { taskFailureInfo } from '@/features/tasks/task-failure';
import { TaskFailureSummary } from '@/features/tasks/task-failure-summary';
import { formatBytes, formatDateTime } from '@/lib/format';
import { episodeLabel } from '@/lib/presentation';

export function AcquisitionMediaItem({
  acquisitionId,
  summary,
  fallbackTitle,
}: {
  acquisitionId: string;
  summary: AcquisitionTaskSummary;
  fallbackTitle: string;
}) {
  const queryClient = useQueryClient();
  const item = useQuery({
    queryKey: ['episode_task', summary.id],
    queryFn: () => fetchTask(summary.id),
    refetchInterval: (query) => isMediaItemActive(query.state.data) ? 4_000 : false,
  });

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['episode_task', summary.id] });
    void queryClient.invalidateQueries({ queryKey: ['acquisition', acquisitionId] });
    void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
  };

  return (
    <article className="rounded-xl border border-zinc-200/90 bg-white px-4 py-5 shadow-card transition-shadow duration-200 hover:shadow-card-hover sm:px-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="break-words text-sm font-semibold text-zinc-950">{fallbackTitle}</h3>
          <p className="mt-1 text-xs text-zinc-500">最近更新 {formatDateTime(summary.updatedAt)}</p>
        </div>
        <StatusBadge value={item.data?.state ?? summary.state} />
      </div>

      {item.isPending ? <LoadingState className="mt-4" label="正在读取处理项" /> : null}
      {item.error ? <ErrorState className="mt-4" message={item.error.message} onRetry={() => item.refetch()} /> : null}
      {item.data ? <MediaItemContent task={item.data} onChanged={refresh} /> : null}
    </article>
  );
}

function MediaItemContent({ task, onChanged }: { task: Task; onChanged: () => void }) {
  const failure = taskFailureInfo(task);
  const refreshOperation = task.operations.find((operation) => operation.kind === 'emby.refresh');

  return (
    <div className="mt-5 space-y-5">
      {failure ? <TaskFailureSummary info={failure} /> : null}

      <DetailGrid items={[
        { label: '视频转码', value: <StatusBadge value={task.videoState} /> },
        { label: '字幕处理', value: <StatusBadge value={task.subtitleState} /> },
        { label: '规范文件名', value: task.artifacts?.basename ?? '等待生成' },
        ...(task.mediaType === 'episode' ? [{ label: '目标集数', value: episodeLabel(task.targetSeason, task.targetEpisode) }] : []),
      ]} />

      {task.artifacts ? <ArtifactSection taskId={task.id} artifacts={task.artifacts} /> : null}
      <AcquisitionReviewCommands task={task} onChanged={onChanged} />
      {task.review ? <ReviewResult review={task.review} /> : null}
      {task.import ? <ImportResult taskImport={task.import} refreshOperation={refreshOperation} /> : null}
      {task.cleanup ? <CleanupResult cleanup={task.cleanup} /> : null}
      <MediaItemLinks task={task} />
    </div>
  );
}

function ArtifactSection({ taskId, artifacts }: { taskId: string; artifacts: NonNullable<Task['artifacts']> }) {
  return (
    <section className="border-t border-zinc-200 pt-5" aria-labelledby={`artifact-${taskId}`}>
      <h4 id={`artifact-${taskId}`} className="text-sm font-semibold text-zinc-950">审核文件</h4>
      <div className="mt-3">
        <DetailGrid items={[
          { label: '视频', value: `${artifacts.video.format.toUpperCase()} · ${formatBytes(artifacts.video.sizeBytes)}` },
          { label: '字幕', value: `${artifacts.subtitle.format.toUpperCase()} · ${formatBytes(artifacts.subtitle.sizeBytes)}` },
        ]} />
      </div>
      <div className="mt-4"><ArtifactReview taskId={taskId} artifacts={artifacts} /></div>
    </section>
  );
}

function ReviewResult({ review }: { review: NonNullable<Task['review']> }) {
  return (
    <section className="border-t border-zinc-200 pt-5">
      <h4 className="text-sm font-semibold text-zinc-950">审核结果</h4>
      <div className="mt-3"><DetailGrid items={[
        { label: '决定', value: <StatusBadge value={review.decision} /> },
        { label: '备注', value: review.notes || '—' },
        { label: '审核时间', value: formatDateTime(review.reviewedAt) },
      ]} /></div>
    </section>
  );
}

function ImportResult({
  taskImport,
  refreshOperation,
}: {
  taskImport: NonNullable<Task['import']>;
  refreshOperation?: Task['operations'][number];
}) {
  return (
    <section className="border-t border-zinc-200 pt-5">
      <h4 className="text-sm font-semibold text-zinc-950">Emby 入库结果</h4>
      <div className="mt-3"><DetailGrid items={[
        { label: '文件导入', value: <StatusBadge value={taskImport.status} /> },
        { label: '视频目标', value: taskImport.destinationVideoPath ?? '等待写入媒体库' },
        { label: '字幕目标', value: taskImport.destinationSubtitlePath ?? '等待写入媒体库' },
        { label: 'Emby 刷新', value: refreshOperation ? <StatusBadge value={refreshOperation.status} /> : '等待文件导入完成' },
      ]} /></div>
      {refreshOperation ? (
        <ContextLink
          to="/operations/$operationId"
          params={{ operationId: refreshOperation.id }}
          className="mt-3 inline-flex items-center gap-1 text-sm font-medium text-emerald-700 hover:underline"
        >
          查看 Emby 刷新记录<ExternalLink className="size-3.5" aria-hidden="true" />
        </ContextLink>
      ) : null}
    </section>
  );
}

function CleanupResult({ cleanup }: { cleanup: NonNullable<Task['cleanup']> }) {
  return (
    <section className="border-t border-zinc-200 pt-5">
      <h4 className="text-sm font-semibold text-zinc-950">入库后清理</h4>
      <div className="mt-3"><DetailGrid items={[
        { label: '清理状态', value: <CleanupStageBadge value={cleanup.status} /> },
        { label: '源下载', value: cleanup.torrentRemoved ? '已清理' : '等待清理' },
        { label: '暂存文件', value: cleanup.stagedFilesRemoved ? '已清理' : '等待清理' },
      ]} /></div>
    </section>
  );
}

function MediaItemLinks({ task }: { task: Task }) {
  return (
    <div className="flex flex-wrap gap-3 border-t border-zinc-200 pt-4 text-sm">
      <ContextLink to="/tasks/$taskId" params={{ taskId: task.id }} className="font-medium text-emerald-700 hover:underline">处理项诊断</ContextLink>
      <ContextLink to="/downloads/$downloadId" params={{ downloadId: task.downloadId }} className="font-medium text-emerald-700 hover:underline">下载文件诊断</ContextLink>
      {task.embyItemId && task.embyLibraryId ? (
        <ContextLink to="/emby/libraries/$libraryId" params={{ libraryId: task.embyLibraryId }} className="font-medium text-emerald-700 hover:underline">Emby 条目</ContextLink>
      ) : null}
    </div>
  );
}

export function mediaItemTitle(item: AcquisitionTaskSummary, fallbackTitle: string, index: number): string {
  if (item.mediaType === 'movie') return fallbackTitle;
  return item.targetEpisodeTitle
    ? `${episodeLabel(item.targetSeason, item.targetEpisode)} · ${item.targetEpisodeTitle}`
    : item.targetEpisode
      ? episodeLabel(item.targetSeason, item.targetEpisode)
      : `处理项 ${index + 1}`;
}

function isMediaItemActive(task?: Task): boolean {
  return Boolean(task && ['media_queued', 'processing', 'finalizing', 'import_queued', 'importing'].includes(task.state));
}
