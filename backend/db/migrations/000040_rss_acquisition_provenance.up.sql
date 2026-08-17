-- RSS acquisition 的持久 provenance 只保留每个 acquisition 的当前成功事实
-- 和一次进行中的重试状态；事件仍可作为通知与有限期审计记录。
-- 解析函数只接受稳定的内部 UUID/正整数形状；其它 payload 由 trigger no-op。
CREATE FUNCTION rss_provenance_uuid(value text)
RETURNS uuid
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
RETURNS NULL ON NULL INPUT
AS $function$
    SELECT CASE
        WHEN value ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
            THEN value::uuid
        ELSE NULL
    END
$function$;

CREATE FUNCTION rss_provenance_positive_int(value text)
RETURNS integer
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
RETURNS NULL ON NULL INPUT
AS $function$
    SELECT CASE
        WHEN value ~ '^[1-9][0-9]{0,8}$'
            THEN value::integer
        ELSE NULL
    END
$function$;

CREATE TABLE rss_acquisition_provenance (
    acquisition_id uuid PRIMARY KEY,
    rss_entry_id uuid NOT NULL REFERENCES rss_entries (id) ON DELETE CASCADE,
    download_id uuid,
    download_attempt integer,
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
    pending_download_attempt integer,
    pending_enqueue_at timestamptz,
    pending_enqueue_event_sequence bigint,
    pending_task_id uuid,
    pending_task_created_at timestamptz,
    pending_video_ready_at timestamptz,
    pending_subtitle_ready_at timestamptz,
    pending_artifact_ready_at timestamptz,
    pending_reviewed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rss_acquisition_provenance_download_attempt_positive CHECK (
        download_attempt IS NULL OR download_attempt > 0
    ),
    CONSTRAINT rss_acquisition_provenance_pending_attempt_requires_download CHECK (
        pending_download_attempt IS NULL OR pending_download_id IS NOT NULL
    ),
    CONSTRAINT rss_acquisition_provenance_pending_attempt_positive CHECK (
        pending_download_attempt IS NULL OR pending_download_attempt > 0
    ),
    CONSTRAINT rss_acquisition_provenance_pending_sequence_requires_download CHECK (
        pending_enqueue_event_sequence IS NULL OR pending_download_id IS NOT NULL
    ),
    CONSTRAINT rss_acquisition_provenance_pending_sequence_positive CHECK (
        pending_enqueue_event_sequence IS NULL OR pending_enqueue_event_sequence > 0
    ),
    CONSTRAINT rss_acquisition_provenance_pending_task_requires_download CHECK (
        pending_task_id IS NULL OR pending_download_id IS NOT NULL
    ),
    CONSTRAINT rss_acquisition_provenance_import_requires_task CHECK (
        imported_at IS NULL OR (download_id IS NOT NULL AND task_id IS NOT NULL)
    )
);

CREATE INDEX rss_acquisition_provenance_entry_idx
    ON rss_acquisition_provenance (rss_entry_id);

-- 这些列不引用已清理的 download/task 行；rss_entry 生命周期负责清理一行
-- acquisition provenance，因而同一业务实体的重复事件只覆盖已有状态。
CREATE FUNCTION sync_rss_acquisition_provenance_from_event()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
    event_acquisition_id uuid;
    event_download_id uuid;
    event_download_attempt integer;
    event_task_acquisition_id uuid;
