import { useLocation, useRouter, useSearch } from '@tanstack/react-router';
import { useEffect, useRef } from 'react';

import {
  breadcrumbsForLocation,
  defaultReturnForPathname,
  normalizeInternalLocation,
  routeForPathname,
} from '@/app/navigation';

const storagePrefix = 'emby-auto:navigation:';
const lastValidKey = `${storagePrefix}last-valid`;

export type AppNavigationHistoryState = {
  appReturnTo?: string;
  appHistoryDepth?: number;
};

declare module '@tanstack/history' {
  interface HistoryState extends AppNavigationHistoryState {}
}

function safeSessionStorage(): Storage | undefined {
  try {
    return typeof sessionStorage === 'undefined' ? undefined : sessionStorage;
  } catch {
    return undefined;
  }
}

export function appNavigationState(source: string, depth = 1): AppNavigationHistoryState {
  return {
    appReturnTo: normalizeInternalLocation(source),
    appHistoryDepth: Math.max(1, Math.min(10, Math.round(depth))),
  };
}

export function currentAppLocation(href: string): string {
  return normalizeInternalLocation(href) ?? '/';
}

export function contextualSearch(source: string, search: Record<string, unknown> = {}): Record<string, unknown> {
  return { ...search, from: normalizeInternalLocation(source) };
}

export function resolveReturnTarget({
  pathname,
  from,
  state,
}: {
  pathname: string;
  from?: string | null;
  state?: unknown;
}): { target: string; canUseHistory: boolean } {
  const explicit = normalizeInternalLocation(from);
  const historyState = state && typeof state === 'object' ? state as AppNavigationHistoryState : undefined;
  const stateSource = normalizeInternalLocation(historyState?.appReturnTo);
  const target = explicit ?? stateSource ?? defaultReturnForPathname(pathname);
  const canUseHistory = Boolean(
    stateSource &&
    stateSource === target &&
    typeof historyState?.appHistoryDepth === 'number' &&
    historyState.appHistoryDepth >= 1 &&
    historyState.appHistoryDepth <= 10,
  );
  return { target, canUseHistory };
}

export function usePageNavigation() {
  const router = useRouter();
  const location = useLocation();
  const search = useSearch({ strict: false }) as { from?: string };
  const route = routeForPathname(location.pathname);
  const resolved = resolveReturnTarget({ pathname: location.pathname, from: search.from, state: location.state });
  const source = normalizeInternalLocation(search.from) ?? normalizeInternalLocation((location.state as AppNavigationHistoryState | undefined)?.appReturnTo);

  const goBack = () => {
    if (resolved.canUseHistory) {
      const depth = (location.state as AppNavigationHistoryState).appHistoryDepth ?? 1;
      router.history.go(-depth);
      return;
    }
    void router.navigate({ to: resolved.target as never, replace: true });
  };

  return {
    route,
    source,
    returnTarget: resolved.target,
    breadcrumbs: breadcrumbsForLocation(location.pathname, source),
    goBack,
  };
}

function listScrollKey(source: string): string {
  return `${storagePrefix}scroll:${encodeURIComponent(source)}`;
}

export function rememberListPosition(source: string, scrollY = typeof window === 'undefined' ? 0 : window.scrollY): void {
  const normalized = normalizeInternalLocation(source);
  const target = safeSessionStorage();
  if (!normalized || !target) return;
  try {
    target.setItem(listScrollKey(normalized), String(Math.max(0, Math.round(scrollY))));
  } catch {
    // Storage may be blocked by browser policy; navigation remains functional.
  }
}

export function restoreListPosition(source: string): number | undefined {
  const normalized = normalizeInternalLocation(source);
  const target = safeSessionStorage();
  if (!normalized || !target) return undefined;
  try {
    const key = listScrollKey(normalized);
    const stored = target.getItem(key);
    target.removeItem(key);
    if (stored === null) return undefined;
    const value = Number(stored);
    return Number.isFinite(value) && value >= 0 ? value : undefined;
  } catch {
    return undefined;
  }
}

/** Restores once after list rows mount; a background query refresh cannot cancel it. */
export function useListScrollRestoration(source: string, ready: boolean): void {
  const pending = useRef<{ source: string; top?: number } | null>(null);

  useEffect(() => {
    if (!ready) return;
    if (pending.current?.source !== source) {
      pending.current = { source, top: restoreListPosition(source) };
    }
    if (!pending.current || pending.current.top === undefined) return;

    let secondFrame: number | undefined;
    const firstFrame = requestAnimationFrame(() => {
      secondFrame = requestAnimationFrame(() => {
        const current = pending.current;
        if (current?.source === source && current.top !== undefined) {
          window.scrollTo({ top: current.top });
          current.top = undefined;
        }
      });
    });
    return () => {
      cancelAnimationFrame(firstFrame);
      if (secondFrame !== undefined) cancelAnimationFrame(secondFrame);
    };
  }, [ready, source]);
}

export function rememberLastValidAppLocation(value: string): void {
  const normalized = normalizeInternalLocation(value);
  if (!normalized) return;
  try {
    safeSessionStorage()?.setItem(lastValidKey, normalized);
  } catch {
    // Ignore blocked storage.
  }
}

export function lastValidAppLocation(): string {
  try {
    return normalizeInternalLocation(safeSessionStorage()?.getItem(lastValidKey)) ?? '/';
  } catch {
    return '/';
  }
}
