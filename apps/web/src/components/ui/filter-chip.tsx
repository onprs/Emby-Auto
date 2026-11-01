import { Link } from '@tanstack/react-router';
import type { ReactNode } from 'react';

import { SlidingSelection } from '@/components/ui/sliding-selection';
import { cn } from '@/lib/utils';

/**
 * Shared pill-style filter chip used by every list page so status/type
 * filters look and behave identically across the app.
 */
export function FilterChip({
  to,
  params,
  search,
  active,
  label,
  ariaLabel,
}: {
  to: string;
  params?: Record<string, string>;
  search?: Record<string, unknown>;
  active: boolean;
  label: ReactNode;
  ariaLabel?: string;
}) {
  return (
    <Link
      to={to}
      params={params as never}
      search={search as never}
      data-sliding-item
      data-sliding-active={active ? 'true' : undefined}
      aria-pressed={active}
      aria-label={ariaLabel}
      className={cn(
        'relative z-10 shrink-0 whitespace-nowrap rounded-full border px-3.5 py-1.5 text-sm transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-600',
        active
          ? 'border-emerald-300 font-medium text-emerald-950'
          : 'border-zinc-300 text-zinc-600 hover:border-zinc-400 hover:text-zinc-900',
      )}
    >
      {label}
    </Link>
  );
}

export function FilterChipGroup({
  label,
  activeKey,
  className,
  children,
}: {
  label: string;
  activeKey: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <SlidingSelection
      variant="pill"
      ariaLabel={label}
      activeKey={activeKey}
      scrollActiveIntoView
      className={cn('mb-4', className)}
      groupClassName="gap-2 py-0.5"
    >
      {children}
    </SlidingSelection>
  );
}
