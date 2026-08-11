import { screen } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import { DeletionFeedback } from '@/features/deletions/deletion-feedback';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const operationId = '40000000-0000-0000-0000-000000000001';

function operation(status: 'queued' | 'failed') {
  return {
    id: operationId,
    resourceType: 'acquisition',
    resourceId: '40000000-0000-0000-0000-000000000002',
    kind: 'acquisition.delete',
    status,
    attempt: 1,
    maxAttempts: 5,
    errorCode: status === 'failed' ? 'acquisition_delete_path_unsafe' : undefined,
    errorMessage: status === 'failed' ? 'outside configured roots' : undefined,
    createdAt: '2026-07-26T03:00:00Z',
    updatedAt: '2026-07-26T03:00:01Z',
    finishedAt: status === 'failed' ? '2026-07-26T03:00:01Z' : undefined,
  };
}

describe('DeletionFeedback', () => {
  it('shows an accepted deletion as still running', async () => {
    server.use(http.get(`*/api/v1/operations/${operationId}`, () => HttpResponse.json(operation('queued'))));
    renderWithProviders(<DeletionFeedback items={[{ resourceId: 'task-1', label: '测试任务', operationId }]} />);

    expect(await screen.findByText('正在彻底删除 1 项，已完成 0 项')).toBeInTheDocument();
  });

  it('shows the backend cleanup failure without exposing the raw path', async () => {
    server.use(http.get(`*/api/v1/operations/${operationId}`, () => HttpResponse.json(operation('failed'))));
    renderWithProviders(<DeletionFeedback items={[{ resourceId: 'task-1', label: '测试任务', operationId }]} />);

    expect(await screen.findByText('删除完成 0 项，失败 1 项')).toBeInTheDocument();
    expect(screen.getByText('测试任务：待删除文件不在允许的临时目录内，请检查路径设置。')).toBeInTheDocument();
    expect(screen.queryByText(/outside configured roots/)).not.toBeInTheDocument();
  });
});
