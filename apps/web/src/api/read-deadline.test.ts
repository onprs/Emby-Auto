import { afterEach, describe, expect, it, vi } from 'vitest';

import { unwrap } from '@/api/app-client';
import { getSetupStatus } from '@/api/generated/sdk.gen';
import { withReadDeadline } from '@/api/read-deadline';

afterEach(() => vi.restoreAllMocks());

describe('普通读取期限', () => {
  it('取消一直没有响应头的读取，并保留调用方主动取消的原因', async () => {
    const request = withReadDeadline(new Request('http://localhost/api/v1/setup/status'), 10);
    await new Promise<void>((resolve) => request.signal.addEventListener('abort', () => resolve(), { once: true }));
    expect(request.signal.reason.name).toBe('TimeoutError');

    const controller = new AbortController();
    const cancelled = withReadDeadline(new Request('http://localhost/api/v1/auth/session', { signal: controller.signal }));
    controller.abort(new DOMException('取消读取', 'AbortError'));
    expect(cancelled.signal.reason).toBe(controller.signal.reason);
  });

  it('响应头到达后响应体停滞，SDK 仍返回可识别的超时错误', async () => {
    const timeout = AbortSignal.timeout.bind(AbortSignal);
    vi.spyOn(AbortSignal, 'timeout').mockImplementation(() => timeout(10));
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const request = input as Request;
      return new Response(new ReadableStream({
        start(controller) {
          request.signal.addEventListener('abort', () => controller.error(request.signal.reason), { once: true });
        },
      }), { headers: { 'Content-Type': 'application/json' } });
    });

    await expect(unwrap(getSetupStatus({ fetch: fetcher }), '读取失败')).rejects.toMatchObject({
      code: 'request_timeout',
      message: '读取超时，请检查网络后重试',
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it('写操作、事件流和媒体流不使用普通读取期限', () => {
    const requests = [
      new Request('http://localhost/api/v1/dashboard/background-runtime', { method: 'PUT', body: '{}' }),
      new Request('http://localhost/api/v1/events'),
      new Request('http://localhost/api/v1/tasks/task-id/artifacts/video'),
    ];
    for (const request of requests) expect(withReadDeadline(request)).toBe(request);
  });
});
