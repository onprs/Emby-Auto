import { useMutation } from '@tanstack/react-query';
import { Check, Library, X } from 'lucide-react';
import { useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import type { Task } from '@/api/generated/types.gen';
import { Button } from '@/components/ui/button';
import { ConfirmDialog } from '@/components/ui/confirm-dialog';
import { ErrorState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { importTaskCommand, reviewTaskCommand } from '@/features/tasks/api';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { friendlyError } from '@/lib/presentation';

export function AcquisitionReviewCommands({ task, onChanged }: { task: Task; onChanged: () => void }) {
  const [notes, setNotes] = useState('');
  const [confirmation, setConfirmation] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const holder = useState(() => new IdempotencyKeyHolder())[0];
  const command = useMutation({
    mutationFn: async (action: 'approve' | 'reject' | 'legacy-import') => {
      setError(null);
      setNotice(null);
      const key = holder.get();
      if (action === 'legacy-import') return importTaskCommand(task.id, key, task.version);
      return reviewTaskCommand(task.id, key, task.version, action === 'approve' ? 'approved' : 'rejected', notes);
    },
    onSuccess: (_result, action) => {
      holder.reset();
      setConfirmation(false);
      setNotice(action === 'approve'
        ? '审核已通过，入库任务已自动创建。'
        : action === 'reject'
          ? '审核结果已保存。'
          : '入库任务已创建。');
      onChanged();
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        holder.reset();
        onChanged();
      }
      setError(cause instanceof ApiFailure
        ? friendlyError(cause.code, cause.message)
        : cause instanceof Error
          ? cause.message
          : '操作失败');
    },
  });

  if (!task.actions.canReview && !task.actions.canImport && !notice && !error) return null;

  return (
    <section className="border-t border-zinc-200 pt-5" aria-labelledby={`review-command-${task.id}`}>
      <h4 id={`review-command-${task.id}`} className="text-sm font-semibold text-zinc-950">人工审核</h4>
      {task.actions.canReview ? (
        <div className="mt-3 max-w-xl space-y-2">
          <Label htmlFor={`review-notes-${task.id}`}>审核备注</Label>
          <Input
            id={`review-notes-${task.id}`}
            value={notes}
            onChange={(event) => setNotes(event.target.value)}
            placeholder="可填写文件质量或拒绝原因"
          />
        </div>
      ) : null}
      {notice ? (
        <p className="mt-3 rounded-lg border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-800" role="status">{notice}</p>
      ) : null}
      {error ? <ErrorState className="mt-3" message={error} /> : null}
      <ConfirmDialog
        open={confirmation}
        title="确认拒绝"
        lines={['拒绝后不会执行 Emby 入库，审核记录会保留在此任务中。']}
        confirmLabel="确认拒绝"
        running={command.isPending}
        onConfirm={() => command.mutate('reject')}
        onCancel={() => setConfirmation(false)}
      />
      <div className="mt-4 flex flex-wrap gap-2">
        {task.actions.canReview ? (
          <>
            <Button type="button" onClick={() => command.mutate('approve')} disabled={command.isPending}>
              <Check aria-hidden="true" />审核通过并入库
            </Button>
            <Button type="button" variant="outline" onClick={() => setConfirmation(true)} disabled={command.isPending}>
              <X aria-hidden="true" />拒绝
            </Button>
          </>
        ) : null}
        {task.actions.canImport ? (
          <Button type="button" onClick={() => command.mutate('legacy-import')} disabled={command.isPending}>
            <Library aria-hidden="true" />继续历史任务入库
          </Button>
        ) : null}
      </div>
    </section>
  );
}
