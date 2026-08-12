import type { TranscodeProfileConfiguration } from '@/api/generated/types.gen';

type VideoCodec = TranscodeProfileConfiguration['videoCodec'];
type Encoder = TranscodeProfileConfiguration['encoder'];
type Container = TranscodeProfileConfiguration['container'];
type QualityMode = TranscodeProfileConfiguration['qualityMode'];
type AudioCodec = NonNullable<TranscodeProfileConfiguration['audioCodec']>;
type ProfilePreset = Omit<TranscodeProfileConfiguration, 'name'>;

export type TranscodeRecommendationId = 'balanced-h264' | 'archive-hevc' | 'archive-av1' | 'nvidia-hevc';

export type TranscodeRecommendation = {
  id: TranscodeRecommendationId;
  label: string;
  summary: string;
  profile: ProfilePreset;
};

export const encoderOptions: Record<VideoCodec, readonly Encoder[]> = {
  h264: ['libx264', 'h264_nvenc'],
  hevc: ['libx265', 'hevc_nvenc'],
  av1: ['libsvtav1', 'libaom-av1', 'av1_nvenc'],
};

export const containerOptions: Record<VideoCodec, readonly Container[]> = {
  h264: ['mp4', 'matroska'],
  hevc: ['mp4', 'matroska'],
  av1: ['mp4', 'matroska', 'webm'],
};

export const audioCodecOptions: Record<Container, readonly AudioCodec[]> = {
  mp4: ['aac', 'ac3', 'eac3'],
  matroska: ['aac', 'ac3', 'eac3', 'flac', 'opus', 'vorbis'],
  webm: ['opus', 'vorbis'],
};

export const pixelFormatOptions: TranscodeProfileConfiguration['pixelFormat'][] = [
  'yuv420p',
  'yuv420p10le',
  'yuv422p',
  'yuv422p10le',
  'yuv444p',
  'yuv444p10le',
  'nv12',
  'p010le',
];

export const transcodeRecommendations: readonly TranscodeRecommendation[] = [
  {
    id: 'balanced-h264',
    label: '通用兼容 · H.264',
    summary: '兼容性优先，适合多数 Emby 客户端直接播放。',
    profile: {
      videoCodec: 'h264', encoder: 'libx264', container: 'mp4', fileExtension: 'mp4',
      qualityMode: 'crf', qualityValue: 20, audioPolicy: 'transcode', audioCodec: 'aac',
      preset: 'medium', pixelFormat: 'yuv420p', threadCount: 0, maxConcurrency: 1,
    },
  },
  {
    id: 'archive-hevc',
    label: '高压缩 · HEVC 10-bit',
    summary: '压缩率优先，编码较慢；旧设备的直接播放兼容性较低。',
    profile: {
      videoCodec: 'hevc', encoder: 'libx265', container: 'matroska', fileExtension: 'mkv',
      qualityMode: 'crf', qualityValue: 22, audioPolicy: 'copy',
      preset: 'slow', pixelFormat: 'yuv420p10le', threadCount: 0, maxConcurrency: 1,
    },
  },
  {
    id: 'archive-av1',
    label: '高效存档 · AV1 10-bit',
    summary: '体积优先，编码耗时最长；适合支持 AV1 的新设备。',
    profile: {
      videoCodec: 'av1', encoder: 'libsvtav1', container: 'matroska', fileExtension: 'mkv',
      qualityMode: 'crf', qualityValue: 28, audioPolicy: 'copy',
      preset: '6', pixelFormat: 'yuv420p10le', threadCount: 0, maxConcurrency: 1,
    },
  },
  {
    id: 'nvidia-hevc',
    label: 'NVIDIA 高速 · HEVC 10-bit',
    summary: '需要 NVIDIA GPU 与可用的 NVENC 驱动，速度优先。',
    profile: {
      videoCodec: 'hevc', encoder: 'hevc_nvenc', container: 'matroska', fileExtension: 'mkv',
      qualityMode: 'cq', qualityValue: 23, audioPolicy: 'copy',
      preset: 'p4', pixelFormat: 'p010le', threadCount: 0, maxConcurrency: 1,
    },
  },
];

