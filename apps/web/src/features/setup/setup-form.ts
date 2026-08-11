import type { InitializeSetupRequest, TranscodeProfileConfiguration } from '@/api/generated/types.gen';

export type SetupForm = {
  database: {
    host: string;
    port: string;
    database: string;
    username: string;
    password: string;
    sslMode: 'disable' | 'require' | 'verify-ca' | 'verify-full';
  };
  administrator: {
    username: string;
    password: string;
    confirmation: string;
  };
  services: {
    qbUrl: string;
    qbUsername: string;
    qbPassword: string;
    embyUrl: string;
    embyApiKey: string;
    tmdbApiToken: string;
  };
  paths: {
    downloadRoot: string;
    workRoot: string;
    stagingRoot: string;
    animeLibraryRoot: string;
    movieLibraryRoot: string;
    ffmpegPath: string;
    ffprobePath: string;
  };
  transcode: TranscodeProfileConfiguration;
};

export const defaultSetupForm: SetupForm = {
  database: {
    host: 'postgres',
    port: '5432',
    database: 'emby_auto',
    username: 'emby_auto',
    password: '',
    sslMode: 'disable',
  },
  administrator: { username: 'admin', password: '', confirmation: '' },
  services: {
    qbUrl: 'http://host.docker.internal:8080',
    qbUsername: 'admin',
    qbPassword: '',
    embyUrl: 'http://host.docker.internal:8096/emby',
    embyApiKey: '',
    tmdbApiToken: '',
  },
  paths: {
    downloadRoot: '/srv/emby-auto/downloads',
    workRoot: '/srv/emby-auto/work',
    stagingRoot: '/srv/emby-auto/staging',
    animeLibraryRoot: '/srv/emby-auto/media/anime',
    movieLibraryRoot: '/srv/emby-auto/media/movies',
    ffmpegPath: '/usr/bin/ffmpeg',
    ffprobePath: '/usr/bin/ffprobe',
  },
  transcode: {
    name: 'default-h264',
    videoCodec: 'h264',
    encoder: 'libx264',
    container: 'mp4',
    fileExtension: 'mp4',
    qualityMode: 'crf',
    qualityValue: 20,
    audioPolicy: 'transcode',
    audioCodec: 'aac',
    preset: 'medium',
    pixelFormat: 'yuv420p',
    threadCount: 0,
    maxConcurrency: 1,
  },
};

export function buildSetupRequest(form: SetupForm, databaseManagedExternally: boolean): InitializeSetupRequest {
  const request: InitializeSetupRequest = {
    administrator: {
      username: form.administrator.username.trim(),
      password: form.administrator.password,
    },
    configuration: {
      qBittorrent: {
        url: form.services.qbUrl.trim(),
        username: form.services.qbUsername.trim(),
        password: form.services.qbPassword,
      },
      emby: {
        url: form.services.embyUrl.trim(),
        apiKey: form.services.embyApiKey,
      },
      tmdb: { apiToken: form.services.tmdbApiToken },
      paths: {
        downloadRoot: form.paths.downloadRoot.trim(),
        workRoot: form.paths.workRoot.trim(),
        stagingRoot: form.paths.stagingRoot.trim(),
        animeLibraryRoot: form.paths.animeLibraryRoot.trim(),
        movieLibraryRoot: form.paths.movieLibraryRoot.trim(),
        ffmpegPath: form.paths.ffmpegPath.trim(),
        ffprobePath: form.paths.ffprobePath.trim(),
      },
      transcode: form.transcode,
    },
  };
  if (!databaseManagedExternally) {
    request.database = {
      host: form.database.host.trim(),
      port: Number(form.database.port),
      database: form.database.database.trim(),
      username: form.database.username.trim(),
      password: form.database.password,
      sslMode: form.database.sslMode,
    };
  }
  return request;
}

export function validateSetupStep(step: number, databaseManagedExternally: boolean, form: SetupForm): string {
  if (step === 0 && !databaseManagedExternally) {
    const port = Number(form.database.port);
    if (!form.database.host.trim() || !form.database.database.trim() || !form.database.username.trim()) {
      return '请填写完整的数据库连接信息';
    }
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      return '数据库端口无效';
    }
  }
  if (step === 1) {
    if (!form.administrator.username.trim()) {
      return '请填写管理员用户名';
    }
    if (form.administrator.password.length < 8) {
      return '管理员密码至少需要 8 个字符';
    }
    if (form.administrator.password !== form.administrator.confirmation) {
      return '两次输入的管理员密码不一致';
    }
  }
  if (step === 2) {
    if (!validHttpURL(form.services.qbUrl)) {
      return 'qBittorrent URL 必须是有效的 HTTP(S) 地址';
    }
    if (!form.services.qbUsername.trim() || !form.services.qbPassword) {
      return '请填写 qBittorrent 用户名和密码';
    }
    if (!validHttpURL(form.services.embyUrl) || !form.services.embyApiKey) {
      return '请填写有效的 Emby URL 和 API key';
    }
    if (!form.services.tmdbApiToken) {
      return '请填写 TMDb API Read Access Token（v4）';
    }
  }
  if (step === 3) {
    if (Object.values(form.paths).some((value) => !value.trim())) {
      return '请填写全部媒体目录和工具路径';
    }
  }
  if (step === 4) {
    const profile = form.transcode;
    if (!profile.name.trim()) {
      return '请填写转码配置名称';
    }
    if (profile.qualityValue < 0 || !Number.isFinite(profile.qualityValue)) {
      return '转码质量值无效';
    }
    if (profile.threadCount < 0 || profile.threadCount > 256 || !Number.isInteger(profile.threadCount)) {
      return '线程数必须是 0 到 256 的整数';
    }
    if (profile.maxConcurrency < 1 || profile.maxConcurrency > 64 || !Number.isInteger(profile.maxConcurrency)) {
      return '转码并发数必须是 1 到 64 的整数';
    }
  }
  return '';
}

function validHttpURL(value: string): boolean {
  try {
    const parsed = new URL(value);
    return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && Boolean(parsed.hostname) && !parsed.username && !parsed.password;
  } catch {
    return false;
  }
}
