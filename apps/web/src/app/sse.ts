import type { Query, QueryClient, QueryFilters, QueryKey } from '@tanstack/react-query';

import { reportSessionLoss } from '@/app/session-runtime';

export type SseStatus = 'connecting' | 'open' | 'closed';

type SseEvent = {
  id: string;
  topic: string;
  resourceType?: string;
  resourceId?: string;
  operationId?: string;
};

type StatusListener = (status: SseStatus) => void;

type EventStreamOptions = {
  fetch?: typeof globalThis.fetch;
  reconnectBaseMs?: number;
  inactivityTimeoutMs?: number;
  connectionTimeoutMs?: number;
  refreshIntervalMs?: number;
};

type ParsedFrame = {
  id: string;
  event: string;
  data: string;
};

const LAST_EVENT_KEY = 'emby_auto_last_event_id';
const MAX_RECONNECT_DELAY_MS = 30_000;
const DEFAULT_INACTIVITY_TIMEOUT_MS = 25_000;

const resourceQueryKeys: Record<string, (resourceId: string) => QueryKey[]> = {
  acquisition: (id) => [['acquisition', id], ['acquisitions'], ['rss']],
  download: (id) => [['download', id], ['downloads'], ['acquisitions'], ['rss']],
  episode_task: (id) => [['episode_task', id], ['tasks'], ['acquisitions'], ['rss']],
  emby_scan: (id) => [['emby-scan', id], ['emby-scans'], ['emby-libraries']],
  rss_subscription: (id) => [['rss', id], ['rss']],
  rss_entry: () => [['rss']],
  rss_adjudication_batch: () => [['rss'], ['agent-resolutions']],
  agent_resolution: (id) => [['agent-resolution', id], ['agent-resolutions']],
  search_run: (id) => [['search', id], ['searches']],
};

/** One application-level, resumable SSE connection backed by fetch streaming. */
export class EventStream {
  private readonly queryClient: QueryClient;
  private readonly fetcher: typeof globalThis.fetch;
  private readonly reconnectBaseMs: number;
  private readonly inactivityTimeoutMs: number;
  private readonly connectionTimeoutMs: number;
  private readonly refreshIntervalMs: number;
  private readonly pendingQueries = new Set<Query>();
  private refreshTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly listeners = new Set<StatusListener>();
  private abortController: AbortController | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private lastEventId: string | null = null;
  /** 当前连接是否在等待服务端发送订阅边界帧（无游标时的首连或游标失效后的恢复）。 */
  private boundaryPending = false;
  private generation = 0;
  private reconnectAttempt = 0;
  private running = false;

  constructor(queryClient: QueryClient, options: EventStreamOptions = {}) {
    this.queryClient = queryClient;
    this.fetcher = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.reconnectBaseMs = options.reconnectBaseMs ?? 1_000;
    this.inactivityTimeoutMs = options.inactivityTimeoutMs ?? DEFAULT_INACTIVITY_TIMEOUT_MS;
    this.connectionTimeoutMs = options.connectionTimeoutMs ?? 15_000;
    this.refreshIntervalMs = options.refreshIntervalMs ?? 1_000;
  }

  start(): void {
    if (this.running) {
      return;
    }
    this.running = true;
    this.generation += 1;
    this.reconnectAttempt = 0;
    this.lastEventId = this.readCursor();
    void this.connect(this.generation);
  }

