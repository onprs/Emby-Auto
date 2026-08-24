import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import { canDeleteTask, canRetryTask, deleteTask, retryTask, runBatch, type TaskActionTarget } from '@/features/tasks/task-actions';
import { server } from '@/test/msw-server';

function taskTarget(overrides: Partial<TaskActionTarget> = {}): TaskActionTarget {
  return { id: 't1', acquisitionId: 'a1', version: 1, downloadId: 'd1', state: 'failed', ...overrides };
}

describe('canRetryTask', () => {
  it('allows failed tasks with a failure stage', () => {
    expect(canRetryTask({ state: 'failed', failureStage: 'video', cleanup: undefined })).toBe(true);
  });
  it('allows failed + video failed + no failureStage', () => {
    expect(canRetryTask({ state: 'failed', failureStage: undefined, cleanup: undefined, videoState: 'failed', subtitleState: 'ass_ready' })).toBe(true);
  });
  it('allows failed + subtitle failed + no failureStage', () => {
    expect(canRetryTask({ state: 'failed', failureStage: undefined, cleanup: undefined, videoState: 'video_ready', subtitleState: 'failed' })).toBe(true);
  });
  it('allows processing + one failed (video)', () => {
    expect(canRetryTask({ state: 'processing', failureStage: undefined, cleanup: undefined, videoState: 'failed', subtitleState: 'ass_ready' })).toBe(true);
  });
  it('allows processing + one failed (subtitle)', () => {
    expect(canRetryTask({ state: 'processing', failureStage: undefined, cleanup: undefined, videoState: 'video_ready', subtitleState: 'failed' })).toBe(true);
  });
  it('rejects processing + both ready/no failed', () => {
    expect(canRetryTask({ state: 'processing', failureStage: undefined, cleanup: undefined, videoState: 'video_ready', subtitleState: 'ass_ready' })).toBe(false);
    expect(canRetryTask({ state: 'processing', failureStage: undefined, cleanup: undefined, videoState: 'transcode_queued', subtitleState: 'subtitle_queued' })).toBe(false);
  });
  it('keeps legacy failed/finalize behavior', () => {
    expect(canRetryTask({ state: 'failed', failureStage: 'finalize', cleanup: undefined, videoState: 'video_ready', subtitleState: 'ass_ready' })).toBe(true);
    expect(canRetryTask({ state: 'failed', failureStage: 'import', cleanup: undefined, videoState: 'video_ready', subtitleState: 'ass_ready' })).toBe(true);
    expect(canRetryTask({ state: 'failed', failureStage: undefined, cleanup: undefined, videoState: 'video_ready', subtitleState: 'ass_ready' })).toBe(false);
  });
  it('allows imported tasks with failed cleanup and rejects other imported', () => {
    expect(canRetryTask({ state: 'imported', failureStage: undefined, cleanup: { status: 'failed' } as never })).toBe(true);
    expect(canRetryTask({ state: 'imported', failureStage: undefined, cleanup: { status: 'completed' } as never })).toBe(false);
    expect(canRetryTask({ state: 'imported', failureStage: undefined, cleanup: undefined })).toBe(false);
  });
  it('rejects plain failed without stage or branch', () => {
    expect(canRetryTask({ state: 'processing', failureStage: undefined, cleanup: undefined })).toBe(false);
    expect(canRetryTask({ state: 'failed', failureStage: undefined, cleanup: undefined })).toBe(false);
  });
});

describe('canDeleteTask', () => {
  it('allows not-yet-imported tasks', () => {
    expect(canDeleteTask({ state: 'failed', cleanup: undefined })).toBe(true);
    expect(canDeleteTask({ state: 'processing', cleanup: undefined })).toBe(true);
    expect(canDeleteTask({ state: 'cancelled', cleanup: undefined })).toBe(true);
  });
  it('allows deleting workflow records after import without deleting library files', () => {
    expect(canDeleteTask({ state: 'imported', cleanup: { status: 'completed' } as never })).toBe(true);
    expect(canDeleteTask({ state: 'imported', cleanup: { status: 'failed' } as never })).toBe(true);
  });
});

describe('retryTask', () => {
  it('posts a retry command', async () => {
    let captured: Record<string, unknown> = {};
    server.use(
      http.post('*/api/v1/tasks/t1/retry', async ({ request }) => {
        captured = { body: await request.json(), key: request.headers.get('Idempotency-Key') };
        return HttpResponse.json({ operationId: 'op1', status: 'queued' }, { status: 202 });
      }),
    );
    const result = await retryTask(taskTarget(), 'k1');
    expect(result.ok).toBe(true);
    expect(captured).toMatchObject({ key: 'k1', body: { expectedVersion: 1 } });
  });
  it('returns a failure result on error', async () => {
    server.use(http.post('*/api/v1/tasks/t1/retry', () => HttpResponse.json({ code: 'state_conflict', message: 'conflict', details: {}, requestId: 'r' }, { status: 409 })));
    const result = await retryTask(taskTarget(), 'k1');
    expect(result.ok).toBe(false);
    expect(result.error).toBeTruthy();
  });
});

describe('deleteTask', () => {
  it('submits one acquisition-owned deletion command', async () => {
    let capturedKey = '';
    server.use(
      http.delete('*/api/v1/acquisitions/a1', ({ request }) => {
        capturedKey = request.headers.get('Idempotency-Key') ?? '';
        return HttpResponse.json({ operationId: 'o2', status: 'queued' }, { status: 202 });
      }),
    );
    const result = await deleteTask(taskTarget({ state: 'processing' }), 'k1');
    expect(result).toMatchObject({ ok: true, operationId: 'o2' });
    expect(capturedKey).toBe('k1');
  });
  it('reports acquisition deletion failure', async () => {
    server.use(
      http.delete('*/api/v1/acquisitions/a1', () => HttpResponse.json({ code: 'deletion_in_progress', message: 'in progress', details: {}, requestId: 'r' }, { status: 409 })),
    );
    const result = await deleteTask(taskTarget({ state: 'failed' }), 'k1');
    expect(result.ok).toBe(false);
  });
});

describe('runBatch', () => {
  it('collects per-item success and failure', async () => {
    server.use(
      http.post('*/api/v1/tasks/:id/retry', ({ params }) => {
        if (params.id === 'bad') {
          return HttpResponse.json({ code: 'x', message: 'boom', details: {}, requestId: 'r' }, { status: 500 });
        }
        return HttpResponse.json({ operationId: 'op', status: 'queued' }, { status: 202 });
      }),
    );
    const results = await runBatch([taskTarget({ id: 'ok1' }), taskTarget({ id: 'bad' }), taskTarget({ id: 'ok2' })], 'retry', (t) => `k-${t.id}`);
    expect(results.map((r) => r.ok)).toEqual([true, false, true]);
  });
});
