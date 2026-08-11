import { useQuery } from '@tanstack/react-query';

import { ContextLink } from '@/components/context-link';
import { DataTable } from '@/components/resource';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { fetchResourceEvents } from '@/features/events/api';
import { formatDateTime } from '@/lib/format';
import { sanitizeTechnicalDetails } from '@/lib/sanitize';

export function EventHistory({ resourceType, resourceId }: { resourceType: string; resourceId: string }) {
  const events = useQuery({
    queryKey: ['events', resourceType, resourceId],
    queryFn: () => fetchResourceEvents(resourceType, resourceId),
  });

  return (
    <Card>
      <CardHeader><CardTitle>事件历史</CardTitle></CardHeader>
      <CardContent>
        {events.isPending ? (
          <LoadingState label="正在读取事件" />
        ) : events.error ? (
          <ErrorState message={events.error.message} onRetry={() => events.refetch()} />
        ) : events.data.items.length === 0 ? (
          <EmptyState title="暂无事件" />
        ) : (
          <DataTable head={['事件', '操作', '时间', '数据']}>
            {events.data.items.map((event) => {
              const eventData = event.data ? sanitizeTechnicalDetails(JSON.stringify(event.data)) : '—';
              return (
              <tr key={event.id}>
                <td className="px-4 py-3 font-medium text-zinc-900">{event.topic}</td>
                <td className="px-4 py-3 text-zinc-600">
                  {event.operationId ? <ContextLink to="/operations/$operationId" params={{ operationId: event.operationId }} className="hover:underline">查看</ContextLink> : '—'}
                </td>
                <td className="px-4 py-3 text-zinc-600">{formatDateTime(event.occurredAt)}</td>
                <td className="max-w-72 truncate px-4 py-3 font-mono text-xs text-zinc-600" title={eventData === '—' ? '' : eventData}>
                  {eventData}
                </td>
              </tr>
              );
            })}
          </DataTable>
        )}
      </CardContent>
    </Card>
  );
}
