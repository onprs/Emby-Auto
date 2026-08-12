import { describe, expect, it } from 'vitest';

import { IdempotencyKeyHolder, newIdempotencyKey } from '@/lib/idempotency';

describe('newIdempotencyKey', () => {
  it('generates unique non-empty keys', () => {
    const first = newIdempotencyKey();
    const second = newIdempotencyKey();
    expect(first).not.toBe('');
    expect(second).not.toBe('');
    expect(first).not.toBe(second);
  });
});

describe('IdempotencyKeyHolder', () => {
  it('reuses one key across retries until reset', () => {
    const holder = new IdempotencyKeyHolder();
    const key = holder.get();
    expect(holder.get()).toBe(key);
    expect(holder.get()).toBe(key);
    holder.reset();
    expect(holder.get()).not.toBe(key);
  });
});
