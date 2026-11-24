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

  it('游标失效后丢弃旧水位，收到新边界时只做一次全量失效', async () => {
    sessionStorage.setItem('emby_auto_last_event_id', '10000000-0000-0000-0000-000000000099');
    const queryClient = new QueryClient();
    queryClient.setQueryData(['tasks'], { items: [] });
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries');
    const fullRefreshes = () => invalidate.mock.calls.filter(([filters]) => filters !== undefined && 'predicate' in filters);
    const head = '10000000-0000-0000-0000-000000000100';
    let calls = 0;
    let deliverBoundary!: ReadableStreamDefaultController<Uint8Array>;
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
          start(controller) {
            deliverBoundary = controller;
          },
        }),
        { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
      );
    });
    const events = new EventStream(queryClient, { fetch: fetcher as typeof fetch, reconnectBaseMs: 1, refreshIntervalMs: 5 });

    try {
      events.start();
      await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
      const secondHeaders = new Headers(fetcher.mock.calls[1]?.[1]?.headers);
      expect(secondHeaders.has('Last-Event-ID')).toBe(false);
      // 新水位尚未到达时不能先失效：全量重读必须发生在拿到边界之后。
      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(fullRefreshes()).toHaveLength(0);

      deliverBoundary.enqueue(encoder.encode(`id: ${head}\nevent: stream.started\ndata: {"cursor":"${head}"}\n\n`));
      await vi.waitFor(() => expect(sessionStorage.getItem('emby_auto_last_event_id')).toBe(head));
      await vi.waitFor(() => expect(invalidate).toHaveBeenCalledWith(
        expect.objectContaining({ predicate: expect.any(Function) }),
        { cancelRefetch: false },
      ));
      expect(fullRefreshes()).toHaveLength(1);
      expect(queryClient.getQueryState(['tasks'])?.isInvalidated).toBe(true);
    } finally {
      events.stop();
      queryClient.clear();
    }
  });

  it('首次订阅从边界开始，并补取边界前已开始的初始化读取', async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient();
    const head = '10000000-0000-0000-0000-000000000101';
    let finishInitial!: (value: string) => void;
    let reads = 0;
    const read = vi.fn(() => {
      reads += 1;
      if (reads === 1) {
        return new Promise<string>((resolve) => { finishInitial = resolve; });
      }
      return Promise.resolve('边界后的最新状态');
    });
    const observer = new QueryObserver(queryClient, {
      queryKey: ['downloads', 'list'], queryFn: read, staleTime: Infinity,
    });
    const unsubscribe = observer.subscribe(() => undefined);
    const requests: Headers[] = [];
    let deliver!: ReadableStreamDefaultController<Uint8Array>;
    const events = new EventStream(queryClient, {
      fetch: async (_input: RequestInfo | URL, init?: RequestInit) => {
        requests.push(new Headers(init?.headers));
        return new Response(new ReadableStream<Uint8Array>({ start(controller) { deliver = controller; } }));
      },
      refreshIntervalMs: 10,
    });

    try {
      events.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(requests).toHaveLength(1);
      // 首连不携带任何历史游标，服务端据此返回当前水位边界而不是重放保留期历史。
      expect(requests[0].has('Last-Event-ID')).toBe(false);

      deliver.enqueue(encoder.encode(`id: ${head}\nevent: stream.started\ndata: {"cursor":"${head}"}\n\n`));
      await vi.advanceTimersByTimeAsync(0);
      expect(sessionStorage.getItem('emby_auto_last_event_id')).toBe(head);
      // 初始化读取仍在进行，边界失效不能先发一次会覆盖它的新请求。
      expect(read).toHaveBeenCalledTimes(1);

      finishInitial('初始化读取的旧状态');
      await vi.advanceTimersByTimeAsync(20);
      expect(read).toHaveBeenCalledTimes(2);
      expect(queryClient.getQueryData(['downloads', 'list'])).toBe('边界后的最新状态');
    } finally {
      events.stop();
      unsubscribe();
      queryClient.clear();
    }
  });

  it('边界后的重连从新水位继续，不再重放历史', async () => {
    const queryClient = new QueryClient();
    const head = '10000000-0000-0000-0000-000000000102';
    let calls = 0;
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => {
      calls += 1;
      if (calls === 1) {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              controller.enqueue(encoder.encode(`id: ${head}\nevent: stream.started\ndata: {"cursor":"${head}"}\n\n`));
              controller.close();
            },
          }),
          { status: 200 },
        );
      }
      return new Response(new ReadableStream<Uint8Array>({ start() {} }), { status: 200 });
    });
    const events = new EventStream(queryClient, { fetch: fetcher as typeof fetch, reconnectBaseMs: 1, refreshIntervalMs: 5 });

    try {
      events.start();
      await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
      const headers = new Headers(fetcher.mock.calls[1]?.[1]?.headers);
      expect(headers.get('Last-Event-ID')).toBe(head);
    } finally {
      events.stop();
      queryClient.clear();
    }
  });

  it('重连后刷新断线期间失败的读取，即使事件游标已经推进', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let apiAvailable = true;
    let value = 'before';
    const observer = new QueryObserver(queryClient, {
      queryKey: ['searches', 'recent'],
      queryFn: async () => {
        if (!apiAvailable) throw new Error('API 暂时不可用');
        return value;
      },
    });
    const unsubscribe = observer.subscribe(() => {});
    await vi.waitFor(() => expect(observer.getCurrentResult().data).toBe('before'));
    let firstStream!: ReadableStreamDefaultController<Uint8Array>;
    const encoder = new TextEncoder();
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>({ start(controller) { firstStream = controller; } }), { status: 200 }))
      .mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(encoder.encode(': connected\n\n')); } }), { status: 200 }));
    const events = new EventStream(queryClient, { fetch: fetcher as typeof fetch, reconnectBaseMs: 1, refreshIntervalMs: 1 });
    try {
      events.start();
      await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
      apiAvailable = false;
      firstStream.enqueue(encoder.encode('id: 10000000-0000-0000-0000-000000000020\ndata: {"topic":"search.completed","resourceType":"search_run","resourceId":"search-id"}\n\n'));
      await vi.waitFor(() => expect(observer.getCurrentResult().isError).toBe(true));
      apiAvailable = true;
      value = 'after';
      firstStream.error(new Error('连接中断'));
      await vi.waitFor(() => expect(observer.getCurrentResult().data).toBe('after'));
      const headers = new Headers(fetcher.mock.calls[1]?.[1]?.headers);
      expect(headers.get('Last-Event-ID')).toBe('10000000-0000-0000-0000-000000000020');
    } finally {
      events.stop();
      unsubscribe();
      queryClient.clear();
    }
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
