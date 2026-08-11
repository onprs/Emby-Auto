import { describe, expect, it } from 'vitest';

import {
  downloadDisplayStatus,
  downloadFollowupLabel,
  downloadRetryLabel,
  downloadWaitsForMapping,
} from '@/features/downloads/download-presentation';

describe('download presentation', () => {
  it('shows a completed transfer with a mapping follow-up instead of a download failure', () => {
    const download = {
      status: 'failed',
      progress: 1,
      clientState: 'uploading',
      failureStage: 'materialize',
      errorCode: 'mapping_profile_required',
    };

    expect(downloadDisplayStatus(download)).toBe('completed');
    expect(downloadWaitsForMapping(download)).toBe(true);
    expect(downloadFollowupLabel(download)).toBe('等待剧集映射');
    expect(downloadRetryLabel(download)).toBe('重试准备处理');
  });

  it('keeps a real transfer failure classified as a download failure', () => {
    const download = {
      status: 'failed',
      progress: 0.42,
      clientState: 'error',
      failureStage: 'sync',
      errorCode: 'download_storage_unavailable',
    };

    expect(downloadDisplayStatus(download)).toBe('failed');
    expect(downloadWaitsForMapping(download)).toBe(false);
    expect(downloadFollowupLabel(download)).toBeNull();
    expect(downloadRetryLabel(download)).toBe('重试下载');
  });

  it('keeps file resolution pending visible before qBittorrent payload download starts', () => {
    const download = {
      status: 'file_resolution_pending',
      progress: 0,
      clientState: 'metadata_ready',
    };

    expect(downloadDisplayStatus(download)).toBe('file_resolution_pending');
    expect(downloadWaitsForMapping(download)).toBe(false);
    expect(downloadFollowupLabel(download)).toBeNull();
  });

  it('labels file-resolution apply failures separately from transfer retries', () => {
    expect(downloadRetryLabel({ status: 'failed', progress: 0, failureStage: 'file_resolution' })).toBe('重试文件解析');
  });

  it('labels non-mapping materialization errors as media preparation failures', () => {
    const download = {
      status: 'failed',
      progress: 1,
      clientState: 'uploading',
      failureStage: 'materialize',
      errorCode: 'media_storage_unavailable',
    };

    expect(downloadDisplayStatus(download)).toBe('completed');
    expect(downloadWaitsForMapping(download)).toBe(false);
    expect(downloadFollowupLabel(download)).toBe('准备媒体处理未完成');
    expect(downloadRetryLabel(download)).toBe('重试准备处理');
  });
});
