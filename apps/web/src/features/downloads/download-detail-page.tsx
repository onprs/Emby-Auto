import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocation, useNavigate } from '@tanstack/react-router';
import { Trash2 } from 'lucide-react';
import { useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import type { Download } from '@/api/generated/types.gen';
import { appNavigationState, currentAppLocation } from '@/app/navigation-context';
import { ContextLink } from '@/components/context-link';
import { deleteAcquisitionCommand, fetchAcquisition } from '@/features/acquisitions/api';
import { DeletionFeedback, type DeletionSubmission } from '@/features/deletions/deletion-feedback';
import { cancelDownloadCommand, fetchDownload, retryDownloadCommand, saveFileResolutionCommand, saveFileSelectionCommand } from '@/features/downloads/api';
import {
  downloadDisplayStatus,
  downloadFollowupLabel,
  downloadRetryLabel,
  downloadWaitsForMapping,
  formatSourceEpisodeInput,
  parseSourceEpisodeInput,
  sourceCoordinateLabel,
} from '@/features/downloads/download-presentation';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { EventHistory } from '@/features/events/event-history';
import { ResourceOperationHistory } from '@/features/operations/resource-operation-history';
import { DetailErrorState, DetailGrid, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ConfirmDialog } from '@/components/ui/confirm-dialog';
import { ErrorState } from '@/components/ui/feedback';
import { formatBytes, formatDateTime, formatPercent } from '@/lib/format';
import { clientStateLabel, decisionSourceLabel, failureStageLabel, friendlyError, mediaKindLabel, reasonLabel } from '@/lib/presentation';

export function DownloadDetailPage({ downloadId }: { downloadId: string }) {
  const queryClient = useQueryClient();
  const download = useQuery({
    queryKey: ['download', downloadId],
    queryFn: () => fetchDownload(downloadId),
    refetchInterval: (query) => (isActive(query.state.data) ? 3_000 : false),
  });

  const acquisition = useQuery({
    queryKey: ['acquisition', download.data?.acquisitionId],
    queryFn: () => fetchAcquisition(download.data!.acquisitionId),
    enabled: Boolean(download.data?.acquisitionId),
  });

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['download', downloadId] });

  if (download.isPending) {
    return <DetailLoadingState title="下载详情" label="正在读取下载" />;
  }
  if (!download.data) {
    return <DetailErrorState title="下载详情" message={download.error?.message ?? '无法读取下载'} onRetry={() => download.refetch()} />;
  }
  const value = download.data;
  const displayStatus = downloadDisplayStatus(value);
  const followup = downloadFollowupLabel(value);

  return (
    <PageBody>
      <PageHeader title="下载进度" description="查看文件下载和后续处理状态" actions={<StatusBadge value={displayStatus} />} />

      {followup ? (
        <div className="mb-6 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3" role="status">
          <p className="text-sm font-medium text-amber-900">{followup}</p>
          <p className="mt-1 text-sm text-amber-800">{friendlyError(value.errorCode, value.errorMessage)}</p>
        </div>
      ) : value.errorMessage ? <ErrorState className="mb-6" message={friendlyError(value.errorCode, value.errorMessage)} /> : null}

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>当前进度</CardTitle>
          </CardHeader>
          <CardContent>
            <DetailGrid
              items={[
                { label: '状态', value: <StatusBadge value={displayStatus} /> },
                { label: '下载进度', value: formatPercent(value.progress) },
                { label: 'qBittorrent', value: clientStateLabel(value.clientState) },
                { label: '最近更新', value: formatDateTime(value.lastSyncedAt ?? value.updatedAt) },
                ...(value.failureStage ? [{ label: '未完成步骤', value: failureStageLabel(value.failureStage) }] : []),
                ...(value.fileResolutionSource ? [{ label: '文件解析来源', value: decisionSourceLabel(value.fileResolutionSource) }] : []),
                {
                  label: '关联获取',
                  value: (
                    <ContextLink to="/acquisitions/$acquisitionId" params={{ acquisitionId: value.acquisitionId }} className="text-emerald-700 hover:underline">
                      查看内容任务
                    </ContextLink>
                  ),
                },
                ...(acquisition.data?.tasks.length ? [{
                  label: '关联媒体任务',
                  value: (
                    <div className="flex flex-wrap gap-x-3 gap-y-1">
                      {acquisition.data.tasks.map((task, index) => (
                        <ContextLink key={task.id} to="/tasks/$taskId" params={{ taskId: task.id }} className="text-emerald-700 hover:underline">
                          {task.targetEpisodeTitle ?? `任务 ${index + 1}`}
                        </ContextLink>
                      ))}
                    </div>
                  ),
                }] : []),
              ]}
            />
          </CardContent>
        </Card>

        <DownloadCommands download={value} onChanged={refresh} />
      </div>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>文件选择</CardTitle>
          <CardDescription>核对自动选择结果；可在物化前修正</CardDescription>
        </CardHeader>
        <CardContent>
          <FileSelection download={value} onChanged={refresh} />
        </CardContent>
      </Card>

      <details className="mt-6 border-y border-zinc-200 py-4">
        <summary className="cursor-pointer text-sm font-medium text-zinc-700">诊断信息</summary>
        <DetailGrid
          items={[
            { label: '下载 ID', value: value.id },
            { label: 'qBittorrent hash', value: value.torrentHash ?? '—' },
            { label: '源文件位置', value: value.savePath ?? '—' },
            { label: '原始错误码', value: value.errorCode ?? '—' },
          ]}
        />
        <div className="mt-6"><ResourceOperationHistory resourceType="download" resourceId={downloadId} /></div>
        <div className="mt-6"><EventHistory resourceType="download" resourceId={downloadId} /></div>
      </details>
    </PageBody>
  );
}

