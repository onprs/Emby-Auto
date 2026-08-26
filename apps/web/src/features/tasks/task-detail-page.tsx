import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocation, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import type { Task } from '@/api/generated/types.gen';
import { cancelTaskCommand, fetchTask, importTaskCommand, reviewTaskCommand } from '@/features/tasks/api';
import { canDeleteTask, deleteTask, type TaskActionResult } from '@/features/tasks/task-actions';
import { DeletionFeedback, type DeletionSubmission } from '@/features/deletions/deletion-feedback';
import { ArtifactReview } from '@/features/tasks/artifact-review';
import { taskFailureInfo } from '@/features/tasks/task-failure';
import { TaskFailurePanel } from '@/features/tasks/task-failure-panel';
import { EventHistory } from '@/features/events/event-history';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { appNavigationState, currentAppLocation } from '@/app/navigation-context';
import { ContextLink } from '@/components/context-link';
import { DetailErrorState, DetailGrid, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { CleanupStageBadge, StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ConfirmDialog } from '@/components/ui/confirm-dialog';
import { ErrorState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { formatBytes, formatDateTime } from '@/lib/format';
import { episodeLabel, failureStageLabel, friendlyError, operationLabel } from '@/lib/presentation';

export function TaskDetailPage({ taskId }: { taskId: string }) {
  const queryClient = useQueryClient();
  const task = useQuery({
    queryKey: ['episode_task', taskId],
    queryFn: () => fetchTask(taskId),
    refetchInterval: (query) => (isActive(query.state.data) ? 4_000 : false),
  });

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['episode_task', taskId] });
  };

  if (task.isPending) {
    return <DetailLoadingState title="媒体处理详情" label="正在读取任务" />;
  }
  if (!task.data) {
    return <DetailErrorState title="媒体处理详情" message={task.error?.message ?? '无法读取任务'} onRetry={() => task.refetch()} />;
  }
  const value = task.data;
  const failure = taskFailureInfo(value);
  const isMovie = value.mediaType === 'movie';
  const title = isMovie ? (value.movieTitle ?? '未命名电影') : (value.seriesTitle ?? '未命名番剧');
  const mediaDescription = isMovie
    ? `电影${value.releaseYear ? ` · ${value.releaseYear}` : ''}`
    : `${episodeLabel(
      value.targetSeason ?? value.sourceSeason,
      value.targetEpisode ?? value.sourceEpisode,
      value.targetEpisode ? 0 : value.sourceEpisodeFractionHundredths,
    )}${value.targetEpisodeTitle ? ` · ${value.targetEpisodeTitle}` : ''}`;

  return (
    <PageBody>
      <PageHeader title={title} description={mediaDescription} actions={<StatusBadge value={value.state} />} />

      <TaskFailurePanel task={value} onChanged={refresh} />

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>当前进度</CardTitle>
            <CardDescription>视频和中文字幕会分别准备，完成后进入审核</CardDescription>
          </CardHeader>
          <CardContent>
            <DetailGrid
              items={[
                { label: '整体状态', value: <StatusBadge value={value.state} /> },
                { label: '视频', value: <StatusBadge value={value.videoState} /> },
                { label: '中文字幕', value: <StatusBadge value={value.subtitleState} /> },
                ...(value.failureStage ? [{ label: '未完成步骤', value: failureStageLabel(value.failureStage) }] : []),
                { label: '最近更新', value: formatDateTime(value.updatedAt) },
                {
                  label: '关联下载',
                  value: (
                    <ContextLink to="/downloads/$downloadId" params={{ downloadId: value.downloadId }} className="text-emerald-700 hover:underline">
                      查看或删除源下载
                    </ContextLink>
                  ),
                },
                {
                  label: '关联获取',
                  value: (
                    <ContextLink to="/acquisitions/$acquisitionId" params={{ acquisitionId: value.acquisitionId }} className="text-emerald-700 hover:underline">
                      查看内容任务
                    </ContextLink>
                  ),
                },
              ]}
            />
          </CardContent>
        </Card>

        <CommandPanel task={value} onChanged={refresh} />
      </div>

      {value.artifacts ? (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle>待审核文件</CardTitle>
            <CardDescription>{value.artifacts.basename}</CardDescription>
          </CardHeader>
          <CardContent>
            <DetailGrid
              items={[
                {
                  label: '视频',
                  value: `${value.artifacts.video.format.toUpperCase()} · ${formatBytes(value.artifacts.video.sizeBytes)}`,
                },
                {
                  label: '字幕',
                  value: `${value.artifacts.subtitle.format.toUpperCase()} · ${formatBytes(value.artifacts.subtitle.sizeBytes)}`,
                },
              ]}
            />
            <div className="mt-6">
              <ArtifactReview taskId={taskId} artifacts={value.artifacts} />
            </div>
          </CardContent>
        </Card>
      ) : null}

      {value.review ? (
        <Card className="mt-6">
          <CardHeader><CardTitle>审核记录</CardTitle></CardHeader>
          <CardContent>
            <DetailGrid items={[
              { label: '决定', value: <StatusBadge value={value.review.decision} /> },
              { label: '备注', value: value.review.notes || '—' },
              { label: '审核时间', value: formatDateTime(value.review.reviewedAt) },
            ]} />
          </CardContent>
        </Card>
      ) : null}

      {value.import ? (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle>入库</CardTitle>
          </CardHeader>
          <CardContent>
            <DetailGrid
              items={[
                { label: '状态', value: <StatusBadge value={value.import.status} /> },
                { label: '视频位置', value: value.import.destinationVideoPath ?? '等待入库' },
                { label: '字幕位置', value: value.import.destinationSubtitlePath ?? '等待入库' },
                ...(value.import.errorMessage ? [{ label: '问题', value: failure?.stage === 'import' ? failure.summary : '入库未完成，请查看失败信息。' }] : []),
              ]}
            />
          </CardContent>
        </Card>
      ) : null}

      {value.cleanup ? (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle>清理</CardTitle>
            <CardDescription>资源入库后自动删除源文件、种子和转码缓存</CardDescription>
          </CardHeader>
          <CardContent>
            <DetailGrid
              items={[
                { label: '状态', value: <CleanupStageBadge value={value.cleanup.status} /> },
                { label: '下载任务', value: value.cleanup.torrentRemoved ? '已清理' : '等待清理' },
                { label: '临时文件', value: value.cleanup.stagedFilesRemoved ? '已清理' : '等待清理' },
                ...(value.cleanup.errorMessage ? [{ label: '问题', value: failure?.stage === 'cleanup' ? failure.summary : '清理未完成，请查看失败信息。' }] : []),
              ]}
            />
          </CardContent>
        </Card>
      ) : null}

      {value.embyItemId && value.embyLibraryId ? (
        <p className="mt-6 text-sm"><ContextLink to="/emby/libraries/$libraryId" params={{ libraryId: value.embyLibraryId }} className="text-emerald-700 hover:underline">查看关联 Emby 条目</ContextLink></p>
      ) : null}
      <details className="mt-6 border-y border-zinc-200 py-4">
        <summary className="cursor-pointer text-sm font-medium text-zinc-700">诊断信息</summary>
        <DetailGrid items={[
          { label: '任务 ID', value: value.id },
          { label: '视频校验值', value: value.artifacts?.video.checksumSha256 ?? '—' },
          { label: '字幕校验值', value: value.artifacts?.subtitle.checksumSha256 ?? '—' },
        ]} />
        {value.operations.length > 0 ? (
          <ul className="mt-6 divide-y divide-zinc-100 border-y border-zinc-200">
            {value.operations.map((operation) => (
              <li key={operation.id} className="flex items-center justify-between gap-3 py-2.5">
                <ContextLink to="/operations/$operationId" params={{ operationId: operation.id }} className="text-sm font-medium text-zinc-900 hover:underline">{operationLabel(operation.kind)}</ContextLink>
                <StatusBadge value={operation.status} />
              </li>
            ))}
          </ul>
        ) : null}
        <div className="mt-6"><EventHistory resourceType="episode_task" resourceId={taskId} /></div>
      </details>
    </PageBody>
  );
}

