import { unwrap } from '@/api/app-client';
import { listEvents } from '@/api/generated/sdk.gen';
import type { EventPage } from '@/api/generated/types.gen';

export function fetchResourceEvents(resourceType: string, resourceId: string, cursor?: string): Promise<EventPage> {
  return unwrap<EventPage>(
    listEvents({ query: { resourceType, resourceId, cursor, limit: 50 } }),
    '无法读取事件历史',
  );
}
