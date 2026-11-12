import { AlertTriangle, X } from 'lucide-react';
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';

export type ActionFeedbackEntry = {
  key: string;
  message: string;
};

type ActionFeedbackValue = {
  alerts: ActionFeedbackEntry[];
  push: (key: string, message: string) => void;
  dismiss: (key: string) => void;
};

const noop = () => undefined;

/**
 * 行内危险操作的失败反馈统一收口到应用外壳中的一个提示区，避免多个列表行
 * 各自渲染固定定位提示时占用同一坐标。没有 Provider 时只记录而不展示，
 * 真实应用始终由 AppLayout 提供。
 */
const ActionFeedbackContext = createContext<ActionFeedbackValue>({
  alerts: [],
  push: noop,
  dismiss: noop,
});

export function ActionFeedbackProvider({ children }: { children: ReactNode }) {
  const [alerts, setAlerts] = useState<ActionFeedbackEntry[]>([]);

  const push = useCallback((key: string, message: string) => {
    setAlerts((current) => [...current.filter((entry) => entry.key !== key), { key, message }]);
  }, []);

  const dismiss = useCallback((key: string) => {
    setAlerts((current) => current.filter((entry) => entry.key !== key));
  }, []);

  const value = useMemo(() => ({ alerts, push, dismiss }), [alerts, push, dismiss]);
  return <ActionFeedbackContext.Provider value={value}>{children}</ActionFeedbackContext.Provider>;
}

export function useActionFeedback(): ActionFeedbackValue {
  return useContext(ActionFeedbackContext);
}

/** 单一固定提示区：多条失败提示按顺序堆叠，区域有高度上限并可滚动。 */
export function ActionFeedbackAlerts() {
  const { alerts, dismiss } = useActionFeedback();
  if (alerts.length === 0) {
    return null;
  }
  return (
    <div className="fixed inset-x-4 bottom-4 z-50 flex max-h-[60vh] flex-col gap-2 overflow-y-auto sm:inset-x-auto sm:right-4 sm:w-[min(22rem,calc(100vw-2rem))]">
      {alerts.map((alert) => (
        <div
          key={alert.key}
          role="alert"
          className="rounded-lg border border-red-200 bg-white px-3.5 py-2.5 shadow-xl ring-1 ring-zinc-950/5"
        >
          <div className="flex items-start gap-2.5">
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-red-500" aria-hidden="true" />
            <p className="min-w-0 flex-1 break-words text-xs leading-relaxed text-red-800">{alert.message}</p>
            <button
              type="button"
              aria-label="关闭"
              className="shrink-0 rounded p-0.5 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700"
              onClick={() => dismiss(alert.key)}
            >
              <X className="size-3.5" aria-hidden="true" />
            </button>
          </div>
        </div>
      ))}
    </div>
  );
}