function isActive(download: Download | undefined): boolean {
  if (!download) {
    return false;
  }
  return ['enqueue_pending', 'file_resolution_pending', 'downloading', 'selecting_files'].includes(download.status);
}

function DownloadCommands({ download, onChanged }: { download: Download; onChanged: () => void }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const source = currentAppLocation(location.href);
  const [error, setError] = useState<string | null>(null);
  const [confirmation, setConfirmation] = useState<'cancel' | 'delete' | null>(null);
  const [deletions, setDeletions] = useState<DeletionSubmission[]>([]);
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const command = useMutation({
    mutationFn: async (action: 'retry' | 'cancel' | 'delete') => {
      setError(null);
      const key = holder.get();
      try {
        if (action === 'retry') return await retryDownloadCommand(download.id, key, download.version);
        if (action === 'delete') return await deleteAcquisitionCommand(download.acquisitionId, key);
        return await cancelDownloadCommand(download.id, key, download.version);
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
        setDeletions([{ resourceId: download.acquisitionId, label: `下载记录 ${download.attempt}`, operationId: result.operationId }]);
        void queryClient.invalidateQueries({ queryKey: ['downloads'] });
        void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
        return;
      }
      onChanged();
      void navigate({
        to: '/operations/$operationId',
        params: { operationId: result.operationId },
        search: { from: source },
        state: appNavigationState(source),
      });
    },
    onError: (cause) => setError(cause instanceof ApiFailure ? friendlyError(cause.code, cause.message) : cause instanceof Error ? cause.message : '操作失败'),
  });

  const waitsForMapping = downloadWaitsForMapping(download);
  const canRetry = download.actions.canRetry && !waitsForMapping;
  const canCancel = download.actions.canCancel;
  const canDelete = download.actions.canDelete;

  return (
    <Card>
      <CardHeader>
        <CardTitle>操作</CardTitle>
      </CardHeader>
      <CardContent>
        <DeletionFeedback
          items={deletions}
          onDismiss={() => setDeletions([])}
          onSettled={() => {
            void queryClient.invalidateQueries({ queryKey: ['downloads'] });
            void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
          }}
        />
        {error ? <ErrorState className="mb-4" message={error} /> : null}
        <ConfirmDialog
          open={confirmation !== null}
          title={confirmation === 'delete' ? '确认删除下载' : '确认停止下载'}
          danger={confirmation === 'delete'}
          lines={
            confirmation === 'delete'
              ? [
                  '将删除这条下载所属的完整任务流程。',
                  '未被其他内容使用的 qBittorrent 任务、源文件和临时文件也会删除。',
                  '已经成功入库到 Emby 的正式资源不会被删除。',
                ]
              : ['将停止当前下载，但保留已经下载的文件。']
          }
          confirmLabel={confirmation === 'delete' ? '确认删除' : '确认停止'}
          running={command.isPending}
          onConfirm={() => confirmation && command.mutate(confirmation)}
          onCancel={() => setConfirmation(null)}
        />
        {waitsForMapping ? <p className="mb-3 text-sm text-amber-800">请先在关联任务中完成剧集映射，保存后会自动继续准备媒体处理。</p> : null}
        <div className="flex flex-wrap gap-2">
          {canRetry ? (
            <Button type="button" onClick={() => command.mutate('retry')} disabled={command.isPending}>
              {downloadRetryLabel(download)}
            </Button>
          ) : null}
          {canCancel && !confirmation ? (
            <Button type="button" variant="outline" onClick={() => setConfirmation('cancel')} disabled={command.isPending}>
              停止下载
            </Button>
          ) : null}
          {canDelete && !confirmation ? (
            <Button type="button" variant="outline" className="border-red-300 text-red-700 hover:bg-red-50" onClick={() => setConfirmation('delete')} disabled={command.isPending}>
              <Trash2 />删除下载
            </Button>
          ) : null}
          {!canRetry && !canCancel && !canDelete ? <p className="text-sm text-zinc-500">当前没有需要操作的内容</p> : null}
        </div>
      </CardContent>
    </Card>
  );
}