function isActive(task: Task | undefined): boolean {
  if (!task) {
    return false;
  }
  return ['media_queued', 'processing', 'finalizing', 'import_queued', 'importing'].includes(task.state);
}

function CommandPanel({ task, onChanged }: { task: Task; onChanged: () => void }) {
  const navigate = useNavigate();
  const location = useLocation();
  const source = currentAppLocation(location.href);
  const queryClient = useQueryClient();
  const [notes, setNotes] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [confirmation, setConfirmation] = useState<'reject' | 'cancel' | 'delete' | null>(null);
  const [deletions, setDeletions] = useState<DeletionSubmission[]>([]);
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const command = useMutation({
    mutationFn: async (action: 'approve' | 'reject' | 'import' | 'cancel' | 'delete') => {
      setError(null);
      const key = holder.get();
      try {
        if (action === 'approve' || action === 'reject') {
          return await reviewTaskCommand(task.id, key, task.version, action === 'approve' ? 'approved' : 'rejected', notes);
        }
        if (action === 'import') {
          return await importTaskCommand(task.id, key, task.version);
        }
        if (action === 'delete') {
          return await deleteTask(task, key);
        }
        return await cancelTaskCommand(task.id, key, task.version);
      } catch (cause) {
        if (cause instanceof ApiFailure && cause.isConflict) {
          holder.reset();
          onChanged();
        }
        throw cause;
      }
    },
    onSuccess: (result, action) => {
      holder.reset();
      setConfirmation(null);
      if (action === 'delete') {
        void queryClient.invalidateQueries({ queryKey: ['tasks'] });
        void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
        void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
        const outcome = result as TaskActionResult;
        if (outcome.ok && outcome.operationId) {
          setDeletions([{ resourceId: task.acquisitionId, label: task.mediaType === 'movie' ? (task.movieTitle ?? '电影任务') : (task.seriesTitle ?? '媒体任务'), operationId: outcome.operationId }]);
          return;
        }
        setError(outcome.error ?? '删除失败');
        return;
      }
      onChanged();
      if (result && 'operationId' in result && typeof result.operationId === 'string') {
        void navigate({
          to: '/operations/$operationId',
          params: { operationId: result.operationId },
          search: { from: source },
          state: appNavigationState(source),
        });
      }
    },
    onError: (cause) => setError(cause instanceof ApiFailure ? friendlyError(cause.code, cause.message) : cause instanceof Error ? cause.message : '操作失败'),
  });

  const canReview = task.actions.canReview;
  const canImport = task.actions.canImport;
  const canCancel = task.actions.canCancel;
  const canDelete = canDeleteTask(task);

  return (
    <Card>
      <CardHeader>
        <CardTitle>操作</CardTitle>
        <CardDescription>这里只显示现在能做的操作</CardDescription>
      </CardHeader>
      <CardContent>
        <DeletionFeedback
          items={deletions}
          onDismiss={() => setDeletions([])}
          onSettled={() => {
            void queryClient.invalidateQueries({ queryKey: ['tasks'] });
            void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
          }}
        />
        {canReview ? (
          <div className="mb-4 space-y-2">
            <Label htmlFor="review-notes">审核备注</Label>
            <Input id="review-notes" value={notes} onChange={(event) => setNotes(event.target.value)} placeholder="备注（拒绝时请说明原因）" />
          </div>
        ) : null}
        {error ? <ErrorState className="mb-4" message={error} /> : null}
        <ConfirmDialog
          open={confirmation !== null}
          title={confirmation === 'delete' ? '确认删除任务' : confirmation === 'reject' ? '确认拒绝' : '确认取消'}
          danger={confirmation === 'delete'}
          lines={
            confirmation === 'reject'
              ? ['拒绝后任务不会入库，审核记录会永久保留。']
              : confirmation === 'delete'
                ? [
                    '停止正在进行的下载、转码或处理。',
                    '删除尚未入库的下载源文件、种子任务和临时缓存。',
                    '已经成功入库到 Emby 的正式资源不会被删除。',
                  ]
                : ['取消会请求所有排队中或运行中的处理操作停止，已产生的产物不会自动删除。']
          }
          confirmLabel={confirmation === 'delete' ? '确认删除' : '确认'}
          running={command.isPending}
          onConfirm={() => confirmation && command.mutate(confirmation)}
          onCancel={() => setConfirmation(null)}
        />
        <div className="flex flex-wrap gap-2">
          {canReview ? (
            <>
              <Button type="button" onClick={() => command.mutate('approve')} disabled={command.isPending}>
                审核通过并入库
              </Button>
              <Button type="button" variant="outline" onClick={() => setConfirmation('reject')} disabled={command.isPending}>
                拒绝
              </Button>
            </>
          ) : null}
          {canImport ? (
            <Button type="button" onClick={() => command.mutate('import')} disabled={command.isPending}>
              继续历史任务入库
            </Button>
          ) : null}
          {canCancel ? (
            <Button type="button" variant="outline" onClick={() => setConfirmation('cancel')} disabled={command.isPending}>
              取消任务
            </Button>
          ) : null}
          {canDelete ? (
            <Button type="button" variant="outline" className="border-red-300 text-red-700 hover:bg-red-50" onClick={() => setConfirmation('delete')} disabled={command.isPending}>
              删除任务
            </Button>
          ) : null}
          {!canReview && !canImport && !canCancel && !canDelete ? <p className="text-sm text-zinc-500">当前没有需要操作的内容</p> : null}
        </div>
      </CardContent>
    </Card>
  );
}
