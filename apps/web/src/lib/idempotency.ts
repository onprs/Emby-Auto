/**
 * Idempotency-Key lifecycle helper.
 *
 * A key stays stable for one user command and any of its network retries.
 * A new key is generated only when the user issues a brand-new command.
 */
export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return `cmd-${Date.now()}-${Math.random().toString(36).slice(2, 12)}`;
}

/**
 * Holds one idempotency key per logical command instance and reuses it across
 * retries until reset. Components keep one holder per pending command.
 */
export class IdempotencyKeyHolder {
  private key: string | null = null;

  get(): string {
    if (this.key === null) {
      this.key = newIdempotencyKey();
    }
    return this.key;
  }

  reset(): void {
    this.key = null;
  }
}
