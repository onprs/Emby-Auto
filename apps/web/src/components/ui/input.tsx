import type { InputHTMLAttributes } from 'react';

import { useFieldHintId } from '@/components/ui/field-hint';
import { cn } from '@/lib/utils';

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  const fieldHintId = useFieldHintId();
  return (
    <input
      aria-describedby={fieldHintId}
      className={cn(
        'flex h-10 w-full rounded-lg border border-zinc-300 bg-white px-3 text-sm text-zinc-950 shadow-sm outline-none transition-[border-color,box-shadow] duration-150 placeholder:text-zinc-400 hover:border-zinc-400 focus:border-emerald-600 focus:ring-4 focus:ring-emerald-600/10 disabled:cursor-not-allowed disabled:bg-zinc-100 disabled:text-zinc-500',
        className,
      )}
      {...props}
    />
  );
}
