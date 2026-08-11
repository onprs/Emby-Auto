import type { Acquisition } from '@/api/generated/types.gen';
import { deleteAcquisitionCommand } from '@/features/acquisitions/api';
import { cancelTaskCommand, fetchTask, retryTaskCommand } from '@/features/tasks/api';
import { cancelDownloadCommand, fetchDownload, retryDownloadCommand } from '@/features/downloads/api';

/**
 * Lifecycle helpers for an acquisition (one content item with a download and
 * zero or more episode tasks). Deletion is one backend-owned acquisition
 * command, keeping torrent, file and reference safety behind a single Worker
 * boundary. Imported Emby library content is never part of its cleanup inventory.
 */

const ACTIVE_TASK_STATES = new Set(['media_queued', 'processing', 'finalizing', 'awaiting_review', 'approved', 'import_queued', 'importing']);
const ACTIVE_DOWNLOAD_STATES = new Set(['enqueue_pending', 'downloading', 'selecting_files']);

export interface AcquisitionActionResult {
  ok: boolean;
  operationId?: string;
  error?: string;
}

function toErrorMessage(cause: unknown): string {
  return cause instanceof Error ? cause.message : '操作失败';
}

export function acquisitionHasActiveWork(item: Acquisition): boolean {
  if (item.download && ACTIVE_DOWNLOAD_STATES.has(item.download.status)) {
    return true;
  }
  return item.tasks.some((task) => ACTIVE_TASK_STATES.has(task.state));
}

export function acquisitionRetryableTasks(item: Acquisition) {
  return item.tasks.filter((task) => task.state === 'failed' && task.failureStage);
}

/** Workflow deletion never includes imported Emby destination paths. */
export function acquisitionCanDelete(_item: Acquisition): boolean {
  return true;
}

/** Retries the failed download, or every failed media task after download. */
export async function retryAcquisition(item: Acquisition, key: string): Promise<AcquisitionActionResult> {
  if (item.download?.status === 'failed' && item.download.failureStage) {
    try {
      const download = await fetchDownload(item.download.id);
      await retryDownloadCommand(download.id, `${key}:download`, download.version);
      return { ok: true };
    } catch (cause) {
      return { ok: false, error: toErrorMessage(cause) };
    }
  }
  return retryAcquisitionTasks(item, key);
}

/** Retries every failed (recoverable) media task of the acquisition. */
export async function retryAcquisitionTasks(item: Acquisition, key: string): Promise<AcquisitionActionResult> {
  const retryable = acquisitionRetryableTasks(item);
  if (retryable.length === 0) {
    return { ok: false, error: '没有可重试的任务' };
  }
  for (const summary of retryable) {
    try {
      const task = await fetchTask(summary.id);
      await retryTaskCommand(summary.id, `${key}:${summary.id}`, task.version);
    } catch (cause) {
      return { ok: false, error: toErrorMessage(cause) };
    }
  }
  return { ok: true };
}

/** Cancels the download and every active task, without removing files. */
export async function cancelAcquisitionWork(item: Acquisition, key: string): Promise<AcquisitionActionResult> {
  try {
    if (item.downloadId && item.download && ACTIVE_DOWNLOAD_STATES.has(item.download.status)) {
      const download = await fetchDownload(item.downloadId);
      await cancelDownloadCommand(item.downloadId, `${key}:dl-cancel`, download.version);
    }
    for (const summary of item.tasks.filter((task) => ACTIVE_TASK_STATES.has(task.state))) {
      const task = await fetchTask(summary.id);
      await cancelTaskCommand(summary.id, `${key}:task-cancel:${summary.id}`, task.version);
    }
    return { ok: true };
  } catch (cause) {
    return { ok: false, error: toErrorMessage(cause) };
  }
}

/** Submits one backend-owned cleanup for the complete lifecycle task. */
export async function deleteAcquisition(item: Acquisition, key: string): Promise<AcquisitionActionResult> {
  try {
    const result = await deleteAcquisitionCommand(item.id, key);
    return { ok: true, operationId: result.operationId };
  } catch (cause) {
    return { ok: false, error: toErrorMessage(cause) };
  }
}
