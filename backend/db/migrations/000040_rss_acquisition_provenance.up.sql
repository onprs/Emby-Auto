-- RSS acquisition 的持久 provenance 只保留每个 acquisition 的当前成功事实
-- 和一次进行中的重试状态；事件仍可作为通知与有限期审计记录。
CREATE TABLE rss_acquisition_provenance (
    acquisition_id uuid PRIMARY KEY,
    rss_entry_id uuid NOT NULL REFERENCES rss_entries (id) ON DELETE CASCADE,
    download_id uuid,
    task_id uuid,
    acquisition_created_at timestamptz,
    task_created_at timestamptz,
    video_ready_at timestamptz,
    subtitle_ready_at timestamptz,
    artifact_ready_at timestamptz,
    reviewed_at timestamptz,
    imported_at timestamptz,
    archived_at timestamptz,
    pending_download_id uuid,
    pending_enqueue_at timestamptz,
    pending_task_id uuid,
    pending_task_created_at timestamptz,
    pending_video_ready_at timestamptz,
    pending_subtitle_ready_at timestamptz,
    pending_artifact_ready_at timestamptz,
    pending_reviewed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rss_acquisition_provenance_pending_task_requires_download CHECK (
        pending_task_id IS NULL OR pending_download_id IS NOT NULL
    ),
    CONSTRAINT rss_acquisition_provenance_import_requires_task CHECK (
        imported_at IS NULL OR (download_id IS NOT NULL AND task_id IS NOT NULL)
    )
);

CREATE INDEX rss_acquisition_provenance_entry_idx
    ON rss_acquisition_provenance (rss_entry_id);

CREATE INDEX rss_acquisition_provenance_pending_task_idx
    ON rss_acquisition_provenance (pending_task_id)
    WHERE pending_task_id IS NOT NULL;

-- 这些列不引用已清理的 download/task 行；rss_entry 生命周期负责清理一行
-- acquisition provenance，因而同一业务实体的重复事件只覆盖已有状态。
CREATE FUNCTION sync_rss_acquisition_provenance_from_event()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
    event_acquisition_id uuid;
    event_download_id uuid;
