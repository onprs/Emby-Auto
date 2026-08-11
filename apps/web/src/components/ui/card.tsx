import type { HTMLAttributes, ReactNode } from 'react';

import { cn } from '@/lib/utils';

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('rounded-xl border border-zinc-200/90 bg-white shadow-card transition-shadow duration-200 hover:shadow-card-hover', className)} {...props} />;
}

export function CardHeader({ className, action, fill = false, children, ...props }: HTMLAttributes<HTMLDivElement> & { action?: ReactNode; fill?: boolean }) {
  if (action && fill) {
    return (
      <div className={cn('flex flex-wrap items-start justify-between gap-3 border-b border-zinc-100 px-5 py-4', className)} {...props}>
        {children}
        <div className="shrink-0">{action}</div>
      </div>
    );
  }
  if (action) {
    return (
      <div className={cn('flex items-center justify-between gap-3 border-b border-zinc-100 px-5 py-4', className)} {...props}>
        <div className="min-w-0">{children}</div>
        <div className="shrink-0">{action}</div>
      </div>
    );
  }
  return <div className={cn('border-b border-zinc-100 px-5 py-4', className)} {...props}>{children}</div>;
}

export function CardTitle({ className, ...props }: HTMLAttributes<HTMLHeadingElement>) {
  return <h3 className={cn('text-base font-semibold tracking-tight text-zinc-950', className)} {...props} />;
}

export function CardDescription({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn('mt-1 text-sm text-zinc-500', className)} {...props} />;
}

export function CardContent({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('px-5 py-4', className)} {...props} />;
}
