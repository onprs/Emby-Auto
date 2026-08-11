import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  guardProtectedRequest,
  isSessionExpired,
  registerSessionLossHandler,
  setRuntimeSession,
} from '@/app/session-runtime';

afterEach(() => {
  setRuntimeSession(null);
  vi.restoreAllMocks();
});

describe('session runtime', () => {
  it('recognises malformed and expired server sessions', () => {
    const user = { id: '10000000-0000-0000-0000-000000000001', username: 'admin' };
    expect(isSessionExpired({ user, expiresAt: 'invalid' }, 1)).toBe(true);
    expect(isSessionExpired({ user, expiresAt: '2026-01-01T00:00:00Z' }, Date.parse('2026-01-01T00:00:00Z'))).toBe(true);
    expect(isSessionExpired({ user, expiresAt: '2026-01-01T00:00:01Z' }, Date.parse('2026-01-01T00:00:00Z'))).toBe(false);
  });

  it('blocks protected requests after expiry and reports session loss', () => {
    const listener = vi.fn();
    const unregister = registerSessionLossHandler(listener);
    setRuntimeSession({
      user: { id: '10000000-0000-0000-0000-000000000001', username: 'admin' },
      expiresAt: '2026-01-01T00:00:00Z',
    });
    const protectedRequest = new Request('http://localhost/api/v1/tasks', { method: 'POST' });
    expect(() => guardProtectedRequest(protectedRequest, Date.parse('2026-01-01T00:00:01Z'))).toThrow('session_expired');
    expect(listener).toHaveBeenCalledWith('expired');

    expect(() => guardProtectedRequest(new Request('http://localhost/api/v1/auth/session'), Date.parse('2026-01-01T00:00:01Z'))).not.toThrow();
    unregister();
  });
});