BEGIN
    IF NEW.topic = 'rss.entry.enqueueing'
       AND NEW.resource_type = 'rss_entry'
       AND NEW.resource_id IS NOT NULL
       AND NEW.data ? 'acquisitionId'
       AND NEW.data ? 'downloadId' THEN
        event_acquisition_id := (NEW.data->>'acquisitionId')::uuid;
        event_download_id := (NEW.data->>'downloadId')::uuid;

        INSERT INTO rss_acquisition_provenance (
            acquisition_id,
            rss_entry_id,
            pending_download_id,
            pending_enqueue_at,
            updated_at
        ) VALUES (
            event_acquisition_id,
            NEW.resource_id,
            event_download_id,
            NEW.occurred_at,
            clock_timestamp()
        )
        ON CONFLICT (acquisition_id) DO UPDATE
        SET pending_download_id = EXCLUDED.pending_download_id,
            pending_enqueue_at = CASE
                WHEN rss_acquisition_provenance.pending_download_id IS DISTINCT FROM EXCLUDED.pending_download_id
                    THEN EXCLUDED.pending_enqueue_at
                ELSE LEAST(rss_acquisition_provenance.pending_enqueue_at, EXCLUDED.pending_enqueue_at)
            END,
            pending_task_id = CASE
                WHEN rss_acquisition_provenance.pending_download_id IS DISTINCT FROM EXCLUDED.pending_download_id
                    THEN NULL
                ELSE rss_acquisition_provenance.pending_task_id
            END,
            pending_task_created_at = CASE
                WHEN rss_acquisition_provenance.pending_download_id IS DISTINCT FROM EXCLUDED.pending_download_id
                    THEN NULL
                ELSE rss_acquisition_provenance.pending_task_created_at
            END,
            pending_video_ready_at = CASE
                WHEN rss_acquisition_provenance.pending_download_id IS DISTINCT FROM EXCLUDED.pending_download_id
                    THEN NULL
                ELSE rss_acquisition_provenance.pending_video_ready_at
            END,
            pending_subtitle_ready_at = CASE
                WHEN rss_acquisition_provenance.pending_download_id IS DISTINCT FROM EXCLUDED.pending_download_id
                    THEN NULL
                ELSE rss_acquisition_provenance.pending_subtitle_ready_at
            END,
            pending_artifact_ready_at = CASE
                WHEN rss_acquisition_provenance.pending_download_id IS DISTINCT FROM EXCLUDED.pending_download_id
                    THEN NULL
                ELSE rss_acquisition_provenance.pending_artifact_ready_at
            END,
            pending_reviewed_at = CASE
                WHEN rss_acquisition_provenance.pending_download_id IS DISTINCT FROM EXCLUDED.pending_download_id
                    THEN NULL
                ELSE rss_acquisition_provenance.pending_reviewed_at
            END,
            updated_at = clock_timestamp();
        RETURN NEW;
    END IF;

    IF NEW.topic = 'task.created'
       AND NEW.resource_type = 'episode_task'
       AND NEW.resource_id IS NOT NULL
       AND NEW.data ? 'downloadId' THEN
        event_download_id := (NEW.data->>'downloadId')::uuid;
        UPDATE rss_acquisition_provenance AS provenance
        SET pending_task_id = NEW.resource_id,
            pending_task_created_at = CASE
                WHEN provenance.pending_task_id IS DISTINCT FROM NEW.resource_id
                    THEN NEW.occurred_at
                ELSE LEAST(provenance.pending_task_created_at, NEW.occurred_at)
            END,
            pending_video_ready_at = CASE
                WHEN provenance.pending_task_id IS DISTINCT FROM NEW.resource_id
                    THEN NULL
                ELSE provenance.pending_video_ready_at
            END,
            pending_subtitle_ready_at = CASE
                WHEN provenance.pending_task_id IS DISTINCT FROM NEW.resource_id
                    THEN NULL
                ELSE provenance.pending_subtitle_ready_at
            END,
            pending_artifact_ready_at = CASE
                WHEN provenance.pending_task_id IS DISTINCT FROM NEW.resource_id
                    THEN NULL
                ELSE provenance.pending_artifact_ready_at
            END,
            pending_reviewed_at = CASE
                WHEN provenance.pending_task_id IS DISTINCT FROM NEW.resource_id
                    THEN NULL
                ELSE provenance.pending_reviewed_at
            END,
            updated_at = clock_timestamp()
        WHERE provenance.pending_download_id = event_download_id;
        RETURN NEW;
    END IF;

    IF NEW.resource_type = 'episode_task'
       AND NEW.resource_id IS NOT NULL
       AND NEW.topic IN ('task.video_ready', 'task.subtitle_ready', 'task.awaiting_review', 'task.reviewed') THEN
        IF NEW.topic = 'task.video_ready' THEN
            UPDATE rss_acquisition_provenance AS provenance
            SET pending_video_ready_at = CASE
                    WHEN provenance.pending_task_id = NEW.resource_id
                        THEN GREATEST(COALESCE(provenance.pending_video_ready_at, NEW.occurred_at), NEW.occurred_at)
                    ELSE provenance.pending_video_ready_at
                END,
                video_ready_at = CASE
                    WHEN provenance.task_id = NEW.resource_id
                        THEN GREATEST(COALESCE(provenance.video_ready_at, NEW.occurred_at), NEW.occurred_at)
                    ELSE provenance.video_ready_at
                END,
                updated_at = clock_timestamp()
            WHERE provenance.pending_task_id = NEW.resource_id OR provenance.task_id = NEW.resource_id;
        ELSIF NEW.topic = 'task.subtitle_ready' THEN
            UPDATE rss_acquisition_provenance AS provenance
            SET pending_subtitle_ready_at = CASE
                    WHEN provenance.pending_task_id = NEW.resource_id
                        THEN GREATEST(COALESCE(provenance.pending_subtitle_ready_at, NEW.occurred_at), NEW.occurred_at)
                    ELSE provenance.pending_subtitle_ready_at
                END,
                subtitle_ready_at = CASE
                    WHEN provenance.task_id = NEW.resource_id
                        THEN GREATEST(COALESCE(provenance.subtitle_ready_at, NEW.occurred_at), NEW.occurred_at)
                    ELSE provenance.subtitle_ready_at
                END,
                updated_at = clock_timestamp()
            WHERE provenance.pending_task_id = NEW.resource_id OR provenance.task_id = NEW.resource_id;
        ELSIF NEW.topic = 'task.awaiting_review' THEN
            UPDATE rss_acquisition_provenance AS provenance
            SET pending_artifact_ready_at = CASE
                    WHEN provenance.pending_task_id = NEW.resource_id
                        THEN GREATEST(COALESCE(provenance.pending_artifact_ready_at, NEW.occurred_at), NEW.occurred_at)
                    ELSE provenance.pending_artifact_ready_at
                END,
                artifact_ready_at = CASE
                    WHEN provenance.task_id = NEW.resource_id
                        THEN GREATEST(COALESCE(provenance.artifact_ready_at, NEW.occurred_at), NEW.occurred_at)
                    ELSE provenance.artifact_ready_at
                END,
                updated_at = clock_timestamp()
            WHERE provenance.pending_task_id = NEW.resource_id OR provenance.task_id = NEW.resource_id;
        ELSE
            UPDATE rss_acquisition_provenance AS provenance
            SET pending_reviewed_at = CASE
                    WHEN provenance.pending_task_id = NEW.resource_id
                        THEN GREATEST(COALESCE(provenance.pending_reviewed_at, NEW.occurred_at), NEW.occurred_at)
                    ELSE provenance.pending_reviewed_at
                END,
                reviewed_at = CASE
                    WHEN provenance.task_id = NEW.resource_id
                        THEN GREATEST(COALESCE(provenance.reviewed_at, NEW.occurred_at), NEW.occurred_at)
                    ELSE provenance.reviewed_at
                END,
                updated_at = clock_timestamp()
            WHERE provenance.pending_task_id = NEW.resource_id OR provenance.task_id = NEW.resource_id;
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.topic = 'task.imported'
       AND NEW.resource_type = 'episode_task'
       AND NEW.resource_id IS NOT NULL THEN
        UPDATE rss_acquisition_provenance AS provenance
        SET download_id = CASE
                WHEN provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_download_id
                ELSE provenance.download_id
            END,
            task_id = CASE
                WHEN provenance.pending_task_id = NEW.resource_id
                    THEN NEW.resource_id
                ELSE provenance.task_id
            END,
            acquisition_created_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_enqueue_at
                ELSE provenance.acquisition_created_at
            END,
            task_created_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_task_created_at
                ELSE provenance.task_created_at
            END,
            video_ready_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_video_ready_at
                ELSE provenance.video_ready_at
            END,
            subtitle_ready_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_subtitle_ready_at
                ELSE provenance.subtitle_ready_at
            END,
            artifact_ready_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_artifact_ready_at
                ELSE provenance.artifact_ready_at
            END,
            reviewed_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_reviewed_at
                ELSE provenance.reviewed_at
            END,
            imported_at = GREATEST(COALESCE(provenance.imported_at, NEW.occurred_at), NEW.occurred_at),
            updated_at = clock_timestamp()
        WHERE provenance.pending_task_id = NEW.resource_id OR provenance.task_id = NEW.resource_id;
        RETURN NEW;
    END IF;

    IF NEW.topic = 'acquisition.delete_completed'
       AND NEW.resource_type = 'acquisition'
       AND NEW.resource_id IS NOT NULL THEN
        UPDATE rss_acquisition_provenance AS provenance
        SET archived_at = GREATEST(COALESCE(provenance.archived_at, NEW.occurred_at), NEW.occurred_at),
            updated_at = clock_timestamp()
        WHERE provenance.acquisition_id = NEW.resource_id;
        RETURN NEW;
    END IF;

    RETURN NEW;
