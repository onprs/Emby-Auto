import { cn } from '@/lib/utils';

export type OverallProgressTone = 'active' | 'complete' | 'attention' | 'neutral';

export function normalizedOverallProgress(value: number): number {
  const bounded = Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0));
  return Math.round(bounded * 1000) / 10;
}

export function formatOverallProgress(value: number): string {
  const percent = normalizedOverallProgress(value);
  return `${Number.isInteger(percent) ? percent.toFixed(0) : percent.toFixed(1)}%`;
}

export function OverallProgressBar({
  value,
  label,
  ariaLabel,
  tone = 'active',
  compact = false,
  className,
}: {
  value: number;
  label: string;
  ariaLabel: string;
  tone?: OverallProgressTone;
  compact?: boolean;
  className?: string;
}) {
  const percent = normalizedOverallProgress(value);
  const formatted = formatOverallProgress(value);

  return (
    <div className={cn('w-full min-w-0', compact && 'max-w-72', className)}>
      <div className="mb-1 flex items-center justify-between gap-3 text-xs">
        <span className="min-w-0 truncate font-medium text-zinc-700" title={label}>{label}</span>
        <span className="shrink-0 tabular-nums text-zinc-500">{formatted}</span>
      </div>
      <div
        className={cn('w-full overflow-hidden rounded-full bg-zinc-200/80', compact ? 'h-1.5' : 'h-2')}
        role="progressbar"
        aria-label={ariaLabel}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        aria-valuetext={formatted}
      >
        <div
          className={cn(
            'h-full rounded-full transition-[width] duration-500 ease-out',
            tone === 'attention' && 'bg-red-600',
            tone === 'complete' && 'bg-emerald-700',
            tone === 'active' && 'bg-sky-600',
            tone === 'neutral' && 'bg-zinc-500',
          )}
          style={{ width: `${percent}%` }}
        />
      </div>
    </div>
  );
}
