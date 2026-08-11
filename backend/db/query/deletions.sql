-- name: GetDownloadAcquisitionDeletionTarget :one
SELECT acquisition_id
FROM downloads
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: LockDownloadAcquisitionDeletionTarget :one
SELECT id, acquisition_id, version
FROM downloads
WHERE id = sqlc.arg(id)
  AND acquisition_id = sqlc.arg(acquisition_id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: LockAcquisitionForDeletion :one
SELECT id, deletion_requested_at
FROM acquisitions
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: ListAcquisitionDeletionTaskIDs :many
SELECT id
FROM episode_tasks
WHERE acquisition_id = sqlc.arg(acquisition_id)
ORDER BY id;

-- name: ListAcquisitionDeletionDownloads :many
SELECT
    download.id,
    download.torrent_hash,
    download.save_path,
    EXISTS (
        SELECT 1
        FROM downloads AS other
        JOIN acquisitions AS other_acquisition ON other_acquisition.id = other.acquisition_id
        WHERE other.acquisition_id <> download.acquisition_id
          AND other.deleted_at IS NULL
          AND other_acquisition.deletion_requested_at IS NULL
          AND (
              other_acquisition.source_kind <> 'rss'
              OR EXISTS (
                  SELECT 1
                  FROM rss_entries AS other_entry
                  JOIN rss_subscriptions AS other_subscription ON other_subscription.id = other_entry.subscription_id
                  WHERE other_entry.id = other_acquisition.rss_entry_id
                    AND other_subscription.deleted_at IS NULL
              )
          )
          AND download.torrent_hash IS NOT NULL
          AND lower(other.torrent_hash) = lower(download.torrent_hash)
    ) AS preserve_torrent,
    EXISTS (
        SELECT 1
        FROM downloads AS other
        JOIN acquisitions AS other_acquisition ON other_acquisition.id = other.acquisition_id
        WHERE other.acquisition_id <> download.acquisition_id
          AND other.deleted_at IS NULL
          AND other_acquisition.deletion_requested_at IS NULL
          AND (
              other_acquisition.source_kind <> 'rss'
              OR EXISTS (
                  SELECT 1
                  FROM rss_entries AS other_entry
                  JOIN rss_subscriptions AS other_subscription ON other_subscription.id = other_entry.subscription_id
                  WHERE other_entry.id = other_acquisition.rss_entry_id
                    AND other_subscription.deleted_at IS NULL
              )
          )
          AND download.save_path IS NOT NULL
          AND lower(btrim(other.save_path)) = lower(btrim(download.save_path))
    ) AS preserve_path
FROM downloads AS download
WHERE download.acquisition_id = sqlc.arg(acquisition_id)
ORDER BY download.attempt, download.id;

-- name: ListAcquisitionDeletionArtifactPaths :many
SELECT artifact.file_path
FROM media_artifacts AS artifact
JOIN episode_tasks AS task ON task.id = artifact.task_id
WHERE task.acquisition_id = sqlc.arg(acquisition_id)
ORDER BY artifact.id;

-- name: ListAcquisitionDeletionLibraryFiles :many
WITH deletion_imports AS (
    SELECT imported.id, imported.destination_video_path AS file_path
    FROM imports AS imported
    JOIN episode_tasks AS task ON task.id = imported.task_id
    WHERE task.acquisition_id = sqlc.arg(acquisition_id)
      AND imported.status = 'succeeded'
      AND imported.destination_video_path IS NOT NULL
    UNION ALL
    SELECT imported.id, imported.destination_subtitle_path AS file_path
    FROM imports AS imported
    JOIN episode_tasks AS task ON task.id = imported.task_id
    WHERE task.acquisition_id = sqlc.arg(acquisition_id)
      AND imported.status = 'succeeded'
      AND imported.destination_subtitle_path IS NOT NULL
)
SELECT
    deletion_import.file_path::text AS file_path,
    EXISTS (
        SELECT 1
        FROM imports AS other_import
        JOIN episode_tasks AS other_task ON other_task.id = other_import.task_id
        JOIN acquisitions AS other_acquisition ON other_acquisition.id = other_task.acquisition_id
        WHERE other_acquisition.id <> sqlc.arg(acquisition_id)
          AND other_acquisition.deletion_requested_at IS NULL
          AND other_import.status = 'succeeded'
          AND (
              other_import.destination_video_path = deletion_import.file_path
              OR other_import.destination_subtitle_path = deletion_import.file_path
          )
    ) AS preserve
FROM deletion_imports AS deletion_import
ORDER BY deletion_import.file_path, deletion_import.id;

-- name: RequestAcquisitionOperationCancellations :execrows
UPDATE operations AS operation
SET cancel_requested_at = COALESCE(operation.cancel_requested_at, now()),
    updated_at = now()
WHERE operation.status IN ('queued', 'running')
  AND operation.kind <> 'acquisition.delete'
  AND operation.id <> sqlc.arg(excluded_operation_id)
  AND (
      (operation.resource_type = 'acquisition' AND operation.resource_id = sqlc.arg(acquisition_id))
      OR (
          operation.resource_type = 'episode_task'
          AND operation.resource_id IN (
              SELECT task.id
              FROM episode_tasks AS task
              WHERE task.acquisition_id = sqlc.arg(acquisition_id)
          )
      )
      OR (
          operation.resource_type = 'download'
          AND operation.resource_id IN (
              SELECT download.id
              FROM downloads AS download
              WHERE download.acquisition_id = sqlc.arg(acquisition_id)
          )
      )
  );

-- name: CancelAcquisitionTasksForDeletion :execrows
UPDATE episode_tasks
SET state = 'cancelled',
    video_state = CASE WHEN video_state = 'video_ready' THEN video_state ELSE 'cancelled' END,
    subtitle_state = CASE WHEN subtitle_state = 'ass_ready' THEN subtitle_state ELSE 'cancelled' END,
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE acquisition_id = sqlc.arg(acquisition_id)
  AND state NOT IN ('imported', 'cancelled');

-- name: CancelAcquisitionDownloadsForDeletion :execrows
UPDATE downloads
SET status = 'cancelled',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE acquisition_id = sqlc.arg(acquisition_id)
  AND deleted_at IS NULL
  AND status IN ('enqueue_pending', 'downloading', 'completed', 'selecting_files');

-- name: MarkAcquisitionDeletionRequested :one
UPDATE acquisitions
SET deletion_requested_at = COALESCE(deletion_requested_at, now()),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND deletion_requested_at IS NULL
RETURNING *;

-- name: CountAcquisitionDeletionActiveOperations :one
SELECT count(*)::bigint
FROM operations AS operation
WHERE operation.id <> sqlc.arg(operation_id)
  AND (
      operation.status = 'running'
      OR (operation.status = 'queued' AND operation.cancel_requested_at IS NULL)
  )
  AND (
      (operation.resource_type = 'acquisition' AND operation.resource_id = sqlc.arg(acquisition_id))
      OR (
          operation.resource_type = 'episode_task'
          AND operation.resource_id IN (
              SELECT task.id
              FROM episode_tasks AS task
              WHERE task.acquisition_id = sqlc.arg(acquisition_id)
          )
      )
      OR (
          operation.resource_type = 'download'
          AND operation.resource_id IN (
              SELECT download.id
              FROM downloads AS download
              WHERE download.acquisition_id = sqlc.arg(acquisition_id)
          )
      )
  );

-- name: DeleteArtifactSetsForAcquisition :execrows
DELETE FROM artifact_sets
WHERE task_id IN (
    SELECT id
    FROM episode_tasks
    WHERE acquisition_id = sqlc.arg(acquisition_id)
);

-- name: DeleteMediaArtifactsForAcquisition :execrows
DELETE FROM media_artifacts
WHERE task_id IN (
    SELECT id
    FROM episode_tasks
    WHERE acquisition_id = sqlc.arg(acquisition_id)
);

-- name: DeleteEpisodeTasksForAcquisition :execrows
DELETE FROM episode_tasks
WHERE acquisition_id = sqlc.arg(acquisition_id);

-- name: DeleteAcquisitionWorkflow :execrows
DELETE FROM acquisitions
WHERE id = sqlc.arg(id)
  AND deletion_requested_at IS NOT NULL;

-- name: ListSubscriptionDeletionAcquisitionIDs :many
SELECT acquisition.id
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE entry.subscription_id = sqlc.arg(subscription_id)
ORDER BY acquisition.id;

-- name: CountSubscriptionDeletionActiveOperations :one
SELECT count(*)::bigint
FROM operations AS operation
WHERE operation.id <> sqlc.arg(operation_id)
  AND operation.resource_type = 'rss_subscription'
  AND operation.resource_id = sqlc.arg(subscription_id)
  AND (
      operation.status = 'running'
      OR (operation.status = 'queued' AND operation.cancel_requested_at IS NULL)
  );

-- name: DeleteArchivedRSSSubscription :execrows
DELETE FROM rss_subscriptions AS subscription
WHERE subscription.id = sqlc.arg(id)
  AND subscription.deleted_at IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM acquisitions AS acquisition
      JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
      WHERE entry.subscription_id = subscription.id
  );
