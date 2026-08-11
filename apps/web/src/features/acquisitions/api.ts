import { ApiFailure, unwrap } from '@/api/app-client';
import { deleteAcquisition, getAcquisition, listAcquisitions } from '@/api/generated/sdk.gen';
import type { Acquisition, AcquisitionPage, AcquisitionStageKey, CommandAccepted } from '@/api/generated/types.gen';

export type AcquisitionPhase = 'mapping_pending' | 'downloading' | 'processing' | 'awaiting_review' | 'importing' | 'completed' | 'attention';
export type AcquisitionSortBy = 'content' | 'source_kind' | 'progress' | 'updated_at';
export type SortOrder = 'asc' | 'desc';

const lifecycleStageKeys: AcquisitionStageKey[] = [
  'source',
  'download',
  'mapping',
  'transcode',
  'subtitle',
  'rename',
  'organize',
  'review',
  'import',
];
const contractMismatchMessage = '后端服务版本与当前页面不匹配，请重启 API 和 Worker 后重试。';

export async function fetchAcquisitions(
  cursor: string | undefined,
  sourceKind: string | '',
  phase?: AcquisitionPhase,
  sortBy?: AcquisitionSortBy,
  sortOrder?: SortOrder,
): Promise<AcquisitionPage> {
  const page = await unwrap<unknown>(
    listAcquisitions({ query: { limit: 50, cursor, sourceKind: sourceKind === '' ? undefined : (sourceKind as 'search' | 'rss' | 'manual'), phase, sortBy, sortOrder } }),
    '无法读取任务',
  );
  if (!isRecord(page) || !Array.isArray(page.items)) {
    throwContractMismatch();
  }
  page.items.forEach(assertAcquisitionLifecycle);
  return page as AcquisitionPage;
}

export async function fetchAcquisition(acquisitionId: string): Promise<Acquisition> {
  const acquisition = await unwrap<unknown>(getAcquisition({ path: { acquisitionId } }), '无法读取任务');
  assertAcquisitionLifecycle(acquisition);
  return acquisition;
}

export function deleteAcquisitionCommand(acquisitionId: string, idempotencyKey: string): Promise<CommandAccepted> {
  return unwrap<CommandAccepted>(
    deleteAcquisition({ path: { acquisitionId }, headers: { 'Idempotency-Key': idempotencyKey } }),
    '删除任务失败',
  );
}

function assertAcquisitionLifecycle(value: unknown): asserts value is Acquisition {
  if (!isRecord(value)) {
    throwContractMismatch();
  }
  const stages = value.stages;
  const mapping = value.mapping;
  if (
    !Array.isArray(value.tasks)
    || !Array.isArray(stages)
    || stages.length !== lifecycleStageKeys.length
    || !stages.every((stage, index) => isRecord(stage) && stage.key === lifecycleStageKeys[index])
    || !isRecord(mapping)
    || typeof value.currentStage !== 'string'
    || typeof value.overallProgress !== 'number'
  ) {
    throwContractMismatch();
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function throwContractMismatch(): never {
  throw new ApiFailure(
    { code: 'api_contract_mismatch', message: contractMismatchMessage, details: {} },
    contractMismatchMessage,
  );
}
