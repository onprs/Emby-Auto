import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import type { Task } from '@/api/generated/types.gen';
import { TaskFailurePanel } from '@/features/tasks/task-failure-panel';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

function makeDualTask(overrides: Partial<Task> = {}): Task {
  return {
    id: '11111111-1111-1111-1111-111111111111',
    acquisitionId: '22222222-2222-2222-2222-222222222222',
    downloadId: '33333333-3333-3333-3333-333333333333',
    mediaType: 'episode',
    seriesTitle: '测试番剧',
    sourceSeason: 1,
    sourceEpisode: 1,
    targetSeason: 1,
    targetEpisode: 1,
    state: 'failed',
    videoState: 'failed',
    subtitleState: 'failed',
    version: 2,
    failureStage: 'video',
    errorCode: 'ffmpeg_transcode_failed',
    errorMessage: 'video C:\\media\\secret\\video.mkv failed',
    operations: [
      {
        id: 'aaaaaaa1-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
        kind: 'transcode.run',
        status: 'failed',
        maxAttempts: 3,
        attemptCount: 1,
        errorCode: 'ffmpeg_transcode_failed',
        errorMessage: 'video encode C:\\media\\secret\\video.mkv error',
        updatedAt: '2026-07-25T02:00:00Z',
      },
      {
        id: 'bbbbbbb2-bbbb-bbbb-bbbb-bbbbbbbbbbb2',
        kind: 'subtitle.prepare',
        status: 'failed',
        maxAttempts: 3,
        attemptCount: 2,
        errorCode: 'ffmpeg_subtitle_failed',
        errorMessage: 'subtitle /srv/secret/sub.ass error password=hunter2',
        updatedAt: '2026-07-25T02:30:00Z',
      },
    ],
    actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
    createdAt: '2026-07-25T01:00:00Z',
    updatedAt: '2026-07-25T02:00:00Z',
    ...overrides,
  };
}

describe('TaskFailurePanel dual branch', () => {
  it('shows both branches with distinct reasons and single retry with pending guard', async () => {
    const task = makeDualTask();
    let retryCalls = 0;
    server.use(
      http.post('*/api/v1/tasks/:taskId/retry', async () => {
        retryCalls += 1;
        // delay to keep pending visible
        await new Promise((resolve) => setTimeout(resolve, 200));
        return HttpResponse.json({ task: { ...task, version: 3 }, operationId: '99999999-9999-9999-9999-999999999999', status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<TaskFailurePanel task={task} onChanged={() => {}} />);

    // summary must contain dual phrase
    expect(await screen.findByText(/视频和字幕处理失败/)).toBeInTheDocument();
    // branches visible
    expect(screen.getByTestId('task-failure-branches')).toBeInTheDocument();
    expect(screen.getAllByText(/视频转码失败/).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText(/字幕.*失败|FFmpeg 未能准备字幕/).length).toBeGreaterThanOrEqual(1);
    // each branch operation link correctly associated
    expect(screen.getByRole('link', { name: '查看视频转码运行记录' })).toHaveAttribute('href', expect.stringContaining('aaaaaaa1-aaaa-aaaa-aaaa-aaaaaaaaaaa1'));
    expect(screen.getByRole('link', { name: '查看字幕处理运行记录' })).toHaveAttribute('href', expect.stringContaining('bbbbbbb2-bbbb-bbbb-bbbb-bbbbbbbbbbb2'));
    // only one retry button
    const retryButtons = screen.getAllByRole('button', { name: /重试任务/ });
    expect(retryButtons).toHaveLength(1);
    const retryButton = retryButtons[0];
    // pending disables
    const clickPromise = userEvent.click(retryButton);
    expect(await screen.findByText(/提交中/, {}, { timeout: 2000 })).toBeInTheDocument();
    expect(retryButton).toBeDisabled();
    await clickPromise;
    await waitFor(async () => {
      const submitted = screen.queryByText(/重试请求已提交/);
      if (submitted) {
        expect(submitted).toBeInTheDocument();
        return;
      }
      const error = screen.queryByRole('alert');
      if (error) throw new Error('error alert: ' + error.textContent);
      throw new Error('waiting for submitted');
    }, { timeout: 5000 });
    expect(retryCalls).toBe(1);
    // second attempt while already submitted should not create duplicate (button now shows submitted state, not pending)
    // ensure no extra call after success
    await waitFor(() => expect(retryButton).not.toBeDisabled(), { timeout: 3000 }).catch(() => {});
    expect(retryCalls).toBe(1);
    // technical details sanitized
    const details = document.querySelector('pre')?.textContent ?? '';
    expect(details).not.toContain('C:\\media\\secret');
    expect(details).not.toContain('hunter2');
    expect(details).not.toContain('/srv/secret');
  });

  it('keeps single branch rendering unchanged', async () => {
    const task = makeDualTask({
      videoState: 'failed',
      subtitleState: 'ass_ready',
      failureStage: 'video',
      operations: [
        {
          id: 'ccccccc3-cccc-cccc-cccc-ccccccccccc3',
          kind: 'transcode.run',
          status: 'failed',
          maxAttempts: 3,
          attemptCount: 1,
          errorCode: 'ffmpeg_transcode_failed',
          errorMessage: 'video error',
          updatedAt: '2026-07-25T02:00:00Z',
        },
      ],
    });
    renderWithProviders(<TaskFailurePanel task={task} onChanged={() => {}} />);
    expect(await screen.findByText('视频转码失败：FFmpeg 未能完成视频转换')).toBeInTheDocument();
    expect(screen.queryByTestId('task-failure-branches')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '重试任务' })).toBeInTheDocument();
  });
});
