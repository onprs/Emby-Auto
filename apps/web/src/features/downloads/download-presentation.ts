type DownloadPresentationSource = {
  status: string;
  progress: number;
  clientState?: string | null;
  failureStage?: string | null;
  errorCode?: string | null;
};

const mappingMaterializationErrorCodes = new Set([
  'mapping_profile_required',
  'episode_mapping_required',
  'mapping_source_invalid',
  'mapping_source_out_of_range',
  'mapping_context_incomplete',
  'mapping_target_out_of_range',
  'mapping_title_missing',
]);

export function downloadWaitsForMapping(download: DownloadPresentationSource): boolean {
  return download.status === 'failed'
    && download.failureStage === 'materialize'
    && Boolean(download.errorCode && mappingMaterializationErrorCodes.has(download.errorCode));
}

export function downloadTransferCompleted(download: DownloadPresentationSource): boolean {
  return ['completed', 'selecting_files', 'materialized'].includes(download.status)
    || (download.status === 'failed' && download.failureStage === 'materialize' && download.progress >= 1);
}

export function downloadDisplayStatus(download: DownloadPresentationSource): string {
  if (downloadTransferCompleted(download)) return 'completed';
  if (download.status !== 'downloading') return download.status;
  if (download.clientState === 'pausedDL' || download.clientState === 'stoppedDL') return 'download_paused';
  if (download.clientState === 'metaDL' || download.clientState === 'queuedDL' || download.clientState === 'stalledDL') return 'download_waiting';
  return download.status;
}

export function downloadFollowupLabel(download: DownloadPresentationSource): string | null {
  if (download.status !== 'failed' || download.failureStage !== 'materialize') return null;
  return downloadWaitsForMapping(download) ? '等待剧集映射' : '准备媒体处理未完成';
}

export function downloadRetryLabel(download: DownloadPresentationSource): '重试下载' | '重试文件解析' | '重试准备处理' {
  if (download.failureStage === 'file_resolution') return '重试文件解析';
  return download.failureStage === 'materialize' ? '重试准备处理' : '重试下载';
}
