import { unwrap } from '@/api/app-client';
import { createAcquisition, createSearch, getSearch, listRecentSearchCandidates, listSearches } from '@/api/generated/sdk.gen';
import type {
  AcquisitionCommandAccepted,
  CreateAcquisitionRequest,
  ReleaseCandidate,
  ReleaseCandidatePage,
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

export function fetchRecentSearches(): Promise<SearchRunPage> {
  return unwrap<SearchRunPage>(listSearches({ query: { limit: 5 } }), '无法读取最近搜索');
}

export function startSearch(key: string, query: string): Promise<SearchCommandAccepted> {
  return unwrap<SearchCommandAccepted>(createSearch({ headers: { 'Idempotency-Key': key }, body: { query } }), '创建搜索失败');
}

export function selectCandidate(key: string, body: CreateAcquisitionRequest): Promise<AcquisitionCommandAccepted> {
  return unwrap<AcquisitionCommandAccepted>(createAcquisition({ headers: { 'Idempotency-Key': key }, body }), '创建获取失败');
}

export function fetchRecentCandidates(limit = 5): Promise<ReleaseCandidatePage> {
  const bounded = Math.min(5, Math.max(1, limit));
  return unwrap<ReleaseCandidatePage>(listRecentSearchCandidates({ query: { limit: bounded } }), '无法读取最近搜索');
}

export function fetchRecentCandidateItems(limit = 5): Promise<ReleaseCandidate[]> {
  return fetchRecentCandidates(limit).then((page) => page.items);
}
