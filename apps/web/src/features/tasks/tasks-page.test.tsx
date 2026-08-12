import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import type { Task } from '@/api/generated/types.gen';
import { TasksPage } from '@/features/tasks/tasks-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

function failedTask(): Task {
  return {
    id: '11111111-1111-1111-1111-111111111111',
    acquisitionId: '22222222-2222-2222-2222-222222222222',
    downloadId: '33333333-3333-3333-3333-333333333333',
    mediaType: 'episode',
    seriesTitle: '失败任务示例',
    sourceSeason: 1,
    sourceEpisode: 1,
    targetSeason: 1,
    targetEpisode: 1,
    state: 'failed',
    videoState: 'failed',
    subtitleState: 'ass_ready',
    version: 2,
    failureStage: 'video',
    errorCode: 'ffmpeg_transcode_failed',
    errorMessage: 'C:\\private\\media\\episode01.mkv: Invalid data found when processing input',
    operations: [],
    actions: { canRetry: true, canCancel: false, canReview: false, canImport: false },
    createdAt: '2026-07-25T01:00:00Z',
    updatedAt: '2026-07-25T02:00:00Z',
  };
}

describe('TasksPage failure presentation', () => {
  it('shows one readable summary and routes the failure action to task details', async () => {
    const task = failedTask();
    server.use(http.get('*/api/v1/tasks', () => HttpResponse.json({ items: [task] })));
    const { router } = renderWithProviders(<TasksPage />, { routePath: '/tasks', initialEntry: '/tasks' });

    const summaries = await screen.findAllByText('视频转码失败：源文件格式不受支持');
    expect(summaries.length).toBeGreaterThan(0);
    expect(summaries[0].closest('[title]')).toHaveAttribute('title', '视频转码失败：源文件格式不受支持');
    expect(screen.queryByText(/Invalid data found/)).not.toBeInTheDocument();
    expect(screen.queryByText(/ffmpeg_transcode_failed/)).not.toBeInTheDocument();

    await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[0]);
    await userEvent.click(await screen.findByRole('menuitem', { name: '查看失败原因' }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/tasks/${task.id}`));
    expect(router.state.location.search).toMatchObject({ from: '/tasks' });
    expect(router.state.location.state).toMatchObject({ appReturnTo: '/tasks', appHistoryDepth: 1 });
  });
});
