import { unwrap } from '@/api/app-client';
import { getOperation, listOperations } from '@/api/generated/sdk.gen';
import type { Operation, OperationPage } from '@/api/generated/types.gen';

export function fetchOperations(cursor: string | undefined, filters: { resourceType?: string; resourceId?: string; status?: string }): Promise<OperationPage> {
  return unwrap<OperationPage>(
    listOperations({
      query: {
        limit: 20,
        cursor,
        resourceType: filters.resourceType || undefined,
        resourceId: filters.resourceId || undefined,
        status: (filters.status || undefined) as 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | undefined,
      },
    }),
    '无法读取运行记录',
  );
}

export function fetchOperation(operationId: string): Promise<Operation> {
  return unwrap<Operation>(getOperation({ path: { operationId } }), '无法读取运行记录');
}
