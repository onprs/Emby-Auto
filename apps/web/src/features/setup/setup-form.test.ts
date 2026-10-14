import { describe, expect, it } from 'vitest';

import { defaultSetupForm } from '@/features/setup/setup-form';

describe('defaultSetupForm', () => {
  it('使用本机数据库并要求填写外部服务地址', () => {
    expect(defaultSetupForm.database).toEqual(expect.objectContaining({
      host: '127.0.0.1',
      port: '5432',
      database: 'emby_auto',
      username: 'emby_auto',
    }));
    expect(defaultSetupForm.services).toEqual(expect.objectContaining({
      qbUrl: '',
      embyUrl: '',
    }));
    expect(defaultSetupForm.paths).toEqual({
      downloadRoot: '/srv/emby-auto/downloads',
      workRoot: '/srv/emby-auto/work',
      stagingRoot: '/srv/emby-auto/staging',
      animeLibraryRoot: '/srv/emby-auto/media/anime',
      movieLibraryRoot: '/srv/emby-auto/media/movies',
      ffmpegPath: '/usr/bin/ffmpeg',
      ffprobePath: '/usr/bin/ffprobe',
    });
  });
});
