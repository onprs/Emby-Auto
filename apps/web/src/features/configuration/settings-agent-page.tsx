import { Bot, LoaderCircle } from 'lucide-react';
import { useEffect, useState } from 'react';

import type { AgentConfiguration, DashboardDependencyStatus, SecretUpdate } from '@/api/generated/types.gen';
import { DetailErrorState, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { revealSecrets } from '@/features/configuration/api';
import { ConfiguredBadge, ConnectivityButton, Field, SecretField, SettingsFeedback } from '@/features/configuration/settings-shared';
import { secretPayload, useConfigurationSave, useSettingsData } from '@/features/configuration/use-settings';
import { cn } from '@/lib/utils';

export function SettingsAgentPage() {
  const { configuration, configurationPending, configurationError, refetchConfiguration, dependencies } = useSettingsData();
  if (configurationPending) {
    return <DetailLoadingState title="Agent" label="正在读取配置" />;
  }
  if (configurationError || !configuration) {
    return <DetailErrorState title="Agent" message={configurationError?.message ?? '无法读取配置'} onRetry={() => refetchConfiguration()} />;
  }
  return <AgentForm configuration={configuration} dependency={dependencies?.agent} />;
}

function AgentForm({
  configuration,
  dependency,
}: {
  configuration: NonNullable<ReturnType<typeof useSettingsData>['configuration']>;
  dependency?: DashboardDependencyStatus;
}) {
  const [settings, setSettings] = useState(() => normalizeCapabilitySettings(configuration.agent));
  const [apiKey, setApiKey] = useState('');
  const [savedAPIKey, setSavedAPIKey] = useState<string | null>(null);
  const { save, error, conflict, savedAt } = useConfigurationSave(configuration);

  useEffect(() => {
    setSettings(normalizeCapabilitySettings(configuration.agent));
    setApiKey('');
    setSavedAPIKey(null);
    if (!configuration.agent.apiKey.configured) return undefined;

    let cancelled = false;
    revealSecrets()
      .then((secrets) => {
        if (cancelled || secrets.agentApiKey === undefined) return;
        setApiKey(secrets.agentApiKey);
        setSavedAPIKey(secrets.agentApiKey);
      })
      .catch(() => {
        // An unavailable reveal must preserve the saved key through a keep action.
      });
    return () => {
      cancelled = true;
    };
  }, [configuration]);

  const apiKeyPayload = (): SecretUpdate => secretPayload(apiKey, savedAPIKey, configuration.agent.apiKey.configured);
  const keyAction = apiKeyPayload().action;
  const keyAvailable = keyAction === 'set' || (keyAction === 'keep' && configuration.agent.apiKey.configured);
  const connectivityReady = settings.baseUrl.trim() !== '' && settings.model.trim() !== '' && keyAvailable;
  const saveReady = !settings.enabled || connectivityReady;

  const setEnabled = (enabled: boolean) => {
    setSettings((current) => enabled ? { ...current, enabled: true } : {
      ...current,
      enabled: false,
      rssCoordinateMode: 'off',
      downloadFileSelectionMode: 'off',
      catalogMatchEnabled: false,
      episodeMappingEnabled: false,
      allowAutomaticEpisodeMapping: false,
      subtitleVideoMatchMode: 'off',
    });
  };
  const setRSSFiltering = (enabled: boolean) => {
    setSettings((current) => ({ ...current, rssCoordinateMode: enabled ? 'validated_auto' : 'off' }));
  };
  const setDownloadFileResolution = (enabled: boolean) => {
    setSettings((current) => ({ ...current, downloadFileSelectionMode: enabled ? 'validated_auto' : 'off' }));
  };
  const setMappingEnabled = (enabled: boolean) => {
    setSettings((current) => ({
      ...current,
      episodeMappingEnabled: enabled,
      allowAutomaticEpisodeMapping: enabled,
    }));
  };
  const setSubtitleVideoMatch = (enabled: boolean) => {
    setSettings((current) => ({ ...current, subtitleVideoMatchMode: enabled ? 'validated_auto' : 'off' }));
  };

  return (
    <PageBody>
      <PageHeader title="Agent" description="确定性规则优先，Agent 仅处理无法唯一判断的异常情况" />
      <SettingsFeedback conflict={conflict} error={error} savedAt={savedAt} />

      <div className="grid gap-5 xl:grid-cols-[minmax(0,1.1fr)_minmax(22rem,0.9fr)]">
        <Card>
          <CardHeader
            fill
            action={
              <ConfiguredBadge
                configured={configuration.agent.apiKey.configured}
                configuredLabel="API key 已配置"
                missingLabel="API key 未配置"
              />
            }
          >
            <CardTitle className="flex items-center gap-2"><Bot className="size-4" aria-hidden="true" />连接</CardTitle>
            <CardDescription>OpenAI-compatible Chat Completions</CardDescription>
          </CardHeader>
          <CardContent className="space-y-5">
            <ToggleRow
              id="agent-enabled"
              label="启用 Agent 辅助"
              checked={settings.enabled}
              onChange={setEnabled}
            />
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="API 协议" htmlFor="agent-protocol">
                <select
                  id="agent-protocol"
                  value={settings.protocol}
                  onChange={() => undefined}
                  className="h-10 w-full rounded-lg border border-zinc-300 bg-white px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-600"
                >
                  <option value="openai_chat_completions">OpenAI-compatible</option>
                </select>
              </Field>
              <Field label="模型" htmlFor="agent-model">
                <Input id="agent-model" value={settings.model} onChange={(event) => setSettings({ ...settings, model: event.target.value })} />
              </Field>
            </div>
            <Field label="Base URL" htmlFor="agent-base-url">
              <Input
                id="agent-base-url"
                type="url"
                placeholder="https://provider.example/v1"
                value={settings.baseUrl}
                onChange={(event) => setSettings({ ...settings, baseUrl: event.target.value })}
              />
            </Field>
            <SecretField
              label="Agent API key"
              value={apiKey}
              onChange={setApiKey}
              placeholder={configuration.agent.apiKey.configured && savedAPIKey === null ? '正在读取已保存值' : '请输入 API key'}
            />
            <div className="grid gap-4 sm:grid-cols-2">
              <ToggleRow
                id="agent-use-proxy"
                label="使用网络代理"
                checked={settings.useNetworkProxy}
                onChange={(useNetworkProxy) => setSettings({ ...settings, useNetworkProxy })}
              />
              <Field label="请求超时（秒）" htmlFor="agent-timeout">
                <Input
                  id="agent-timeout"
                  type="number"
                  min={10}
                  max={120}
                  step={1}
                  value={settings.requestTimeoutSeconds}
                  onChange={(event) => {
                    const value = event.currentTarget.valueAsNumber;
                    setSettings({ ...settings, requestTimeoutSeconds: Number.isFinite(value) ? Math.min(120, Math.max(10, Math.trunc(value))) : 60 });
                  }}
                />
              </Field>
            </div>
            <ConnectivityButton
              target="agent"
              previous={dependency}
              payload={{
                agent: {
                  protocol: settings.protocol,
                  baseUrl: settings.baseUrl,
                  model: settings.model,
                  apiKey: apiKeyPayload(),
                  useNetworkProxy: settings.useNetworkProxy,
                },
              }}
              disabled={!connectivityReady}
            />
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>辅助能力</CardTitle>
            <CardDescription>开关仅允许兜底介入，不会跳过确定性规则</CardDescription>
          </CardHeader>
          <CardContent className="space-y-5">
            <ToggleRow
              id="agent-rss-filtering"
              label="RSS 发布筛选"
              checked={settings.rssCoordinateMode !== 'off'}
              disabled={!settings.enabled}
              onChange={setRSSFiltering}
            />
            <ToggleRow
              id="agent-download-file-resolution"
              label="下载文件解析"
              checked={settings.downloadFileSelectionMode !== 'off'}
              disabled={!settings.enabled}
              onChange={setDownloadFileResolution}
            />
            <ToggleRow
              id="agent-catalog-match"
              label="TMDb 候选辅助"
              checked={settings.catalogMatchEnabled}
              disabled={!settings.enabled}
              onChange={(catalogMatchEnabled) => setSettings((current) => ({ ...current, catalogMatchEnabled }))}
            />
            <ToggleRow
              id="agent-episode-mapping"
              label="剧集 Mapping"
              checked={settings.episodeMappingEnabled && settings.allowAutomaticEpisodeMapping}
              disabled={!settings.enabled}
              onChange={setMappingEnabled}
            />
            <ToggleRow
              id="agent-subtitle-video-match"
              label="字幕-视频匹配"
              checked={settings.subtitleVideoMatchMode !== 'off'}
              disabled={!settings.enabled}
              onChange={setSubtitleVideoMatch}
            />
          </CardContent>
        </Card>
      </div>

      <div className="sticky bottom-0 z-20 mt-8 flex justify-end border-t border-zinc-200 bg-surface/95 py-4 backdrop-blur-sm">
        <Button
          type="button"
          variant="accent"
          disabled={save.isPending || !saveReady}
          onClick={() => save.mutate({ agent: { ...settings, apiKey: apiKeyPayload() } })}
        >
          {save.isPending ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : null}
          {save.isPending ? '正在保存' : '保存 Agent 配置'}
        </Button>
      </div>
    </PageBody>
  );
}

function ToggleRow({
  id,
  label,
  checked,
  disabled = false,
  onChange,
}: {
  id: string;
  label: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <label className={cn('flex items-center justify-between gap-4 text-sm font-medium text-zinc-800', disabled && 'opacity-50')} htmlFor={id}>
      <span>{label}</span>
      <input
        id={id}
        type="checkbox"
        className="size-4 shrink-0 accent-emerald-700"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
    </label>
  );
}

function normalizeCapabilitySettings(settings: AgentConfiguration): AgentConfiguration {
  if (!settings.enabled) {
    return {
      ...settings,
      rssCoordinateMode: 'off',
      downloadFileSelectionMode: 'off',
      catalogMatchEnabled: false,
      episodeMappingEnabled: false,
      allowAutomaticEpisodeMapping: false,
      subtitleVideoMatchMode: 'off',
    };
  }
  const episodeMappingEnabled = settings.episodeMappingEnabled || settings.allowAutomaticEpisodeMapping;
  return {
    ...settings,
    rssCoordinateMode: settings.rssCoordinateMode === 'off' ? 'off' : 'validated_auto',
    downloadFileSelectionMode: settings.downloadFileSelectionMode === 'off' ? 'off' : 'validated_auto',
    subtitleVideoMatchMode: settings.subtitleVideoMatchMode === 'off' ? 'off' : 'validated_auto',
    episodeMappingEnabled,
    allowAutomaticEpisodeMapping: episodeMappingEnabled,
  };
}