export const encoderLabels: Record<Encoder, string> = {
  libx264: 'libx264 · CPU / 兼容优先',
  h264_nvenc: 'h264_nvenc · NVIDIA GPU / 速度优先',
  libx265: 'libx265 · CPU / 高压缩',
  hevc_nvenc: 'hevc_nvenc · NVIDIA GPU / 速度优先',
  libsvtav1: 'libsvtav1 · CPU / AV1 推荐',
  'libaom-av1': 'libaom-av1 · CPU / 最慢',
  av1_nvenc: 'av1_nvenc · NVIDIA GPU / 速度优先',
};

export const pixelFormatLabels: Record<TranscodeProfileConfiguration['pixelFormat'], string> = {
  yuv420p: 'yuv420p · 8-bit / 兼容优先',
  yuv420p10le: 'yuv420p10le · 10-bit / CPU 编码',
  yuv422p: 'yuv422p · 8-bit 4:2:2',
  yuv422p10le: 'yuv422p10le · 10-bit 4:2:2',
  yuv444p: 'yuv444p · 8-bit 4:4:4',
  yuv444p10le: 'yuv444p10le · 10-bit 4:4:4',
  nv12: 'nv12 · 8-bit / 硬件编码',
  p010le: 'p010le · 10-bit / 硬件编码',
};

export function presetOptions(encoder: Encoder): string[] {
  if (encoder === 'libx264' || encoder === 'libx265') {
    return ['ultrafast', 'superfast', 'veryfast', 'faster', 'fast', 'medium', 'slow', 'slower', 'veryslow', 'placebo'];
  }
  if (encoder === 'libsvtav1') {
    return Array.from({ length: 14 }, (_, index) => String(index));
  }
  if (encoder === 'libaom-av1') {
    return Array.from({ length: 9 }, (_, index) => String(index));
  }
  return ['p1', 'p2', 'p3', 'p4', 'p5', 'p6', 'p7', 'slow', 'medium', 'fast'];
}

export function qualityModeOptions(encoder: Encoder): QualityMode[] {
  return encoder.endsWith('_nvenc') ? ['cq', 'bitrate'] : ['crf', 'bitrate'];
}

export function qualityValueOptions(mode: QualityMode, current: number): number[] {
  const options = mode === 'bitrate'
    ? [2000, 4000, 6000, 8000, 12000, 16000, 20000, 30000, 50000]
    : mode === 'cq'
      ? [18, 20, 23, 25, 28, 32]
      : [16, 18, 20, 22, 23, 26, 28, 32, 35];
  return withCurrentNumber(options, current);
}

export function threadCountOptions(current: number): number[] {
  return withCurrentNumber([0, 1, 2, 4, 6, 8, 12, 16, 24, 32, 48, 64, 96, 128, 192, 256], current);
}

export function concurrencyOptions(current: number): number[] {
  return withCurrentNumber([1, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64], current);
}

export function withVideoCodec(profile: TranscodeProfileConfiguration, videoCodec: VideoCodec): TranscodeProfileConfiguration {
  const encoder = encoderOptions[videoCodec][0];
  const container = containerOptions[videoCodec].includes(profile.container) ? profile.container : containerOptions[videoCodec][0];
  return normalizeTranscode({
    ...profile,
    videoCodec,
    encoder,
    container,
    qualityMode: 'crf',
    qualityValue: recommendedQualityValue(videoCodec, 'crf'),
    preset: recommendedPreset(encoder),
    pixelFormat: videoCodec === 'h264' ? 'yuv420p' : 'yuv420p10le',
  });
}

