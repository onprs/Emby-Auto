import { unwrap } from '@/api/app-client';
import { cancelDownload, deleteDownload, getDownload, listDownloads, retryDownload, saveDownloadFileResolution, saveDownloadFileSelection } from '@/api/generated/sdk.gen';
import type { CommandAccepted, Download, DownloadCommandAccepted, DownloadFileResolutionItem, DownloadPage, ListDownloadsData } from '@/api/generated/types.gen';

export type DownloadFilters = Omit<NonNullable<ListDownloadsData['query']>, 'cursor' | 'limit'>;

export function fetchDownloads(
  cursor: string | undefined,
  filters: DownloadFilters,
): Promise<DownloadPage> {
  return unwrap<DownloadPage>(listDownloads({ query: { limit: 50, cursor, ...filters } }), '无法读取下载');
}

export function fetchDownload(downloadId: string): Promise<Download> {
  return unwrap<Download>(getDownload({ path: { downloadId } }), '无法读取下载');
}

export function retryDownloadCommand(downloadId: string, key: string, expectedVersion: number): Promise<DownloadCommandAccepted> {
  return unwrap<DownloadCommandAccepted>(retryDownload({ path: { downloadId }, headers: { 'Idempotency-Key': key }, body: { expectedVersion } }), '重试失败');
}

export function cancelDownloadCommand(downloadId: string, key: string, expectedVersion: number): Promise<DownloadCommandAccepted> {
  return unwrap<DownloadCommandAccepted>(cancelDownload({ path: { downloadId }, headers: { 'Idempotency-Key': key }, body: { expectedVersion } }), '取消失败');
}

export function deleteDownloadCommand(downloadId: string, key: string, expectedVersion: number): Promise<CommandAccepted> {
  return unwrap<CommandAccepted>(
    deleteDownload({ path: { downloadId }, query: { expectedVersion }, headers: { 'Idempotency-Key': key } }),
    '删除下载失败',
  );
}

export function saveFileResolutionCommand(
  downloadId: string,
  key: string,
  expectedVersion: number,
  files: DownloadFileResolutionItem[],
): Promise<DownloadCommandAccepted> {
  return unwrap<DownloadCommandAccepted>(
    saveDownloadFileResolution({ path: { downloadId }, headers: { 'Idempotency-Key': key }, body: { expectedVersion, files } }),
    '保存文件解析失败',
  );
}

export function saveFileSelectionCommand(
  downloadId: string,
  key: string,
  expectedVersion: number,
  files: { fileId: string; selected: boolean }[],
): Promise<DownloadCommandAccepted> {
  return unwrap<DownloadCommandAccepted>(
    saveDownloadFileSelection({ path: { downloadId }, headers: { 'Idempotency-Key': key }, body: { expectedVersion, files } }),
    '保存文件选择失败',
  );
}
