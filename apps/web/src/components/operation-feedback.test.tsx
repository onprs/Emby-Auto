import { act, screen, waitFor } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';

import { OperationFeedback } from '@/components/operation-feedback';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const operationId = '73000000-0000-4000-8000-000000000001';

describe('页面内的操作反馈', () => {
  it('在原页面显示后台失败，并且仅刷新一次关联内容', async () => {
    let status: 'running' | 'failed' = 'running';
    const onSettled = vi.fn();
    server.use(http.get(`*/api/v1/operations/${operationId}`, () => HttpResponse.json({
      id: operationId, kind: 'rss.poll', status,
      errorCode: status === 'failed' ? 'fixture_unavailable' : undefined,
      errorMessage: status === 'failed' ? '订阅源暂时不可用' : undefined,
    })));
    const { router, queryClient } = renderWithProviders(<OperationFeedback operationId={operationId} onSettled={onSettled} />);
    expect(await screen.findByRole('status')).toHaveTextContent('操作已提交，正在处理');
    expect(onSettled).not.toHaveBeenCalled();
    status = 'failed';
    await act(async () => { await queryClient.invalidateQueries({ queryKey: ['operation', operationId] }); });
    expect(await screen.findByRole('alert')).toHaveTextContent('订阅源暂时不可用');
    await waitFor(() => expect(onSettled).toHaveBeenCalledTimes(1));
    expect(router.state.location.pathname).toBe('/');
    expect(screen.getByRole('link', { name: '查看操作日志' })).toHaveAttribute('href', expect.stringContaining(`/operations/${operationId}`));
    await act(async () => { await queryClient.invalidateQueries({ queryKey: ['operation', operationId] }); });
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it('保留读取失败提示，重新读取成功后再通知调用页面', async () => {
    let available = false;
    const onSettled = vi.fn();
    server.use(http.get(`*/api/v1/operations/${operationId}`, () => available
      ? HttpResponse.json({ id: operationId, kind: 'download.cancel', status: 'succeeded' })
      : HttpResponse.json({ code: 'service_unavailable', message: '稍后重试', details: {} }, { status: 503 })));
    const { queryClient } = renderWithProviders(<OperationFeedback operationId={operationId} onSettled={onSettled} />);
    expect(await screen.findByRole('alert')).toHaveTextContent('暂时无法读取操作结果');
    expect(onSettled).not.toHaveBeenCalled();
    available = true;
    await act(async () => { await queryClient.invalidateQueries({ queryKey: ['operation', operationId] }); });
    expect(await screen.findByRole('status')).toHaveTextContent('操作已完成');
    expect(onSettled).toHaveBeenCalledTimes(1);
  });
});