  stop(options: { clearCursor?: boolean } = {}): void {
    this.running = false;
    this.generation += 1;
    this.abortController?.abort();
    this.abortController = null;
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.refreshTimer !== null) {
      clearTimeout(this.refreshTimer);
      this.refreshTimer = null;
    }
    // 页面离开时保留失效标记，下一次挂载仍会获取最新数据。
    for (const query of this.pendingQueries) {
      const markStale = () => {
        if (this.queryClient.getQueryCache().get(query.queryHash) === query) query.invalidate();
      };
      markStale();
      // 已开始的读取仍可能返回事件发生前的数据，不能覆盖离开时的失效标记。
      if (query.state.fetchStatus === 'fetching') void query.promise?.then(markStale, markStale);
    }
    this.pendingQueries.clear();
    if (options.clearCursor ?? true) {
      this.lastEventId = null;
      this.boundaryPending = false;
      this.clearCursor();
    }
    this.emit('closed');
  }

  onStatus(listener: StatusListener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private async connect(generation: number): Promise<void> {
    if (!this.running || generation !== this.generation) {
      return;
    }
    this.emit('connecting');
    const controller = new AbortController();
    this.abortController = controller;
    // 无持久化游标时服务端以当前可见最新事件为边界，并先发送边界帧；有游标时继续增量重放。
    const resumeCursor = this.lastEventId;
    const boundaryExpected = resumeCursor === null;
    this.boundaryPending = boundaryExpected;
    const headers = new Headers({ Accept: 'text/event-stream' });
    if (resumeCursor) {
      headers.set('Last-Event-ID', resumeCursor);
    }

    const connectionTimer = setTimeout(() => controller.abort(), this.connectionTimeoutMs);
    try {
      const response = await this.fetcher('/api/v1/events', {
        credentials: 'include',
        headers,
        signal: controller.signal,
      });
      if (!this.running || generation !== this.generation) {
        await response.body?.cancel();
        return;
      }
      if (response.status === 401) {
        reportSessionLoss('unauthorized');
        this.stop();
        return;
      }
      if (response.status === 409 && this.lastEventId && (await isCursorConflict(response))) {
        if (!this.running || generation !== this.generation) return;
        // 游标已被保留策略清理：丢弃旧游标并立即重连，由新连接的边界帧做一次全量失效后
        // 从新的水位继续，不再从最早事件重放。
        this.lastEventId = null;
        this.clearCursor();
        this.scheduleReconnect(generation, 0);
        return;
      }
      if (!response.ok || !response.body) {
        throw new Error(`event stream returned HTTP ${response.status}`);
      }
      clearTimeout(connectionTimer);
      if (this.reconnectAttempt > 0 && !boundaryExpected) {
        // 事件游标可能已在读取失败前推进，重连后补取受保护的数据。
        // 等待边界帧时由边界统一失效，避免重复全量重读。
        this.queueRefresh({
          predicate: (query) => query.queryKey[0] !== 'setup-status' && query.queryKey[0] !== 'session',
        });
      }
      this.reconnectAttempt = 0;
      this.emit('open');
      await this.consume(response.body, generation, controller.signal);
      if (this.running && generation === this.generation) {
        this.scheduleReconnect(generation);
      }
    } catch {
      if (this.running && generation === this.generation) {
        this.scheduleReconnect(generation);
      }
    } finally {
      clearTimeout(connectionTimer);
      controller.abort();
      if (this.abortController === controller) {
        this.abortController = null;
      }
    }
  }

  private async consume(body: ReadableStream<Uint8Array>, generation: number, signal: AbortSignal): Promise<void> {
    const reader = body.getReader();
    const cancel = () => { void reader.cancel().catch(() => undefined); };
    signal.addEventListener('abort', cancel, { once: true });
    if (signal.aborted) cancel();
    const decoder = new TextDecoder();
    let buffer = '';
    try {
      while (this.running && generation === this.generation) {
        const result = await readWithInactivityTimeout(reader, this.inactivityTimeoutMs);
        if (!this.running || generation !== this.generation) break;
        if (result === null) {
          await reader.cancel('event stream heartbeat timed out');
          throw new Error('event stream heartbeat timed out');
        }
        if (result.done) {
          buffer += decoder.decode();
          break;
        }
        buffer += decoder.decode(result.value, { stream: true });
        const parsed = extractFrames(buffer);
        buffer = parsed.remainder;
        for (const frame of parsed.frames) {
          this.handle(frame);
        }
      }
    } finally {
      signal.removeEventListener('abort', cancel);
      reader.releaseLock();
    }
  }

  private handle(frame: ParsedFrame): void {
    if (frame.event === 'stream.started') {
      this.handleStartBoundary(frame);
      return;
    }
    let parsed: SseEvent | null = null;
    try {
      parsed = JSON.parse(frame.data) as SseEvent;
    } catch {
      parsed = null;
    }
    const cursor = frame.id || parsed?.id;
    if (cursor) {
      this.lastEventId = cursor;
      this.writeCursor(cursor);
    }
    if (parsed?.topic) {
      this.invalidate(parsed);
      if (parsed.topic.startsWith('agent.')) {
        this.queueRefresh({ queryKey: ['agent-resolutions'], exact: false });
      }
    }
  }

  /**
   * 处理服务端在无游标订阅时发送的边界帧：记录水位，并全量重读一次已加载的查询。
   * 边界只代表当前可见最新事件，不是事务 settled 水位；初始化读取可能发生在边界之前，
   * 边界后的全量读取可覆盖读取完成前的业务数据。更早取得 sequence、但在边界之后才提交
   * 的事务不会被该游标重放，需要下一次事件或手动刷新；这是已知的低概率残余。
   */
  private handleStartBoundary(frame: ParsedFrame): void {
    let cursor: string | null = frame.id || null;
    try {
      const payload = JSON.parse(frame.data) as { cursor?: unknown };
      if (typeof payload.cursor === 'string') {
        cursor = cursor ?? payload.cursor;
      }
    } catch {
      // 边界帧正文不可解析时仍以 SSE id 为准。
    }
    if (cursor) {
      this.lastEventId = cursor;
      this.writeCursor(cursor);
    }
    if (!this.boundaryPending) {
      return;
    }
    this.boundaryPending = false;
    this.queueRefresh({
      predicate: (query) => query.queryKey[0] !== 'setup-status' && query.queryKey[0] !== 'session',
    });
  }

  private invalidate(event: SseEvent): void {
    const resourceType = event.resourceType;
    const resourceId = event.resourceId;
    if (resourceType && resourceId) {
      const keys = resourceQueryKeys[resourceType]?.(resourceId) ?? [[resourceType, resourceId], [resourceType]];
      for (const queryKey of keys) {
        this.queueRefresh({ queryKey, exact: false });
      }
      this.queueRefresh({ queryKey: ['events', resourceType, resourceId], exact: false });
    }
    if (event.operationId) {
      this.queueRefresh({ queryKey: ['operation', event.operationId] });
      this.queueRefresh({ queryKey: ['operations'], exact: false });
    }
    this.queueRefresh({ queryKey: ['dashboard'], exact: true });
  }

  private queueRefresh(filters: QueryFilters): void {
    for (const query of this.queryClient.getQueryCache().findAll(filters)) {
      this.pendingQueries.add(query);
    }
    this.scheduleRefresh();
  }

  private scheduleRefresh(): void {
    if (!this.running || this.refreshTimer !== null || this.pendingQueries.size === 0) return;
    this.refreshTimer = setTimeout(() => {
      this.refreshTimer = null;
      const ready = new Set<Query>();
      for (const query of this.pendingQueries) {
        if (this.queryClient.getQueryCache().get(query.queryHash) !== query) {
          this.pendingQueries.delete(query);
        } else if (query.state.fetchStatus !== 'fetching') {
          ready.add(query);
          this.pendingQueries.delete(query);
        }
      }
      // 等待慢请求完成后补取一次，避免连续事件取消请求或漏掉最终状态。
      if (ready.size > 0) {
        void this.queryClient.invalidateQueries(
          { predicate: (query) => ready.has(query) },
          { cancelRefetch: false },
        );
      }
      this.scheduleRefresh();
    }, this.refreshIntervalMs);
  }

  private scheduleReconnect(generation: number, explicitDelay?: number): void {
    if (!this.running || generation !== this.generation || this.reconnectTimer !== null) {
      return;
    }
    this.emit('closed');
    const delay = explicitDelay ?? Math.min(this.reconnectBaseMs * 2 ** this.reconnectAttempt, MAX_RECONNECT_DELAY_MS);
    this.reconnectAttempt += 1;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      void this.connect(generation);
    }, delay);
  }

  private emit(status: SseStatus): void {
    for (const listener of this.listeners) {
      listener(status);
    }
  }

  private readCursor(): string | null {
    try {
      return globalThis.sessionStorage?.getItem(LAST_EVENT_KEY) ?? null;
    } catch {
      return null;
    }
  }

  private writeCursor(value: string): void {
    try {
      globalThis.sessionStorage?.setItem(LAST_EVENT_KEY, value);
    } catch {
      // The live stream remains usable when sessionStorage is unavailable.
    }
  }

  private clearCursor(): void {
    try {
      globalThis.sessionStorage?.removeItem(LAST_EVENT_KEY);
    } catch {
      // Ignore unavailable storage.
    }
  }
}

