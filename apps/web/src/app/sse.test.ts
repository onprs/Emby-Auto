import { QueryClient, QueryObserver } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { EventStream } from '@/app/sse';
import { registerSessionLossHandler } from '@/app/session-runtime';

const encoder = new TextEncoder();

afterEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('EventStream', () => {
  it('合并事件突发，等待慢请求完成后补取最终状态，停止后不再刷新', async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient();
    let deliver!: ReadableStreamDefaultController<Uint8Array>;
    let finishSlowRead!: (value: string) => void;
    const signals: AbortSignal[] = [];
    const read = vi.fn(({ signal }: { signal: AbortSignal }) => {
      signals.push(signal);
      if (signals.length === 1) return new Promise<string>((resolve) => { finishSlowRead = resolve; });
      return Promise.resolve('最终状态');
    });
    const observer = new QueryObserver(queryClient, {
      queryKey: ['rss', 'subscription'], queryFn: read, initialData: '旧状态', staleTime: Infinity,
    });
    const unsubscribe = observer.subscribe(() => undefined);
    const events = new EventStream(queryClient, {
      fetch: async () => new Response(new ReadableStream<Uint8Array>({ start(controller) { deliver = controller; } })),
    });
    const notify = () => deliver.enqueue(encoder.encode(
      'data: {"topic":"rss.updated","resourceType":"rss_subscription","resourceId":"subscription"}\n\n',
    ));
    events.start();
    await vi.advanceTimersByTimeAsync(0);
    for (let i = 0; i < 50; i += 1) notify();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(read).toHaveBeenCalledTimes(1);
    for (let i = 0; i < 50; i += 1) notify();
    await vi.advanceTimersByTimeAsync(3_000);
    expect(read).toHaveBeenCalledTimes(1);
    expect(signals[0].aborted).toBe(false);
    finishSlowRead('中间状态');
    await vi.advanceTimersByTimeAsync(1_000);
    expect(read).toHaveBeenCalledTimes(2);
    expect(queryClient.getQueryData(['rss', 'subscription'])).toBe('最终状态');
    notify();
    await vi.advanceTimersByTimeAsync(0);
    events.stop();
    await vi.advanceTimersByTimeAsync(2_000);
    expect(read).toHaveBeenCalledTimes(2);
    unsubscribe();
    queryClient.clear();
  });

  it('离开页面后慢请求返回，仍保留未处理事件的失效状态', async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient();
    let finish!: (value: string) => void;
    queryClient.setQueryData(['rss'], '旧状态');
    const reading = queryClient.fetchQuery({
      queryKey: ['rss'], queryFn: () => new Promise<string>((resolve) => { finish = resolve; }),
    });
    const events = new EventStream(queryClient, {
      fetch: async () => new Response(new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(encoder.encode('data: {"topic":"rss.updated","resourceType":"rss_entry","resourceId":"entry"}\n\n'));
        },
      })),
    });
    events.start();
    await vi.advanceTimersByTimeAsync(0);
    events.stop({ clearCursor: false });
    finish('事件前的状态');
    await reading;
    expect(queryClient.getQueryState(['rss'])?.isInvalidated).toBe(true);
    queryClient.clear();
  });

  it('连接一直没有响应头时取消并重连', async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(init.signal?.reason), { once: true });
    }));
    const events = new EventStream(new QueryClient(), { fetch: fetcher, connectionTimeoutMs: 10, reconnectBaseMs: 1 });
    events.start();
    await vi.advanceTimersByTimeAsync(11);
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[0][1]?.signal?.aborted).toBe(true);
    events.stop();
    await vi.advanceTimersByTimeAsync(100);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('忽略停止后才返回的旧连接认证失败', async () => {
    let respond!: (response: Response) => void;
    const losses = vi.fn();
    const unregister = registerSessionLossHandler(losses);
    const events = new EventStream(new QueryClient(), {
      fetch: () => new Promise<Response>((resolve) => { respond = resolve; }),
    });
    events.start();
    events.stop();
    respond(new Response(null, { status: 401 }));
    await Promise.resolve();
    await Promise.resolve();
    expect(losses).not.toHaveBeenCalled();
    unregister();
  });
  it('starts once, sends the persisted cursor, and consumes named events', async () => {
    sessionStorage.setItem('emby_auto_last_event_id', '10000000-0000-0000-0000-000000000001');
    const queryClient = new QueryClient();
    const keys = [
      ['episode_task', '20000000-0000-0000-0000-000000000001'],
      ['tasks'], ['rss'], ['operation', '30000000-0000-0000-0000-000000000001'], ['dashboard'],
    ];
    for (const key of keys) queryClient.setQueryData(key, {});
    queryClient.setQueryData(['dashboard', 'system-metrics'], {});
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      expect(headers.get('Last-Event-ID')).toBe('10000000-0000-0000-0000-000000000001');
      const stream = new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(
            encoder.encode(
              'id: 10000000-0000-0000-0000-000000000002\n' +
                'event: task.updated\n' +
                'data: {"id":"10000000-0000-0000-0000-000000000002","topic":"task.updated","resourceType":"episode_task","resourceId":"20000000-0000-0000-0000-000000000001","operationId":"30000000-0000-0000-0000-000000000001"}\n\n',
            ),
          );
          controller.close();
        },
      });
      return new Response(stream, { status: 200, headers: { 'Content-Type': 'text/event-stream' } });
    });
    const events = new EventStream(queryClient, { fetch: fetcher as typeof fetch, reconnectBaseMs: 60_000, refreshIntervalMs: 5 });

    events.start();
    events.start();
    await vi.waitFor(() => {
      expect(sessionStorage.getItem('emby_auto_last_event_id')).toBe('10000000-0000-0000-0000-000000000002');
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => {
      for (const key of keys) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
    });
    expect(queryClient.getQueryState(['dashboard', 'system-metrics'])?.isInvalidated).toBe(false);

    events.stop({ clearCursor: false });
    expect(sessionStorage.getItem('emby_auto_last_event_id')).toBe('10000000-0000-0000-0000-000000000002');
  });

  it('clears an invalid cursor, revalidates protected queries, and reconnects', async () => {
    sessionStorage.setItem('emby_auto_last_event_id', '10000000-0000-0000-0000-000000000099');
    const queryClient = new QueryClient();
    queryClient.setQueryData(['tasks'], { items: [] });
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries');
    let calls = 0;
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => {
      calls += 1;
      if (calls === 1) {
        return new Response(JSON.stringify({ code: 'event_cursor_not_found', message: 'missing', details: {} }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      return new Response(
        new ReadableStream<Uint8Array>({
          start() {
            // Keep the recovered stream open until stop aborts it.
          },
        }),
        { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
      );
    });
    const events = new EventStream(queryClient, { fetch: fetcher as typeof fetch, reconnectBaseMs: 1, refreshIntervalMs: 5 });

    events.start();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    expect(sessionStorage.getItem('emby_auto_last_event_id')).toBeNull();
    await vi.waitFor(() => expect(invalidate).toHaveBeenCalledWith(
      expect.objectContaining({ predicate: expect.any(Function) }),
      { cancelRefetch: false },
    ));
    const secondHeaders = new Headers(fetcher.mock.calls[1]?.[1]?.headers);
    expect(secondHeaders.has('Last-Event-ID')).toBe(false);
    events.stop();
  });

  it('reconnects when a half-open stream misses the heartbeat deadline', async () => {
    const queryClient = new QueryClient();
    let calls = 0;
    const fetcher = vi.fn(async () => {
      calls += 1;
      const call = calls;
      let heartbeat: ReturnType<typeof setInterval> | undefined;
      return new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            if (call === 2) {
              heartbeat = setInterval(() => controller.enqueue(encoder.encode(': recovered\n\n')), 2);
            }
          },
          cancel() {
            if (heartbeat) {
              clearInterval(heartbeat);
            }
          },
        }),
        { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
      );
    });
    const events = new EventStream(queryClient, {
      fetch: fetcher as typeof fetch,
      reconnectBaseMs: 1,
      inactivityTimeoutMs: 5,
    });

    events.start();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    events.stop();
  });

  it('reports a 401 and stops without reconnecting', async () => {
    const queryClient = new QueryClient();
    const fetcher = vi.fn(async () => new Response(null, { status: 401 }));
    const losses: string[] = [];
    const unregister = registerSessionLossHandler((reason) => losses.push(reason));
    const events = new EventStream(queryClient, { fetch: fetcher as typeof fetch, reconnectBaseMs: 1 });

    events.start();
    await vi.waitFor(() => expect(losses).toEqual(['unauthorized']));
    await new Promise((resolve) => setTimeout(resolve, 5));
    expect(fetcher).toHaveBeenCalledTimes(1);
    unregister();
    events.stop();
  });
});
