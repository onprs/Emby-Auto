import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import type { Task } from '@/api/generated/types.gen';
import { TaskDetailPage } from '@/features/tasks/task-detail-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

function makeTask(overrides: Partial<Task>): Task {
  return {
    id: '11111111-1111-1111-1111-111111111111',
    acquisitionId: '22222222-2222-2222-2222-222222222222',
    downloadId: '77777777-7777-7777-7777-777777777777',
    mediaType: 'episode',
    seriesTitle: '测试番剧',
    sourceSeason: 1,
    sourceEpisode: 1,
    targetSeason: 1,
    targetEpisode: 1,
    targetEpisodeTitle: '第一集',
    state: 'awaiting_review',
    videoState: 'video_ready',
    subtitleState: 'ass_ready',
    version: 5,
    operations: [],
    actions: { canRetry: false, canCancel: true, canReview: true, canImport: false },
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    artifacts: {
      id: '33333333-3333-3333-3333-333333333333',
      basename: 'ep1',
      video: { id: '44444444-4444-4444-4444-444444444444', kind: 'video', filePath: '/tmp/v.mkv', format: 'mkv', sizeBytes: 1000, checksumSha256: 'a'.repeat(64) },
      subtitle: { id: '55555555-5555-5555-5555-555555555555', kind: 'subtitle', filePath: '/tmp/s.ass', format: 'ass', sizeBytes: 100, checksumSha256: 'b'.repeat(64) },
    },
    ...overrides,
  };
}

function mockTask(task: Task) {
  server.use(
    http.get(`*/api/v1/tasks/${task.id}`, () => HttpResponse.json(task)),
    http.get('*/api/v1/events/history', () => HttpResponse.json({ items: [] })),
  );
}

