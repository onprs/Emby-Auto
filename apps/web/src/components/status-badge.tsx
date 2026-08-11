import { Badge } from '@/components/ui/badge';
import { formatEnum } from '@/lib/format';
import { acquisitionStages, cleanupStages } from '@/lib/presentation';

type Tone = 'neutral' | 'success' | 'warning' | 'danger' | 'info';

const statusConfig: Record<string, { label: string; tone: Tone }> = {
  queued: { label: '排队中', tone: 'info' },
  running: { label: '运行中', tone: 'info' },
  succeeded: { label: '成功', tone: 'success' },
  failed: { label: '失败', tone: 'danger' },
  cancelled: { label: '已取消', tone: 'neutral' },
  completed: { label: '已完成', tone: 'success' },
  enqueue_pending: { label: '等待下载', tone: 'info' },
  file_resolution_pending: { label: '等待文件解析', tone: 'warning' },
  downloading: { label: '下载中', tone: 'info' },
  proposed: { label: '建议已生成', tone: 'warning' },
  review_required: { label: '待确认', tone: 'warning' },
  applied: { label: '已应用', tone: 'success' },
  expired: { label: '已过期', tone: 'neutral' },
  download_waiting: { label: '等待下载', tone: 'warning' },
  download_paused: { label: '已暂停', tone: 'warning' },
  selecting_files: { label: '正在准备文件', tone: 'warning' },
  materialized: { label: '已进入处理', tone: 'success' },
  media_queued: { label: '等待处理', tone: 'info' },
  processing: { label: '处理中', tone: 'info' },
  finalizing: { label: '正在检查结果', tone: 'warning' },
  awaiting_review: { label: '待审核', tone: 'warning' },
  approved: { label: '已批准', tone: 'success' },
  rejected: { label: '已拒绝', tone: 'danger' },
  import_queued: { label: '等待入库', tone: 'info' },
  importing: { label: '入库中', tone: 'info' },
  imported: { label: '已入库', tone: 'success' },
  transcode_queued: { label: '等待转换视频', tone: 'info' },
  transcoding: { label: '正在转换视频', tone: 'info' },
  video_ready: { label: '视频已准备好', tone: 'success' },
  subtitle_queued: { label: '等待准备字幕', tone: 'info' },
  extracting_or_converting: { label: '正在准备字幕', tone: 'info' },
  ass_ready: { label: '字幕已准备好', tone: 'success' },
  discovered: { label: '已发现', tone: 'neutral' },
  enqueueing: { label: '正在添加下载', tone: 'info' },
  enqueued: { label: '已安排下载', tone: 'success' },
  enqueue_failed: { label: '添加下载失败', tone: 'danger' },
  pending: { label: '待处理', tone: 'warning' },
  mapped: { label: '已映射', tone: 'success' },
  duplicate: { label: '重复', tone: 'neutral' },
  rejected_entry: { label: '已拒绝', tone: 'danger' },
  unconsumable: { label: '不可消费', tone: 'neutral' },
  searching: { label: '搜索中', tone: 'info' },
  inactive: { label: '未应用', tone: 'neutral' },
  applying: { label: '应用中', tone: 'info' },
  active: { label: '已应用', tone: 'success' },
  restoring: { label: '恢复中', tone: 'warning' },
};

export function StatusBadge({ value }: { value: string | null | undefined }) {
  const raw = value ?? '';
  const config = statusConfig[raw];
  if (!config) {
    return <Badge tone="neutral" title={formatEnum(raw)}>状态已更新</Badge>;
  }
  return <Badge tone={config.tone}>{config.label}</Badge>;
}

/** Renders a cleanup status as a user-facing cleanup stage badge. */
export function CleanupStageBadge({ value }: { value: string | null | undefined }) {
  const raw = value ?? '';
  const config = cleanupStages[raw];
  if (!config) {
    return <Badge tone="neutral" title={formatEnum(raw)}>状态已更新</Badge>;
  }
  return <Badge tone={config.tone}>{config.label}</Badge>;
}

/** Renders an acquisition aggregate status as a user-facing task stage badge. */
export function AcquisitionStageBadge({ value }: { value: string | null | undefined }) {
  const raw = value ?? '';
  const config = acquisitionStages[raw];
  if (!config) {
    return <Badge tone="neutral" title={formatEnum(raw)}>状态已更新</Badge>;
  }
  return <Badge tone={config.tone}>{config.label}</Badge>;
}
