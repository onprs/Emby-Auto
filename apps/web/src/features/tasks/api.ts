import { unwrap } from '@/api/app-client';
import { cancelTask, getTask, importTask, listTasks, retryTask, reviewTask } from '@/api/generated/sdk.gen';
import type { Task, TaskCommandAccepted, TaskPage, TaskState } from '@/api/generated/types.gen';

export function fetchTasks(
  cursor: string | undefined,
  state: TaskState | '',
  phase?: 'processing' | 'awaiting_review' | 'importing' | 'failed' | 'cleanup_failed',
): Promise<TaskPage> {
  return unwrap<TaskPage>(
    listTasks({ query: { limit: 50, cursor, state: state === '' ? undefined : state, phase } }),
    '无法读取任务',
  );
}

export function fetchTask(taskId: string): Promise<Task> {
  return unwrap<Task>(getTask({ path: { taskId } }), '无法读取任务');
}

export function reviewTaskCommand(taskId: string, key: string, expectedVersion: number, decision: 'approved' | 'rejected', notes: string) {
  return unwrap<Task>(reviewTask({ path: { taskId }, headers: { 'Idempotency-Key': key }, body: { expectedVersion, decision, notes } }), '审核失败');
}

export function importTaskCommand(taskId: string, key: string, expectedVersion: number): Promise<TaskCommandAccepted> {
  return unwrap<TaskCommandAccepted>(importTask({ path: { taskId }, headers: { 'Idempotency-Key': key }, body: { expectedVersion } }), '入库失败');
}

export function retryTaskCommand(taskId: string, key: string, expectedVersion: number): Promise<TaskCommandAccepted> {
  return unwrap<TaskCommandAccepted>(retryTask({ path: { taskId }, headers: { 'Idempotency-Key': key }, body: { expectedVersion } }), '重试失败');
}

export function cancelTaskCommand(taskId: string, key: string, expectedVersion: number): Promise<TaskCommandAccepted> {
  return unwrap<TaskCommandAccepted>(cancelTask({ path: { taskId }, headers: { 'Idempotency-Key': key }, body: { expectedVersion } }), '取消失败');
}
