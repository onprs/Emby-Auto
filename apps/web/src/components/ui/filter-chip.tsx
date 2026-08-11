import { Link } from '@tanstack/react-router';
import type { ReactNode } from 'react';

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
      aria-pressed={active}
      aria-label={ariaLabel}
      className={cn(
        'rounded-full px-3.5 py-1.5 text-sm transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-600',
        active
          ? 'bg-zinc-900 font-medium text-white shadow-sm'
          : 'border border-zinc-300 bg-white text-zinc-600 hover:border-zinc-400 hover:bg-zinc-50 hover:text-zinc-900',
      )}
    >
      {label}
    </Link>
  );
}
