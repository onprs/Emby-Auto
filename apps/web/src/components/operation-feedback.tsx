import { useQuery } from '@tanstack/react-query';
import { useEffect, useRef } from 'react';

import { ContextLink } from '@/components/context-link';
import { Button } from '@/components/ui/button';
import { fetchOperation } from '@/features/operations/api';
import { friendlyError } from '@/lib/presentation';

// 在原页面跟踪操作结果，避免提交成功后丢失异步失败信息。
export function OperationFeedback({ operationId, onSettled, onPendingChange }: { operationId?: string; onSettled: () => void; onPendingChange?: (pending: boolean) => void }) {
  const notified = useRef(new Set<string>());
  const operation = useQuery({
    queryKey: ['operation', operationId],
    queryFn: () => fetchOperation(operationId!),
    enabled: Boolean(operationId),
    refetchInterval: (query) => query.state.data && !['queued', 'running'].includes(query.state.data.status) ? false : 2_000,
  });
  const status = operation.data?.status;
  const terminal = status !== undefined && !['queued', 'running'].includes(status);
  useEffect(() => {
    onPendingChange?.(Boolean(operationId) && !terminal);
  }, [operationId, terminal, onPendingChange]);
  useEffect(() => {
    if (operationId && terminal && !notified.current.has(operationId)) {
      notified.current.add(operationId);
      onSettled();
    }
  }, [operationId, terminal, onSettled]);

  if (!operationId) return null;
  const failed = status === 'failed' || status === 'cancelled';
  const message = operation.error ? '暂时无法读取操作结果'
    : status === 'failed' ? friendlyError(operation.data?.errorCode, operation.data?.errorMessage)
      : status === 'cancelled' ? '操作已停止'
        : status === 'succeeded' ? '操作已完成' : '操作已提交，正在处理';
  return (
    <div className={`mb-4 flex flex-wrap items-center gap-2 rounded-lg px-3 py-2 text-sm ${failed || operation.error ? 'bg-amber-50 text-amber-800' : 'bg-zinc-50 text-zinc-600'}`} role={failed || operation.error ? 'alert' : 'status'}>
      <p className="min-w-0 flex-1">{message}</p>
      {operation.error ? <Button type="button" variant="outline" size="sm" onClick={() => void operation.refetch()}>重新读取</Button> : null}
      <ContextLink to="/operations/$operationId" params={{ operationId }} className="shrink-0 font-medium hover:underline">查看操作日志</ContextLink>
    </div>
  );
}
