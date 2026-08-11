import { describe, expect, it } from 'vitest';

import { ApiFailure, isApiError, unwrap } from '@/api/app-client';

describe('isApiError', () => {
  it('recognises structured API errors', () => {
    expect(isApiError({ code: 'not_found', message: 'missing', details: {}, requestId: 'r1' })).toBe(true);
  });

  it('rejects non-error shapes', () => {
    expect(isApiError(null)).toBe(false);
    expect(isApiError({ message: 'x' })).toBe(false);
    expect(isApiError('error')).toBe(false);
  });
});

describe('unwrap', () => {
  it('returns the data payload on success', async () => {
    await expect(unwrap({ data: { id: 1 } }, 'fallback')).resolves.toEqual({ id: 1 });
  });

  it('throws an ApiFailure with status and request id on error', async () => {
    const promise = unwrap(
      { error: { code: 'state_conflict', message: 'conflict', details: {}, requestId: 'req-9' }, response: { status: 409 } },
      'fallback',
    );
    await expect(promise).rejects.toMatchObject({ code: 'state_conflict', status: 409, requestId: 'req-9' });
  });

  it('exposes auth, not-found, conflict, and unavailable helpers', async () => {
    const failure = async (status: number): Promise<ApiFailure> => {
      try {
        await unwrap({ error: { code: 'x', message: 'm', details: {} }, response: { status } }, 'fallback');
        throw new Error('expected unwrap to fail');
      } catch (error) {
        return error as ApiFailure;
      }
    };

    const unauthorized = await failure(401);
    const notFound = await failure(404);
    const conflict = await failure(409);
    const unavailable = await failure(503);

    expect(conflict).toBeInstanceOf(ApiFailure);
    expect(unauthorized.isUnauthorized).toBe(true);
    expect(notFound.isNotFound).toBe(true);
    expect(conflict.isConflict).toBe(true);
    expect(unavailable.isUnavailable).toBe(true);
  });
});
