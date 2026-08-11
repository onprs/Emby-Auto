import { useState } from 'react';

import { Button } from '@/components/ui/button';
import { ConfirmDialog } from '@/components/ui/confirm-dialog';
import type { TaskActionResult, TaskActionTarget } from '@/features/tasks/task-actions';

interface TaskBatchBarProps {
  count: number;
  retryable: number;
  deletable: number;
  retryTargets: TaskActionTarget[];
  deleteTargets: TaskActionTarget[];
  results: TaskActionResult[] | null;
  onAction: (action: 'retry' | 'delete', targets: TaskActionTarget[]) => Promise<TaskActionResult[]>;
  onClear: () => void;
}

/** Floating bar for bulk retry/delete with explicit confirmations. */
export function TaskBatchBar({ count, retryable, deletable, retryTargets, deleteTargets, results, onAction, onClear }: TaskBatchBarProps) {
  const [confirm, setConfirm] = useState<'retry' | 'delete' | null>(null);
  const [running, setRunning] = useState(false);

  const run = async (action: 'retry' | 'delete') => {
    setRunning(true);
    try {
      const eligible = action === 'retry' ? retryTargets : deleteTargets;
      await onAction(action, eligible);
      setConfirm(null);
    } finally {
      setRunning(false);
    }
  };

  const failed = results?.filter((result) => !result.ok) ?? [];

  return (
    <div className="mb-4 animate-scale-in rounded-xl border border-emerald-200 bg-white px-4 py-3 shadow-md ring-1 ring-emerald-100" role="region" aria-label="批量操作">
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-sm text-zinc-800">已选 {count} 项</span>
        <Button type="button" size="default" variant="outline" disabled={running || retryable === 0} onClick={() => setConfirm('retry')}>
          批量重试{retryable > 0 ? ` (${retryable})` : ''}
        </Button>
        <Button type="button" size="default" variant="outline" className="border-red-300 text-red-700 hover:bg-red-50" disabled={running || deletable === 0} onClick={() => setConfirm('delete')}>
          批量删除{deletable > 0 ? ` (${deletable})` : ''}
        </Button>
        <Button type="button" size="default" variant="ghost" disabled={running} onClick={onClear}>
          取消选择
        </Button>
      </div>

      <ConfirmDialog
        open={confirm !== null}
        title={confirm === 'delete' ? `确认删除 ${deletable} 项任务` : `确认重试 ${retryable} 项任务`}
        danger={confirm === 'delete'}
        lines={
          confirm === 'delete'
            ? [
                '停止这些任务正在进行的下载、转码或处理。',
                '删除尚未入库的下载源文件、种子任务和临时缓存。',
                '已经成功入库到 Emby 的正式资源不会被删除。',
              ]
            : [`将重试 ${retryable} 项可恢复的任务（复用原任务配置，创建新的执行记录，保留此前失败信息）。`]
        }
        confirmLabel={confirm === 'delete' ? '确认删除' : '确认重试'}
        running={running}
        onConfirm={() => confirm && void run(confirm)}
        onCancel={() => setConfirm(null)}
      />

      {results && !confirm ? (
          <div className={failed.length === 0 ? 'mt-3 rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-2 text-sm text-emerald-800' : 'mt-3 rounded-lg border border-red-200 bg-red-50 px-4 py-2 text-sm text-red-800'} role="status">
            {failed.length === 0
              ? `全部 ${results.length} 项操作成功。`
              : `${results.length - failed.length} 项成功，${failed.length} 项失败（失败任务已保留，可查看详情后重试）。`}
          </div>
      ) : null}
    </div>
  );
}
