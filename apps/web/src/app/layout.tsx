import { useMutation } from '@tanstack/react-query';
import { Link, Outlet, useLocation } from '@tanstack/react-router';
import {
  Activity,
  LogOut,
  Menu,
  PanelLeftClose,
  PanelLeftOpen,
  X,
} from 'lucide-react';
import { useEffect, useRef, useState } from 'react';

import { endSession } from '@/api/app-client';
import type { Session } from '@/api/generated/types.gen';
import { appNavigation, moduleForPathname, routeForPathname, type AppNavigationItem } from '@/app/navigation';
import { rememberLastValidAppLocation } from '@/app/navigation-context';
import type { EventStream, SseStatus } from '@/app/sse';
import { reportSessionLoss } from '@/app/session-runtime';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

const sidebarStorageKey = 'emby-auto:navigation:sidebar-collapsed';

function initialSidebarCollapsed(): boolean {
  try {
    return localStorage.getItem(sidebarStorageKey) === 'true';
  } catch {
    return false;
  }
}

export function AppLayout({ session, events }: { session: Session; events: EventStream }) {
  const [sseStatus, setSseStatus] = useState<SseStatus>('connecting');
  const [sidebarCollapsed, setSidebarCollapsed] = useState(initialSidebarCollapsed);
  const [mobileOpen, setMobileOpen] = useState(false);
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const mobileDrawerRef = useRef<HTMLElement>(null);
  const wasMobileOpen = useRef(false);
  const location = useLocation();
  const activeModule = moduleForPathname(location.pathname);
  const visibleNavigation = appNavigation.filter((item) => item.visible(session));

  useEffect(() => events.onStatus(setSseStatus), [events]);

  useEffect(() => {
    setMobileOpen(false);
    const route = routeForPathname(location.pathname);
    if (route && route.id !== 'forbidden') {
      rememberLastValidAppLocation(location.href);
    }
  }, [location.href, location.pathname]);

  useEffect(() => {
    if (wasMobileOpen.current && !mobileOpen) {
      menuButtonRef.current?.focus();
    }
    wasMobileOpen.current = mobileOpen;
  }, [mobileOpen]);

  useEffect(() => {
    if (!mobileOpen) return;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    closeButtonRef.current?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setMobileOpen(false);
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(mobileDrawerRef.current?.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ) ?? []);
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && (document.activeElement === first || !mobileDrawerRef.current?.contains(document.activeElement))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [mobileOpen]);

  const logout = useMutation({
    mutationFn: endSession,
    onSettled: () => reportSessionLoss('logout'),
  });

  const toggleSidebar = () => {
    setSidebarCollapsed((current) => {
      const next = !current;
      try {
        localStorage.setItem(sidebarStorageKey, String(next));
      } catch {
        // Local storage is optional; the sidebar still works for this session.
      }
      return next;
    });
  };

  return (
    <div className="min-h-screen bg-surface">
      <aside
        className={cn(
          'fixed inset-y-0 left-0 z-40 hidden flex-col bg-zinc-950 text-zinc-100 shadow-xl transition-[width] duration-300 ease-out lg:flex',
          sidebarCollapsed ? 'w-[72px]' : 'w-[228px]',
        )}
        aria-label="桌面主导航"
      >
        <div className={cn('flex h-16 shrink-0 items-center gap-2.5 border-b border-white/10', sidebarCollapsed ? 'justify-center px-2' : 'px-5')}>
          <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-emerald-500/15 ring-1 ring-emerald-400/30">
            <Activity className="size-4 text-emerald-400" aria-hidden="true" />
          </span>
          {!sidebarCollapsed ? <span className="animate-fade-in truncate font-semibold tracking-tight">Emby Auto</span> : null}
        </div>
        <NavigationList items={visibleNavigation} activeModule={activeModule} collapsed={sidebarCollapsed} />
        <div className="shrink-0 border-t border-white/10 p-3">
          <button
            type="button"
            className={cn('flex h-10 w-full cursor-pointer items-center rounded-lg text-zinc-400 transition-colors hover:bg-white/5 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500', sidebarCollapsed ? 'justify-center' : 'gap-3 px-3')}
            aria-label={sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'}
            title={sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'}
            onClick={toggleSidebar}
          >
            {sidebarCollapsed ? <PanelLeftOpen className="size-4" aria-hidden="true" /> : <PanelLeftClose className="size-4" aria-hidden="true" />}
            {!sidebarCollapsed ? <span className="text-sm">收起侧边栏</span> : null}
          </button>
        </div>
      </aside>

      {mobileOpen ? (
        <div className="fixed inset-0 z-50 lg:hidden">
          <button
            type="button"
            className="absolute inset-0 animate-fade-in bg-zinc-950/55 backdrop-blur-sm"
            aria-label="关闭导航遮罩"
            onClick={() => {
              setMobileOpen(false);
              menuButtonRef.current?.focus();
            }}
          />
          <aside ref={mobileDrawerRef} className="relative flex h-full w-[min(84vw,300px)] animate-slide-in-right flex-col bg-zinc-950 text-zinc-100 shadow-2xl" role="dialog" aria-modal="true" aria-label="主导航抽屉">
            <div className="flex h-16 shrink-0 items-center justify-between border-b border-white/10 px-4">
              <div className="flex items-center gap-2.5">
                <span className="grid size-8 place-items-center rounded-lg bg-emerald-500/15 ring-1 ring-emerald-400/30">
                  <Activity className="size-4 text-emerald-400" aria-hidden="true" />
                </span>
                <span className="font-semibold tracking-tight">Emby Auto</span>
              </div>
              <Button ref={closeButtonRef} type="button" variant="ghost" size="icon" className="text-zinc-300 hover:bg-white/10 hover:text-white" aria-label="关闭主导航" title="关闭" onClick={() => setMobileOpen(false)}>
                <X />
              </Button>
            </div>
            <NavigationList items={visibleNavigation} activeModule={activeModule} collapsed={false} onNavigate={() => setMobileOpen(false)} />
          </aside>
        </div>
      ) : null}

      <div className={cn('min-w-0 transition-[padding] duration-300 ease-out', sidebarCollapsed ? 'lg:pl-[72px]' : 'lg:pl-[228px]')}>
        <header className="sticky top-0 z-30 flex h-14 items-center justify-between gap-3 border-b border-zinc-200/80 bg-white/85 px-4 shadow-[0_1px_2px_rgb(9_9_11/0.04)] backdrop-blur-md sm:px-6 lg:h-16">
          <div className="flex min-w-0 items-center gap-3">
            <Button
              ref={menuButtonRef}
              type="button"
              variant="ghost"
              size="icon"
              className="lg:hidden"
              aria-label="打开主导航"
              title="打开主导航"
              aria-expanded={mobileOpen}
              onClick={() => setMobileOpen(true)}
            >
              <Menu />
            </Button>
            <RealtimeIndicator status={sseStatus} />
          </div>
          <div className="flex min-w-0 items-center gap-2.5">
            <span className="hidden size-8 place-items-center rounded-full bg-emerald-100 text-xs font-semibold text-emerald-800 ring-1 ring-emerald-200 sm:grid" aria-hidden="true">
              {session.user.username.slice(0, 1).toUpperCase()}
            </span>
            <span className="hidden max-w-48 truncate text-sm font-medium text-zinc-700 sm:block">{session.user.username}</span>
            <span className="hidden h-5 w-px bg-zinc-200 sm:block" aria-hidden="true" />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              title="退出登录"
              aria-label="退出登录"
              onClick={() => logout.mutate()}
              disabled={logout.isPending}
            >
              <LogOut />
            </Button>
          </div>
        </header>
        <main id="main-content" className="min-w-0">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function NavigationList({
  items,
  activeModule,
  collapsed,
  onNavigate,
}: {
  items: AppNavigationItem[];
  activeModule?: string;
  collapsed: boolean;
  onNavigate?: () => void;
}) {
  return (
    <nav className="scrollbar-thin min-h-0 flex-1 overflow-y-auto px-3 py-3" aria-label="主导航">
      <ul className="space-y-1">
        {items.map((item) => {
          const active = item.id === activeModule;
          const Icon = item.icon;
          return (
            <li key={item.id}>
              <Link
                to={item.to}
                aria-label={collapsed ? item.label : undefined}
                aria-current={active ? 'page' : undefined}
                title={collapsed ? item.label : undefined}
                onClick={onNavigate}
                className={cn(
                  'group relative flex h-10 min-w-0 items-center rounded-lg text-sm transition-all duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500',
                  collapsed ? 'justify-center px-2' : 'gap-3 px-3',
                  active ? 'bg-white/10 font-medium text-white shadow-sm' : 'text-zinc-400 hover:bg-white/5 hover:text-zinc-100 hover:pl-[0.9rem]',
                )}
              >
                {active ? <span className="absolute left-0 top-1/2 h-5 w-1 -translate-y-1/2 rounded-full bg-emerald-400 shadow-[0_0_8px_rgb(52_211_153/0.6)]" aria-hidden="true" /> : null}
                <Icon className={cn('size-4 shrink-0 transition-transform duration-150', !active && 'group-hover:scale-110')} aria-hidden="true" />
                {!collapsed ? <span className="truncate">{item.label}</span> : null}
              </Link>
              {active && item.children && !collapsed ? (
                <ul className="ml-7 mt-1 animate-fade-in space-y-1 border-l border-white/10 pl-3">
                  {item.children.map((child) => (
                    <li key={child.to}>
                      <Link
                        to={child.to}
                        onClick={onNavigate}
                        activeOptions={{ exact: child.to === item.to, includeSearch: false }}
                        className="block truncate rounded px-2 py-1.5 text-xs text-zinc-400 transition-colors hover:bg-white/5 hover:text-white"
                        activeProps={{ className: 'bg-white/10 font-medium text-white' }}
                      >
                        {child.label}
                      </Link>
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

function RealtimeIndicator({ status }: { status: SseStatus }) {
  const label = status === 'open' ? '实时已连接' : status === 'connecting' ? '实时连接中' : '实时已断开';
  return (
    <span className="flex items-center gap-2 rounded-full border border-zinc-200 bg-white px-2.5 py-1 text-xs text-zinc-500 shadow-sm" role="status" aria-live="polite">
      <span className="relative flex size-2" aria-hidden="true">
        {status !== 'closed' ? (
          <span className={cn('absolute inline-flex size-full animate-pulse-soft rounded-full', status === 'open' ? 'bg-emerald-400' : 'bg-amber-400')} />
        ) : null}
        <span
          className={cn(
            'relative inline-flex size-2 rounded-full',
            status === 'open' ? 'bg-emerald-500' : status === 'connecting' ? 'bg-amber-500' : 'bg-zinc-400',
          )}
        />
      </span>
      <span className="hidden sm:inline">{label}</span>
    </span>
  );
}
