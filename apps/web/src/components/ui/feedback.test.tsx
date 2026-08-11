import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { NotFoundPage } from '@/app/not-found';
import { ErrorState } from '@/components/ui/feedback';
import { renderWithProviders } from '@/test/render';

describe('feedback boundaries', () => {
  it('renders long server paths and request IDs in a wrapping alert', async () => {
    const longPath = `/media/${'long-directory/'.repeat(30)}episode.mkv`;
    renderWithProviders(<ErrorState message={longPath} requestId="request-503" />);

    const message = await screen.findByText(longPath);
    expect(message).toHaveClass('break-words');
    expect(screen.getByRole('alert')).toContainElement(message);
    expect(screen.getByText('请求 ID：request-503')).toBeInTheDocument();
  });

  it('keeps keyboard focus on retry while invoking the recovery command', async () => {
    const retry = vi.fn();
    renderWithProviders(<ErrorState message="服务暂时不可用" onRetry={retry} />);

    await userEvent.tab();
    const button = screen.getByRole('button', { name: '重试' });
    expect(button).toHaveFocus();
    await userEvent.keyboard('{Enter}');
    expect(retry).toHaveBeenCalledOnce();
    expect(button).toHaveFocus();
  });

  it('provides a keyboard-accessible recovery route for a missing resource', async () => {
    renderWithProviders(<NotFoundPage />);

    expect(await screen.findByRole('heading', { name: '页面不存在' })).toBeInTheDocument();
    await userEvent.tab();
    expect(screen.getByRole('link', { name: '返回仪表盘' })).toHaveFocus();
  });
});
