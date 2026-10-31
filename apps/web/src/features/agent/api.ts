import { unwrap } from '@/api/app-client';
import { getAgentResolution } from '@/api/generated/sdk.gen';
import type { AgentResolution } from '@/api/generated/types.gen';

export function fetchAgentResolution(resolutionId: string): Promise<AgentResolution> {
  return unwrap<AgentResolution>(
    getAgentResolution({ path: { resolutionId } }),
    '无法读取自动处理状态',
  );
}
