import type { Session } from '@/api/generated/types.gen';

type SessionLossReason = 'unauthorized' | 'expired' | 'logout';
type SessionLossHandler = (reason: SessionLossReason) => void;

let expiresAt: number | null = null;
let lossHandler: SessionLossHandler | null = null;

export function setRuntimeSession(session: Session | null): void {
  expiresAt = session ? Date.parse(session.expiresAt) : null;
  if (expiresAt !== null && !Number.isFinite(expiresAt)) {
    expiresAt = null;
  }
}

export function isSessionExpired(session: Session, now = Date.now()): boolean {
  const expiration = Date.parse(session.expiresAt);
  return !Number.isFinite(expiration) || expiration <= now;
}

export function registerSessionLossHandler(handler: SessionLossHandler): () => void {
  lossHandler = handler;
  return () => {
    if (lossHandler === handler) {
      lossHandler = null;
    }
  };
}

export function reportSessionLoss(reason: SessionLossReason): void {
  expiresAt = null;
  lossHandler?.(reason);
}

export function guardProtectedRequest(request: Request, now = Date.now()): Request {
  if (!isProtectedApiRequest(request) || expiresAt === null || expiresAt > now) {
    return request;
  }
  reportSessionLoss('expired');
  throw new Error('session_expired');
}

export function observeApiResponse(response: Response, request: Request): Response {
  if (response.status === 401 && isProtectedApiRequest(request)) {
    reportSessionLoss('unauthorized');
  }
  return response;
}

function isProtectedApiRequest(request: Request): boolean {
  const path = new URL(request.url, globalThis.location?.origin ?? 'http://localhost').pathname;
  if (!path.startsWith('/api/v1/')) {
    return false;
  }
  return !path.startsWith('/api/v1/setup') && !path.startsWith('/api/v1/health') && !path.startsWith('/api/v1/auth');
}