async function readWithInactivityTimeout(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  timeoutMs: number,
): Promise<ReadableStreamReadResult<Uint8Array> | null> {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const timedOut = new Promise<null>((resolve) => {
    timer = setTimeout(() => resolve(null), timeoutMs);
  });
  try {
    return await Promise.race([reader.read(), timedOut]);
  } finally {
    if (timer !== null) {
      clearTimeout(timer);
    }
  }
}

function extractFrames(input: string): { frames: ParsedFrame[]; remainder: string } {
  const frames: ParsedFrame[] = [];
  let remainder = input;
  while (true) {
    const boundary = remainder.search(/\r?\n\r?\n/);
    if (boundary < 0) {
      break;
    }
    const separator = remainder.slice(boundary).match(/^\r?\n\r?\n/)?.[0] ?? '\n\n';
    const raw = remainder.slice(0, boundary);
    remainder = remainder.slice(boundary + separator.length);
    const frame = parseFrame(raw);
    if (frame) {
      frames.push(frame);
    }
  }
  return { frames, remainder };
}

function parseFrame(raw: string): ParsedFrame | null {
  let id = '';
  let event = 'message';
  const data: string[] = [];
  for (const line of raw.split(/\r?\n/)) {
    if (line === '' || line.startsWith(':')) {
      continue;
    }
    const separator = line.indexOf(':');
    const field = separator < 0 ? line : line.slice(0, separator);
    let value = separator < 0 ? '' : line.slice(separator + 1);
    if (value.startsWith(' ')) {
      value = value.slice(1);
    }
    if (field === 'id' && !value.includes('\0')) {
      id = value;
    } else if (field === 'event') {
      event = value;
    } else if (field === 'data') {
      data.push(value);
    }
  }
  return data.length > 0 ? { id, event, data: data.join('\n') } : null;
}

async function isCursorConflict(response: Response): Promise<boolean> {
  try {
    const body = (await response.json()) as { code?: string };
    return body.code === 'event_cursor_not_found';
  } catch {
    return false;
  }
}
