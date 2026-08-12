import { unwrap } from '@/api/app-client';
import { previewAcquisitionEpisodeMapping, saveAcquisitionEpisodeMapping } from '@/api/generated/sdk.gen';
import type { EpisodeMappingPreview, EpisodeMappingPlanRequest, SavedEpisodeMapping, SaveEpisodeMappingRequest } from '@/api/generated/types.gen';

export function previewMapping(acquisitionId: string, body: EpisodeMappingPlanRequest): Promise<EpisodeMappingPreview> {
  return unwrap<EpisodeMappingPreview>(
    previewAcquisitionEpisodeMapping({ path: { acquisitionId }, body }),
    '预览映射失败',
  );
}

export function saveMapping(acquisitionId: string, key: string, body: SaveEpisodeMappingRequest): Promise<SavedEpisodeMapping> {
  return unwrap<SavedEpisodeMapping>(
    saveAcquisitionEpisodeMapping({ path: { acquisitionId }, headers: { 'Idempotency-Key': key }, body }),
    '保存映射失败',
  );
}
