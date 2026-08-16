-- 可丢弃事件的单一事实来源。仅列出已确认由业务表持久化当前状态、
-- 且不被 read model 用作 provenance 的固定 topic；未知或未来 topic 默认保留。
-- 后续迁移如需调整函数，必须同时重建依赖它的 partial index。
CREATE FUNCTION event_is_discardable(event_topic text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
RETURNS NULL ON NULL INPUT
AS $function$
    SELECT event_topic IN (
        'configuration.updated',

        'operation.queued',
        'operation.started',
        'operation.succeeded',
        'operation.retry_scheduled',
        'operation.failed',
        'operation.cancel_requested',
        'operation.cancelled',
        'operation.recovered',

        'download.enqueue_failed',
        'download.sync_failed',
        'download.materialize_failed',
        'download.manifest_persisted',
        'download.enqueued',
        'download.selection_applied',
        'download.progressed',
        'download.completed',
        'download.materialized',
        'download.removed',
        'download.retry_requested',
        'download.cancel_requested',
        'download.removal_requested',
        'download.file_resolution_saved',
        'download.file_selection_saved',
        'download.mapping_recovered',

        'search.created',
        'search.started',
        'search.completed',
        'search.failed',
        'search.cancelled',
        'acquisition.created',
        'acquisition.delete_requested',

        'task.finalizing',
        'task.import_queued',
        'task.cleanup_completed',
        'task.retry_requested',
        'task.cancel_requested',
        'task.media_failed',
        'task.import_failed',
        'task.cleanup_failed',
        'task.cleanup_cancelled',
        'task.import_cancelled',
        'task.media_cancelled',

        'agent.resolution_queued',
        'agent.resolution_failed',
        'agent.resolution_cancelled',
        'rss.adjudication_applied',
        'rss.coordinate_resolved',
        'subtitle.video_match_saved',
        'tmdb.series_synchronized',
        'mapping.profile_saved',
        'rss.mapping_profile_applied',
        'emby.scan_completed',
        'emby.scan_failed',
        'emby.scan_cancelled',

        'rss.entry.ignored',
        'rss.entry.target_occupied',
        'rss.entry.fulfillment_expired',
        'rss.entry.enqueue_failed',
        'rss.mapping_discovery_recorded',
        'rss.polled',
        'rss.poll_failed',
        'rss.poll_completed',
        'rss.subscription.fulfilled',
        'rss.subscription.final_imported',
        'rss.subscription.created',
        'rss.subscription.updated',
        'rss.subscription.archived',
        'rss.subscription.delete_requested',
        'rss.subscription.delete_completed',
        'rss.subscription.delete_partial',
        'rss.subscription.completion_retained'
    )
$function$;

-- 只索引清理候选，永久保留的旧 provenance 不参与周期扫描。
CREATE INDEX events_discardable_occurred_at_sequence_idx
    ON events (occurred_at, event_sequence)
    WHERE event_is_discardable(topic);
