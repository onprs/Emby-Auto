import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';

import { cn } from '@/lib/utils';

export type SlidingSelectionVariant = 'underline' | 'pill' | 'segmented';

interface IndicatorRect {
  left: number;
  top: number;
  width: number;
  height: number;
}

function measureIndicator(item: HTMLElement, variant: SlidingSelectionVariant): IndicatorRect {
  if (variant === 'underline') {
    const inset = Math.min(8, item.offsetWidth / 4);
    return {
      left: item.offsetLeft + inset,
      top: item.offsetTop + item.offsetHeight - 2,
      width: Math.max(item.offsetWidth - inset * 2, 8),
      height: 2,
    };
  }
  return {
    left: item.offsetLeft,
    top: item.offsetTop,
    width: item.offsetWidth,
    height: item.offsetHeight,
  };
}

function sameIndicator(left: IndicatorRect | null, right: IndicatorRect | null): boolean {
  if (left === null || right === null) return left === right;
  return Math.abs(left.left - right.left) < 0.5 &&
    Math.abs(left.top - right.top) < 0.5 &&
    Math.abs(left.width - right.width) < 0.5 &&
    Math.abs(left.height - right.height) < 0.5;
}

export function SlidingSelection({
  variant,
  children,
  activeKey,
  ariaLabel,
  role = 'group',
  className,
  groupClassName,
  scrollActiveIntoView = false,
}: {
  variant: SlidingSelectionVariant;
  children: ReactNode;
  activeKey?: string;
  ariaLabel?: string;
  role?: 'group' | 'navigation';
  className?: string;
  groupClassName?: string;
  scrollActiveIntoView?: boolean;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const groupRef = useRef<HTMLDivElement>(null);
  const [indicator, setIndicator] = useState<IndicatorRect | null>(null);
  const [animated, setAnimated] = useState(false);

  const measure = useCallback(() => {
    const group = groupRef.current;
    const active = group?.querySelector<HTMLElement>('[data-sliding-active="true"]');
    const next = group && active ? measureIndicator(active, variant) : null;
    setIndicator((current) => (sameIndicator(current, next) ? current : next));
  }, [variant]);

  useLayoutEffect(() => {
    measure();
  });

  useEffect(() => {
    const frame = requestAnimationFrame(() => setAnimated(true));
    return () => cancelAnimationFrame(frame);
  }, []);

  useEffect(() => {
    const group = groupRef.current;
    if (!group) return undefined;

    let active = true;
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => {
      if (active) measure();
    });
    observer?.observe(group);
    group.querySelectorAll<HTMLElement>('[data-sliding-item]').forEach((item) => observer?.observe(item));

    const onResize = () => measure();
    window.addEventListener('resize', onResize);
    document.fonts?.ready.then(() => {
      if (active) measure();
    }).catch(() => undefined);
    return () => {
      active = false;
      observer?.disconnect();
      window.removeEventListener('resize', onResize);
    };
  }, [measure]);

  useEffect(() => {
    if (!scrollActiveIntoView) return;
    const container = scrollRef.current;
    const active = groupRef.current?.querySelector<HTMLElement>('[data-sliding-active="true"]');
    if (!container || !active) return;

    const activeRect = active.getBoundingClientRect();
    const containerRect = container.getBoundingClientRect();
    const inset = 16;
    let nextScrollLeft = container.scrollLeft;
    if (activeRect.left < containerRect.left + inset) {
      nextScrollLeft -= containerRect.left + inset - activeRect.left;
    } else if (activeRect.right > containerRect.right - inset) {
      nextScrollLeft += activeRect.right - (containerRect.right - inset);
    }
    if (Math.abs(nextScrollLeft - container.scrollLeft) < 1) return;

    const reduceMotion = typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    const left = Math.max(0, nextScrollLeft);
    if (typeof container.scrollTo === 'function') {
      container.scrollTo({ left, behavior: reduceMotion ? 'auto' : 'smooth' });
    } else {
      container.scrollLeft = left;
    }
  }, [activeKey, scrollActiveIntoView]);

  const indicatorClassName = variant === 'underline'
    ? 'rounded-full bg-emerald-600'
    : variant === 'pill'
      ? 'rounded-full bg-emerald-100 ring-1 ring-emerald-200/80'
      : 'rounded-md bg-white shadow-sm ring-1 ring-zinc-200/70';

  return (
    <div
      ref={scrollRef}
      className={cn(
        'relative min-w-0 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden',
        className,
      )}
    >
      <div
        ref={groupRef}
        role={role}
        aria-label={ariaLabel}
        className={cn('relative flex w-max min-w-full', groupClassName)}
      >
        <span
          aria-hidden="true"
          data-sliding-indicator
          className={cn('pointer-events-none absolute left-0 top-0 z-0', indicatorClassName)}
          style={{
            width: indicator?.width ?? 0,
            height: indicator?.height ?? 0,
            opacity: indicator ? 1 : 0,
            transform: indicator ? `translate3d(${indicator.left}px, ${indicator.top}px, 0)` : 'translate3d(0, 0, 0)',
            transition: animated
              ? 'transform 280ms cubic-bezier(0.22, 1, 0.36, 1), width 280ms cubic-bezier(0.22, 1, 0.36, 1), height 280ms cubic-bezier(0.22, 1, 0.36, 1), opacity 180ms ease'
              : 'none',
          }}
        />
        {children}
      </div>
    </div>
  );
}
