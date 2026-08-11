import { beforeEach, describe, expect, it, vi } from 'vitest';

import {
  appNavigationState,
  lastValidAppLocation,
  rememberLastValidAppLocation,
  rememberListPosition,
  resolveReturnTarget,
  restoreListPosition,
} from '@/app/navigation-context';

describe('application navigation context', () => {
  beforeEach(() => sessionStorage.clear());

  it('prefers a validated immediate source and falls back to the owning list', () => {
    expect(resolveReturnTarget({
      pathname: '/downloads/id',
      from: '/tasks/task-id?from=%2Ftasks%3Fstate%3Dfailed',
      state: appNavigationState('/downloads?phase=failed'),
    })).toEqual({ target: '/tasks/task-id?from=%2Ftasks%3Fstate%3Dfailed', canUseHistory: false });

    expect(resolveReturnTarget({ pathname: '/downloads/id', from: 'https://evil.test' }))
      .toEqual({ target: '/acquisitions', canUseHistory: false });
  });

  it('uses browser history only when state proves the previous entry is the same source', () => {
    const state = appNavigationState('/downloads?phase=failed');
    expect(resolveReturnTarget({ pathname: '/downloads/id', from: '/downloads?phase=failed', state }))
      .toEqual({ target: '/downloads?phase=failed', canUseHistory: true });
  });

  it('restores one exact list URL scroll position once', () => {
    const source = '/downloads?phase=paused&sortBy=updated_at&sortOrder=desc&query=show';
    rememberListPosition(source, 640);
    expect(restoreListPosition(source)).toBe(640);
    expect(restoreListPosition(source)).toBeUndefined();
    expect(restoreListPosition('/downloads?phase=failed')).toBeUndefined();
  });

  it('keeps only a valid last route for 404 recovery', () => {
    rememberLastValidAppLocation('/rss?cursor=11111111-1111-4111-8111-111111111111');
    expect(lastValidAppLocation()).toBe('/rss?cursor=11111111-1111-4111-8111-111111111111');
    rememberLastValidAppLocation('https://evil.test/');
    expect(lastValidAppLocation()).toBe('/rss?cursor=11111111-1111-4111-8111-111111111111');
  });

  it('survives unavailable session storage', () => {
    const getter = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
    expect(lastValidAppLocation()).toBe('/');
    getter.mockRestore();
  });
});
