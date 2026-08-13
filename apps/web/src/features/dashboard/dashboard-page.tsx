import { lazy, Suspense } from 'react';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { AlertTriangle, ArrowRight, CheckCircle2, Download, Eye, Film, ListChecks, LoaderCircle, Pause, Play, RefreshCw, Server } from 'lucide-react';

import type { Acquisition, BackgroundRuntime, DashboardAttentionItem, DashboardRecentOperation } from '@/api/generated/types.gen';
import { fetchBackgroundRuntime, fetchDashboardSummary, fetchDashboardSystemMetrics, setBackgroundRuntime } from '@/features/dashboard/api';
import { ContextLink } from '@/components/context-link';
import { PageBody, PageHeader } from '@/components/resource';
import { AcquisitionStageBadge, StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { ErrorState, LoadingState } from '@/components/ui/feedback';
import { acquisitionFailureInfo } from '@/features/tasks/task-failure';
import { formatDateTime } from '@/lib/format';
import {
  acquisitionPipelineStageLabels,
  episodeLabel,
  friendlyError,
  operationLabel,
  sourceKindLabel,
} from '@/lib/presentation';
import { cn } from '@/lib/utils';

const RECENT_LIMIT = 5;
const SystemResourceCharts = lazy(() => import('@/features/dashboard/system-resource-charts'));

export function DashboardPage() {
  const queryClient = useQueryClient();
  const summary = useQuery({ queryKey: ['dashboard'], queryFn: fetchDashboardSummary });
  const backgroundRuntime = useQuery({
    queryKey: ['dashboard', 'background-runtime'],
    queryFn: fetchBackgroundRuntime,
    refetchInterval: 3_000,
    refetchIntervalInBackground: false,
  });
  const backgroundCommand = useMutation({
    mutationFn: setBackgroundRuntime,
    onSuccess: (runtime) => queryClient.setQueryData(['dashboard', 'background-runtime'], runtime),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: ['dashboard', 'background-runtime'] }),
  });
  const metrics = useQuery({
    queryKey: ['dashboard', 'system-metrics'],
    queryFn: fetchDashboardSystemMetrics,
    refetchInterval: 2_000,
    refetchIntervalInBackground: false,
    staleTime: 1_000,
  });

  if (summary.isPending) return <LoadingState label="正在读取概览" />;
  if (summary.error || !summary.data) {
    return <ErrorState message={summary.error?.message ?? '无法读取概览'} onRetry={() => summary.refetch()} />;
  }

  const { counts, attentionItems, recentOperations, recentImports, recentScans, dependencies, links } = summary.data;
  const needsAttention = counts.attention;

  return (
    <PageBody>
      <PageHeader title="仪表盘" description="先处理需要你介入的内容，其余工作会自动继续" />

      <BackgroundRuntimeControl
        runtime={backgroundRuntime.data}
        pending={backgroundRuntime.isPending}
        commandPending={backgroundCommand.isPending}
        error={backgroundCommand.error?.message ?? backgroundRuntime.error?.message}
        onToggle={(state) => backgroundCommand.mutate(state)}
        onRetry={() => void backgroundRuntime.refetch()}
      />

      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4" aria-label="工作概览">
        <CountCard to={links.downloading} icon={Download} label="下载中" value={counts.downloading} />
        <CountCard to={links.processing} icon={ListChecks} label="处理中" value={counts.processing + counts.importing} />
        <CountCard to={links.awaitingReview} icon={Eye} label="待审核" value={counts.awaitingReview} tone={counts.awaitingReview > 0 ? 'attention' : 'default'} />
        <CountCard to="/acquisitions?phase=attention" icon={AlertTriangle} label="需要处理" value={needsAttention} tone={needsAttention > 0 ? 'alert' : 'default'} />
      </section>

      <Suspense fallback={<SystemResourceChartsFallback />}>
        <SystemResourceCharts
          metrics={metrics.data}
          pending={metrics.isPending}
          error={metrics.error?.message}
          onRetry={() => void metrics.refetch()}
        />
      </Suspense>

      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Card aria-label="需要处理的任务">
          <CardHeader action={<ViewAllLink to="/acquisitions?phase=attention" />}>
            <CardTitle>需要处理</CardTitle>
          </CardHeader>
          <CardContent>
            {attentionItems.length === 0 ? (
              <p className="flex items-center gap-2 rounded-lg bg-emerald-50 px-3 py-2.5 text-sm text-emerald-700"><CheckCircle2 className="size-4" />当前没有需要人工处理的任务</p>
            ) : (
              <ul className="max-h-96 divide-y divide-zinc-100 overflow-y-auto overscroll-contain pr-1">
                {attentionItems.slice(0, RECENT_LIMIT).map((item) => (
                  <AttentionRow key={item.acquisition.id} item={item} />
                ))}
              </ul>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader><CardTitle>连接状态</CardTitle></CardHeader>
          <CardContent>
            <ul className="grid gap-x-6 sm:grid-cols-2">
              <DependencyRow label="qBittorrent" status={dependencies.qBittorrent} />
              <DependencyRow label="TMDb" status={dependencies.tmdb} />
              <DependencyRow label="Emby" status={dependencies.emby} />
              <DependencyRow label="FFmpeg" status={dependencies.mediaTools} />
              <DependencyRow label="Agent" status={dependencies.agent} />
            </ul>
          </CardContent>
        </Card>
      </div>

      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Card aria-label="最近运行记录">
          <CardHeader action={<ViewAllLink to="/operations" />}>
            <CardTitle>最近运行</CardTitle>
          </CardHeader>
          <CardContent>
            {recentOperations.length === 0 ? (
              <p className="text-sm text-zinc-500">暂无运行记录</p>
            ) : (
              <ul className="divide-y divide-zinc-100">
                {recentOperations.slice(0, RECENT_LIMIT).map((operation) => (
                  <RecentOperationRow key={operation.id} operation={operation} />
                ))}
              </ul>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader action={<ViewAllLink to="/tasks?state=imported" />}>
            <CardTitle>最近入库</CardTitle>
          </CardHeader>
          <CardContent>
            {recentImports.length === 0 ? <p className="text-sm text-zinc-500">还没有内容入库</p> : (
              <ul className="divide-y divide-zinc-100">
                {recentImports.slice(0, RECENT_LIMIT).map((item) => (
                  <li key={item.taskId} className="py-3">
                    <ContextLink to="/tasks/$taskId" params={{ taskId: item.taskId }} className="block truncate text-sm font-medium text-zinc-900 hover:underline">
                      {item.mediaType === 'movie'
                        ? `${item.movieTitle ?? '未命名电影'}${item.releaseYear ? ` (${item.releaseYear})` : ''}`
                        : `${item.seriesTitle ?? '未命名番剧'} · 第 ${item.seasonNumber} 季第 ${item.episodeNumber} 集`}
                    </ContextLink>
                    <p className="mt-1 text-xs text-zinc-500">{formatDateTime(item.completedAt)}</p>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>

      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Card className="lg:col-span-2">
          <CardHeader action={<ViewAllLink to="/emby" />}>
            <CardTitle>媒体库活动</CardTitle>
          </CardHeader>
          <CardContent>
            {recentScans.length === 0 ? <p className="text-sm text-zinc-500">还没有从 Emby 更新目录</p> : (
              <ul className="divide-y divide-zinc-100">
                {recentScans.slice(0, 4).map((scan) => (
                  <li key={scan.id} className="flex items-center justify-between gap-3 py-3">
                    <ContextLink to="/emby/scans/$scanId" params={{ scanId: scan.id }} className="flex min-w-0 items-center gap-2 text-sm font-medium text-zinc-900 hover:underline">
                      <Film className="size-4 shrink-0 text-zinc-400" />
                      <span className="truncate">目录已获取 {scan.itemCount} 个媒体条目</span>
                    </ContextLink>
                    <StatusBadge value={scan.status} />
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>
    </PageBody>
  );
}

function BackgroundRuntimeControl({
  runtime,
  pending,
  commandPending,
  error,
  onToggle,
  onRetry,
}: {
  runtime?: BackgroundRuntime;
  pending: boolean;
  commandPending: boolean;
  error?: string;
  onToggle: (state: 'running' | 'stopped') => void;
  onRetry: () => void;
}) {
  const transitioning = runtime?.state === 'transitioning' || commandPending;
  const running = runtime?.state === 'running';
  const stateLabel = transitioning ? '正在切换' : running ? '后台任务运行中' : runtime?.state === 'stopped' ? '后台任务已停止' : '后台状态不可用';

  return (
    <section className="mb-6 flex min-h-14 flex-wrap items-center justify-between gap-3 rounded-xl border border-zinc-200/90 bg-white px-4 py-3 shadow-card" aria-label="后台任务控制">
      <div className="flex min-w-0 items-center gap-3">
        <span className="relative flex size-2.5 shrink-0" aria-hidden="true">
          {running && !transitioning ? <span className="absolute inline-flex size-full animate-pulse-soft rounded-full bg-emerald-400" /> : null}
          <span className={cn(
            'relative inline-flex size-2.5 rounded-full',
            running && !transitioning ? 'bg-emerald-500' : transitioning ? 'bg-amber-500' : runtime?.state === 'stopped' ? 'bg-zinc-400' : 'bg-red-500',
          )} />
        </span>
        <div className="min-w-0">
          <p className="text-sm font-medium text-zinc-900">{pending ? '正在读取后台状态' : stateLabel}</p>
          {error ? <p className="mt-0.5 truncate text-xs text-red-700">{error}</p> : null}
        </div>
      </div>
      {runtime ? (
        <Button
          type="button"
          size="sm"
          variant={running ? 'outline' : 'accent'}
          className={running ? 'border-red-200 text-red-700 hover:border-red-300 hover:bg-red-50' : undefined}
          disabled={transitioning}
          onClick={() => onToggle(running ? 'stopped' : 'running')}
        >
          {transitioning ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : running ? <Pause aria-hidden="true" /> : <Play aria-hidden="true" />}
          {transitioning ? '正在切换' : running ? '停止后台任务' : '启动后台任务'}
        </Button>
      ) : (
        <Button type="button" size="sm" variant="outline" disabled={pending} onClick={onRetry}>
          {pending ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : <RefreshCw aria-hidden="true" />}
          重试
        </Button>
      )}
    </section>
  );
}

function AttentionRow({ item }: { item: DashboardAttentionItem }) {
  const acquisition = item.acquisition;
  const presentation = attentionPresentation(item);
  const context = [
    acquisition.mediaType === 'episode' ? episodeLabel(acquisition.sourceSeason, acquisition.sourceEpisode) : null,
    sourceKindLabel(acquisition.sourceKind),
    acquisitionPipelineStageLabels[acquisition.currentStage],
    formatDateTime(acquisition.updatedAt),
  ].filter(Boolean).join(' · ');

  return (
    <li className="py-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <ContextLink
            to="/acquisitions/$acquisitionId"
            params={{ acquisitionId: acquisition.id }}
            className="block truncate text-sm font-semibold text-zinc-950 hover:underline"
          >
            {acquisitionTitle(acquisition)}
          </ContextLink>
          <p className="mt-1 text-xs text-zinc-500">{context}</p>
        </div>
        <AcquisitionStageBadge value={acquisition.aggregateStatus} />
      </div>
      <p className="mt-3 text-sm font-medium text-amber-800">{presentation.title}</p>
      <p className="mt-1 text-sm text-zinc-700">{presentation.description}</p>
      <p className="mt-1 text-sm text-zinc-600"><span className="font-medium text-zinc-800">下一步：</span>{presentation.nextStep}</p>
      <ContextLink
        to="/acquisitions/$acquisitionId"
        params={{ acquisitionId: acquisition.id }}
        aria-label={`${presentation.action}：${acquisitionTitle(acquisition)}`}
        className="mt-3 inline-flex items-center gap-1 text-sm font-medium text-emerald-700 hover:underline"
      >
        {presentation.action}<ArrowRight className="size-4" aria-hidden="true" />
      </ContextLink>
    </li>
  );
}

function attentionPresentation(item: DashboardAttentionItem): { title: string; description: string; nextStep: string; action: string } {
  const acquisition = item.acquisition;
  switch (item.reason) {
    case 'mapping_required': {
      const { mappedVideoCount, selectedVideoCount } = acquisition.mapping;
      return {
        title: '需要确认剧集对应关系',
        description: selectedVideoCount > 0
          ? `${mappedVideoCount} / ${selectedVideoCount} 个视频已确认集数，其余文件无法继续处理。`
          : '下载已经完成，但还没有识别出可用于剧集映射的正片视频。',
        nextStep: selectedVideoCount > 0 ? '完成剩余文件的季集映射。' : '检查文件选择并确认正片视频。',
        action: '设置剧集映射',
      };
    }
    case 'workflow_failed': {
      const failure = acquisitionFailureInfo(acquisition);
      return {
        title: failure?.summary ?? `${acquisitionPipelineStageLabels[acquisition.currentStage]}未完成`,
        description: failure?.detail ?? friendlyError(item.errorCode, item.errorMessage),
        nextStep: failure?.recommendation ?? '打开任务查看失败阶段，再决定重试或更换资源。',
        action: '查看失败原因',
      };
    }
    case 'cleanup_failed':
      return {
        title: '临时文件清理失败',
        description: '正式媒体已经保留，但下载源文件、qBittorrent 任务或暂存文件没有完整清理。',
        nextStep: '检查文件占用、目录权限和 qBittorrent 连接后重试清理。',
        action: '处理清理问题',
      };
    case 'emby_refresh_failed':
      return {
        title: 'Emby 刷新失败',
        description: '媒体文件已经写入媒体库，但 Emby 尚未确认识别到最新内容。',
        nextStep: '检查 Emby 连接和媒体库配置后重新刷新。',
        action: '处理刷新问题',
      };
    case 'review_rejected':
      return {
        title: '审核未通过，未执行入库',
        description: '该任务保留了审核结果和处理产物，没有被标记为已入库。',
        nextStep: '查看审核结果，确认是否重新处理或删除这项任务。',
        action: '查看审核结果',
      };
  }
}

function acquisitionTitle(acquisition: Acquisition): string {
  if (acquisition.mediaType === 'movie') {
    return `${acquisition.movieTitle ?? '未命名电影'}${acquisition.releaseYear ? ` (${acquisition.releaseYear})` : ''}`;
  }
  return acquisition.seriesTitle ?? '未命名番剧';
}

function RecentOperationRow({ operation }: { operation: DashboardRecentOperation }) {
  return (
    <li className="flex items-center justify-between gap-3 py-3">
      <div className="min-w-0">
        <ContextLink to="/operations/$operationId" params={{ operationId: operation.id }} className="block truncate text-sm font-medium text-zinc-900 hover:underline">
          {operationLabel(operation.kind)}
        </ContextLink>
        {operation.status === 'failed' ? (
          <p className="mt-0.5 truncate text-xs text-red-700">{friendlyError(operation.errorCode, operation.errorMessage)}</p>
        ) : (
          <p className="mt-0.5 text-xs text-zinc-500">{formatDateTime(operation.updatedAt)}</p>
        )}
      </div>
      <StatusBadge value={operation.status} />
    </li>
  );
}

function ViewAllLink({ to }: { to: string }) {
  return (
    <Link to={to} className="text-sm font-medium text-emerald-700 hover:underline">
      查看全部
    </Link>
  );
}

function SystemResourceChartsFallback() {
  return (
    <section className="mt-6" aria-label="系统资源">
      <div className="mb-3">
        <h2 className="text-base font-semibold text-zinc-950">系统资源</h2>
        <p className="mt-1 text-xs text-zinc-500">正在加载资源图表</p>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        {['CPU 使用率', '内存使用率', '网络速度'].map((title) => (
          <Card key={title} className="min-w-0">
            <CardHeader><CardTitle>{title}</CardTitle></CardHeader>
            <CardContent><div className="skeleton h-44" /></CardContent>
          </Card>
        ))}
        <Card className="min-w-0">
          <CardHeader><CardTitle>磁盘</CardTitle></CardHeader>
          <CardContent><div className="skeleton h-44" /></CardContent>
        </Card>
      </div>
    </section>
  );
}

function CountCard({ to, icon: Icon, label, value, tone = 'default' }: { to: string; icon: typeof Download; label: string; value: number; tone?: 'default' | 'attention' | 'alert' }) {
  return (
    <Link
      to={to}
      className="group flex items-center gap-4 rounded-xl border border-zinc-200/90 bg-white px-5 py-4 shadow-card transition-all duration-200 hover:-translate-y-0.5 hover:border-emerald-300 hover:shadow-card-hover"
    >
      <span
        className={cn(
          'grid size-11 shrink-0 place-items-center rounded-xl transition-transform duration-200 group-hover:scale-105',
          tone === 'alert' && 'bg-red-50 text-red-600 ring-1 ring-red-100',
          tone === 'attention' && 'bg-amber-50 text-amber-600 ring-1 ring-amber-100',
          tone === 'default' && 'bg-emerald-50 text-emerald-600 ring-1 ring-emerald-100',
        )}
      >
        <Icon className="size-5" aria-hidden="true" />
      </span>
      <span className="min-w-0">
        <span className="block text-[1.65rem] font-semibold leading-8 tabular-nums tracking-tight text-zinc-950">{value}</span>
        <span className="block text-sm text-zinc-500">{label}</span>
      </span>
    </Link>
  );
}

function DependencyRow({ label, status }: { label: string; status: { configured: boolean; lastTestSuccess?: boolean; lastTestedAt?: string } }) {
  const healthy = status.configured && status.lastTestSuccess === true;
  return (
    <li className="flex items-center justify-between gap-3 border-b border-zinc-100 py-3 last:border-b-0">
      <span className="flex items-center gap-2 text-sm text-zinc-800"><Server className="size-4 text-zinc-400" />{label}</span>
      <span className={cn('flex items-center gap-1.5 text-sm font-medium', healthy ? 'text-emerald-700' : 'text-amber-700')}>
        <span className={cn('size-1.5 rounded-full', healthy ? 'bg-emerald-500' : 'bg-amber-500')} aria-hidden="true" />
        {healthy ? '可用' : status.configured ? '待检查' : '未设置'}
      </span>
    </li>
  );
}
