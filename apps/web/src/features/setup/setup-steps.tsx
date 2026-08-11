import type { Dispatch, ReactNode, SetStateAction } from 'react';

import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select } from '@/components/ui/select';
import { TranscodeProfileFields } from '@/features/configuration/transcode-profile-fields';
import type { SetupForm } from '@/features/setup/setup-form';

type SetupStepContentProps = {
  step: number;
  databaseManagedExternally: boolean;
  form: SetupForm;
  setForm: Dispatch<SetStateAction<SetupForm>>;
  disabled: boolean;
};

export function SetupStepContent({ step, databaseManagedExternally, form, setForm, disabled }: SetupStepContentProps) {
  if (step === 0) {
    return <DatabaseStep form={form} setForm={setForm} disabled={disabled} />;
  }
  if (step === 1) {
    return <AdministratorStep form={form} setForm={setForm} disabled={disabled} databaseManagedExternally={databaseManagedExternally} />;
  }
  if (step === 2) {
    return <ServicesStep form={form} setForm={setForm} disabled={disabled} />;
  }
  if (step === 3) {
    return <StorageStep form={form} setForm={setForm} disabled={disabled} />;
  }
  if (step === 4) {
    return <TranscodeStep form={form} setForm={setForm} disabled={disabled} />;
  }
  return <ReviewStep form={form} databaseManagedExternally={databaseManagedExternally} />;
}

function DatabaseStep({ form, setForm, disabled }: StepProps) {
  const update = (patch: Partial<SetupForm['database']>) => setForm((current) => ({
    ...current,
    database: { ...current.database, ...patch },
  }));
  return (
    <fieldset className="grid gap-5" disabled={disabled}>
      <legend className="mb-6 text-lg font-semibold text-zinc-900">PostgreSQL</legend>
      <div className="grid gap-5 sm:grid-cols-[1fr_140px]">
        <Field label="主机" htmlFor="database-host">
          <Input id="database-host" value={form.database.host} onChange={(event) => update({ host: event.target.value })} autoComplete="off" />
        </Field>
        <Field label="端口" htmlFor="database-port">
          <Input id="database-port" value={form.database.port} onChange={(event) => update({ port: event.target.value })} inputMode="numeric" />
        </Field>
      </div>
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="数据库" htmlFor="database-name">
          <Input id="database-name" value={form.database.database} onChange={(event) => update({ database: event.target.value })} autoComplete="off" />
        </Field>
        <Field label="SSL 模式" htmlFor="database-ssl">
          <Select
            id="database-ssl"
            value={form.database.sslMode}
            onChange={(value) => update({ sslMode: value as SetupForm['database']['sslMode'] })}
            options={[
              { value: 'disable', label: 'disable' },
              { value: 'require', label: 'require' },
              { value: 'verify-ca', label: 'verify-ca' },
              { value: 'verify-full', label: 'verify-full' },
            ]}
          />
        </Field>
      </div>
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="用户名" htmlFor="database-username">
          <Input id="database-username" value={form.database.username} onChange={(event) => update({ username: event.target.value })} autoComplete="username" />
        </Field>
        <Field label="密码" htmlFor="database-password">
          <Input id="database-password" type="password" value={form.database.password} onChange={(event) => update({ password: event.target.value })} autoComplete="new-password" />
        </Field>
      </div>
    </fieldset>
  );
}

