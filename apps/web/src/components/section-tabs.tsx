import { Link, Outlet, useLocation } from '@tanstack/react-router';
import type { LucideIcon } from 'lucide-react';

import { SlidingSelection } from '@/components/ui/sliding-selection';
import { cn } from '@/lib/utils';

export interface SectionTabDefinition {
  id: string;
  label: string;
  to: string;
}

export interface SectionNavigationDefinition {
  moduleLabel: string;
  moduleIcon: LucideIcon;
  ariaLabel: string;
  sections: readonly SectionTabDefinition[];
  preserveFrom?: boolean;
}

function normalizePathname(pathname: string): string {
  return pathname.replace(/\/+$/, '') || '/';
}

export function sectionIdForPathname(
  sections: readonly SectionTabDefinition[],
  pathname: string,
): string | undefined {
  const current = normalizePathname(pathname);
  return sections.find((section) => current === section.to || current.startsWith(`${section.to}/`))?.id;
}

export function SectionTabs({
  moduleLabel,
  moduleIcon: ModuleIcon,
  ariaLabel,
  sections,
  activeId,
  preserveFrom = false,
}: SectionNavigationDefinition & { activeId?: string }) {
  return (
    <div className="sticky top-14 z-20 border-b border-zinc-200/90 bg-white/90 backdrop-blur-md lg:top-16">
      <div className="mx-auto w-full max-w-[88rem] px-4 sm:px-6">
        <div className="flex items-stretch">
          <div className="hidden w-32 shrink-0 items-center gap-2 border-r border-zinc-200 py-3 pr-4 md:flex">
            <ModuleIcon className="size-4 text-zinc-400" aria-hidden="true" />
            <span data-section-module-label className="whitespace-nowrap text-sm font-semibold text-zinc-900">{moduleLabel}</span>
          </div>
          <SlidingSelection
            variant="underline"
            role="navigation"
            ariaLabel={ariaLabel}
            activeKey={activeId}
            scrollActiveIntoView
            className="flex-1 md:pl-3"
            groupClassName="items-stretch gap-1"
          >
            {sections.map((section) => {
              const active = section.id === activeId;
              return (
                <Link
                  key={section.id}
                  to={section.to}
                  search={preserveFrom
                    ? (previous) => {
                        const from = typeof previous === 'object' && previous !== null
                          ? (previous as { from?: unknown }).from
                          : undefined;
                        return typeof from === 'string' ? { from } : {};
                      }
                    : {}}
                  data-sliding-item
                  data-sliding-active={active ? 'true' : undefined}
                  aria-current={active ? 'page' : undefined}
                  className={cn(
                    'relative z-10 flex h-12 shrink-0 items-center whitespace-nowrap rounded-t-md px-3 text-sm font-medium transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-emerald-600',
                    active
                      ? 'text-emerald-700'
                      : 'text-zinc-500 hover:bg-zinc-50 hover:text-zinc-900',
                  )}
                >
                  {section.label}
                </Link>
              );
            })}
          </SlidingSelection>
        </div>
      </div>
    </div>
  );
}

export function SectionTabsLayout(navigation: SectionNavigationDefinition) {
  const location = useLocation();
  const activeId = sectionIdForPathname(navigation.sections, location.pathname);

  return (
    <>
      <SectionTabs {...navigation} activeId={activeId} />
      <Outlet />
    </>
  );
}
