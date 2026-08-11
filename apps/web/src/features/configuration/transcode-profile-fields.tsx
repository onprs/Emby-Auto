import type { TranscodeProfileConfiguration } from '@/api/generated/types.gen';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select } from '@/components/ui/select';
import {
  applyRecommendation,
  audioCodecOptions,
  concurrencyOptions,
  containerOptions,
  encoderLabels,
  encoderOptions,
  pixelFormatLabels,
  pixelFormatOptions,
  presetOptions,
  qualityModeOptions,
  qualityValueOptions,
  selectedRecommendation,
  threadCountOptions,
  transcodeRecommendations,
  withAudioPolicy,
  withContainer,
  withEncoder,
  withQualityMode,
  withVideoCodec,
  type TranscodeRecommendationId,
} from '@/features/configuration/transcode-options';
import { cn } from '@/lib/utils';

type TranscodeProfileFieldsProps = {
  profile: TranscodeProfileConfiguration;
  onChange: (profile: TranscodeProfileConfiguration) => void;
  idPrefix: string;
  disabled?: boolean;
};

export function TranscodeProfileFields({ profile, onChange, idPrefix, disabled = false }: TranscodeProfileFieldsProps) {
  const recommendationId = selectedRecommendation(profile);
  const recommendation = transcodeRecommendations.find((candidate) => candidate.id === recommendationId);
  const encoders = encoderOptions[profile.videoCodec];
  const containers = containerOptions[profile.videoCodec];
  const audioCodecs = audioCodecOptions[profile.container];
  const qualityModes = qualityModeOptions(profile.encoder);
  const qualities = qualityValueOptions(profile.qualityMode, profile.qualityValue);
  const presets = presetOptions(profile.encoder);

  const id = (name: string) => `${idPrefix}-${name}`;
  const update = (patch: Partial<TranscodeProfileConfiguration>) => onChange({ ...profile, ...patch });

  return (
    <fieldset disabled={disabled} className="contents">
      <legend className="sr-only">转码参数</legend>
      <Field label="推荐方案" htmlFor={id('recommendation')} className="sm:col-span-2 lg:col-span-3" hint={recommendation?.summary ?? '自定义组合仍会在保存时执行完整兼容性校验。'}>
        <Select
          id={id('recommendation')}
          value={recommendationId}
          onChange={(value) => {
            if (value !== 'custom') {
              onChange(applyRecommendation(profile, value as TranscodeRecommendationId));
            }
          }}
          options={[
            { value: 'custom', label: '自定义参数' },
            ...transcodeRecommendations.map((item) => ({ value: item.id as string, label: item.label })),
          ]}
        />
      </Field>

      <Field label="配置名称" htmlFor={id('profile-name')}>
        <Input id={id('profile-name')} value={profile.name} onChange={(event) => update({ name: event.target.value })} />
      </Field>

      <Field label="视频编码" htmlFor={id('video-codec')} hint="H.264 兼容性最好；HEVC 与 AV1 更节省空间。">
        <Select
          id={id('video-codec')}
          value={profile.videoCodec}
          onChange={(value) => onChange(withVideoCodec(profile, value as TranscodeProfileConfiguration['videoCodec']))}
          options={[
            { value: 'h264', label: 'H.264 · 兼容优先' },
            { value: 'hevc', label: 'HEVC · 高压缩' },
            { value: 'av1', label: 'AV1 · 新设备 / 高压缩' },
          ]}
        />
      </Field>

      <Field label="编码器" htmlFor={id('encoder')} hint={profile.encoder.endsWith('_nvenc') ? '需要 NVIDIA GPU、驱动和包含 NVENC 的 FFmpeg。' : '使用 CPU 编码，不依赖独立显卡。'}>
        <Select
          id={id('encoder')}
          value={profile.encoder}
          onChange={(value) => onChange(withEncoder(profile, value as TranscodeProfileConfiguration['encoder']))}
          options={encoders.map((encoder) => ({ value: encoder as string, label: encoderLabels[encoder] }))}
        />
      </Field>

      <Field label="封装格式" htmlFor={id('container')}>
        <Select
          id={id('container')}
          value={profile.container}
          onChange={(value) => onChange(withContainer(profile, value as TranscodeProfileConfiguration['container']))}
          options={containers.map((container) => ({ value: container as string, label: containerLabel(container) }))}
        />
      </Field>

      <Field label="文件扩展名" htmlFor={id('extension')} hint="由封装格式自动确定。">
        <Input id={id('extension')} value={profile.fileExtension} readOnly className="bg-zinc-100" />
      </Field>

      <Field label="质量模式" htmlFor={id('quality-mode')}>
        <Select
          id={id('quality-mode')}
          value={profile.qualityMode}
          onChange={(value) => onChange(withQualityMode(profile, value as TranscodeProfileConfiguration['qualityMode']))}
          options={qualityModes.map((mode) => ({ value: mode as string, label: qualityModeLabel(mode) }))}
        />
      </Field>

      <Field label="质量值" htmlFor={id('quality-value')} hint={profile.qualityMode === 'bitrate' ? '码率越高，画质和文件体积越大。' : '数值越低，画质和文件体积越大。'}>
        <Select
          id={id('quality-value')}
          value={String(profile.qualityValue)}
          onChange={(value) => update({ qualityValue: Number(value) })}
          options={qualities.map((quality) => ({ value: String(quality), label: qualityValueLabel(profile.qualityMode, quality, profile.videoCodec) }))}
        />
      </Field>

      <Field label="音频策略" htmlFor={id('audio-policy')}>
        <Select
          id={id('audio-policy')}
          value={profile.audioPolicy}
          onChange={(value) => onChange(withAudioPolicy(profile, value as TranscodeProfileConfiguration['audioPolicy']))}
          options={[
            { value: 'copy', label: '复制原音轨 · 保留音质' },
            { value: 'transcode', label: '统一转码 · 兼容优先' },
          ]}
        />
      </Field>

      {profile.audioPolicy === 'transcode' ? (
        <Field label="音频编码" htmlFor={id('audio-codec')}>
          <Select
            id={id('audio-codec')}
            value={profile.audioCodec ?? audioCodecs[0]}
            onChange={(value) => update({ audioCodec: value as NonNullable<TranscodeProfileConfiguration['audioCodec']> })}
            options={audioCodecs.map((codec) => ({ value: codec as string, label: audioCodecLabel(codec) }))}
          />
        </Field>
      ) : null}

      <Field label="preset" htmlFor={id('preset')} hint="更慢的 preset 通常压缩率更高，但不会直接改变目标质量值。">
        <Select
          id={id('preset')}
          value={profile.preset}
          onChange={(value) => update({ preset: value })}
          options={presets.map((preset) => ({ value: preset, label: presetLabel(profile.encoder, preset) }))}
        />
      </Field>

      <Field label="像素格式" htmlFor={id('pixel-format')}>
        <Select
          id={id('pixel-format')}
          value={profile.pixelFormat}
          onChange={(value) => update({ pixelFormat: value as TranscodeProfileConfiguration['pixelFormat'] })}
          options={pixelFormatOptions.map((pixelFormat) => ({ value: pixelFormat, label: pixelFormatLabels[pixelFormat] }))}
        />
      </Field>

      <Field label="线程数" htmlFor={id('thread-count')} hint="自动会让 FFmpeg 根据编码器和主机资源决定。">
        <Select
          id={id('thread-count')}
          value={String(profile.threadCount)}
          onChange={(value) => update({ threadCount: Number(value) })}
          options={threadCountOptions(profile.threadCount).map((threads) => ({ value: String(threads), label: threads === 0 ? '自动 · 推荐' : `${threads} 线程` }))}
        />
      </Field>

      <Field label="转码并发数" htmlFor={id('max-concurrency')} hint="建议从 1 开始；并发会同时增加 CPU、内存和磁盘压力。">
        <Select
          id={id('max-concurrency')}
          value={String(profile.maxConcurrency)}
          onChange={(value) => update({ maxConcurrency: Number(value) })}
          options={concurrencyOptions(profile.maxConcurrency).map((concurrency) => ({ value: String(concurrency), label: concurrency === 1 ? '1 · 推荐' : String(concurrency) }))}
        />
      </Field>
    </fieldset>
  );
}

