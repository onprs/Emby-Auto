-- name: ListWorkflowRetryTargets :many
SELECT DISTINCT mapping.target_episode_id
FROM acquisitions AS acquisition
JOIN episode_mappings AS mapping ON mapping.profile_id = acquisition.mapping_profile_id
WHERE acquisition.id = sqlc.arg(acquisition_id)
  AND mapping.target_episode_id IS NOT NULL
  AND mapping.mapping_status = 'mapped'
  AND (
      EXISTS (
          SELECT 1 FROM episode_tasks AS task
          WHERE task.acquisition_id = acquisition.id AND task.mapping_id = mapping.id
            AND (sqlc.narg(task_id)::uuid IS NULL OR task.id = sqlc.narg(task_id)::uuid)
      )
      OR (sqlc.narg(task_id)::uuid IS NULL AND (
          EXISTS (
              SELECT 1 FROM rss_entries AS entry
              WHERE entry.id = acquisition.rss_entry_id
                AND entry.source_season = mapping.source_season
                AND entry.source_episode = mapping.source_episode
                AND entry.source_episode_fraction_hundredths = mapping.source_episode_fraction_hundredths
          )
          OR EXISTS (
              SELECT 1 FROM downloads AS download
              JOIN download_files AS file ON file.download_id = download.id
              WHERE download.acquisition_id = acquisition.id AND download.deleted_at IS NULL
                AND file.selected AND file.media_kind = 'video'
                AND file.source_season = mapping.source_season AND file.source_episode = mapping.source_episode
                AND file.source_episode_fraction_hundredths = mapping.source_episode_fraction_hundredths
          )
          OR (acquisition.source_payload->'singleEpisode' = 'true'::jsonb
              AND acquisition.source_payload->>'sourceSeason' = mapping.source_season::text
              AND acquisition.source_payload->>'sourceEpisode' = mapping.source_episode::text
              AND COALESCE(acquisition.source_payload->>'sourceEpisodeFractionHundredths', '0') = mapping.source_episode_fraction_hundredths::text)
      ))
  )
ORDER BY mapping.target_episode_id;

-- name: WorkflowRetryHasPendingCancellation :one
SELECT EXISTS (
    SELECT 1 FROM operations AS operation
    WHERE operation.status IN ('queued', 'running')
      AND (sqlc.arg(require_idle)::boolean OR operation.cancel_requested_at IS NOT NULL OR operation.kind IN ('task.cancel', 'download.cancel', 'acquisition.delete', 'rss.subscription.delete'))
      AND (
          (operation.resource_type = 'acquisition' AND operation.resource_id = sqlc.arg(acquisition_id))
          OR (operation.resource_type = 'episode_task' AND operation.resource_id IN (
              SELECT task.id FROM episode_tasks AS task
              WHERE task.acquisition_id = sqlc.arg(acquisition_id)
                AND (sqlc.narg(task_id)::uuid IS NULL OR task.id = sqlc.narg(task_id)::uuid)
          ))
          OR (operation.resource_type = 'download' AND operation.resource_id IN (
              SELECT download.id FROM downloads AS download WHERE download.acquisition_id = sqlc.arg(acquisition_id)
          ))
      )
)::boolean;

-- name: WorkflowRetrySourceDeleted :one
SELECT EXISTS (
    SELECT 1 FROM acquisitions AS acquisition
    JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
    JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
    WHERE acquisition.id = sqlc.arg(acquisition_id) AND subscription.deleted_at IS NOT NULL
)::boolean;

-- name: RequeueCancelledDownload :one
UPDATE downloads
SET status = 'enqueue_pending',
    torrent_hash = NULL,
    save_path = NULL,
    client_state = NULL,
    last_synced_at = NULL,
    progress = 0,
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    agent_resolution_id = NULL,
    completed_at = NULL,
    version = version + 1,
    updated_at = now()
