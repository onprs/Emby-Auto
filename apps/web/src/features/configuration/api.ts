import { unwrap } from '@/api/app-client';
import { getConfiguration, getEventStats, revealConfigurationSecrets, testConnectivity, updateConfiguration } from '@/api/generated/sdk.gen';
import type { Configuration, ConnectivityTestRequest, ConnectivityTestResult, EventStats, RevealedSecrets, UpdateConfigurationRequest } from '@/api/generated/types.gen';

export function fetchConfiguration(): Promise<Configuration> {
  return unwrap<Configuration>(getConfiguration(), '无法读取配置');
}

export function fetchEventStats(): Promise<EventStats> {
  return unwrap<EventStats>(getEventStats(), '无法读取事件统计');
}

export function saveConfiguration(body: UpdateConfigurationRequest): Promise<Configuration> {
  return unwrap<Configuration>(updateConfiguration({ body }), '保存配置失败');
}

export function revealSecrets(): Promise<RevealedSecrets> {
  return unwrap<RevealedSecrets>(revealConfigurationSecrets({ cache: 'no-store' }), '无法读取密钥');
}

export function testConnection(body: ConnectivityTestRequest): Promise<ConnectivityTestResult> {
  return unwrap<ConnectivityTestResult>(testConnectivity({ body }), '连接测试失败');
}
