import { unwrap } from '@/api/app-client';
import {
  createEmbyScan,
  getEmbyScan,
  listEmbyLibraries,
  listEmbyLibraryItems,
  listEmbyScans,
  refreshEmbyLibrary,
} from '@/api/generated/sdk.gen';
import type {
  EmbyLibrary,
  EmbyLibraryItemPage,
  EmbyScan,
  EmbyScanCommandAccepted,
  EmbyScanPage,
  CommandAccepted,
} from '@/api/generated/types.gen';

export function fetchScans(cursor: string | undefined): Promise<EmbyScanPage> {
  return unwrap<EmbyScanPage>(listEmbyScans({ query: { limit: 50, cursor } }), '无法读取目录更新记录');
}

export function fetchScan(scanId: string): Promise<EmbyScan> {
  return unwrap<EmbyScan>(getEmbyScan({ path: { scanId } }), '无法读取目录更新结果');
}

export function startScan(key: string): Promise<EmbyScanCommandAccepted> {
  return unwrap<EmbyScanCommandAccepted>(createEmbyScan({ headers: { 'Idempotency-Key': key } }), '无法开始从 Emby 更新目录');
}

export function fetchLibraries(): Promise<EmbyLibrary[]> {
  return unwrap<EmbyLibrary[]>(listEmbyLibraries(), '无法读取媒体库');
}

export function refreshEmby(key: string): Promise<CommandAccepted> {
  return unwrap<CommandAccepted>(refreshEmbyLibrary({ headers: { 'Idempotency-Key': key } }), '请求 Emby 扫描文件失败');
}

export function fetchLibraryItems(
  libraryId: string,
  cursor: string | undefined,
  filters: { itemType?: 'Series' | 'Season' | 'Episode' | 'Movie'; name?: string; providerId?: string; present?: boolean },
): Promise<EmbyLibraryItemPage> {
  return unwrap<EmbyLibraryItemPage>(
    listEmbyLibraryItems({
      path: { libraryId },
      query: { limit: 50, cursor, ...filters },
    }),
    '无法读取媒体库条目',
  );
}
