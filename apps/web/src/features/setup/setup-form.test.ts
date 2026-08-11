import { describe, expect, it } from 'vitest';

import { defaultSetupForm } from '@/features/setup/setup-form';

describe('defaultSetupForm', () => {
  it('uses addresses and paths reachable from the generic Compose deployment', () => {
    expect(defaultSetupForm.database).toEqual(expect.objectContaining({
      host: 'postgres',
      port: '5432',
      database: 'emby_auto',
      username: 'emby_auto',
    }));
    expect(defaultSetupForm.services).toEqual(expect.objectContaining({
      qbUrl: 'http://host.docker.internal:8080',
      embyUrl: 'http://host.docker.internal:8096/emby',
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
