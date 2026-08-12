import { AlertTriangle } from 'lucide-react';
import { useEffect, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

/**
 * A centered confirmation dialog used by both list row actions and detail
 * pages, so every destructive operation shares one visual style. Renders as a
 * floating modal (does not affect surrounding layout), closes on Escape or
 * backdrop click, and shows a loading state while running.
 */

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  danger?: boolean;
  /** Short subtitle under the title. */
  subtitle?: string;
  /** Impact lines shown as a bulleted list. */
  lines: string[];
  confirmLabel: string;
  cancelLabel?: string;
  running?: boolean;
  confirmDisabled?: boolean;
  /** Extra content rendered above the impact lines. */
  children?: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({
  open,
  title,
  danger,
  subtitle,
  lines,
  confirmLabel,
  cancelLabel = '返回',
  running,
  confirmDisabled,
  children,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  useEffect(() => {
    if (!open) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !running) {
        onCancel();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [open, running, onCancel]);

  if (!open) {
    return null;
  }

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex animate-fade-in items-center justify-center bg-zinc-950/40 p-4"
      role="presentation"
      onClick={() => !running && onCancel()}
    >
      <div
        role="alertdialog"
        aria-modal="true"
        aria-label={title}
        className="w-full max-w-md animate-scale-in overflow-hidden rounded-xl bg-white shadow-2xl ring-1 ring-zinc-950/10"
        onClick={(event) => event.stopPropagation()}
      >
        <div className={cn('flex items-start gap-3 border-b px-5 py-4', danger ? 'border-red-100 bg-red-50/60' : 'border-zinc-100')}>
          <span className={cn('mt-0.5 inline-flex size-9 shrink-0 items-center justify-center rounded-full', danger ? 'bg-red-100 text-red-600' : 'bg-amber-100 text-amber-600')}>
            <AlertTriangle className="size-5" aria-hidden="true" />
          </span>
          <div className="min-w-0">
            <h3 className="text-base font-semibold text-zinc-950">{title}</h3>
            <p className="mt-0.5 text-xs text-zinc-500">{subtitle ?? (danger ? '此操作无法撤销，请确认影响范围' : '请确认后继续')}</p>
          </div>
        </div>
        <div className="px-5 py-4">
          {children}
          <ul className="space-y-2">
            {lines.map((line) => (
              <li key={line} className="flex items-start gap-2.5 text-sm text-zinc-700">
                <span className={cn('mt-1.5 size-1.5 shrink-0 rounded-full', danger ? 'bg-red-400' : 'bg-zinc-400')} aria-hidden="true" />
                <span className="min-w-0">{line}</span>
              </li>
            ))}
          </ul>
        </div>
        <div className="flex justify-end gap-2 border-t border-zinc-100 bg-zinc-50/60 px-5 py-3.5">
          <Button type="button" variant="outline" onClick={onCancel} disabled={running}>
            {cancelLabel}
          </Button>
          <Button
            type="button"
            className={danger ? 'bg-red-600 text-white hover:bg-red-700 focus-visible:ring-red-600' : undefined}
            onClick={onConfirm}
            disabled={running || confirmDisabled}
          >
            {running ? '执行中…' : confirmLabel}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
