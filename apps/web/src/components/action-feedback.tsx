import { AlertTriangle, X } from 'lucide-react';
import { createContext, useCallback, useContext, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';

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

/** 单一提示区：位于内容流内并吸顶，最高占 60vh；最新提示进入内部滚动区可视范围。 */
export function ActionFeedbackAlerts() {
  const { alerts, dismiss } = useActionFeedback();
  const regionRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const hostRef = useRef<HTMLElement | null>(null);
  const latest = alerts[alerts.length - 1];

  // 把提示区的实际高度交给页面吸顶层级使用，分区导航据此让出空间。
  useLayoutEffect(() => {
    const region = regionRef.current;
    if (!region) {
      const previousHost = hostRef.current;
      if (previousHost) {
        previousHost.style.removeProperty('--feedback-height');
      }
      hostRef.current = null;
      return;
    }
    const host = region.parentElement;
    if (!host) return;
    hostRef.current = host;
    const update = () => host.style.setProperty('--feedback-height', `${region.offsetHeight}px`);
    update();
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(update);
    observer.observe(region);
    return () => {
      observer.disconnect();
      host.style.removeProperty('--feedback-height');
    };
  }, [alerts.length]);

  // 只滚动提示区自身：新增或替换最新一条时把它带入可视区，不移动页面与焦点。
  useLayoutEffect(() => {
    const scroller = scrollRef.current;
    if (scroller) {
      scroller.scrollTop = scroller.scrollHeight;
    }
  }, [latest?.key, latest?.message]);

  if (alerts.length === 0) {
    return null;
  }
  return (
    <div
      ref={regionRef}
      className="pointer-events-none sticky z-20 max-h-[60vh] px-4 sm:px-6"
      style={{ top: 'var(--app-header-height, 3.5rem)' }}
    >
      <div className="mx-auto flex w-full max-w-[88rem] justify-end">
        <div
          ref={scrollRef}
          className="pointer-events-auto flex max-h-[60vh] w-full flex-col gap-2 overflow-y-auto overscroll-contain bg-surface py-3 sm:w-[min(22rem,100%)]"
        >
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
      </div>
    </div>
  );
}
