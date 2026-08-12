import { unwrap } from '@/api/app-client';
import { getBackgroundRuntime, getDashboardSummary, getDashboardSystemMetrics, updateBackgroundRuntime } from '@/api/generated/sdk.gen';
import type { BackgroundRuntime, DashboardSummary, SystemMetricsSnapshot } from '@/api/generated/types.gen';

export function fetchDashboardSummary(): Promise<DashboardSummary> {
  return unwrap<DashboardSummary>(getDashboardSummary(), '无法读取概览');
}

export function fetchDashboardSystemMetrics(): Promise<SystemMetricsSnapshot> {
  return unwrap<SystemMetricsSnapshot>(getDashboardSystemMetrics(), '无法读取系统资源');
}

export function fetchBackgroundRuntime(): Promise<BackgroundRuntime> {
  return unwrap<BackgroundRuntime>(getBackgroundRuntime(), '无法读取后台运行状态');
}

export function setBackgroundRuntime(state: 'running' | 'stopped'): Promise<BackgroundRuntime> {
  return unwrap<BackgroundRuntime>(updateBackgroundRuntime({ body: { state } }), state === 'running' ? '无法启动后台任务' : '无法停止后台任务');
}
