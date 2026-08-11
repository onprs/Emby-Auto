import { unwrap } from '@/api/app-client';
import {
  getAgentResolution,
  listAgentResolutions,
} from '@/api/generated/sdk.gen';
import type {
  AgentResolution,
  AgentResolutionCapability,
  AgentResolutionPage,
} from '@/api/generated/types.gen';

export function fetchAgentResolution(resolutionId: string): Promise<AgentResolution> {
  return unwrap<AgentResolution>(
    getAgentResolution({ path: { resolutionId } }),
    '无法读取自动处理状态',
  );
}

export function fetchAgentResolutions(resourceId: string, capability: AgentResolutionCapability): Promise<AgentResolutionPage> {
  return unwrap<AgentResolutionPage>(
    listAgentResolutions({ query: { resourceId, capability, limit: 20 } }),
    '无法读取自动处理状态',
  );
}
