import {
  Captions,
  CheckCircle2,
  ClipboardCheck,
  Download,
  FilePenLine,
  Film,
  FolderTree,
  Library,
  Radio,
  Waypoints,
  type LucideIcon,
} from 'lucide-react';

import type { Acquisition, AcquisitionStage, AcquisitionStageKey } from '@/api/generated/types.gen';
import { formatOverallProgress, OverallProgressBar } from '@/components/overall-progress';
import { Badge } from '@/components/ui/badge';
import { formatDateTime } from '@/lib/format';
import {
  acquisitionPipelineStageLabels,
  acquisitionPipelineStageStatuses,
  sourceKindLabel,
} from '@/lib/presentation';
import { cn } from '@/lib/utils';

const stageIcons: Record<AcquisitionStageKey, LucideIcon> = {
  source: Radio,
  download: Download,
  mapping: Waypoints,
  transcode: Film,
  subtitle: Captions,
  rename: FilePenLine,
  organize: FolderTree,
  review: ClipboardCheck,
  import: Library,
};

type TaskProgressValue = Pick<Acquisition, 'aggregateStatus' | 'currentStage' | 'overallProgress'>;

export function TaskProgress({
  task,
  compact = false,
  ariaLabel = '任务整体进度',
}: {
  task: TaskProgressValue;
  compact?: boolean;
  ariaLabel?: string;
}) {
  const complete = task.aggregateStatus === 'completed';
  const failed = task.aggregateStatus === 'failed' || task.aggregateStatus === 'rejected';
  const cancelled = task.aggregateStatus === 'cancelled';
  const label = complete ? '生命周期完成' : acquisitionPipelineStageLabels[task.currentStage];

  return (
    <OverallProgressBar
      value={task.overallProgress}
      label={label}
      ariaLabel={ariaLabel}
      compact={compact}
      tone={failed ? 'attention' : complete ? 'complete' : cancelled ? 'neutral' : 'active'}
    />
  );
}

export function TaskStageTimeline({ task }: { task: Acquisition }) {
  return (
    <ol className="divide-y divide-zinc-100 overflow-hidden rounded-xl border border-zinc-200/90 bg-white shadow-card">
      {task.stages.map((stage) => {
        const Icon = stageIcons[stage.key];
        const current = task.currentStage === stage.key && task.aggregateStatus !== 'completed';
        return (
          <li key={stage.key} className={cn('grid gap-3 px-4 py-4 transition-colors sm:grid-cols-[2.25rem_minmax(9rem,0.7fr)_minmax(12rem,1.3fr)_auto] sm:items-center', current && 'bg-sky-50/70')}>
            <span
              className={cn(
                'grid size-9 place-items-center rounded-full ring-1',
                stage.status === 'completed'
                  ? 'bg-emerald-50 text-emerald-700 ring-emerald-200'
                  : current
                    ? 'bg-sky-50 text-sky-700 ring-sky-200'
                    : 'bg-zinc-50 text-zinc-400 ring-zinc-200',
              )}
            >
              {stage.status === 'completed' ? <CheckCircle2 className="size-5" aria-hidden="true" /> : <Icon className="size-5" aria-hidden="true" />}
            </span>
            <div className="min-w-0">
              <p className="text-sm font-medium text-zinc-950">{acquisitionPipelineStageLabels[stage.key]}</p>
              {stage.updatedAt ? <p className="mt-0.5 text-xs text-zinc-500">{formatDateTime(stage.updatedAt)}</p> : null}
            </div>
            <p className="min-w-0 break-words text-sm text-zinc-600">{stageResult(task, stage)}</p>
            <StageBadge status={stage.status} />
          </li>
        );
      })}
    </ol>
  );
}

function StageBadge({ status }: { status: AcquisitionStage['status'] }) {
  const config = acquisitionPipelineStageStatuses[status];
  return <Badge tone={config?.tone ?? 'neutral'}>{config?.label ?? '状态已更新'}</Badge>;
}

function stageResult(task: Acquisition, stage: AcquisitionStage): string {
  switch (stage.key) {
    case 'source':
      return task.sourceKind === 'rss' ? 'RSS 链接已生成任务' : `${sourceKindLabel(task.sourceKind)}已生成任务`;
    case 'download':
      if (!task.download) return '等待创建下载';
      if (stage.status === 'completed') return `第 ${task.download.attempt} 次下载完成`;
      return `第 ${task.download.attempt} 次下载 · ${formatOverallProgress(task.download.progress)}`;
    case 'mapping':
      if (stage.status === 'skipped') return '电影无需剧集映射';
      return task.mapping.selectedVideoCount > 0
        ? `${task.mapping.mappedVideoCount} / ${task.mapping.selectedVideoCount} 个视频已确认集数`
        : '等待识别视频文件';
    case 'transcode':
      return itemCountResult(stage, '个视频已完成转码');
    case 'subtitle':
      return itemCountResult(stage, '份 ASS 字幕已准备');
    case 'rename': {
      const names = task.tasks.map((item) => item.artifactBasename).filter(Boolean);
      return names.length === 1 ? names[0]! : itemCountResult(stage, '组文件已使用规范名称');
    }
    case 'organize':
      return itemCountResult(stage, '组文件已进入规范作品目录');
    case 'review':
      return itemCountResult(stage, '个处理项已审核');
    case 'import':
      return importResult(task, stage);
  }
}

function itemCountResult(stage: AcquisitionStage, suffix: string): string {
  if (stage.totalItems === 0) return '等待生成处理项';
  return `${stage.completedItems} / ${stage.totalItems} ${suffix}`;
}

function importResult(task: Acquisition, stage: AcquisitionStage): string {
  if (stage.status === 'skipped') return '审核未通过，未执行入库';
  if (stage.totalItems === 0) return '等待生成处理项';
  const refreshes = task.tasks.map((item) => item.embyRefreshStatus).filter(Boolean);
  if (stage.status === 'completed') {
    if (refreshes.some((status) => status === 'failed')) return `${stage.completedItems} / ${stage.totalItems} 已校验入库，Emby 刷新失败`;
    if (refreshes.some((status) => status === 'queued' || status === 'running')) return `${stage.completedItems} / ${stage.totalItems} 已校验入库，正在请求 Emby 扫描文件`;
    if (refreshes.length > 0 && refreshes.every((status) => status === 'succeeded')) return `${stage.completedItems} / ${stage.totalItems} 已校验入库并已请求 Emby 扫描文件`;
  }
  return `${stage.completedItems} / ${stage.totalItems} 个处理项已写入媒体库`;
}