function FileSelection({ download, onChanged }: { download: Download; onChanged: () => void }) {
  const [selection, setSelection] = useState<Record<string, boolean>>({});
  const [coordinates, setCoordinates] = useState<Record<string, { season: string; episode: string }>>({});
  const location = useLocation();
  const source = currentAppLocation(location.href);
  const [error, setError] = useState<string | null>(null);
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const navigate = useNavigate();
  const resolving = download.actions.canResolveFiles;
  const editable = download.actions.canEditFileSelection || resolving;

  const save = useMutation({
    mutationFn: async () => {
      setError(null);
      const files = download.files.map((file) => {
        const coordinate = coordinates[file.id];
        const sourceSeason = positiveNumber(coordinate?.season) ?? file.sourceSeason;
        const parsedEpisode = parseSourceEpisodeInput(coordinate?.episode);
        const sourceEpisode = parsedEpisode?.episode ?? file.sourceEpisode;
        const sourceEpisodeFractionHundredths = parsedEpisode?.fractionHundredths ?? file.sourceEpisodeFractionHundredths ?? 0;
        return {
          fileId: file.id,
          selected: selection[file.id] ?? file.selected,
          ...(sourceSeason ? { sourceSeason } : {}),
          ...(sourceEpisode ? { sourceEpisode, sourceEpisodeFractionHundredths } : {}),
        };
      });
      try {
        if (resolving) return await saveFileResolutionCommand(download.id, holder.get(), download.version, files);
        return await saveFileSelectionCommand(download.id, holder.get(), download.version, files.map(({ fileId, selected }) => ({ fileId, selected })));
      } catch (cause) {
        if (cause instanceof ApiFailure && cause.isConflict) {
          holder.reset();
          onChanged();
        }
        throw cause;
      }
    },
    onSuccess: (result) => {
      holder.reset();
      setSelection({});
      setCoordinates({});
      onChanged();
      void navigate({
        to: '/operations/$operationId',
        params: { operationId: result.operationId },
        search: { from: source },
        state: appNavigationState(source),
      });
    },
    onError: (cause) => setError(cause instanceof ApiFailure ? friendlyError(cause.code, cause.message) : cause instanceof Error ? cause.message : '保存失败'),
  });

  const dirty = Object.keys(selection).length > 0 || Object.keys(coordinates).length > 0;

  return (
    <div>
      {error ? <ErrorState className="mb-4" message={error} /> : null}
      <div className="overflow-x-auto">
        <table className="w-full min-w-[560px] border-collapse text-sm">
          <thead>
            <tr className="border-b border-zinc-200 text-left text-xs font-medium text-zinc-500">
              <th className="px-2 py-2">选择</th>
              <th className="px-2 py-2">文件</th>
              <th className="px-2 py-2">类型</th>
              <th className="px-2 py-2">大小</th>
              <th className="px-2 py-2">源季集</th>
              <th className="px-2 py-2">排除原因</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-100">
            {download.files.map((file) => {
              const selected = selection[file.id] ?? file.selected;
              const isMedia = file.mediaKind === 'video' || file.mediaKind === 'subtitle';
              return (
                <tr key={file.id}>
                  <td className="px-2 py-2">
                    <input
                      type="checkbox"
                      checked={selected}
                      disabled={!editable || !isMedia}
                      onChange={(event) => setSelection((previous) => ({ ...previous, [file.id]: event.target.checked }))}
                      aria-label={`选择 ${file.relativePath}`}
                    />
                  </td>
                  <td className="max-w-0 truncate px-2 py-2 font-mono text-xs text-zinc-800" title={file.relativePath}>
                    {file.relativePath}
                  </td>
                  <td className="px-2 py-2 text-zinc-600">{mediaKindLabel(file.mediaKind)}</td>
                  <td className="px-2 py-2 text-zinc-600">{formatBytes(file.sizeBytes)}</td>
                  <td className="px-2 py-2 text-zinc-600">
                    {resolving && isMedia ? (
                      <div className="grid w-40 grid-cols-2 gap-1">
                        <Input
                          type="number"
                          min={1}
                          value={coordinates[file.id]?.season ?? file.sourceSeason ?? ''}
                          onChange={(event) => setCoordinates((previous) => ({
                            ...previous,
                            [file.id]: {
                              season: event.target.value,
                              episode: previous[file.id]?.episode ?? formatSourceEpisodeInput(file.sourceEpisode, file.sourceEpisodeFractionHundredths),
                            },
                          }))}
                          aria-label={`${file.relativePath} 源季`}
                        />
                        <Input
                          type="number"
                          min={1}
                          step={0.01}
                          value={coordinates[file.id]?.episode ?? formatSourceEpisodeInput(file.sourceEpisode, file.sourceEpisodeFractionHundredths)}
                          onChange={(event) => setCoordinates((previous) => ({ ...previous, [file.id]: { season: previous[file.id]?.season ?? String(file.sourceSeason ?? ''), episode: event.target.value } }))}
                          aria-label={`${file.relativePath} 源集`}
                        />
                      </div>
                    ) : file.sourceSeason && file.sourceEpisode ? sourceCoordinateLabel(file.sourceSeason, file.sourceEpisode, file.sourceEpisodeFractionHundredths) : '—'}
                  </td>
                  <td className="px-2 py-2 text-zinc-600">{reasonLabel(file.exclusionReason) || '—'}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {editable ? (
        <div className="mt-4 flex items-center gap-3">
          <Button type="button" onClick={() => save.mutate()} disabled={!dirty || save.isPending}>
            {resolving ? '保存并开始下载' : '保存并重新物化'}
          </Button>
          {dirty ? <span className="text-sm text-zinc-500">有未保存的修改</span> : null}
        </div>
      ) : (
        <p className="mt-4 text-sm text-zinc-500">文件已经进入处理流程，当前无需调整。</p>
      )}
    </div>
  );
}

function positiveNumber(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined;
}