BEGIN
    IF NEW.topic = 'rss.entry.enqueueing'
       AND NEW.resource_type = 'rss_entry'
       AND NEW.resource_id IS NOT NULL THEN
        IF jsonb_typeof(NEW.data -> 'acquisitionId') IS DISTINCT FROM 'string'
           OR jsonb_typeof(NEW.data -> 'downloadId') IS DISTINCT FROM 'string' THEN
            RETURN NEW;
        END IF;
        IF (NEW.data ->> 'acquisitionId') !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
           OR (NEW.data ->> 'downloadId') !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' THEN
            RETURN NEW;
        END IF;

        event_acquisition_id := rss_provenance_uuid(NEW.data ->> 'acquisitionId');
        event_download_id := rss_provenance_uuid(NEW.data ->> 'downloadId');
        IF event_acquisition_id IS NULL OR event_download_id IS NULL THEN
            RETURN NEW;
        END IF;

        -- The live association is authoritative for attempt ordering. A deleted,
        -- mismatched, or not-yet-created download is a stale event, not a reason
        -- to abort the surrounding business transaction.
        SELECT download.attempt
        INTO event_download_attempt
        FROM downloads AS download
        JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
        WHERE download.id = event_download_id
          AND download.deleted_at IS NULL
          AND acquisition.id = event_acquisition_id
          AND acquisition.rss_entry_id = NEW.resource_id;
        IF NOT FOUND THEN
            RETURN NEW;
        END IF;

        INSERT INTO rss_acquisition_provenance (
            acquisition_id,
            rss_entry_id,
            pending_download_id,
            pending_download_attempt,
            pending_enqueue_at,
            pending_enqueue_event_sequence,
            updated_at
        ) VALUES (
            event_acquisition_id,
            NEW.resource_id,
            event_download_id,
            event_download_attempt,
            NEW.occurred_at,
            NEW.event_sequence,
            clock_timestamp()
        )
        ON CONFLICT (acquisition_id) DO UPDATE
        SET pending_download_id = EXCLUDED.pending_download_id,
            pending_download_attempt = EXCLUDED.pending_download_attempt,
            pending_enqueue_at = CASE
                WHEN rss_acquisition_provenance.pending_download_id IS DISTINCT FROM EXCLUDED.pending_download_id
                    THEN EXCLUDED.pending_enqueue_at
                ELSE LEAST(COALESCE(rss_acquisition_provenance.pending_enqueue_at, EXCLUDED.pending_enqueue_at), EXCLUDED.pending_enqueue_at)
            END,
            pending_enqueue_event_sequence = GREATEST(
                COALESCE(rss_acquisition_provenance.pending_enqueue_event_sequence, 0),
                EXCLUDED.pending_enqueue_event_sequence
            ),
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
            updated_at = clock_timestamp()
        WHERE rss_acquisition_provenance.imported_at IS NULL
          AND (
              rss_acquisition_provenance.pending_download_id IS NULL
              OR rss_acquisition_provenance.pending_download_id = EXCLUDED.pending_download_id
              OR rss_acquisition_provenance.pending_download_attempt IS NULL
              OR EXCLUDED.pending_download_attempt > rss_acquisition_provenance.pending_download_attempt
          );
        RETURN NEW;
    END IF;

    IF NEW.topic = 'task.created'
       AND NEW.resource_type = 'episode_task'
       AND NEW.resource_id IS NOT NULL THEN
        IF jsonb_typeof(NEW.data -> 'downloadId') IS DISTINCT FROM 'string' THEN
            RETURN NEW;
        END IF;
        IF (NEW.data ->> 'downloadId') !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' THEN
            RETURN NEW;
        END IF;
        event_download_id := rss_provenance_uuid(NEW.data ->> 'downloadId');
        IF event_download_id IS NULL THEN
            RETURN NEW;
        END IF;

        SELECT task.acquisition_id
        INTO event_task_acquisition_id
        FROM episode_tasks AS task
        JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
        JOIN downloads AS download ON download.id = source_file.download_id
        WHERE task.id = NEW.resource_id
          AND source_file.download_id = event_download_id
          AND download.acquisition_id = task.acquisition_id
          AND download.deleted_at IS NULL;
        IF NOT FOUND THEN
            RETURN NEW;
        END IF;

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
        WHERE provenance.acquisition_id = event_task_acquisition_id
          AND provenance.imported_at IS NULL
          AND provenance.pending_download_id = event_download_id;
        RETURN NEW;
    END IF;

    IF NEW.resource_type = 'episode_task'
       AND NEW.resource_id IS NOT NULL
       AND NEW.topic IN ('task.video_ready', 'task.subtitle_ready', 'task.awaiting_review', 'task.reviewed') THEN
        SELECT task.acquisition_id
        INTO event_task_acquisition_id
        FROM episode_tasks AS task
        JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
        JOIN downloads AS download ON download.id = source_file.download_id
        WHERE task.id = NEW.resource_id
          AND download.acquisition_id = task.acquisition_id
          AND download.deleted_at IS NULL;
        IF NOT FOUND THEN
            RETURN NEW;
        END IF;

        UPDATE rss_acquisition_provenance AS provenance
        SET pending_video_ready_at = CASE
                WHEN NEW.topic = 'task.video_ready' AND provenance.pending_task_id = NEW.resource_id
                    THEN GREATEST(COALESCE(provenance.pending_video_ready_at, NEW.occurred_at), NEW.occurred_at)
                ELSE provenance.pending_video_ready_at
            END,
            video_ready_at = CASE
                WHEN NEW.topic = 'task.video_ready' AND provenance.task_id = NEW.resource_id
                    THEN GREATEST(COALESCE(provenance.video_ready_at, NEW.occurred_at), NEW.occurred_at)
                ELSE provenance.video_ready_at
            END,
            pending_subtitle_ready_at = CASE
                WHEN NEW.topic = 'task.subtitle_ready' AND provenance.pending_task_id = NEW.resource_id
                    THEN GREATEST(COALESCE(provenance.pending_subtitle_ready_at, NEW.occurred_at), NEW.occurred_at)
                ELSE provenance.pending_subtitle_ready_at
            END,
            subtitle_ready_at = CASE
                WHEN NEW.topic = 'task.subtitle_ready' AND provenance.task_id = NEW.resource_id
                    THEN GREATEST(COALESCE(provenance.subtitle_ready_at, NEW.occurred_at), NEW.occurred_at)
                ELSE provenance.subtitle_ready_at
            END,
            pending_artifact_ready_at = CASE
                WHEN NEW.topic = 'task.awaiting_review' AND provenance.pending_task_id = NEW.resource_id
                    THEN GREATEST(COALESCE(provenance.pending_artifact_ready_at, NEW.occurred_at), NEW.occurred_at)
                ELSE provenance.pending_artifact_ready_at
            END,
            artifact_ready_at = CASE
                WHEN NEW.topic = 'task.awaiting_review' AND provenance.task_id = NEW.resource_id
                    THEN GREATEST(COALESCE(provenance.artifact_ready_at, NEW.occurred_at), NEW.occurred_at)
                ELSE provenance.artifact_ready_at
            END,
            pending_reviewed_at = CASE
                WHEN NEW.topic = 'task.reviewed' AND provenance.pending_task_id = NEW.resource_id
                    THEN GREATEST(COALESCE(provenance.pending_reviewed_at, NEW.occurred_at), NEW.occurred_at)
                ELSE provenance.pending_reviewed_at
            END,
            reviewed_at = CASE
                WHEN NEW.topic = 'task.reviewed' AND provenance.task_id = NEW.resource_id
                    THEN GREATEST(COALESCE(provenance.reviewed_at, NEW.occurred_at), NEW.occurred_at)
                ELSE provenance.reviewed_at
            END,
            updated_at = clock_timestamp()
        WHERE provenance.acquisition_id = event_task_acquisition_id
          AND (provenance.pending_task_id = NEW.resource_id OR provenance.task_id = NEW.resource_id);
        RETURN NEW;
    END IF;

    IF NEW.topic = 'task.imported'
       AND NEW.resource_type = 'episode_task'
       AND NEW.resource_id IS NOT NULL THEN
        SELECT task.acquisition_id
        INTO event_task_acquisition_id
        FROM episode_tasks AS task
        JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
        JOIN downloads AS download ON download.id = source_file.download_id
        WHERE task.id = NEW.resource_id
          AND download.acquisition_id = task.acquisition_id
          AND download.deleted_at IS NULL;
        IF NOT FOUND THEN
            RETURN NEW;
        END IF;

        UPDATE rss_acquisition_provenance AS provenance
        SET download_id = CASE
                WHEN provenance.imported_at IS NULL AND provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_download_id
                ELSE provenance.download_id
            END,
            download_attempt = CASE
                WHEN provenance.imported_at IS NULL AND provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_download_attempt
                ELSE provenance.download_attempt
            END,
            task_id = CASE
                WHEN provenance.imported_at IS NULL AND provenance.pending_task_id = NEW.resource_id
                    THEN NEW.resource_id
                ELSE provenance.task_id
            END,
            acquisition_created_at = CASE
                WHEN provenance.imported_at IS NULL AND provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_enqueue_at
                ELSE provenance.acquisition_created_at
            END,
            task_created_at = CASE
                WHEN provenance.imported_at IS NULL AND provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_task_created_at
                ELSE provenance.task_created_at
            END,
            video_ready_at = CASE
                WHEN provenance.imported_at IS NULL AND provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_video_ready_at
                ELSE provenance.video_ready_at
            END,
            subtitle_ready_at = CASE
                WHEN provenance.imported_at IS NULL AND provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_subtitle_ready_at
                ELSE provenance.subtitle_ready_at
            END,
            artifact_ready_at = CASE
                WHEN provenance.imported_at IS NULL AND provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_artifact_ready_at
                ELSE provenance.artifact_ready_at
            END,
            reviewed_at = CASE
                WHEN provenance.imported_at IS NULL AND provenance.pending_task_id = NEW.resource_id
                    THEN provenance.pending_reviewed_at
                ELSE provenance.reviewed_at
            END,
            imported_at = GREATEST(COALESCE(provenance.imported_at, NEW.occurred_at), NEW.occurred_at),
            pending_download_id = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_download_id
            END,
            pending_download_attempt = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_download_attempt
            END,
            pending_enqueue_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_enqueue_at
            END,
            pending_enqueue_event_sequence = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_enqueue_event_sequence
            END,
            pending_task_id = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_task_id
            END,
            pending_task_created_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_task_created_at
            END,
            pending_video_ready_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_video_ready_at
            END,
            pending_subtitle_ready_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_subtitle_ready_at
            END,
            pending_artifact_ready_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_artifact_ready_at
            END,
            pending_reviewed_at = CASE
                WHEN provenance.pending_task_id = NEW.resource_id THEN NULL
                ELSE provenance.pending_reviewed_at
            END,
            updated_at = clock_timestamp()
        WHERE provenance.acquisition_id = event_task_acquisition_id
          AND (provenance.pending_task_id = NEW.resource_id OR provenance.task_id = NEW.resource_id);
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

