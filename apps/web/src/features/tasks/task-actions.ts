import type { Task } from '@/api/generated/types.gen';
import { deleteAcquisitionCommand } from '@/features/acquisitions/api';
import { retryTaskCommand } from '@/features/tasks/api';

/**
 * Task lifecycle helpers shared by the task list, RSS detail, and task detail
 * pages. Deletion reuses the existing cancel + download-remove commands so all
 * torrent, file, and reference safety rules stay on the backend.
 */

export interface TaskActionTarget {
  id: string;
  acquisitionId: string;
  version: number;
  downloadId: string;
  state: string;
}

export interface TaskActionResult {
  taskId: string;
  ok: boolean;
  operationId?: string;
  error?: string;
}

export function canDeleteTask(_task: Pick<Task, 'state' | 'cleanup'>): boolean {
  return true;
}

function toErrorMessage(cause: unknown): string {
  return cause instanceof Error ? cause.message : '操作失败';
}

/** Retries one recoverable task. */
export async function retryTask(task: TaskActionTarget, key: string): Promise<TaskActionResult> {
  try {
    await retryTaskCommand(task.id, key, task.version);
    return { taskId: task.id, ok: true };
  } catch (cause) {
    return { taskId: task.id, ok: false, error: toErrorMessage(cause) };
  }
}

/** Deletes the complete acquisition that owns this diagnostic task. */
export async function deleteTask(task: TaskActionTarget, key: string): Promise<TaskActionResult> {
  try {
    const result = await deleteAcquisitionCommand(task.acquisitionId, key);
    return { taskId: task.id, ok: true, operationId: result.operationId };
  } catch (cause) {
    return { taskId: task.id, ok: false, error: toErrorMessage(cause) };
  }
}

/** Runs one action over many tasks, collecting per-item results. */
export async function runBatch(
  tasks: TaskActionTarget[],
  action: 'retry' | 'delete',
  keyFor: (task: TaskActionTarget) => string,
): Promise<TaskActionResult[]> {
  const results: TaskActionResult[] = [];
  for (const task of tasks) {
    const key = keyFor(task);
    results.push(action === 'retry' ? await retryTask(task, key) : await deleteTask(task, key));
  }
  return results;
}
