import { client } from '@/api/generated/client.gen';
import { getSession, getSetupStatus, initializeSetup, login, logout } from '@/api/generated/sdk.gen';
import type { ApiError, InitializeSetupRequest, Session, SetupStatus } from '@/api/generated/types.gen';
import {
  guardProtectedRequest,
  isSessionExpired,
  observeApiResponse,
  reportSessionLoss,
  setRuntimeSession,
} from '@/app/session-runtime';

client.setConfig({ credentials: 'include' });
client.interceptors.request.use((request) => guardProtectedRequest(request));
client.interceptors.response.use((response, request) => observeApiResponse(response, request));

/**
 * Unified API failure. Carries the stable server error code, HTTP status and
 * request ID so the UI can branch on conflicts, auth loss and not-found.
 */
export class ApiFailure extends Error {
  readonly code: string;
  readonly details: Record<string, unknown>;
  readonly status: number | null;
  readonly requestId: string | null;

  constructor(error: unknown, fallback: string, status: number | null = null) {
    const apiError = isApiError(error) ? error : undefined;
    super(apiError?.message ?? fallback);
    this.name = 'ApiFailure';
    this.code = apiError?.code ?? 'request_failed';
    this.details = apiError?.details ?? {};
    this.status = status;
    this.requestId = apiError?.requestId ?? null;
  }

  get isUnauthorized(): boolean {
    return this.status === 401;
  }

  get isNotFound(): boolean {
    return this.status === 404;
  }

  get isConflict(): boolean {
    return this.status === 409;
  }

  get isUnavailable(): boolean {
    return this.status === 503;
  }
}

export function isApiError(value: unknown): value is ApiError {
  if (!value || typeof value !== 'object') {
    return false;
  }
  const candidate = value as Partial<ApiError>;
  return typeof candidate.code === 'string' && typeof candidate.message === 'string' && typeof candidate.details === 'object';
}

/**
 * Unwraps a generated SDK result (or its promise) into its data payload or a
 * typed ApiFailure. The data type is supplied explicitly because the SDK
 * returns a discriminated union that a generic helper cannot always narrow.
 */
export async function unwrap<T>(result: unknown, fallback: string): Promise<T> {
  const resolved = await result;
  const value = resolved as { data?: T; error?: unknown; response?: { status?: number } };
  if (value.data !== undefined && value.data !== null) {
    return value.data;
  }
  const status = value.response?.status ?? null;
  throw new ApiFailure(value.error, fallback, status);
}

export async function fetchSetupStatus(): Promise<SetupStatus> {
  return unwrap(await getSetupStatus(), '无法读取初始化状态');
}

export async function submitSetup(body: InitializeSetupRequest): Promise<SetupStatus> {
  return unwrap(await initializeSetup({ body }), '初始化失败');
}

export async function fetchSession(): Promise<Session | null> {
  const result = await getSession();
  if (result.response?.status === 401) {
    setRuntimeSession(null);
    return null;
  }
  const session = await unwrap<Session>(result, '无法读取登录状态');
  if (isSessionExpired(session)) {
    reportSessionLoss('expired');
    return null;
  }
  setRuntimeSession(session);
  return session;
}

export async function endSession(): Promise<void> {
  const result = await logout();
  if (result.response?.status !== 204 && result.response?.status !== 200) {
    if (result.error) {
      throw new ApiFailure(result.error, '退出登录失败', result.response?.status ?? null);
    }
  }
  setRuntimeSession(null);
}

export async function createSession(username: string, password: string): Promise<Session> {
  const session = await unwrap<Session>(await login({ body: { username, password } }), '登录失败');
  if (isSessionExpired(session)) {
    reportSessionLoss('expired');
    throw new ApiFailure({ code: 'session_expired', message: '会话已过期', details: {} }, '会话已过期', 401);
  }
  setRuntimeSession(session);
  return session;
}