-- BEGIN MIGRATION_40_RSS_PROVENANCE_BACKFILL
-- Backfill current and historical successful facts from migration 39 events.
-- The materialized candidate relation reads events once; all later joins and
-- aggregations operate on that bounded topic/resource projection.
WITH candidate_events AS MATERIALIZED (
    SELECT
        event.event_sequence,
        event.topic,
        event.resource_type,
        event.resource_id,
        event.data,
        event.occurred_at
    FROM events AS event
    WHERE event.topic IN (
        'rss.entry.enqueueing',
        'task.created',
        'task.video_ready',
        'task.subtitle_ready',
        'task.awaiting_review',
        'task.reviewed',
        'task.imported',
        'acquisition.delete_completed'
    )
      AND event.resource_id IS NOT NULL
), archived AS (
    SELECT
        event.resource_id AS acquisition_id,
        max(event.occurred_at) AS archived_at,
        max(event.event_sequence) AS archived_event_sequence
    FROM candidate_events AS event
    WHERE event.resource_type = 'acquisition'
      AND event.topic = 'acquisition.delete_completed'
    GROUP BY event.resource_id
), enqueue_candidates AS (
    SELECT
        event.resource_id AS rss_entry_id,
        rss_provenance_uuid(event.data->>'acquisitionId') AS acquisition_id,
        rss_provenance_uuid(event.data->>'downloadId') AS download_id,
        rss_provenance_positive_int(event.data->>'downloadAttempt') AS event_download_attempt,
        rss_provenance_positive_int(event.data->>'enqueueAttempt') AS event_enqueue_attempt,
        event.occurred_at,
        event.event_sequence
    FROM candidate_events AS event
    WHERE event.topic = 'rss.entry.enqueueing'
      AND event.resource_type = 'rss_entry'
      AND jsonb_typeof(event.data->'acquisitionId') = 'string'
      AND jsonb_typeof(event.data->'downloadId') = 'string'
      AND rss_provenance_uuid(event.data->>'acquisitionId') IS NOT NULL
      AND rss_provenance_uuid(event.data->>'downloadId') IS NOT NULL
), historical_enqueue_entry_consistent AS (
    SELECT
        enqueue.acquisition_id
    FROM enqueue_candidates AS enqueue
    GROUP BY enqueue.acquisition_id
    HAVING count(DISTINCT enqueue.rss_entry_id) = 1
), enqueue_ranked AS (
    SELECT
        enqueue.*,
        row_number() OVER (
            PARTITION BY enqueue.acquisition_id
            ORDER BY enqueue.event_sequence
        )::integer AS fallback_download_attempt
    FROM enqueue_candidates AS enqueue
), enqueue_attempts AS (
    SELECT
        enqueue.rss_entry_id,
        enqueue.acquisition_id,
        enqueue.download_id,
        COALESCE(download.attempt, enqueue.event_download_attempt, enqueue.event_enqueue_attempt, enqueue.fallback_download_attempt) AS download_attempt,
        download.acquisition_id AS known_download_acquisition_id,
        download.deleted_at AS known_download_deleted_at,
        download.id IS NOT NULL AS known_download,
        download.id IS NOT NULL AND download.deleted_at IS NULL AS live_download,
        enqueue.occurred_at,
        enqueue.event_sequence
    FROM enqueue_ranked AS enqueue
    LEFT JOIN downloads AS download
      ON download.id = enqueue.download_id
), historical_download_identity AS (
    SELECT
        enqueue.download_id
    FROM enqueue_attempts AS enqueue
    WHERE NOT enqueue.known_download
    GROUP BY enqueue.download_id
    HAVING count(DISTINCT enqueue.acquisition_id) = 1
-- A live enqueue is accepted only when all three business links agree:
-- acquisition -> rss_entry, download -> acquisition, and a non-deleted download.
-- Once acquisition rows are deleted, a known soft-deleted download still has
-- an authoritative owner; only a completely missing download uses the
-- conservative, unobservable event-chain identity check.
), verified_enqueue_attempts AS (
    SELECT
        enqueue.*,
        acquisition.id IS NOT NULL AS live_acquisition,
        archived.archived_event_sequence
    FROM enqueue_attempts AS enqueue
    LEFT JOIN acquisitions AS acquisition ON acquisition.id = enqueue.acquisition_id
    LEFT JOIN archived ON archived.acquisition_id = enqueue.acquisition_id
    LEFT JOIN historical_enqueue_entry_consistent AS historical
      ON historical.acquisition_id = enqueue.acquisition_id
    LEFT JOIN historical_download_identity AS download_history
      ON download_history.download_id = enqueue.download_id
    WHERE (
        acquisition.id IS NOT NULL
        AND acquisition.rss_entry_id = enqueue.rss_entry_id
        AND enqueue.live_download
        AND enqueue.known_download_acquisition_id = enqueue.acquisition_id
    ) OR (
        acquisition.id IS NULL
        AND archived.acquisition_id IS NOT NULL
        AND historical.acquisition_id IS NOT NULL
        AND (
            (
                enqueue.known_download
                AND enqueue.known_download_acquisition_id = enqueue.acquisition_id
                AND enqueue.known_download_deleted_at IS NOT NULL
            )
            OR (
                NOT enqueue.known_download
                AND download_history.download_id IS NOT NULL
            )
        )
    )
), attempt_downloads AS (
    SELECT
        enqueue.acquisition_id,
        enqueue.rss_entry_id,
        enqueue.download_id,
        enqueue.download_attempt,
        min(enqueue.occurred_at) AS occurred_at,
        min(enqueue.event_sequence) AS first_event_sequence,
        max(enqueue.event_sequence) AS last_event_sequence,
        bool_or(enqueue.live_acquisition) AS live_acquisition,
        max(enqueue.archived_event_sequence) AS archived_event_sequence
    FROM verified_enqueue_attempts AS enqueue
    GROUP BY enqueue.acquisition_id, enqueue.rss_entry_id, enqueue.download_id, enqueue.download_attempt
), attempt_winners AS (
    -- The first download accepted for an attempt is the online winner. Replays
    -- of that same download are folded into its time/sequence high-water mark.
    SELECT DISTINCT ON (attempt.acquisition_id, attempt.download_attempt)
        attempt.*
    FROM attempt_downloads AS attempt
    ORDER BY attempt.acquisition_id, attempt.download_attempt, attempt.first_event_sequence, attempt.download_id
), latest_enqueue AS (
    SELECT winner.*, winner.last_event_sequence AS event_sequence
    FROM (
        SELECT DISTINCT ON (attempt.acquisition_id)
            attempt.*
        FROM attempt_winners AS attempt
        ORDER BY attempt.acquisition_id, attempt.download_attempt DESC, attempt.last_event_sequence DESC
    ) AS winner
), task_created_candidates AS (
    SELECT
        event.resource_id AS task_id,
        rss_provenance_uuid(event.data->>'downloadId') AS download_id,
        event.occurred_at AS task_created_at,
        event.event_sequence
    FROM candidate_events AS event
    WHERE event.topic = 'task.created'
      AND event.resource_type = 'episode_task'
      AND jsonb_typeof(event.data->'downloadId') = 'string'
      AND rss_provenance_uuid(event.data->>'downloadId') IS NOT NULL
-- Live tasks are checked through their actual source file. Deleted task rows
-- can only be reconstructed from an already verified deleted acquisition chain;
-- a payload UUID alone is never sufficient for either path.
), task_created_events AS (
    SELECT
        task.acquisition_id,
        candidate.task_id,
        candidate.download_id,
        candidate.task_created_at,
        candidate.event_sequence
    FROM task_created_candidates AS candidate
    JOIN episode_tasks AS task ON task.id = candidate.task_id
    JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
    JOIN downloads AS source_download ON source_download.id = source_file.download_id
    WHERE source_download.id = candidate.download_id
      AND source_download.acquisition_id = task.acquisition_id
      AND source_download.deleted_at IS NULL
    UNION ALL
    SELECT
        enqueue.acquisition_id,
        candidate.task_id,
        candidate.download_id,
        candidate.task_created_at,
        candidate.event_sequence
    FROM task_created_candidates AS candidate
    JOIN attempt_winners AS enqueue ON enqueue.download_id = candidate.download_id
    WHERE NOT enqueue.live_acquisition
      AND NOT EXISTS (
          SELECT 1
          FROM episode_tasks AS task
          WHERE task.id = candidate.task_id
      )
), task_milestones AS (
    SELECT
        event.resource_id AS task_id,
        max(event.occurred_at) FILTER (WHERE event.topic = 'task.video_ready') AS video_ready_at,
        max(event.occurred_at) FILTER (WHERE event.topic = 'task.subtitle_ready') AS subtitle_ready_at,
        max(event.occurred_at) FILTER (WHERE event.topic = 'task.awaiting_review') AS artifact_ready_at,
        max(event.occurred_at) FILTER (WHERE event.topic = 'task.reviewed') AS reviewed_at
    FROM candidate_events AS event
    WHERE event.resource_type = 'episode_task'
      AND event.topic IN ('task.video_ready', 'task.subtitle_ready', 'task.awaiting_review', 'task.reviewed')
    GROUP BY event.resource_id
), task_imported_events AS (
    SELECT
        event.resource_id AS task_id,
        event.occurred_at AS imported_at,
        event.event_sequence AS imported_event_sequence
    FROM candidate_events AS event
    WHERE event.resource_type = 'episode_task'
      AND event.topic = 'task.imported'
), task_current_before_import AS (
    SELECT DISTINCT ON (attempt.acquisition_id, attempt.download_id, attempt.download_attempt, imported.imported_event_sequence)
        attempt.acquisition_id,
        attempt.download_id,
        attempt.download_attempt,
        imported.task_id AS imported_task_id,
        imported.imported_at,
        imported.imported_event_sequence,
        created.task_id,
        (
            SELECT min(history.task_created_at)
            FROM task_created_events AS history
            WHERE history.acquisition_id = attempt.acquisition_id
              AND history.download_id = attempt.download_id
              AND history.task_id = created.task_id
              AND history.event_sequence > attempt.first_event_sequence
              AND history.event_sequence < imported.imported_event_sequence
        ) AS task_created_at,
        created.event_sequence AS task_created_event_sequence
    FROM attempt_winners AS attempt
    JOIN task_created_events AS created
      ON created.acquisition_id = attempt.acquisition_id
     AND created.download_id = attempt.download_id
     AND created.event_sequence > attempt.first_event_sequence
    JOIN task_imported_events AS imported
      ON imported.task_id = created.task_id
     AND imported.imported_event_sequence > created.event_sequence
    WHERE NOT EXISTS (
        SELECT 1
        FROM task_created_events AS newer
        WHERE newer.acquisition_id = attempt.acquisition_id
          AND newer.download_id = attempt.download_id
          AND newer.event_sequence > created.event_sequence
          AND newer.event_sequence < imported.imported_event_sequence
    )
    ORDER BY attempt.acquisition_id, attempt.download_id, attempt.download_attempt, imported.imported_event_sequence, created.event_sequence DESC
), successful_candidates AS (
    SELECT DISTINCT ON (current.acquisition_id, current.imported_event_sequence)
        current.acquisition_id,
        attempt.rss_entry_id,
        attempt.download_id,
        attempt.download_attempt,
        min(enqueue.occurred_at) OVER (
            PARTITION BY current.acquisition_id, current.imported_event_sequence
        ) AS acquisition_created_at,
        current.task_id,
        current.task_created_at,
        milestones.video_ready_at,
        milestones.subtitle_ready_at,
        milestones.artifact_ready_at,
        milestones.reviewed_at,
        current.imported_at,
        current.imported_event_sequence
    FROM task_current_before_import AS current
    JOIN attempt_winners AS attempt
      ON attempt.acquisition_id = current.acquisition_id
     AND attempt.download_id = current.download_id
     AND attempt.download_attempt = current.download_attempt
    JOIN verified_enqueue_attempts AS enqueue
      ON enqueue.acquisition_id = attempt.acquisition_id
     AND enqueue.download_id = attempt.download_id
     AND enqueue.download_attempt = attempt.download_attempt
     AND enqueue.event_sequence < current.imported_event_sequence
    LEFT JOIN task_milestones AS milestones ON milestones.task_id = current.task_id
    WHERE current.task_id = current.imported_task_id
      AND NOT EXISTS (
          SELECT 1
          FROM attempt_winners AS higher
          WHERE higher.acquisition_id = attempt.acquisition_id
            AND higher.download_attempt > attempt.download_attempt
            AND higher.first_event_sequence < current.imported_event_sequence
      )
      AND (
          attempt.live_acquisition
          OR current.imported_event_sequence < attempt.archived_event_sequence
      )
    ORDER BY current.acquisition_id, current.imported_event_sequence, enqueue.event_sequence
), successful_history AS (
    -- The first valid import wins. A higher attempt only invalidates an older
    -- chain when its enqueue arrived before that chain's import.
    SELECT DISTINCT ON (candidate.acquisition_id)
        candidate.acquisition_id,
        candidate.download_id,
        candidate.download_attempt,
        candidate.acquisition_created_at,
        candidate.task_id,
        candidate.task_created_at,
        candidate.video_ready_at,
        candidate.subtitle_ready_at,
        candidate.artifact_ready_at,
        candidate.reviewed_at,
        candidate.imported_at,
        candidate.imported_event_sequence
    FROM successful_candidates AS candidate
    ORDER BY candidate.acquisition_id, candidate.imported_event_sequence
), pending_task_candidates AS (
    SELECT
        latest.acquisition_id,
        latest.download_id,
        latest.download_attempt,
        latest.occurred_at,
        latest.event_sequence,
        created.task_id,
        min(created.task_created_at) AS task_created_at,
        milestones.video_ready_at,
        milestones.subtitle_ready_at,
        milestones.artifact_ready_at,
        milestones.reviewed_at,
        max(created.event_sequence) AS task_event_sequence
    FROM latest_enqueue AS latest
    JOIN task_created_events AS created
      ON created.acquisition_id = latest.acquisition_id
     AND created.download_id = latest.download_id
     AND created.event_sequence > latest.first_event_sequence
    LEFT JOIN task_milestones AS milestones ON milestones.task_id = created.task_id
    LEFT JOIN successful_history AS successful ON successful.acquisition_id = latest.acquisition_id
    WHERE successful.acquisition_id IS NULL
      AND (
          latest.live_acquisition
          OR created.event_sequence < latest.archived_event_sequence
      )
    GROUP BY
        latest.acquisition_id,
        latest.download_id,
        latest.download_attempt,
        latest.occurred_at,
        latest.event_sequence,
        created.task_id,
        milestones.video_ready_at,
        milestones.subtitle_ready_at,
        milestones.artifact_ready_at,
        milestones.reviewed_at
), pending_tasks AS (
    SELECT DISTINCT ON (candidate.acquisition_id)
        candidate.acquisition_id,
        candidate.download_id,
        candidate.download_attempt,
        candidate.occurred_at,
        candidate.event_sequence,
        candidate.task_id,
        candidate.task_created_at,
        candidate.video_ready_at,
        candidate.subtitle_ready_at,
        candidate.artifact_ready_at,
        candidate.reviewed_at
    FROM pending_task_candidates AS candidate
    ORDER BY candidate.acquisition_id, candidate.task_event_sequence DESC, candidate.task_id
)
INSERT INTO rss_acquisition_provenance (
    acquisition_id,
    rss_entry_id,
    download_id,
    download_attempt,
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
    pending_download_attempt,
    pending_enqueue_at,
    pending_enqueue_event_sequence,
    pending_task_id,
    pending_task_created_at,
    pending_video_ready_at,
    pending_subtitle_ready_at,
    pending_artifact_ready_at,
    pending_reviewed_at
)
-- successful_history is the only condition that clears the latest pending
-- enqueue; the optional pending task CTE cannot erase it by failing to join.
SELECT
    latest.acquisition_id,
    latest.rss_entry_id,
    successful.download_id,
    successful.download_attempt,
    successful.task_id,
    successful.acquisition_created_at,
    successful.task_created_at,
    successful.video_ready_at,
    successful.subtitle_ready_at,
    successful.artifact_ready_at,
    successful.reviewed_at,
    successful.imported_at,
    archived.archived_at,
    CASE
        WHEN successful.acquisition_id IS NULL THEN latest.download_id
        ELSE NULL
    END,
    CASE
        WHEN successful.acquisition_id IS NULL THEN latest.download_attempt
        ELSE NULL
    END,
    CASE
        WHEN successful.acquisition_id IS NULL THEN latest.occurred_at
        ELSE NULL
    END,
    CASE
        WHEN successful.acquisition_id IS NULL THEN latest.event_sequence
        ELSE NULL
    END,
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
    download_attempt = EXCLUDED.download_attempt,
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
    pending_download_attempt = EXCLUDED.pending_download_attempt,
    pending_enqueue_at = EXCLUDED.pending_enqueue_at,
    pending_enqueue_event_sequence = EXCLUDED.pending_enqueue_event_sequence,
    pending_task_id = EXCLUDED.pending_task_id,
    pending_task_created_at = EXCLUDED.pending_task_created_at,
    pending_video_ready_at = EXCLUDED.pending_video_ready_at,
    pending_subtitle_ready_at = EXCLUDED.pending_subtitle_ready_at,
    pending_artifact_ready_at = EXCLUDED.pending_artifact_ready_at,
    pending_reviewed_at = EXCLUDED.pending_reviewed_at,
    updated_at = clock_timestamp();

-- END MIGRATION_40_RSS_PROVENANCE_BACKFILL

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
