import { MoreVertical } from 'lucide-react';
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

import { cn } from '@/lib/utils';

/**
 * A reusable "more actions" dropdown: a fixed-size vertical-ellipsis trigger
 * that opens a menu of dynamic items. Dangerous items are separated and
 * pinned to the bottom. The menu is positioned with `fixed` so it is never
 * clipped by a scrolling table container, flips upward near the viewport
 * bottom, and closes on outside click, Escape, or after an action.
 */

export interface ActionMenuItem {
  key: string;
  label: ReactNode;
  danger?: boolean;
  disabled?: boolean;
  title?: string;
  onSelect: () => void;
}

interface Position {
  top?: number;
  bottom?: number;
  left?: number;
  right?: number;
  maxHeight: number;
}

export function ActionMenu({ items, ariaLabel = '更多操作' }: { items: ActionMenuItem[]; ariaLabel?: string }) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<Position | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) {
      return;
    }
    const compute = () => {
      const rect = triggerRef.current!.getBoundingClientRect();
      const menuHeight = menuRef.current?.scrollHeight ?? 0;
      const menuWidth = menuRef.current?.offsetWidth ?? 176;
      const gap = 6;
      const spaceBelow = window.innerHeight - rect.bottom - gap;
      const openUp = spaceBelow < menuHeight && rect.top > spaceBelow;
      const left = Math.max(8, Math.min(rect.right - menuWidth, window.innerWidth - menuWidth - 8));
      if (openUp) {
        setPosition({ bottom: window.innerHeight - rect.top + gap, left, maxHeight: Math.max(80, rect.top - 16) });
      } else {
        setPosition({ top: rect.bottom + gap, left, maxHeight: Math.max(80, spaceBelow - 8) });
      }
    };
    compute();
    window.addEventListener('resize', compute);
    window.addEventListener('scroll', compute, true);
    return () => {
      window.removeEventListener('resize', compute);
      window.removeEventListener('scroll', compute, true);
    };
  }, [open, items.length]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (rootRef.current && !rootRef.current.contains(target) && menuRef.current && !menuRef.current.contains(target)) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  if (items.length === 0) {
    return null;
  }

  const normal = items.filter((item) => !item.danger);
  const dangerous = items.filter((item) => item.danger);

  const renderItem = (item: ActionMenuItem) => (
    <li key={item.key} role="none">
      <button
        type="button"
        role="menuitem"
        disabled={item.disabled}
        title={item.title}
        className={cn(
          'flex w-full items-center px-3 py-2 text-left text-sm transition-colors',
          item.danger ? 'text-red-700 hover:bg-red-50' : 'text-zinc-800 hover:bg-zinc-100',
          item.disabled && 'cursor-not-allowed opacity-50',
        )}
        onClick={() => {
          setOpen(false);
          item.onSelect();
        }}
      >
        {item.label}
      </button>
    </li>
  );

  return (
    <div ref={rootRef} className="inline-block" onClick={(event) => event.stopPropagation()}>
      <button
        ref={triggerRef}
        type="button"
        aria-label={ariaLabel}
        title={ariaLabel}
        aria-haspopup="menu"
        aria-expanded={open}
        className="inline-flex size-8 items-center justify-center rounded-md text-zinc-500 transition-colors hover:bg-zinc-100 hover:text-zinc-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-600"
        onClick={() => {
          setPosition(null);
          setOpen((value) => !value);
        }}
      >
        <MoreVertical className="size-4" aria-hidden="true" />
      </button>
      {open
        ? createPortal(
            <div
              ref={menuRef}
              role="menu"
              style={{
                position: 'fixed',
                top: position ? position.top : 0,
                bottom: position?.bottom,
                left: position?.left ?? 0,
                maxHeight: position?.maxHeight ?? Math.max(80, window.innerHeight - 16),
                visibility: position ? 'visible' : 'hidden',
              }}
              className="z-50 min-w-44 animate-scale-in overflow-y-auto rounded-lg border border-zinc-200 bg-white py-1 shadow-xl"
            >
              <ul role="none">{normal.map(renderItem)}</ul>
              {normal.length > 0 && dangerous.length > 0 ? <div className="my-1 border-t border-zinc-200" role="separator" /> : null}
              {dangerous.length > 0 ? <ul role="none">{dangerous.map(renderItem)}</ul> : null}
            </div>,
            document.body,
          )
        : null}
    </div>
  );
}
