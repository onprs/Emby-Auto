import { ApiFailure, unwrap } from '@/api/app-client';
import {
  createRssSubscription,
  deleteRssSubscription,
  getRssSubscription,
  listRssEntries,
  listRssSubscriptions,
  lookupRssFeed,
  pollRssSubscription,
  updateRssSubscription,
} from '@/api/generated/sdk.gen';
import type {
  CommandAccepted,
  CreateRssSubscriptionRequest,
  RssFeedLookup,
  RssSubscription,
  RssSubscriptionPage,
  RssEntryPage,
  UpdateRssSubscriptionRequest,
} from '@/api/generated/types.gen';

export type RssSubscriptionSortBy = 'name' | 'series_title' | 'source_season' | 'enabled' | 'progress' | 'next_poll_at';
export type RssEntrySortBy = 'title' | 'episode' | 'progress' | 'discovered_at';
export type RssEntryGroup = 'confirmed' | 'skipped';
export type SortOrder = 'asc' | 'desc';

const contractMismatchMessage = '后端服务版本与当前页面不匹配，请重启 API 和 Worker 后重试。';

export async function fetchSubscriptions(cursor: string | undefined, query?: string, sortBy?: RssSubscriptionSortBy, sortOrder?: SortOrder): Promise<RssSubscriptionPage> {
  const page = await unwrap<unknown>(listRssSubscriptions({ query: { limit: 50, cursor, query, sortBy, sortOrder } }), '无法读取 RSS 订阅');
  if (!isRecord(page) || !Array.isArray(page.items)) {
    throwContractMismatch();
  }
  page.items.forEach(assertSubscriptionProgress);
  return page as RssSubscriptionPage;
}

export async function fetchSubscription(subscriptionId: string): Promise<RssSubscription> {
  const subscription = await unwrap<unknown>(getRssSubscription({ path: { subscriptionId } }), '无法读取 RSS 订阅');
  assertSubscriptionProgress(subscription);
  return subscription;
}

export async function fetchEntries(
  subscriptionId: string,
  cursor: string | undefined,
  status?: 'discovered' | 'enqueueing' | 'enqueued' | 'enqueue_failed',
  group?: RssEntryGroup,
  query?: string,
  rejectReason?: string,
  sortBy?: RssEntrySortBy,
  sortOrder?: SortOrder,
): Promise<RssEntryPage> {
  const page = await unwrap<unknown>(listRssEntries({ path: { subscriptionId }, query: { limit: 50, cursor, status, group, query, rejectReason, sortBy, sortOrder } }), '无法读取 RSS 条目');
  if (!isRecord(page) || !Array.isArray(page.items)) {
    throwContractMismatch();
  }
  page.items.forEach(assertEntryProgress);
  return page as RssEntryPage;
}

export function createSubscription(body: CreateRssSubscriptionRequest): Promise<RssSubscription> {
  return unwrap<RssSubscription>(createRssSubscription({ body }), '创建订阅失败');
}

export function lookupFeed(feedUrl: string): Promise<RssFeedLookup> {
  return unwrap<RssFeedLookup>(lookupRssFeed({ body: { feedUrl } }), '无法识别 RSS 内容');
}

export function updateSubscription(subscriptionId: string, body: UpdateRssSubscriptionRequest): Promise<RssSubscription> {
  return unwrap<RssSubscription>(updateRssSubscription({ path: { subscriptionId }, body }), '更新订阅失败');
}

export function archiveSubscription(subscriptionId: string, expectedVersion: number, idempotencyKey: string, deleteImported = false): Promise<CommandAccepted> {
  return unwrap<CommandAccepted>(
    deleteRssSubscription({ path: { subscriptionId }, query: { expectedVersion, deleteImported }, headers: { 'Idempotency-Key': idempotencyKey } }),
    '删除订阅失败',
  );
}

export function pollSubscription(subscriptionId: string, key: string): Promise<CommandAccepted> {
  return unwrap<CommandAccepted>(pollRssSubscription({ path: { subscriptionId }, headers: { 'Idempotency-Key': key } }), '触发轮询失败');
}

function assertSubscriptionProgress(value: unknown): asserts value is RssSubscription {
  if (
    !isRecord(value)
    || !Array.isArray(value.includeKeywords)
    || !value.includeKeywords.every((keyword) => typeof keyword === 'string')
    || !Array.isArray(value.excludeKeywords)
    || !value.excludeKeywords.every((keyword) => typeof keyword === 'string')
    || typeof value.autoEpisodeMapping !== 'boolean'
    || typeof value.autoReview !== 'boolean'
    || typeof value.cleanupSourceOnCompletion !== 'boolean'
    || (value.completedAt != null && typeof value.completedAt !== 'string')
    || !validProgress(value.overallProgress)
    || !validCount(value.taskCount)
    || !validCount(value.completedTaskCount)
    || !validCount(value.attentionTaskCount)
  ) {
    throwContractMismatch();
  }
}

function assertEntryProgress(value: unknown): void {
  if (!isRecord(value)) {
    throwContractMismatch();
  }
  if (typeof value.acquisitionId !== 'string') {
    return;
  }
  const progress = value.acquisitionProgress;
  if (
    !isRecord(progress)
    || typeof progress.aggregateStatus !== 'string'
    || typeof progress.currentStage !== 'string'
    || !validProgress(progress.overallProgress)
  ) {
    throwContractMismatch();
  }
}

function validProgress(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1;
}

function validCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0;
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
