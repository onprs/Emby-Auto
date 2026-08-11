import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';

import type { InitializeSetupRequest, SetupStatus } from '@/api/generated/types.gen';
import { SetupWizard } from '@/features/setup/setup-wizard';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const requiredSetup: SetupStatus = {
  state: 'required',
  databaseConfigured: true,
  databaseManagedExternally: true,
  administratorConfigured: false,
};

async function reachReview() {
  await userEvent.type(await screen.findByLabelText('密码'), 'administrator-password');
  await userEvent.type(screen.getByLabelText('确认密码'), 'administrator-password');
  await userEvent.click(screen.getByRole('button', { name: '继续' }));

  await userEvent.type(await screen.findByLabelText('密码'), 'qb-password');
  await userEvent.type(screen.getByLabelText('Emby API key'), 'emby-key');
  await userEvent.type(screen.getByLabelText('TMDb API Read Access Token'), 'tmdb-token');
  await userEvent.click(screen.getByRole('button', { name: '继续' }));
  await screen.findByLabelText('下载根目录');
  await userEvent.click(screen.getByRole('button', { name: '继续' }));
  await screen.findByLabelText('配置名称');
  await userEvent.click(screen.getByRole('button', { name: '继续' }));
  await screen.findByText('初始化确认');
}

describe('SetupWizard', () => {
  it('submits administrator, services, tools, directories, and transcode in one request', async () => {
    let captured: InitializeSetupRequest | null = null;
    server.use(
      http.post('*/api/v1/setup/initialize', async ({ request }) => {
        captured = (await request.json()) as InitializeSetupRequest;
        return HttpResponse.json({ ...requiredSetup, state: 'completed', administratorConfigured: true });
      }),
    );
    const completed = vi.fn();
    renderWithProviders(<SetupWizard status={requiredSetup} onCompleted={completed} />);

    await reachReview();
    await userEvent.click(screen.getByRole('button', { name: '开始初始化' }));

    await waitFor(() => expect(completed).toHaveBeenCalledOnce());
    expect(captured).not.toBeNull();
    const body = captured as unknown as InitializeSetupRequest;
    expect(body.database).toBeUndefined();
    expect(body.administrator).toEqual({ username: 'admin', password: 'administrator-password' });
    expect(body.configuration.qBittorrent).toEqual({
      url: 'http://host.docker.internal:8080',
      username: 'admin',
      password: 'qb-password',
    });
    expect(body.configuration.emby.apiKey).toBe('emby-key');
    expect(body.configuration.tmdb.apiToken).toBe('tmdb-token');
    expect(body.configuration.paths).toEqual(expect.objectContaining({
      downloadRoot: '/srv/emby-auto/downloads',
      animeLibraryRoot: '/srv/emby-auto/media/anime',
      movieLibraryRoot: '/srv/emby-auto/media/movies',
      ffmpegPath: '/usr/bin/ffmpeg',
      ffprobePath: '/usr/bin/ffprobe',
    }));
    expect(body.configuration.transcode).toEqual(expect.objectContaining({
      videoCodec: 'h264',
      encoder: 'libx264',
      maxConcurrency: 1,
    }));
  });

  it('clears every submitted secret after initialization fails', async () => {
    server.use(
      http.post('*/api/v1/setup/initialize', () => HttpResponse.json({
        code: 'invalid_configuration',
        message: 'the configuration is invalid',
        details: { field: 'paths.movieLibraryRoot', reason: 'must be an absolute path' },
        requestId: 'setup-request-1',
      }, { status: 400 })),
    );
    renderWithProviders(<SetupWizard status={requiredSetup} onCompleted={vi.fn()} />);

    await reachReview();
    await userEvent.click(screen.getByRole('button', { name: '开始初始化' }));
    await screen.findByText(/paths\.movieLibraryRoot must be an absolute path/);
    await screen.findByLabelText('下载根目录');

    await userEvent.click(screen.getByRole('button', { name: '返回' }));
    expect(await screen.findByLabelText('密码')).toHaveValue('');
    expect(screen.getByLabelText('Emby API key')).toHaveValue('');
    expect(screen.getByLabelText('TMDb API Read Access Token')).toHaveValue('');

    await userEvent.click(screen.getByRole('button', { name: '返回' }));
    expect(await screen.findByLabelText('密码')).toHaveValue('');
    expect(screen.getByLabelText('确认密码')).toHaveValue('');
  });
});