function AdministratorStep({ form, setForm, disabled, databaseManagedExternally }: StepProps & { databaseManagedExternally: boolean }) {
  const update = (patch: Partial<SetupForm['administrator']>) => setForm((current) => ({
    ...current,
    administrator: { ...current.administrator, ...patch },
  }));
  return (
    <fieldset className="grid gap-5" disabled={disabled}>
      <legend className="mb-6 text-lg font-semibold text-zinc-900">初始管理员</legend>
      {databaseManagedExternally ? (
        <p className="border-l-2 border-emerald-600 bg-emerald-50 px-4 py-3 text-sm text-emerald-900">PostgreSQL 由部署环境管理</p>
      ) : null}
      <Field label="用户名" htmlFor="admin-username">
        <Input id="admin-username" value={form.administrator.username} onChange={(event) => update({ username: event.target.value })} autoComplete="username" />
      </Field>
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="密码" htmlFor="admin-password">
          <Input id="admin-password" type="password" value={form.administrator.password} onChange={(event) => update({ password: event.target.value })} autoComplete="new-password" />
        </Field>
        <Field label="确认密码" htmlFor="admin-confirmation">
          <Input id="admin-confirmation" type="password" value={form.administrator.confirmation} onChange={(event) => update({ confirmation: event.target.value })} autoComplete="new-password" />
        </Field>
      </div>
    </fieldset>
  );
}

function ServicesStep({ form, setForm, disabled }: StepProps) {
  const update = (patch: Partial<SetupForm['services']>) => setForm((current) => ({
    ...current,
    services: { ...current.services, ...patch },
  }));
  return (
    <fieldset disabled={disabled}>
      <legend className="mb-6 text-lg font-semibold text-zinc-900">外部服务</legend>
      <div className="grid gap-5 border-b border-zinc-200 pb-6">
        <h2 className="text-sm font-semibold text-zinc-900">qBittorrent</h2>
        <Field label="URL" htmlFor="setup-qb-url">
          <Input id="setup-qb-url" type="url" value={form.services.qbUrl} onChange={(event) => update({ qbUrl: event.target.value })} autoComplete="url" />
        </Field>
        <div className="grid gap-5 sm:grid-cols-2">
          <Field label="用户名" htmlFor="setup-qb-username">
            <Input id="setup-qb-username" value={form.services.qbUsername} onChange={(event) => update({ qbUsername: event.target.value })} autoComplete="username" />
          </Field>
          <Field label="密码" htmlFor="setup-qb-password">
            <Input id="setup-qb-password" type="password" value={form.services.qbPassword} onChange={(event) => update({ qbPassword: event.target.value })} autoComplete="new-password" />
          </Field>
        </div>
      </div>
      <div className="grid gap-5 border-b border-zinc-200 py-6 sm:grid-cols-2">
        <Field label="Emby URL" htmlFor="setup-emby-url">
          <Input id="setup-emby-url" type="url" value={form.services.embyUrl} onChange={(event) => update({ embyUrl: event.target.value })} autoComplete="url" />
        </Field>
        <Field label="Emby API key" htmlFor="setup-emby-key">
          <Input id="setup-emby-key" type="password" value={form.services.embyApiKey} onChange={(event) => update({ embyApiKey: event.target.value })} autoComplete="new-password" />
        </Field>
      </div>
      <div className="grid gap-5 pt-6">
        <Field label="TMDb API Read Access Token" htmlFor="setup-tmdb-token">
          <Input id="setup-tmdb-token" type="password" value={form.services.tmdbApiToken} onChange={(event) => update({ tmdbApiToken: event.target.value })} autoComplete="new-password" />
        </Field>
      </div>
    </fieldset>
  );
}

