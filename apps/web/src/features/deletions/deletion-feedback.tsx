import { useQueries } from '@tanstack/react-query';
import { AlertTriangle, CheckCircle2, LoaderCircle, X } from 'lucide-react';
import { useEffect, useMemo, useRef } from 'react';

import { fetchOperation } from '@/features/operations/api';
import { friendlyError } from '@/lib/presentation';

export interface DeletionSubmission {
  resourceId: string;
  label: string;
  operationId?: string;
  error?: string;
}

export function DeletionFeedback({
  items,
  onDismiss,
  onSettled,
}: {
  items: DeletionSubmission[];
  onDismiss?: () => void;
  onSettled?: () => void;
}) {
  const tracked = useMemo(() => items.filter((item) => item.operationId), [items]);
  const operationQueries = useQueries({
    queries: tracked.map((item) => ({
      queryKey: ['operation', item.operationId],
      queryFn: () => fetchOperation(item.operationId as string),
      refetchInterval: (query: { state: { data?: { status?: string } } }) => isTerminal(query.state.data?.status) ? false : 1_000,
      staleTime: 0,
    })),
  });
  const operations = new Map(tracked.map((item, index) => [item.operationId, operationQueries[index]]));
  const states = items.map((item) => {
    if (item.error) return { item, state: 'failed' as const, message: item.error };
    const query = operations.get(item.operationId);
    const operation = query?.data;
    if (!operation || operation.status === 'queued' || operation.status === 'running') {
      return { item, state: 'pending' as const, message: query?.error instanceof Error ? '正在重新读取删除状态' : undefined };
    }
    if (operation.status === 'succeeded') return { item, state: 'succeeded' as const };
    return {
      item,
      state: 'failed' as const,
      message: friendlyError(operation.errorCode, operation.errorMessage ?? (operation.status === 'cancelled' ? '删除已取消' : '后台清理失败')),
    };
  });
  const pending = states.filter((item) => item.state === 'pending');
  const succeeded = states.filter((item) => item.state === 'succeeded');
  const failed = states.filter((item) => item.state === 'failed');
  const settled = items.length > 0 && pending.length === 0;
  const settledSignature = settled ? states.map((item) => `${item.item.resourceId}:${item.state}`).join('|') : '';
  const notified = useRef('');

  useEffect(() => {
    if (settledSignature && notified.current !== settledSignature) {
      notified.current = settledSignature;
      onSettled?.();
    }
  }, [onSettled, settledSignature]);

  if (items.length === 0) return null;

  const tone = failed.length > 0 ? 'border-red-200 bg-red-50 text-red-800' : pending.length > 0 ? 'border-sky-200 bg-sky-50 text-sky-900' : 'border-emerald-200 bg-emerald-50 text-emerald-800';
  const Icon = failed.length > 0 ? AlertTriangle : pending.length > 0 ? LoaderCircle : CheckCircle2;
  const summary = pending.length > 0
    ? `正在彻底删除 ${pending.length} 项，已完成 ${succeeded.length} 项`
    : failed.length > 0
      ? `删除完成 ${succeeded.length} 项，失败 ${failed.length} 项`
      : `已成功删除 ${succeeded.length} 项`;

  return (
    <div className={`mb-4 rounded-xl border px-4 py-3 text-sm ${tone}`} role={failed.length > 0 ? 'alert' : 'status'}>
      <div className="flex items-start gap-3">
        <Icon className={`mt-0.5 size-4 shrink-0 ${pending.length > 0 ? 'animate-spin' : ''}`} aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <p className="font-medium">{summary}</p>
          {failed.length > 0 ? (
            <ul className="mt-1 space-y-1">
              {failed.map(({ item, message }) => <li key={item.resourceId} className="break-words">{item.label}：{message ?? '删除失败'}</li>)}
            </ul>
          ) : null}
          {pending.some((item) => item.message) ? <p className="mt-1 text-xs opacity-80">后台仍在运行，页面会继续刷新状态。</p> : null}
        </div>
        {settled && onDismiss ? (
          <button type="button" aria-label="关闭删除结果" className="shrink-0 p-0.5 opacity-70 hover:opacity-100" onClick={onDismiss}>
            <X className="size-4" aria-hidden="true" />
          </button>
        ) : null}
      </div>
    </div>
  );
}

function isTerminal(status?: string): boolean {
  return status === 'succeeded' || status === 'failed' || status === 'cancelled';
}
