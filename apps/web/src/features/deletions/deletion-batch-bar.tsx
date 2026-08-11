import { Trash2, X } from 'lucide-react';
import { useState } from 'react';

import { Button } from '@/components/ui/button';
import { ConfirmDialog } from '@/components/ui/confirm-dialog';

export function DeletionBatchBar({
  count,
  running,
  noun,
  lines,
  onDelete,
  onClear,
}: {
  count: number;
  running: boolean;
  noun: string;
  lines: string[];
  onDelete: () => Promise<void>;
  onClear: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  return (
    <div className="mb-4 animate-scale-in rounded-xl border border-emerald-200 bg-white px-4 py-3 shadow-md ring-1 ring-emerald-100" role="region" aria-label="批量操作">
      <ConfirmDialog
        open={confirming}
        title={`确认删除 ${count} 项${noun}`}
        danger
        lines={lines}
        confirmLabel="确认批量删除"
        running={running}
        onConfirm={() => void onDelete().finally(() => setConfirming(false))}
        onCancel={() => setConfirming(false)}
      />
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-sm font-medium text-zinc-900">已选择 {count} 项</span>
        <Button type="button" variant="outline" className="border-red-300 text-red-700 hover:bg-red-50" disabled={running} onClick={() => setConfirming(true)}>
          <Trash2 aria-hidden="true" />批量删除
        </Button>
        <Button type="button" variant="ghost" disabled={running} onClick={onClear}>
          <X aria-hidden="true" />清空选择
        </Button>
      </div>
    </div>
  );
}
