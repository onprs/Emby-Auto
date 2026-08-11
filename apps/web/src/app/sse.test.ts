import { QueryClient } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { EventStream } from '@/app/sse';
import { registerSessionLossHandler } from '@/app/session-runtime';

const encoder = new TextEncoder();

afterEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
});

describe('EventStream', () => {
  it('starts once, sends the persisted cursor, and consumes named events', async () => {
    sessionStorage.setItem('emby_auto_last_event_id', '10000000-0000-0000-0000-000000000001');
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries');
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
    const events = new EventStream(queryClient, { fetch: fetcher as typeof fetch, reconnectBaseMs: 60_000 });

    events.start();
    events.start();
    await vi.waitFor(() => {
      expect(sessionStorage.getItem('emby_auto_last_event_id')).toBe('10000000-0000-0000-0000-000000000002');
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['episode_task', '20000000-0000-0000-0000-000000000001'], exact: false });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['tasks'], exact: false });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['rss'], exact: false });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['operation', '30000000-0000-0000-0000-000000000001'] });

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
    const events = new EventStream(queryClient, { fetch: fetcher as typeof fetch, reconnectBaseMs: 1 });

    events.start();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    expect(sessionStorage.getItem('emby_auto_last_event_id')).toBeNull();
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ predicate: expect.any(Function) }));
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
