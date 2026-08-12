import type { QueryClient, QueryKey } from '@tanstack/react-query';

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
  private readonly listeners = new Set<StatusListener>();
  private abortController: AbortController | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private lastEventId: string | null = null;
  private generation = 0;
  private reconnectAttempt = 0;
  private running = false;

  constructor(queryClient: QueryClient, options: EventStreamOptions = {}) {
    this.queryClient = queryClient;
    this.fetcher = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.reconnectBaseMs = options.reconnectBaseMs ?? 1_000;
    this.inactivityTimeoutMs = options.inactivityTimeoutMs ?? DEFAULT_INACTIVITY_TIMEOUT_MS;
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
    if (options.clearCursor ?? true) {
      this.lastEventId = null;
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
    const headers = new Headers({ Accept: 'text/event-stream' });
    if (this.lastEventId) {
      headers.set('Last-Event-ID', this.lastEventId);
    }

    try {
      const response = await this.fetcher('/api/v1/events', {
        credentials: 'include',
        headers,
        signal: controller.signal,
      });
      if (response.status === 401) {
        reportSessionLoss('unauthorized');
        this.stop();
        return;
      }
      if (response.status === 409 && this.lastEventId && (await isCursorConflict(response))) {
        this.lastEventId = null;
        this.clearCursor();
        await this.invalidateProtectedQueries();
        this.scheduleReconnect(generation, 0);
        return;
      }
      if (!response.ok || !response.body) {
        throw new Error(`event stream returned HTTP ${response.status}`);
      }
      this.reconnectAttempt = 0;
      this.emit('open');
      await this.consume(response.body, generation);
      if (this.running && generation === this.generation) {
        this.scheduleReconnect(generation);
      }
    } catch (error) {
      if (!controller.signal.aborted && this.running && generation === this.generation) {
        this.scheduleReconnect(generation);
      }
    } finally {
      if (this.abortController === controller) {
        this.abortController = null;
      }
    }
  }

  private async consume(body: ReadableStream<Uint8Array>, generation: number): Promise<void> {
    const reader = body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    try {
      while (this.running && generation === this.generation) {
        const result = await readWithInactivityTimeout(reader, this.inactivityTimeoutMs);
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
      reader.releaseLock();
    }
  }

  private handle(frame: ParsedFrame): void {
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
        void this.queryClient.invalidateQueries({ queryKey: ['agent-resolutions'], exact: false });
      }
    }
  }

  private invalidate(event: SseEvent): void {
    const resourceType = event.resourceType;
    const resourceId = event.resourceId;
    if (resourceType && resourceId) {
      const keys = resourceQueryKeys[resourceType]?.(resourceId) ?? [[resourceType, resourceId], [resourceType]];
      for (const queryKey of keys) {
        void this.queryClient.invalidateQueries({ queryKey, exact: false });
      }
      void this.queryClient.invalidateQueries({ queryKey: ['events', resourceType, resourceId], exact: false });
    }
    if (event.operationId) {
      void this.queryClient.invalidateQueries({ queryKey: ['operation', event.operationId] });
      void this.queryClient.invalidateQueries({ queryKey: ['operations'], exact: false });
    }
    void this.queryClient.invalidateQueries({ queryKey: ['dashboard-summary'] });
  }

  private async invalidateProtectedQueries(): Promise<void> {
    await this.queryClient.invalidateQueries({
      predicate: (query) => query.queryKey[0] !== 'setup-status' && query.queryKey[0] !== 'session',
    });
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
  const result = await Promise.race([reader.read(), timedOut]);
  if (timer !== null) {
    clearTimeout(timer);
  }
  return result;
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