WHERE downloads.id = sqlc.arg(id)
  AND downloads.status = 'cancelled'
  AND downloads.deleted_at IS NULL AND downloads.deletion_requested_at IS NULL
  AND downloads.version = sqlc.arg(expected_version)
  AND NOT EXISTS (
      SELECT 1 FROM episode_tasks AS task
      JOIN download_files AS file ON file.id = task.source_video_file_id
      WHERE file.download_id = downloads.id
  )
RETURNING downloads.*;

-- name: RequeueCancelledTaskAfterMedia :one
UPDATE episode_tasks AS task
SET state = sqlc.arg(next_state),
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE task.id = sqlc.arg(id)
  AND task.state = 'cancelled'
  AND task.video_state = 'video_ready' AND task.subtitle_state = 'ass_ready'
  AND task.version = sqlc.arg(expected_version)
  AND sqlc.arg(next_state)::text IN ('finalizing', 'import_queued')
  AND NOT EXISTS (SELECT 1 FROM imports WHERE imports.task_id = task.id AND imports.status = 'succeeded')
RETURNING task.*;

-- name: RequeueCancelledTaskImport :one
UPDATE imports
SET status = 'queued', error_code = NULL, error_message = NULL,
    started_at = NULL, completed_at = NULL, updated_at = now()
WHERE id = (
    SELECT candidate.id FROM imports AS candidate
    WHERE candidate.task_id = sqlc.arg(task_id) AND candidate.status IN ('queued', 'running', 'cancelled', 'failed')
    ORDER BY candidate.attempt DESC LIMIT 1
)
RETURNING *;

-- name: ResetSkippedRSSEntryForRetry :one
UPDATE rss_entries
SET downloadable = true, rejection_reasons = '{}',
    last_error_code = NULL, last_error_message = NULL, last_error_retryable = false,
    enqueue_attempts = GREATEST(enqueue_attempts, 1) + 1, updated_at = now()
WHERE rss_entries.id = sqlc.arg(id)
  AND status IN ('discovered', 'enqueue_failed')
  AND rejection_reasons = ARRAY['target_episode_processing']::text[]
  AND imported_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM rss_entry_adjudications AS adjudication WHERE adjudication.entry_id = rss_entries.id AND adjudication.state = 'ignored')
RETURNING rss_entries.*;

-- name: ReserveRSSEntryRetry :one
UPDATE rss_entries
SET status = CASE WHEN status IN ('enqueued', 'enqueueing') THEN 'discovered' ELSE status END,
    enqueue_attempts = GREATEST(enqueue_attempts, 1) + 1, updated_at = now()
WHERE rss_entries.id = sqlc.arg(id) AND downloadable
  AND status IN ('discovered', 'enqueued', 'enqueueing', 'enqueue_failed') AND imported_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM rss_entry_adjudications AS adjudication WHERE adjudication.entry_id = rss_entries.id AND adjudication.state = 'ignored')
RETURNING rss_entries.*;

-- name: ListRSSPendingEntryRetries :many
SELECT DISTINCT entry.id
FROM rss_entries AS entry
JOIN operations AS operation
  ON operation.resource_type = 'rss_subscription'
 AND operation.resource_id = entry.subscription_id
 AND operation.kind = 'rss.poll' AND operation.payload->>'command' = 'retry-entry'
 AND operation.payload->>'entryId' = entry.id::text
 AND operation.status IN ('queued', 'running')
WHERE entry.subscription_id = sqlc.arg(subscription_id);

-- name: ListRSSSubscriptionEntryAcquisitions :many
SELECT acquisition.*
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE entry.subscription_id = sqlc.arg(subscription_id);

-- name: DashboardRecentTaskIDs :many
SELECT task.id
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN download_files AS file ON file.id = task.source_video_file_id
JOIN downloads AS download ON download.id = file.download_id
WHERE acquisition.deletion_requested_at IS NULL AND download.deleted_at IS NULL
ORDER BY task.updated_at DESC, task.id DESC
LIMIT sqlc.arg(row_limit);
