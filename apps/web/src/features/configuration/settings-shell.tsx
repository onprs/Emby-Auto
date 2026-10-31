import { Link, Outlet, useLocation } from '@tanstack/react-router';
import { Settings } from 'lucide-react';
import { useEffect, useRef } from 'react';

import { cn } from '@/lib/utils';

export const settingsSections = [
  { id: 'services', label: '外部服务', to: '/settings/services' },
  { id: 'agent', label: 'Agent', to: '/settings/agent' },
  { id: 'storage', label: '存储与工具', to: '/settings/storage' },
  { id: 'subtitle-groups', label: '字幕组', to: '/settings/subtitle-groups' },
  { id: 'transcode', label: '转码', to: '/settings/transcode' },
] as const;

function normalizePathname(pathname: string): string {
  return pathname.replace(/\/+$/, '') || '/';
}

export function SettingsTabs() {
  const location = useLocation();
  const pathname = normalizePathname(location.pathname);
  const activeSection = settingsSections.find((section) => section.to === pathname)?.id;
  const activeRef = useRef<HTMLAnchorElement>(null);

  useEffect(() => {
    const node = activeRef.current;
    if (!activeSection || !node || typeof node.scrollIntoView !== 'function') return;
    node.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  }, [activeSection]);

  return (
    <div className="sticky top-14 z-20 border-b border-zinc-200/90 bg-white/90 backdrop-blur-md lg:top-16">
      <div className="mx-auto w-full max-w-[88rem] px-4 sm:px-6">
        <div className="flex items-stretch">
          <div className="hidden shrink-0 items-center gap-2 border-r border-zinc-200 py-3 pr-4 md:flex">
            <Settings className="size-4 text-zinc-400" aria-hidden="true" />
            <span className="whitespace-nowrap text-sm font-semibold text-zinc-900">系统设置</span>
          </div>
          <nav
            aria-label="系统设置"
            className="flex min-w-0 flex-1 items-stretch gap-1 overflow-x-auto [scrollbar-width:none] md:pl-3 [&::-webkit-scrollbar]:hidden"
          >
            {settingsSections.map((section) => {
              const active = section.id === activeSection;
              return (
                <Link
                  key={section.id}
                  ref={active ? activeRef : undefined}
                  to={section.to}
                  search={(previous) => previous}
                  aria-current={active ? 'page' : undefined}
                  className={cn(
                    'relative flex h-12 shrink-0 items-center whitespace-nowrap rounded-t-md px-3 text-sm transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-emerald-600',
                    active
                      ? 'font-medium text-emerald-700 after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-full after:bg-emerald-600'
                      : 'text-zinc-500 hover:bg-zinc-50 hover:text-zinc-900',
                  )}
                >
                  {section.label}
                </Link>
              );
            })}
          </nav>
        </div>
      </div>
    </div>
  );
}

export function SettingsLayout() {
  return (
    <>
      <SettingsTabs />
      <Outlet />
    </>
  );
}
