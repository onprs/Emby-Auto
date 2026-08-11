import { Link, useLocation } from '@tanstack/react-router';
import type { AnchorHTMLAttributes, MouseEvent, ReactNode } from 'react';

import { normalizeInternalLocation } from '@/app/navigation';
import {
  appNavigationState,
  contextualSearch,
  currentAppLocation,
  rememberListPosition,
} from '@/app/navigation-context';

type ContextLinkProps = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> & {
  to: string;
  params?: Record<string, string>;
  search?: Record<string, unknown>;
  children?: ReactNode;
  rememberList?: boolean;
};

/** Carries a validated immediate return route to another application page. */
export function ContextLink({
  to,
  params,
  search,
  children,
  rememberList = false,
  onClick,
  ...anchorProps
}: ContextLinkProps) {
  const location = useLocation();
  const source = currentAppLocation(location.href);
  const destination = normalizeInternalLocation(to) ?? '/';

  return (
    <Link
      {...anchorProps}
      to={destination as never}
      params={params as never}
      search={contextualSearch(source, search) as never}
      state={appNavigationState(source)}
      onClick={(event: MouseEvent<HTMLAnchorElement>) => {
        onClick?.(event);
        if (rememberList && !event.defaultPrevented) {
          rememberListPosition(source);
        }
      }}
    >
      {children}
    </Link>
  );
}