export function withEncoder(profile: TranscodeProfileConfiguration, encoder: Encoder): TranscodeProfileConfiguration {
  const qualityMode: QualityMode = encoder.endsWith('_nvenc') ? 'cq' : 'crf';
  return normalizeTranscode({
    ...profile,
    encoder,
    qualityMode,
    qualityValue: recommendedQualityValue(profile.videoCodec, qualityMode),
    preset: recommendedPreset(encoder),
    pixelFormat: encoder.endsWith('_nvenc')
      ? (profile.videoCodec === 'h264' ? 'nv12' : 'p010le')
      : (profile.videoCodec === 'h264' ? 'yuv420p' : 'yuv420p10le'),
  });
}

export function withContainer(profile: TranscodeProfileConfiguration, container: Container): TranscodeProfileConfiguration {
  return normalizeTranscode({ ...profile, container });
}

export function withQualityMode(profile: TranscodeProfileConfiguration, qualityMode: QualityMode): TranscodeProfileConfiguration {
  return { ...profile, qualityMode, qualityValue: recommendedQualityValue(profile.videoCodec, qualityMode) };
}

export function withAudioPolicy(
  profile: TranscodeProfileConfiguration,
  audioPolicy: TranscodeProfileConfiguration['audioPolicy'],
): TranscodeProfileConfiguration {
  if (audioPolicy === 'copy') {
    const { audioCodec: _audioCodec, ...withoutAudioCodec } = profile;
    return { ...withoutAudioCodec, audioPolicy };
  }
  return { ...profile, audioPolicy, audioCodec: audioCodecOptions[profile.container][0] };
}

export function applyRecommendation(
  profile: TranscodeProfileConfiguration,
  recommendationId: TranscodeRecommendationId,
): TranscodeProfileConfiguration {
  const recommendation = transcodeRecommendations.find((candidate) => candidate.id === recommendationId);
  return recommendation ? { name: profile.name, ...recommendation.profile } : profile;
}

export function selectedRecommendation(profile: TranscodeProfileConfiguration): TranscodeRecommendationId | 'custom' {
  for (const recommendation of transcodeRecommendations) {
    if (profileMatches(profile, recommendation.profile)) return recommendation.id;
  }
  return 'custom';
}

function normalizeTranscode(profile: TranscodeProfileConfiguration): TranscodeProfileConfiguration {
  const fileExtension = profile.container === 'matroska' ? 'mkv' : profile.container;
  if (profile.audioPolicy === 'copy') {
    const { audioCodec: _audioCodec, ...withoutAudioCodec } = profile;
    return { ...withoutAudioCodec, fileExtension };
  }
  const allowedAudioCodecs = audioCodecOptions[profile.container];
  const audioCodec = profile.audioCodec && allowedAudioCodecs.includes(profile.audioCodec)
    ? profile.audioCodec
    : allowedAudioCodecs[0];
  return { ...profile, fileExtension, audioCodec };
}

function recommendedQualityValue(videoCodec: VideoCodec, mode: QualityMode): number {
  if (mode === 'bitrate') return 8000;
  if (mode === 'cq') return 23;
  if (videoCodec === 'av1') return 28;
  if (videoCodec === 'hevc') return 22;
  return 20;
}

function recommendedPreset(encoder: Encoder): string {
  if (encoder === 'libx264' || encoder === 'libx265') return 'medium';
  if (encoder === 'libsvtav1') return '6';
  if (encoder === 'libaom-av1') return '4';
  return 'p4';
}

function withCurrentNumber(options: number[], current: number): number[] {
  if (!Number.isFinite(current) || options.includes(current)) return options;
  return [...options, current].sort((left, right) => left - right);
}

function profileMatches(profile: TranscodeProfileConfiguration, preset: ProfilePreset): boolean {
  return Object.entries(preset).every(([key, value]) => profile[key as keyof TranscodeProfileConfiguration] === value)
    && (preset.audioPolicy === 'transcode' || profile.audioCodec === undefined);
}