describe('TaskDetailPage command guards', () => {
  it('shows review commands only for awaiting_review with artifacts', async () => {
    const task = makeTask({ state: 'awaiting_review' });
    mockTask(task);
    renderWithProviders(<TaskDetailPage taskId={task.id} />);
    expect(await screen.findByRole('button', { name: '审核通过并入库' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '拒绝' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '继续历史任务入库' })).not.toBeInTheDocument();
  });

  it('hides review commands when the artifact pair is missing', async () => {
    const task = makeTask({ state: 'awaiting_review', artifacts: undefined, actions: { canRetry: false, canCancel: true, canReview: false, canImport: false } });
    mockTask(task);
    renderWithProviders(<TaskDetailPage taskId={task.id} />);
    await screen.findByText('测试番剧');
    expect(screen.queryByRole('button', { name: '审核通过并入库' })).not.toBeInTheDocument();
  });

  it('shows the compatibility import command only for historical approved tasks', async () => {
    const task = makeTask({ state: 'approved', actions: { canRetry: false, canCancel: true, canReview: false, canImport: true } });
    mockTask(task);
    renderWithProviders(<TaskDetailPage taskId={task.id} />);
    expect(await screen.findByRole('button', { name: '继续历史任务入库' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '审核通过并入库' })).not.toBeInTheDocument();
  });

  it('shows structured failure information and the stage-specific retry action', async () => {
    const task = makeTask({
      state: 'failed',
      videoState: 'failed',
      failureStage: 'video',
      errorCode: 'ffmpeg_transcode_failed',
      errorMessage: 'encoder exited unexpectedly',
      updatedAt: '2026-07-25T02:30:00Z',
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      operations: [{
        id: '66666666-6666-6666-6666-666666666666',
        kind: 'transcode.run',
        status: 'failed',
        maxAttempts: 3,
        attemptCount: 2,
        errorCode: 'ffmpeg_transcode_failed',
        errorMessage: 'encoder exited unexpectedly',
        updatedAt: '2026-07-25T02:30:00Z',
      }],
    });
    mockTask(task);
    renderWithProviders(<TaskDetailPage taskId={task.id} />);

    expect(await screen.findByRole('heading', { name: '失败信息' })).toBeInTheDocument();
    expect(screen.getByText('视频转码失败：FFmpeg 未能完成视频转换')).toBeInTheDocument();
    expect(screen.getByText('第 1 次执行 · 最近一次尝试 2/3')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '重试任务' })).toBeInTheDocument();
    expect(screen.getByText('查看技术详情').closest('details')).not.toHaveAttribute('open');
  });

  it('keeps the original failure visible when submitting a retry fails', async () => {
    const task = makeTask({
      state: 'failed',
      videoState: 'failed',
      failureStage: 'video',
      errorCode: 'ffmpeg_transcode_failed',
      errorMessage: 'encoder exited unexpectedly',
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
    });
    mockTask(task);
    server.use(http.post(`*/api/v1/tasks/${task.id}/retry`, () => HttpResponse.json({
      code: 'state_conflict', message: 'task version changed', details: {}, requestId: 'request-1',
    }, { status: 409 })));
    renderWithProviders(<TaskDetailPage taskId={task.id} />);

    await userEvent.click(await screen.findByRole('button', { name: '重试任务' }));
    expect(await screen.findByText('视频转码失败：FFmpeg 未能完成视频转换')).toBeInTheDocument();
    expect((await screen.findAllByRole('alert')).some((alert) => alert.textContent?.includes('任务状态已经变化，请刷新后再试'))).toBe(true);
  });

  it('submits cleanup retry and keeps success feedback while refreshing records', async () => {
    const task = makeTask({
      state: 'imported',
      actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
      cleanup: {
        id: '77777777-7777-7777-7777-777777777777',
        attempt: 2,
        status: 'failed',
        torrentRemoved: true,
        stagedFilesRemoved: false,
        errorCode: 'cleanup_delete_failed',
        errorMessage: 'file is being used by another process',
        createdAt: '2026-07-25T01:00:00Z',
        updatedAt: '2026-07-25T02:00:00Z',
      },
    });
    mockTask(task);
    let submittedVersion: number | undefined;
    server.use(http.post(`*/api/v1/tasks/${task.id}/retry`, async ({ request }) => {
      submittedVersion = ((await request.json()) as { expectedVersion: number }).expectedVersion;
      return HttpResponse.json({
        task: { ...task, version: 6, cleanup: { ...task.cleanup!, status: 'queued' } },
        operationId: '88888888-8888-8888-8888-888888888888',
        status: 'queued',
      }, { status: 202 });
    }));
    renderWithProviders(<TaskDetailPage taskId={task.id} />, {
      routePath: '/tasks/$taskId',
      initialEntry: `/tasks/${task.id}`,
    });

    await userEvent.click(await screen.findByRole('button', { name: '重试清理' }));
    await waitFor(() => expect(submittedVersion).toBe(5));
    expect(await screen.findByRole('status')).toHaveTextContent('重试请求已提交');
    expect(screen.getByRole('link', { name: '查看运行' })).toHaveAttribute(
      'href',
      `/operations/88888888-8888-8888-8888-888888888888?from=%2Ftasks%2F${task.id}`,
    );
  });

  it('provides a visible icon return control for direct detail links', async () => {
    const task = makeTask({});
    mockTask(task);
    renderWithProviders(<TaskDetailPage taskId={task.id} />, {
      routePath: '/tasks/$taskId',
      initialEntry: `/tasks/${task.id}`,
    });
    const back = await screen.findByRole('button', { name: '返回' });
    expect(back).toHaveAttribute('title', '返回');
    expect(screen.getByRole('link', { name: '任务' })).toHaveAttribute('href', '/acquisitions');
  });

  it('sends expectedVersion and a stable idempotency key on approve', async () => {
    const task = makeTask({ state: 'awaiting_review' });
    mockTask(task);
    let captured: { version?: number; key?: string } = {};
    server.use(
      http.post(`*/api/v1/tasks/${task.id}/review`, async ({ request }) => {
        captured = {
          version: ((await request.json()) as { expectedVersion: number }).expectedVersion,
          key: request.headers.get('Idempotency-Key') ?? undefined,
        };
        return HttpResponse.json({
          ...task,
          state: 'import_queued',
          version: 7,
          review: {
            id: '88888888-8888-8888-8888-888888888888',
            decision: 'approved',
            notes: '',
            reviewedAt: '2026-01-01T00:01:00Z',
          },
          import: {
            id: '99999999-9999-9999-9999-999999999999',
            attempt: 1,
            status: 'queued',
            createdAt: '2026-01-01T00:01:00Z',
            updatedAt: '2026-01-01T00:01:00Z',
          },
          actions: { canRetry: false, canCancel: true, canReview: false, canImport: false },
        });
      }),
    );
    renderWithProviders(<TaskDetailPage taskId={task.id} />);
    await userEvent.click(await screen.findByRole('button', { name: '审核通过并入库' }));
    await waitFor(() => expect(captured.version).toBe(5));
    expect(captured.key).toBeTruthy();
  });
});
