import { useMutation, useQueryClient } from '@tanstack/react-query';
import { CheckCircle2, CircleAlert, RotateCcw, Settings } from 'lucide-react';
import { useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import type { Task } from '@/api/generated/types.gen';
import { ContextLink } from '@/components/context-link';
import { DetailGrid } from '@/components/resource';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ErrorState } from '@/components/ui/feedback';
import { retryTaskCommand } from '@/features/tasks/api';
import { taskFailureInfo } from '@/features/tasks/task-failure';
import { formatDateTime } from '@/lib/format';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { friendlyError, operationLabel } from '@/lib/presentation';

export function TaskFailurePanel({ task, onChanged }: { task: Task; onChanged: () => void }) {
  const queryClient = useQueryClient();
  const info = taskFailureInfo(task);
  const [submittedOperation, setSubmittedOperation] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const retry = useMutation({
    mutationFn: () => retryTaskCommand(task.id, holder.get(), task.version),
    onSuccess: (result) => {
      holder.reset();
      setSubmitError(null);
      setSubmittedOperation(result.operationId);
      onChanged();
      void queryClient.invalidateQueries({ queryKey: ['tasks'] });
      void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
      void queryClient.invalidateQueries({ queryKey: ['operations'] });
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        holder.reset();
        onChanged();
      }
      setSubmitError(retryFailureMessage(cause));
    },
  });

  if (!info) {
    return submittedOperation ? <RetrySubmitted operationId={submittedOperation} /> : null;
  }

  const latestOperation = task.operations.find((operation) => operation.id === submittedOperation)
    ?? task.operations.find((operation) => operation.id === info.latestOperationId);
  const shouldCheckSettings = info.recommendation.includes('设置') || info.recommendation.includes('服务连接');
  const shouldCheckDownload = !info.canRetry && info.recommendation.includes('重新下载');

  return (
    <Card className="mb-6 border-red-200" aria-labelledby="task-failure-heading">
      <CardHeader className="border-red-100 bg-red-50/50">
        <div className="flex items-start gap-3">
          <span className="inline-flex size-9 shrink-0 items-center justify-center rounded-full bg-red-100 text-red-600">
            <CircleAlert className="size-5" aria-hidden="true" />
          </span>
          <div className="min-w-0">
            <CardTitle id="task-failure-heading">失败信息</CardTitle>
            <CardDescription className="text-red-800">{info.summary}</CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-5">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-zinc-400">详细原因</p>
          <p className="mt-1 text-sm leading-relaxed text-zinc-800">{info.detail}</p>
        </div>

        <DetailGrid items={[
          { label: '失败阶段', value: info.stageLabel },
          { label: '发生时间', value: formatDateTime(info.occurredAt) },
          { label: '最近执行', value: info.attemptLabel },
          { label: '是否可以重试', value: info.canRetry ? '可以，在处理建议完成后重试' : '不建议直接重试' },
          { label: '相关文件或服务', value: info.relatedResource },
          ...(info.displayCode ? [{ label: '排查标识', value: <code className="rounded bg-zinc-100 px-1.5 py-0.5 text-xs">{info.displayCode}</code> }] : []),
        ]} />

        {info.branches && info.branches.length === 2 ? (
          <div className="space-y-3 border-t border-zinc-200 pt-4" data-testid="task-failure-branches">
            <p className="text-xs font-medium uppercase tracking-wide text-zinc-500">分支详情</p>
            <DetailGrid
              items={info.branches.flatMap((branch) => {
                const op = task.operations.find((o) => o.id === branch.latestOperationId);
                return [
                  { label: `${branch.stageLabel}失败原因`, value: branch.detail },
                  { label: `${branch.stageLabel}建议`, value: branch.recommendation },
                  { label: `${branch.stageLabel}最近执行`, value: branch.attemptLabel },
                  ...(op ? [{ label: `${branch.stageLabel}运行记录`, value: `${operationLabel(op.kind)} · ${op.status}` }] : []),
                ];
              })}
            />
            <div className="flex flex-wrap gap-2">
              {info.branches.map((branch) => {
                const op = task.operations.find((o) => o.id === branch.latestOperationId);
                if (!op) return null;
                return (
                  <Button key={branch.stage} type="button" variant="ghost" size="sm" asChild>
                    <ContextLink to="/operations/$operationId" params={{ operationId: op.id }}>
                      查看{branch.stageLabel}运行记录
                    </ContextLink>
                  </Button>
                );
              })}
            </div>
          </div>
        ) : null}

        <div className="border-l-2 border-amber-400 bg-amber-50 px-3 py-2.5">
          <p className="text-xs font-medium text-amber-900">建议处理方式</p>
          <p className="mt-1 text-sm leading-relaxed text-amber-950">{info.recommendation}</p>
        </div>

        {submitError ? <ErrorState message={submitError} /> : null}
        {submittedOperation ? <RetrySubmitted operationId={submittedOperation} /> : null}

        <div className="flex flex-wrap gap-2">
          {info.canRetry && info.retryLabel ? (
            <Button type="button" onClick={() => retry.mutate()} disabled={retry.isPending}>
              <RotateCcw aria-hidden="true" />
              {retry.isPending ? '提交中…' : info.retryLabel}
            </Button>
          ) : null}
          {shouldCheckSettings ? (
            <Button type="button" variant="outline" asChild>
              <ContextLink to="/settings"><Settings aria-hidden="true" />检查配置</ContextLink>
            </Button>
          ) : null}
          {shouldCheckDownload ? (
            <Button type="button" variant="outline" asChild>
              <ContextLink to="/downloads/$downloadId" params={{ downloadId: task.downloadId }}>查看关联下载</ContextLink>
            </Button>
          ) : null}
          {latestOperation ? (
            <Button type="button" variant="ghost" asChild>
              <ContextLink to="/operations/$operationId" params={{ operationId: latestOperation.id }}>
                查看{operationLabel(latestOperation.kind)}记录
              </ContextLink>
            </Button>
          ) : null}
        </div>

        <details className="border-t border-zinc-200 pt-4">
          <summary className="cursor-pointer text-sm font-medium text-zinc-700 hover:text-zinc-950">查看技术详情</summary>
          <pre className="mt-3 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md bg-zinc-950 p-3 text-xs leading-relaxed text-zinc-100">{info.technicalDetails}</pre>
          <p className="mt-2 text-xs text-zinc-500">敏感凭据和完整服务器路径已隐藏。</p>
        </details>
      </CardContent>
    </Card>
  );
}

function RetrySubmitted({ operationId }: { operationId: string }) {
  return (
    <div className="flex items-start gap-2 border border-emerald-200 bg-emerald-50 px-3 py-2.5 text-sm text-emerald-800" role="status">
      <CheckCircle2 className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <p className="min-w-0 flex-1">重试请求已提交，任务状态和运行记录正在刷新。</p>
      <ContextLink to="/operations/$operationId" params={{ operationId }} className="shrink-0 font-medium hover:underline">查看运行</ContextLink>
    </div>
  );
}

function retryFailureMessage(cause: unknown): string {
  if (cause instanceof ApiFailure) {
    return friendlyError(cause.code, cause.message);
  }
  return '未能提交重试请求，请检查网络连接后再试。';
}
