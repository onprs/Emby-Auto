import {
  appNavigationState,
  rememberListPosition,
  restoreListPosition,
  useListScrollRestoration,
} from '@/app/navigation-context';
import { normalizeInternalLocation } from '@/app/navigation';

export const defaultTaskListSource = '/acquisitions';

const lastSourceKey = 'emby-auto:task-list:last-source';

function storage(): Storage | undefined {
  try {
    return typeof sessionStorage === 'undefined' ? undefined : sessionStorage;
  } catch {
    return undefined;
  }
}

/** Legacy task-only adapter. New navigation uses the application-wide context. */
export function normalizeTaskListSource(value?: string | null): string | undefined {
  const normalized = normalizeInternalLocation(value);
  if (!normalized) return undefined;
  const pathname = new URL(normalized, 'http://localhost').pathname;
  return pathname === '/acquisitions' || pathname === '/tasks' ? normalized : undefined;
}

export function rememberTaskListPosition(source: string, scrollY = typeof window === 'undefined' ? 0 : window.scrollY): void {
  const normalized = normalizeTaskListSource(source);
  if (!normalized) return;
  rememberListPosition(normalized, scrollY);
  try {
    storage()?.setItem(lastSourceKey, normalized);
  } catch {
    // The shared navigation context also tolerates unavailable storage.
  }
}

export function lastTaskListSource(): string {
  return normalizeTaskListSource(storage()?.getItem(lastSourceKey)) ?? defaultTaskListSource;
}

export function restoreTaskListPosition(source: string): number | undefined {
  const normalized = normalizeTaskListSource(source);
  return normalized ? restoreListPosition(normalized) : undefined;
}

export const useTaskListScrollRestoration = useListScrollRestoration;

export type TaskListHistoryState = {
  taskListSource?: string;
  taskListDepth?: number;
};

export function taskListHistoryState(source: string, depth: number): TaskListHistoryState {
  const state = appNavigationState(source, depth);
  return {
    taskListSource: state.appReturnTo,
    taskListDepth: state.appHistoryDepth,
  };
}

export function taskListHistoryDepth(state: unknown, source: string): number | undefined {
  if (!state || typeof state !== 'object') return undefined;
  const candidate = state as TaskListHistoryState;
  if (normalizeTaskListSource(candidate.taskListSource) !== normalizeTaskListSource(source)) return undefined;
  return typeof candidate.taskListDepth === 'number' && candidate.taskListDepth >= 1 && candidate.taskListDepth <= 10
    ? Math.round(candidate.taskListDepth)
    : undefined;
}
