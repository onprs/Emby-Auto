import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';

import { ApiFailure } from '@/api/app-client';
import type { Configuration, DashboardDependencyStatus, SecretUpdate, UpdateConfigurationRequest } from '@/api/generated/types.gen';
import { fetchConfiguration, saveConfiguration } from '@/features/configuration/api';
import { fetchDashboardSummary } from '@/features/dashboard/api';

export type SettingsDependencies = {
  qBittorrent: DashboardDependencyStatus;
  tmdb: DashboardDependencyStatus;
  emby: DashboardDependencyStatus;
  mediaTools: DashboardDependencyStatus;
  networkProxy: DashboardDependencyStatus;
  agent: DashboardDependencyStatus;
};

export function useSettingsData() {
  const configuration = useQuery({ queryKey: ['configuration'], queryFn: fetchConfiguration });
  const dashboard = useQuery({ queryKey: ['dashboard'], queryFn: fetchDashboardSummary });
  return {
    configuration: configuration.data,
    configurationPending: configuration.isPending,
    configurationError: configuration.error,
    refetchConfiguration: configuration.refetch,
    dependencies: dashboard.data?.dependencies as SettingsDependencies | undefined,
  };
}

/**
 * 基于输入框当前值与已加载的明文计算密钥更新动作：
 * - 未加载（明文接口不可用）且为空 → keep（不动已保存值）
 * - 已加载且与已保存值一致 → keep
 * - 已加载/已配置但清空 → clear
 * - 其余 → set
 */
export function secretPayload(value: string, saved: string | null, configured: boolean): SecretUpdate {
  if (saved === null) {
    return value === '' ? { action: 'keep' } : { action: 'set', value };
  }
  if (value === saved) {
    return { action: 'keep' };
  }
  if (configured && value === '') {
    return { action: 'clear' };
  }
  return { action: 'set', value };
}

export function useConfigurationSave(configuration: Configuration) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  const [savedAt, setSavedAt] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: (patch: Partial<Omit<UpdateConfigurationRequest, 'expectedVersion'>>) => {
      setError(null);
      setConflict(false);
      const body: UpdateConfigurationRequest = {
        expectedVersion: configuration.version,
        qBittorrent: patch.qBittorrent ?? {
          url: configuration.qBittorrent.url,
          username: configuration.qBittorrent.username,
          password: { action: 'keep' },
          downloadRateLimitKibPerSecond: configuration.qBittorrent.downloadRateLimitKibPerSecond,
          uploadRateLimitKibPerSecond: configuration.qBittorrent.uploadRateLimitKibPerSecond,
        },
        emby: patch.emby ?? { url: configuration.emby.url, apiKey: { action: 'keep' } },
        tmdb: patch.tmdb ?? { apiToken: { action: 'keep' } },
        networkProxy: patch.networkProxy ?? configuration.networkProxy,
        agent: patch.agent ?? {
          ...configuration.agent,
          apiKey: { action: 'keep' },
        },
        paths: patch.paths ?? configuration.paths,
        transcode: patch.transcode ?? configuration.transcode,
      };
      return saveConfiguration(body);
    },
    onSuccess: (updated) => {
      setSavedAt(new Date().toISOString());
      queryClient.setQueryData(['configuration'], updated);
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
    },
    onError: (cause) => {
      if (cause instanceof ApiFailure && cause.isConflict) {
        setConflict(true);
      }
      setError(cause instanceof Error ? cause.message : '保存配置失败');
    },
  });

  return { save, error, conflict, savedAt, resetFeedback: () => { setError(null); setConflict(false); setSavedAt(null); } };
}
