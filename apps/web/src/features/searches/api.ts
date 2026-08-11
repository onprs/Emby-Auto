import { unwrap } from '@/api/app-client';
import { createAcquisition, createSearch, getSearch, listSearches } from '@/api/generated/sdk.gen';
import type {
  AcquisitionCommandAccepted,
  CreateAcquisitionRequest,
  SearchCommandAccepted,
  SearchRun,
  SearchRunPage,
} from '@/api/generated/types.gen';

export function fetchSearches(cursor: string | undefined, status: string, query?: string): Promise<SearchRunPage> {
  return unwrap<SearchRunPage>(
    listSearches({
      query: {
        limit: 50,
        cursor,
        status: status === '' ? undefined : (status as 'queued' | 'running' | 'completed' | 'failed' | 'cancelled'),
        query: query || undefined,
      },
    }),
    '无法读取搜索历史',
  );
}

export function fetchSearch(searchId: string): Promise<SearchRun> {
  return unwrap<SearchRun>(getSearch({ path: { searchId } }), '无法读取搜索');
}

export function startSearch(key: string, query: string): Promise<SearchCommandAccepted> {
  return unwrap<SearchCommandAccepted>(createSearch({ headers: { 'Idempotency-Key': key }, body: { query } }), '创建搜索失败');
}

export function selectCandidate(key: string, body: CreateAcquisitionRequest): Promise<AcquisitionCommandAccepted> {
  return unwrap<AcquisitionCommandAccepted>(createAcquisition({ headers: { 'Idempotency-Key': key }, body }), '创建获取失败');
}
