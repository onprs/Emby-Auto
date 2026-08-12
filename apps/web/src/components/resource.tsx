import { Link } from '@tanstack/react-router';
import { ArrowLeft, ChevronLeft, ChevronRight } from 'lucide-react';
import type { ReactNode } from 'react';

import { usePageNavigation } from '@/app/navigation-context';
import { Button } from '@/components/ui/button';
import { ErrorState, LoadingState } from '@/components/ui/feedback';
import { cn } from '@/lib/utils';

export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  const navigation = usePageNavigation();
  const detail = Boolean(navigation.route?.detail || navigation.source);

  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
      <div className="flex min-w-0 items-start gap-2">
        {detail ? (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="-ml-2 mt-0.5 size-8"
            aria-label="返回"
            title="返回"
            onClick={navigation.goBack}
          >
            <ArrowLeft aria-hidden="true" />
          </Button>
        ) : null}
        <div className="min-w-0">
          {detail && navigation.breadcrumbs.length > 0 ? (
            <nav className="mb-1" aria-label="面包屑">
              <ol className="flex min-w-0 flex-wrap items-center gap-1 text-xs text-zinc-500">
                {navigation.breadcrumbs.map((crumb, index) => (
                  <li key={`${crumb.label}-${crumb.to ?? 'current'}`} className="flex min-w-0 items-center gap-1">
                    {index > 0 ? <ChevronRight className="size-3 shrink-0 text-zinc-400" aria-hidden="true" /> : null}
                    {crumb.to ? (
                      <Link to={crumb.to as never} className="max-w-48 truncate hover:text-zinc-900 hover:underline">{crumb.label}</Link>
                    ) : (
                      <span className="max-w-48 truncate" aria-current="page">{crumb.label}</span>
                    )}
                  </li>
                ))}
              </ol>
            </nav>
          ) : null}
          <h1 className="break-words text-xl font-semibold tracking-tight text-zinc-950 sm:text-[1.6rem] sm:leading-8">{title}</h1>
          {description ? <p className="mt-1.5 max-w-2xl text-sm leading-6 text-zinc-500">{description}</p> : null}
        </div>
      </div>
      {actions ? <div className="flex max-w-full shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  );
}

export function PageBody({ children }: { children: ReactNode }) {
  return <div className="mx-auto w-full max-w-[88rem] animate-fade-in-up px-4 py-6 sm:px-6 sm:py-8">{children}</div>;
}

export function DetailLoadingState({ title, label }: { title: string; label: string }) {
  return (
    <PageBody>
      <PageHeader title={title} />
      <LoadingState label={label} />
    </PageBody>
  );
}

export function DetailErrorState({ title, message, onRetry }: { title: string; message: string; onRetry?: () => void }) {
  return (
    <PageBody>
      <PageHeader title={title} />
      <ErrorState message={message} onRetry={onRetry} />
    </PageBody>
  );
}

export function PaginationControls({
  canGoBack,
  hasNext,
  onPrevious,
  onNext,
  isFetching,
}: {
  canGoBack: boolean;
  hasNext: boolean;
  onPrevious: () => void;
  onNext: () => void;
  isFetching?: boolean;
}) {
  return (
    <div className="flex items-center justify-end gap-2 px-1 py-3">
      <Button type="button" variant="outline" size="default" onClick={onPrevious} disabled={!canGoBack || isFetching}>
        <ChevronLeft />
        上一页
      </Button>
      <Button type="button" variant="outline" size="default" onClick={onNext} disabled={!hasNext || isFetching}>
        下一页
        <ChevronRight />
      </Button>
    </div>
  );
}

export function DataTable({ head, children, minWidth }: { head: ReactNode[]; children: ReactNode; minWidth?: string }) {
  return (
    <div className="overflow-x-auto rounded-xl border border-zinc-200/90 bg-white shadow-card">
      <table className={cn('w-full border-collapse text-sm', minWidth ? `min-w-[${minWidth}]` : 'min-w-[640px]')}>
        <thead>
          <tr className="border-b border-zinc-200 bg-zinc-50/80 text-left text-xs font-medium text-zinc-500">
            {head.map((cell, index) => (
              <th key={index} scope="col" className="px-4 py-3 font-medium">
                {cell}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-100 [&>tr]:transition-colors hover:[&>tr]:bg-zinc-50/70">{children}</tbody>
      </table>
    </div>
  );
}

export function DetailGrid({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="grid gap-x-6 gap-y-4 rounded-xl border border-zinc-200/90 bg-white p-5 shadow-card sm:grid-cols-2">
      {items.map((item) => (
        <div key={item.label} className="min-w-0">
          <dt className="text-xs font-medium uppercase tracking-wide text-zinc-400">{item.label}</dt>
          <dd className="mt-1 break-words text-sm text-zinc-900">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}
