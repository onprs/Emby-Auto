import { AlertTriangle, Inbox, LoaderCircle, RefreshCw } from 'lucide-react';
import type { ReactNode } from 'react';

import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

export function LoadingState({ label = '读取中', className }: { label?: string; className?: string }) {
  return (
    <div className={cn('grid min-h-40 place-items-center px-4 py-8', className)} role="status">
      <span className="flex items-center gap-2 text-sm text-zinc-500">
        <LoaderCircle className="size-4 animate-spin" aria-hidden="true" />
        {label}
      </span>
    </div>
  );
}

export function ErrorState({
  message,
  requestId,
  onRetry,
  className,
}: {
  message: string;
  requestId?: string | null;
  onRetry?: () => void;
  className?: string;
}) {
  return (
    <div className={cn('animate-scale-in rounded-xl border border-red-200 bg-red-50 px-4 py-4', className)} role="alert">
      <div className="flex items-start gap-3">
        <AlertTriangle className="mt-0.5 size-4 shrink-0 text-red-600" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <p className="break-words text-sm text-red-800">{message}</p>
          {requestId ? <p className="mt-1 text-xs text-red-600">请求 ID：{requestId}</p> : null}
          {onRetry ? (
            <Button type="button" variant="outline" size="default" className="mt-3" onClick={onRetry}>
              <RefreshCw />
              重试
            </Button>
          ) : null}
        </div>
      </div>
    </div>
  );
}

export function EmptyState({ title, description, action, className }: { title: string; description?: string; action?: ReactNode; className?: string }) {
  return (
    <div className={cn('grid min-h-40 place-items-center rounded-xl border border-dashed border-zinc-300 bg-white/60 px-4 py-12 text-center', className)}>
      <div className="flex flex-col items-center">
        <span className="grid size-11 place-items-center rounded-full bg-zinc-100 ring-1 ring-zinc-200/80" aria-hidden="true">
          <Inbox className="size-5 text-zinc-400" />
        </span>
        <p className="mt-3 text-sm font-medium text-zinc-700">{title}</p>
        {description ? <p className="mt-1 text-sm text-zinc-500">{description}</p> : null}
        {action ? <div className="mt-4">{action}</div> : null}
      </div>
    </div>
  );
}
