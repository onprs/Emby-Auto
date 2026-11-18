import { useEffect, useId, useRef, useState, type ReactNode } from 'react';

import { useActionFeedback } from '@/components/action-feedback';
import { ActionMenu, type ActionMenuItem } from '@/components/ui/action-menu';
import { ConfirmDialog } from '@/components/ui/confirm-dialog';

/**
 * Combines the "more actions" menu with a two-step confirmation for dangerous
 * operations. The confirmation is a floating dialog; failures are reported to
 * the single application feedback region so multiple rows cannot overlap.
 * Shared by the RSS subscription, task, acquisition, and download list pages.
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
  const executing = useRef(false);
  const [pending, setPending] = useState<RecordAction | null>(null);
  const [running, setRunning] = useState(false);
  const { push, dismiss } = useActionFeedback();
  const alertKey = useId();

  // 行消失（刷新或离开页面）时同步移除该行的失败提示。
  useEffect(() => () => dismiss(alertKey), [alertKey, dismiss]);

  const execute = async (action: RecordAction) => {
    if (executing.current || action.disabled) return;
    dismiss(alertKey);
    if (action.confirmLines && pending?.key !== action.key) {
      setPending(action);
      return;
    }
    executing.current = true;
    setRunning(true);
    try {
      const failure = await action.run();
      if (failure) {
        // 失败时关闭确认框，并把原因交给全局提示区；再次执行必须重新确认。
        setPending(null);
        push(alertKey, failure);
        return;
      }
      setPending(null);
    } catch (cause) {
      setPending(null);
      push(alertKey, cause instanceof Error ? cause.message : '操作失败');
    } finally {
      executing.current = false;
      setRunning(false);
      onChanged?.();
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