function StorageStep({ form, setForm, disabled }: StepProps) {
  const update = (patch: Partial<SetupForm['paths']>) => setForm((current) => ({
    ...current,
    paths: { ...current.paths, ...patch },
  }));
  return (
    <fieldset className="grid gap-5" disabled={disabled}>
      <legend className="mb-6 text-lg font-semibold text-zinc-900">存储与媒体工具</legend>
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="下载根目录" htmlFor="setup-download-root">
          <Input id="setup-download-root" value={form.paths.downloadRoot} onChange={(event) => update({ downloadRoot: event.target.value })} />
        </Field>
        <Field label="工作根目录" htmlFor="setup-work-root">
          <Input id="setup-work-root" value={form.paths.workRoot} onChange={(event) => update({ workRoot: event.target.value })} />
        </Field>
        <Field label="暂存根目录" htmlFor="setup-staging-root">
          <Input id="setup-staging-root" value={form.paths.stagingRoot} onChange={(event) => update({ stagingRoot: event.target.value })} />
        </Field>
        <Field label="番剧媒体库目录" htmlFor="setup-anime-library-root">
          <Input id="setup-anime-library-root" value={form.paths.animeLibraryRoot} onChange={(event) => update({ animeLibraryRoot: event.target.value })} />
        </Field>
        <Field label="电影媒体库目录" htmlFor="setup-movie-library-root">
          <Input id="setup-movie-library-root" value={form.paths.movieLibraryRoot} onChange={(event) => update({ movieLibraryRoot: event.target.value })} />
        </Field>
        <Field label="FFmpeg 路径" htmlFor="setup-ffmpeg-path">
          <Input id="setup-ffmpeg-path" value={form.paths.ffmpegPath} onChange={(event) => update({ ffmpegPath: event.target.value })} />
        </Field>
        <Field label="ffprobe 路径" htmlFor="setup-ffprobe-path">
          <Input id="setup-ffprobe-path" value={form.paths.ffprobePath} onChange={(event) => update({ ffprobePath: event.target.value })} />
        </Field>
      </div>
    </fieldset>
  );
}

function TranscodeStep({ form, setForm, disabled }: StepProps) {
  return (
    <section>
      <h2 className="mb-6 text-lg font-semibold text-zinc-900">转码配置</h2>
      <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
        <TranscodeProfileFields
          profile={form.transcode}
          onChange={(transcode) => setForm((current) => ({ ...current, transcode }))}
          idPrefix="setup"
          disabled={disabled}
        />
      </div>
    </section>
  );
}

function ReviewStep({ form, databaseManagedExternally }: { form: SetupForm; databaseManagedExternally: boolean }) {
  return (
    <fieldset>
      <legend className="mb-6 text-lg font-semibold text-zinc-900">初始化确认</legend>
      <dl className="divide-y divide-zinc-200 border-y border-zinc-200 text-sm">
        <ReviewRow label="数据库" value={databaseManagedExternally ? '部署环境' : `${form.database.host}:${form.database.port}/${form.database.database}`} />
        <ReviewRow label="管理员" value={form.administrator.username.trim()} />
        <ReviewRow label="qBittorrent" value={`${form.services.qbUrl.trim()} · ${form.services.qbUsername.trim()}`} />
        <ReviewRow label="Emby" value={form.services.embyUrl.trim()} />
        <ReviewRow label="TMDb" value="API Read Access Token（v4）已填写" />
        <ReviewRow label="下载目录" value={form.paths.downloadRoot.trim()} />
        <ReviewRow label="工作目录" value={form.paths.workRoot.trim()} />
        <ReviewRow label="暂存目录" value={form.paths.stagingRoot.trim()} />
        <ReviewRow label="番剧媒体库" value={form.paths.animeLibraryRoot.trim()} />
        <ReviewRow label="电影媒体库" value={form.paths.movieLibraryRoot.trim()} />
        <ReviewRow label="媒体工具" value={`${form.paths.ffmpegPath.trim()} · ${form.paths.ffprobePath.trim()}`} />
        <ReviewRow label="转码" value={`${form.transcode.videoCodec} · ${form.transcode.encoder} · ${form.transcode.container} · ${form.transcode.qualityMode} ${form.transcode.qualityValue}`} />
      </dl>
    </fieldset>
  );
}

type StepProps = {
  form: SetupForm;
  setForm: Dispatch<SetStateAction<SetupForm>>;
  disabled: boolean;
};

function Field({ label, htmlFor, children }: { label: string; htmlFor: string; children: ReactNode }) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
    </div>
  );
}

function ReviewRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1 py-3 sm:grid-cols-[120px_1fr] sm:gap-4 sm:py-4">
      <dt className="text-zinc-500">{label}</dt>
      <dd className="min-w-0 break-words font-medium text-zinc-900">{value}</dd>
    </div>
  );
}
