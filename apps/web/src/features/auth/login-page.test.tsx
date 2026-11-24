import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import { LoginPage } from '@/features/auth/login-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const apiRateLimited = {
  code: 'rate_limited',
  message: 'too many login attempts, retry later',
  details: {},
  requestId: 'login-rate-limit-1',
};

const chineseRateLimitMessage = '尝试过于频繁，请稍后再试。';

async function submitLogin() {
  renderWithProviders(<LoginPage />);
  await userEvent.type(await screen.findByLabelText('用户名'), 'admin');
  await userEvent.type(screen.getByLabelText('密码'), 'fixture-password');
  await userEvent.click(await screen.findByRole('button', { name: '登录' }));
}

describe('LoginPage rate-limit feedback', () => {
  it('shows the Chinese rate-limit message for an application JSON 429', async () => {
    server.use(
      http.post('*/api/v1/auth/login', () =>
        HttpResponse.json(apiRateLimited, { status: 429, headers: { 'Retry-After': '30' } })),
    );

    await submitLogin();

    expect(await screen.findByText(chineseRateLimitMessage)).toBeVisible();
  });

  it('shows the Chinese rate-limit message for a proxy non-JSON 429', async () => {
    server.use(
      http.post('*/api/v1/auth/login', () =>
        HttpResponse.text('<html>429 Too Many Requests</html>', {
          status: 429,
          headers: { 'Content-Type': 'text/html' },
        })),
    );

    await submitLogin();

    expect(await screen.findByText(chineseRateLimitMessage)).toBeVisible();
    expect(screen.queryByText(/429 Too Many Requests/)).not.toBeInTheDocument();
  });
});
