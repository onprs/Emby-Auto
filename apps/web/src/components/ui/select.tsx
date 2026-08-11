import { Check, ChevronDown } from 'lucide-react';
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

import { cn } from '@/lib/utils';

export interface SelectOption {
  value: string;
  label: ReactNode;
  disabled?: boolean;
}

interface Position {
  top?: number;
  bottom?: number;
  left: number;
  width: number;
  maxHeight: number;
}

/**
 * A custom styled dropdown that keeps a native <select> in the DOM for form
 * semantics, labels and tests, but renders the trigger and option list with
 * application styling (no native dropdown popup).
 */
export function Select({
  id,
  value,
  onChange,
  options,
  disabled,
  className,
  ariaLabel,
  placeholder,
}: {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  options: SelectOption[];
  disabled?: boolean;
  className?: string;
  ariaLabel?: string;
  placeholder?: string;
}) {
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const [position, setPosition] = useState<Position | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const listRef = useRef<HTMLUListElement>(null);

  const selectedIndex = options.findIndex((option) => option.value === value);
  const selected = selectedIndex >= 0 ? options[selectedIndex] : undefined;
  const enabledOptions = options.map((option, index) => ({ option, index })).filter(({ option }) => !option.disabled);

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) {
      return;
    }
    const compute = () => {
      const rect = triggerRef.current!.getBoundingClientRect();
      const listHeight = listRef.current?.scrollHeight ?? 0;
      const gap = 6;
      const spaceBelow = window.innerHeight - rect.bottom - gap;
      const openUp = spaceBelow < Math.min(listHeight, 280) && rect.top > spaceBelow;
      const width = Math.max(rect.width, 160);
      const left = Math.max(8, Math.min(rect.left, window.innerWidth - width - 8));
      if (openUp) {
        const maxHeight = Math.max(120, rect.top - 16);
        const height = Math.min(listHeight, maxHeight);
        // 锚定精确 top，使下拉框底部恰好贴合触发器，避免底部留白
        setPosition({ top: Math.max(8, rect.top - gap - height), left, width, maxHeight });
      } else {
        setPosition({ top: rect.bottom + gap, left, width, maxHeight: Math.max(120, spaceBelow - 8) });
      }
    };
    compute();
    // 首帧时列表可能尚未渲染（scrollHeight=0），渲染完成后再算一次精确高度
    const frame = requestAnimationFrame(compute);
    window.addEventListener('resize', compute);
    window.addEventListener('scroll', compute, true);
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener('resize', compute);
      window.removeEventListener('scroll', compute, true);
    };
  }, [open, options.length]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (rootRef.current && !rootRef.current.contains(target) && listRef.current && !listRef.current.contains(target)) {
        setOpen(false);
      }
    };
    document.addEventListener('pointerdown', onPointerDown);
    return () => document.removeEventListener('pointerdown', onPointerDown);
  }, [open]);

  useEffect(() => {
    if (open) {
      setActiveIndex(selectedIndex >= 0 ? selectedIndex : (enabledOptions[0]?.index ?? -1));
      requestAnimationFrame(() => {
        listRef.current?.querySelector<HTMLElement>('[data-selected="true"]')?.scrollIntoView({ block: 'nearest' });
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const commit = (index: number) => {
    const option = options[index];
    if (!option || option.disabled) {
      return;
    }
    onChange(option.value);
    setOpen(false);
    triggerRef.current?.focus();
  };

  const move = (direction: 1 | -1) => {
    if (enabledOptions.length === 0) {
      return;
    }
    const currentEnabled = enabledOptions.findIndex(({ index }) => index === activeIndex);
    const nextEnabled = currentEnabled < 0
      ? (direction === 1 ? 0 : enabledOptions.length - 1)
      : (currentEnabled + direction + enabledOptions.length) % enabledOptions.length;
    const next = enabledOptions[nextEnabled].index;
    setActiveIndex(next);
    listRef.current?.children[next]?.scrollIntoView({ block: 'nearest' });
  };

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (disabled) {
      return;
    }
    if (!open) {
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp' || event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        setOpen(true);
      }
      return;
    }
    switch (event.key) {
      case 'Escape':
        event.preventDefault();
        setOpen(false);
        triggerRef.current?.focus();
        break;
      case 'ArrowDown':
        event.preventDefault();
        move(1);
        break;
      case 'ArrowUp':
        event.preventDefault();
        move(-1);
        break;
      case 'Home':
        event.preventDefault();
        if (enabledOptions.length > 0) setActiveIndex(enabledOptions[0].index);
        break;
      case 'End':
        event.preventDefault();
        if (enabledOptions.length > 0) setActiveIndex(enabledOptions[enabledOptions.length - 1].index);
        break;
      case 'Enter':
      case ' ':
        event.preventDefault();
        if (activeIndex >= 0) {
          commit(activeIndex);
        }
        break;
      case 'Tab':
        setOpen(false);
        break;
    }
  };

  const listboxId = id ? `${id}-listbox` : undefined;

  return (
    <div ref={rootRef} className={cn('relative', className)}>
      {/* Native select stays in the DOM for label htmlFor, form semantics and tests. */}
      <select
        id={id}
        aria-hidden="true"
        tabIndex={-1}
        className="sr-only"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        disabled={disabled}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value} disabled={option.disabled}>
            {typeof option.label === 'string' ? option.label : option.value}
          </option>
        ))}
      </select>
      <button
        ref={triggerRef}
        type="button"
        disabled={disabled}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listboxId : undefined}
        className={cn(
          'flex h-10 w-full items-center justify-between gap-2 rounded-lg border border-zinc-300 bg-white px-3 text-left text-sm text-zinc-950 shadow-sm outline-none transition-[border-color,box-shadow] duration-150',
          'hover:border-zinc-400 focus:border-emerald-600 focus:ring-4 focus:ring-emerald-600/10',
          open && 'border-emerald-600 ring-4 ring-emerald-600/10',
          disabled && 'cursor-not-allowed bg-zinc-100 text-zinc-500 hover:border-zinc-300',
        )}
        onClick={() => setOpen((value) => !value)}
        onKeyDown={onKeyDown}
      >
        <span className={cn('min-w-0 truncate', !selected && 'text-zinc-400')}>
          {selected ? selected.label : (placeholder ?? '请选择')}
        </span>
        <ChevronDown className={cn('size-4 shrink-0 text-zinc-500 transition-transform duration-150', open && 'rotate-180')} aria-hidden="true" />
      </button>
      {open
        ? createPortal(
            <ul
              ref={listRef}
              id={listboxId}
              role="listbox"
              aria-label={ariaLabel}
              aria-activedescendant={activeIndex >= 0 && listboxId ? `${listboxId}-option-${activeIndex}` : undefined}
              style={{
                position: 'fixed',
                top: position?.top ?? 0,
                bottom: position?.bottom,
                left: position?.left ?? 0,
                width: position?.width ?? 160,
                maxHeight: position?.maxHeight ?? 280,
                visibility: position ? 'visible' : 'hidden',
              }}
              className="scrollbar-thin z-50 animate-scale-in overflow-y-auto rounded-lg border border-zinc-200 bg-white py-1 shadow-pop"
            >
              {options.map((option, index) => {
                const isSelected = index === selectedIndex;
                const isActive = index === activeIndex;
                return (
                  <li
                    key={option.value}
                    id={listboxId ? `${listboxId}-option-${index}` : undefined}
                    role="option"
                    aria-selected={isSelected}
                    data-selected={isSelected}
                    aria-disabled={option.disabled}
                    className={cn(
                      'flex cursor-pointer items-center justify-between gap-2 px-3 py-2 text-sm transition-colors',
                      isActive && 'bg-emerald-50',
                      isSelected ? 'font-medium text-emerald-800' : 'text-zinc-800',
                      option.disabled ? 'cursor-not-allowed opacity-50' : 'hover:bg-emerald-50',
                    )}
                    onMouseEnter={() => setActiveIndex(index)}
                    onClick={() => commit(index)}
                  >
                    <span className="min-w-0 truncate">{option.label}</span>
                    {isSelected ? <Check className="size-4 shrink-0 text-emerald-600" aria-hidden="true" /> : null}
                  </li>
                );
              })}
            </ul>,
            document.body,
          )
        : null}
    </div>
  );
}