function Field({ label, htmlFor, hint, className, children }: { label: string; htmlFor: string; hint?: string; className?: string; children: React.ReactNode }) {
  const hintId = hint ? `${htmlFor}-hint` : undefined;
  return (
    <div className={cn('min-w-0 space-y-2', className)}>
      <Label htmlFor={htmlFor}>{label}</Label>
      <div aria-describedby={hintId}>{children}</div>
      {hint ? <p id={hintId} className="text-xs leading-5 text-zinc-500">{hint}</p> : null}
    </div>
  );
}

function containerLabel(container: TranscodeProfileConfiguration['container']): string {
  if (container === 'matroska') return 'Matroska · MKV';
  if (container === 'mp4') return 'MP4 · 兼容优先';
  return 'WebM · AV1';
}

function qualityModeLabel(mode: TranscodeProfileConfiguration['qualityMode']): string {
  if (mode === 'crf') return 'CRF · 恒定质量 / CPU';
  if (mode === 'cq') return 'CQ · 恒定质量 / NVENC';
  return '固定码率';
}

function qualityValueLabel(mode: TranscodeProfileConfiguration['qualityMode'], value: number, codec: TranscodeProfileConfiguration['videoCodec']): string {
  if (mode === 'bitrate') return `${value} kbps${value === 8000 ? ' · 1080p 建议' : ''}`;
  const recommended = mode === 'cq' ? 23 : codec === 'av1' ? 28 : codec === 'hevc' ? 22 : 20;
  return `${value}${value === recommended ? ' · 推荐' : ''}`;
}

function audioCodecLabel(codec: NonNullable<TranscodeProfileConfiguration['audioCodec']>): string {
  if (codec === 'aac') return 'AAC · 兼容优先';
  if (codec === 'opus') return 'Opus · 高压缩';
  if (codec === 'flac') return 'FLAC · 无损';
  return codec.toUpperCase();
}

function presetLabel(encoder: TranscodeProfileConfiguration['encoder'], preset: string): string {
  const recommended = encoder === 'libx264' || encoder === 'libx265'
    ? 'medium'
    : encoder === 'libsvtav1'
      ? '6'
      : encoder === 'libaom-av1'
        ? '4'
        : 'p4';
  return `${preset}${preset === recommended ? ' · 推荐' : ''}`;
}
