import { CircleAlert } from 'lucide-react';

import type { TaskFailureInfo } from '@/features/tasks/task-failure';

/** One-line, non-technical failure copy for dense list rows. */
export function TaskFailureSummary({ info, className = '' }: { info: TaskFailureInfo; className?: string }) {
  return (
    <div className={`flex min-w-0 items-center gap-1.5 text-red-700 ${className}`} title={info.summary}>
      <CircleAlert className="size-3.5 shrink-0" aria-hidden="true" />
      <span className="min-w-0 truncate">{info.summary}</span>
    </div>
  );
}