END
$function$;

CREATE TRIGGER rss_acquisition_provenance_event_sync
    AFTER INSERT ON events
    FOR EACH ROW EXECUTE FUNCTION sync_rss_acquisition_provenance_from_event();

-- Backfill current and historical successful facts from migration 39 events.
-- Invalid payloads are ignored so unrelated legacy events cannot block upgrade;
-- application-generated provenance payloads use the validated UUID shape below.
WITH enqueue_events AS (
    SELECT
        event.resource_id AS rss_entry_id,
        (event.data->>'acquisitionId')::uuid AS acquisition_id,
        (event.data->>'downloadId')::uuid AS download_id,
        event.occurred_at,
        event.event_sequence
    FROM events AS event
    WHERE event.resource_type = 'rss_entry'
      AND event.resource_id IS NOT NULL
      AND event.topic = 'rss.entry.enqueueing'
      AND event.data->>'acquisitionId' ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
      AND event.data->>'downloadId' ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
), latest_enqueue AS (
    SELECT DISTINCT ON (acquisition_id)
        enqueue.*
    FROM enqueue_events AS enqueue
    ORDER BY acquisition_id, occurred_at DESC, event_sequence DESC
), task_created_events AS (
    SELECT
        event.resource_id AS task_id,
        (event.data->>'downloadId')::uuid AS download_id,
        event.occurred_at AS task_created_at
    FROM events AS event
    WHERE event.resource_type = 'episode_task'
      AND event.resource_id IS NOT NULL
      AND event.topic = 'task.created'
      AND event.data->>'downloadId' ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
), task_milestones AS (
    SELECT
        event.resource_id AS task_id,
        max(event.occurred_at) FILTER (WHERE event.topic = 'task.video_ready') AS video_ready_at,
        max(event.occurred_at) FILTER (WHERE event.topic = 'task.subtitle_ready') AS subtitle_ready_at,
        max(event.occurred_at) FILTER (WHERE event.topic = 'task.awaiting_review') AS artifact_ready_at,
        max(event.occurred_at) FILTER (WHERE event.topic = 'task.reviewed') AS reviewed_at
    FROM events AS event
    WHERE event.resource_type = 'episode_task'
      AND event.resource_id IS NOT NULL
      AND event.topic IN ('task.video_ready', 'task.subtitle_ready', 'task.awaiting_review', 'task.reviewed')
    GROUP BY event.resource_id
), task_imported_events AS (
    SELECT
        event.resource_id AS task_id,
        max(event.occurred_at) AS imported_at
    FROM events AS event
    WHERE event.resource_type = 'episode_task'
      AND event.resource_id IS NOT NULL
      AND event.topic = 'task.imported'
    GROUP BY event.resource_id
), successful_history AS (
    SELECT DISTINCT ON (enqueue.acquisition_id)
        enqueue.acquisition_id,
        enqueue.download_id,
        enqueue.occurred_at AS acquisition_created_at,
        created.task_id,
        created.task_created_at,
        milestones.video_ready_at,
        milestones.subtitle_ready_at,
        milestones.artifact_ready_at,
        milestones.reviewed_at,
        imported.imported_at
    FROM enqueue_events AS enqueue
    JOIN task_created_events AS created ON created.download_id = enqueue.download_id
    JOIN task_imported_events AS imported ON imported.task_id = created.task_id
    LEFT JOIN task_milestones AS milestones ON milestones.task_id = created.task_id
    ORDER BY enqueue.acquisition_id, imported.imported_at DESC, enqueue.occurred_at DESC, created.task_id
), pending_tasks AS (
    SELECT DISTINCT ON (enqueue.acquisition_id)
        enqueue.acquisition_id,
        created.task_id,
        created.task_created_at,
        milestones.video_ready_at,
        milestones.subtitle_ready_at,
        milestones.artifact_ready_at,
        milestones.reviewed_at
    FROM latest_enqueue AS enqueue
    JOIN task_created_events AS created ON created.download_id = enqueue.download_id
    LEFT JOIN task_milestones AS milestones ON milestones.task_id = created.task_id
    ORDER BY enqueue.acquisition_id, created.task_created_at DESC, created.task_id
), archived AS (
    SELECT
        event.resource_id AS acquisition_id,
        max(event.occurred_at) AS archived_at
    FROM events AS event
    WHERE event.resource_type = 'acquisition'
      AND event.resource_id IS NOT NULL
      AND event.topic = 'acquisition.delete_completed'
    GROUP BY event.resource_id
)
INSERT INTO rss_acquisition_provenance (
    acquisition_id,
    rss_entry_id,
    download_id,
    task_id,
    acquisition_created_at,
    task_created_at,
    video_ready_at,
    subtitle_ready_at,
    artifact_ready_at,
    reviewed_at,
    imported_at,
    archived_at,
    pending_download_id,
    pending_enqueue_at,
    pending_task_id,
    pending_task_created_at,
    pending_video_ready_at,
    pending_subtitle_ready_at,
    pending_artifact_ready_at,
    pending_reviewed_at
)
SELECT
    latest.acquisition_id,
    latest.rss_entry_id,
    successful.download_id,
    successful.task_id,
    successful.acquisition_created_at,
    successful.task_created_at,
    successful.video_ready_at,
    successful.subtitle_ready_at,
    successful.artifact_ready_at,
    successful.reviewed_at,
    successful.imported_at,
    archived.archived_at,
    latest.download_id,
    latest.occurred_at,
    pending.task_id,
    pending.task_created_at,
    pending.video_ready_at,
    pending.subtitle_ready_at,
    pending.artifact_ready_at,
    pending.reviewed_at
