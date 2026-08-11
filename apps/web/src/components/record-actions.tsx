import { AlertTriangle, X } from 'lucide-react';
import { useState, type ReactNode } from 'react';

import { ActionMenu, type ActionMenuItem } from '@/components/ui/action-menu';
import { ConfirmDialog } from '@/components/ui/confirm-dialog';

/**
 * Combines the "more actions" menu with a two-step confirmation for dangerous
 * operations. The confirmation is a floating dialog and the error a floating
 * popover, so neither stretches the height of the list row. Shared by the RSS
 * subscription, task, acquisition, and download list pages.
 */

export interface RecordAction {
  key: string;
  label: ReactNode;
  danger?: boolean;
  disabled?: boolean;
  title?: string;
  /** When set, selecting this action shows a confirmation with these lines. */
  confirmLines?: string[];
  confirmLabel?: string;
  run: () => Promise<string | null>;
}

export function RecordActions({ actions, onChanged }: { actions: RecordAction[]; onChanged?: () => void }) {
  const [pending, setPending] = useState<RecordAction | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const execute = async (action: RecordAction) => {
    setError(null);
    if (action.confirmLines && pending?.key !== action.key) {
      setPending(action);
      return;
    }
    setRunning(true);
    try {
      const failure = await action.run();
      if (failure) {
        setError(failure);
        return;
      }
      setPending(null);
      onChanged?.();
    } finally {
      setRunning(false);
    }
  };

  const menuItems: ActionMenuItem[] = actions.map((action) => ({
    key: action.key,
    label: action.label,
    danger: action.danger,
    disabled: action.disabled || running,
    title: action.title,
    onSelect: () => void execute(action),
  }));

  if (menuItems.length === 0) {
    return null;
  }

  return (
    <div className="relative inline-flex" onClick={(event) => event.stopPropagation()}>
      <ActionMenu items={menuItems} />

      {error ? (
        <div className="absolute right-0 top-full z-40 mt-1.5 w-72 rounded-lg border border-red-200 bg-white px-3.5 py-2.5 shadow-xl ring-1 ring-zinc-950/5" role="alert">
          <div className="flex items-start gap-2.5">
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-red-500" aria-hidden="true" />
            <p className="min-w-0 flex-1 break-words text-xs leading-relaxed text-red-800">{error}</p>
            <button type="button" aria-label="关闭" className="shrink-0 rounded p-0.5 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700" onClick={() => setError(null)}>
              <X className="size-3.5" aria-hidden="true" />
            </button>
          </div>
        </div>
      ) : null}

      <ConfirmDialog
        open={Boolean(pending?.confirmLines)}
        title={pending?.confirmLabel ?? '确认操作'}
        danger={pending?.danger}
        lines={pending?.confirmLines ?? []}
        confirmLabel={pending?.confirmLabel ?? '确认'}
        running={running}
        onConfirm={() => pending && void execute(pending)}
        onCancel={() => setPending(null)}
      />
    </div>
  );
}