FROM latest_enqueue AS latest
JOIN rss_entries AS current_entry ON current_entry.id = latest.rss_entry_id
LEFT JOIN successful_history AS successful ON successful.acquisition_id = latest.acquisition_id
LEFT JOIN pending_tasks AS pending ON pending.acquisition_id = latest.acquisition_id
LEFT JOIN archived ON archived.acquisition_id = latest.acquisition_id
ON CONFLICT (acquisition_id) DO UPDATE
SET rss_entry_id = EXCLUDED.rss_entry_id,
    download_id = EXCLUDED.download_id,
    task_id = EXCLUDED.task_id,
    acquisition_created_at = EXCLUDED.acquisition_created_at,
    task_created_at = EXCLUDED.task_created_at,
    video_ready_at = EXCLUDED.video_ready_at,
    subtitle_ready_at = EXCLUDED.subtitle_ready_at,
    artifact_ready_at = EXCLUDED.artifact_ready_at,
    reviewed_at = EXCLUDED.reviewed_at,
    imported_at = EXCLUDED.imported_at,
    archived_at = EXCLUDED.archived_at,
    pending_download_id = EXCLUDED.pending_download_id,
    pending_enqueue_at = EXCLUDED.pending_enqueue_at,
    pending_task_id = EXCLUDED.pending_task_id,
    pending_task_created_at = EXCLUDED.pending_task_created_at,
    pending_video_ready_at = EXCLUDED.pending_video_ready_at,
    pending_subtitle_ready_at = EXCLUDED.pending_subtitle_ready_at,
    pending_artifact_ready_at = EXCLUDED.pending_artifact_ready_at,
    pending_reviewed_at = EXCLUDED.pending_reviewed_at,
    updated_at = clock_timestamp();

-- 结构化 provenance 已成为读模型来源；旧 provenance 事件进入与其它
-- 可由业务表恢复事件相同的有限期，未知/未来 topic 仍 fail closed。
DROP INDEX IF EXISTS events_discardable_occurred_at_sequence_idx;

CREATE OR REPLACE FUNCTION event_is_discardable(event_topic text)
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
        'acquisition.delete_completed',

        'task.created',
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
        'task.video_ready',
        'task.subtitle_ready',
        'task.awaiting_review',
        'task.reviewed',
        'task.imported',

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

        'rss.entry.enqueueing',
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

CREATE INDEX events_discardable_occurred_at_sequence_idx
    ON events (occurred_at, event_sequence)
    WHERE event_is_discardable(topic);
