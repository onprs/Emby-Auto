-- name: GetDownloadEnqueueCommand :one
SELECT
    d.id,
    d.acquisition_id,
    d.status,
    d.torrent_hash,
    d.file_resolution_source,
    COALESCE(
        CASE a.source_kind
            WHEN 'search' THEN candidate.download_uri
            WHEN 'rss' THEN entry.download_uri
            WHEN 'manual' THEN a.source_uri
        END,
        ''
    )::text AS source_uri
FROM downloads AS d
JOIN acquisitions AS a ON a.id = d.acquisition_id
LEFT JOIN release_candidates AS candidate ON candidate.id = a.release_candidate_id
LEFT JOIN rss_entries AS entry ON entry.id = a.rss_entry_id
WHERE d.id = sqlc.arg(id);

-- name: LockDownloadForEnqueue :one
SELECT *
FROM downloads
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: MarkDownloadLegacyEnqueued :one
UPDATE downloads
SET torrent_hash = sqlc.arg(torrent_hash),
    status = 'downloading',
    save_path = sqlc.arg(save_path),
    progress = 0,
    client_state = 'downloading',
    last_synced_at = now(),
    file_resolution_source = 'deterministic',
    agent_resolution_id = NULL,
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    started_at = COALESCE(started_at, now()),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'enqueue_pending'
RETURNING *;

-- name: MarkDownloadManifestPending :one
UPDATE downloads
SET torrent_hash = sqlc.arg(torrent_hash),
    status = 'file_resolution_pending',
    save_path = sqlc.arg(save_path),
    progress = 0,
    client_state = 'metadata_ready',
    last_synced_at = now(),
    file_resolution_source = sqlc.narg(file_resolution_source),
    agent_resolution_id = NULL,
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    started_at = COALESCE(started_at, now()),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'enqueue_pending'
RETURNING *;

-- name: CreateDownloadFile :one
INSERT INTO download_files (
    id,
    download_id,
    file_index,
    relative_path,
    size_bytes,
    media_kind,
    selected,
    source_season,
    source_episode,
    language
) VALUES (
    sqlc.arg(id),
    sqlc.arg(download_id),
    sqlc.arg(file_index),
    sqlc.arg(relative_path),
    sqlc.arg(size_bytes),
    sqlc.arg(media_kind),
    sqlc.arg(selected),
    sqlc.narg(source_season),
    sqlc.narg(source_episode),
    sqlc.narg(language)
)
RETURNING *;

-- name: MarkDownloadRSSEntryEnqueued :execrows
UPDATE rss_entries AS entry
SET status = 'enqueued',
    enqueued_at = COALESCE(enqueued_at, now()),
    last_error_code = NULL,
    last_error_message = NULL,
    last_error_retryable = false,
    updated_at = now()
FROM acquisitions AS acquisition
JOIN downloads AS download ON download.acquisition_id = acquisition.id
WHERE download.id = sqlc.arg(download_id)
  AND entry.id = acquisition.rss_entry_id
  AND entry.status IN ('enqueueing', 'enqueue_failed');

-- name: GetDownloadSyncCommand :one
SELECT id, status, torrent_hash, client_state, last_synced_at
FROM downloads
WHERE id = sqlc.arg(id);

-- name: UpdateDownloadProgress :one
UPDATE downloads
SET progress = GREATEST(
        progress,
        LEAST(1, GREATEST(0, sqlc.arg(progress_scaled)::bigint::numeric / 100000))
    ),
    client_state = sqlc.arg(client_state),
    last_synced_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'downloading'
RETURNING *;

-- name: MarkDownloadSelectionApplied :one
UPDATE downloads
SET status = 'downloading',
    client_state = 'added',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'file_resolution_pending'
  AND torrent_hash IS NOT NULL
RETURNING *;

-- name: MarkDownloadFileResolutionTerminalFailure :one
UPDATE downloads
SET status = 'failed',
    failure_stage = 'file_resolution',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'file_resolution_pending'
RETURNING *;

-- name: RequeueDownloadFileResolutionStage :one
UPDATE downloads
SET status = 'file_resolution_pending',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'failed'
  AND failure_stage = 'file_resolution'
  AND torrent_hash IS NOT NULL
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: SetDownloadFileResolution :one
UPDATE download_files
SET selected = sqlc.arg(selected),
    source_season = sqlc.narg(source_season),
    source_episode = sqlc.narg(source_episode),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND download_id = sqlc.arg(download_id)
RETURNING *;

-- name: SetDownloadResolutionSource :one
UPDATE downloads
SET status = 'file_resolution_pending',
    file_resolution_source = sqlc.arg(file_resolution_source),
    agent_resolution_id = sqlc.narg(agent_resolution_id),
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND (
      status = 'file_resolution_pending'
      OR (status = 'failed' AND failure_stage = 'file_resolution')
  )
RETURNING *;

-- name: MarkDownloadCompleted :one
UPDATE downloads
SET status = 'completed',
    progress = 1,
    client_state = sqlc.arg(client_state),
    last_synced_at = now(),
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    completed_at = COALESCE(completed_at, now()),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'downloading'
RETURNING *;

-- name: MarkDownloadSyncTerminalFailure :one
UPDATE downloads
SET status = 'failed',
    failure_stage = 'sync',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'downloading'
RETURNING *;

-- name: MarkDownloadMaterializeTerminalFailure :one
UPDATE downloads
SET status = 'failed',
    failure_stage = 'materialize',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('completed', 'selecting_files')
RETURNING *;

-- name: RequeueDownloadEnqueueStage :one
UPDATE downloads
SET status = 'enqueue_pending',
    progress = 0,
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'failed'
  AND failure_stage = 'enqueue'
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: RequeueDownloadSyncStage :one
UPDATE downloads
SET status = 'downloading',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'failed'
  AND failure_stage = 'sync'
  AND torrent_hash IS NOT NULL
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: RequeueDownloadMaterializeStage :one
UPDATE downloads
SET status = 'completed',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'failed'
  AND failure_stage = 'materialize'
  AND torrent_hash IS NOT NULL
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: CountBlockingTasksForDownload :one
SELECT count(*)::bigint
FROM episode_tasks AS task
JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
WHERE source_file.download_id = sqlc.arg(download_id)
  AND task.state NOT IN ('failed', 'cancelled', 'rejected', 'imported');

-- name: TorrentUsedByOtherDownload :one
SELECT EXISTS (
    SELECT 1
    FROM downloads AS target
    JOIN downloads AS other
      ON lower(other.torrent_hash) = lower(target.torrent_hash)
     AND other.id <> target.id
    WHERE target.id = sqlc.arg(download_id)
      AND target.torrent_hash IS NOT NULL
      AND other.deleted_at IS NULL
      AND other.status <> 'cancelled'
)::boolean;

-- name: MarkDownloadRemovalRequested :one
UPDATE downloads
SET deletion_requested_at = COALESCE(deletion_requested_at, now()),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND deletion_requested_at IS NULL
  AND status IN ('completed', 'materialized', 'failed', 'cancelled')
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: MarkDownloadRemoved :one
UPDATE downloads
SET deleted_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND deletion_requested_at IS NOT NULL
  AND deleted_at IS NULL
RETURNING *;

-- name: CancelDownloadIfActive :one
UPDATE downloads
SET status = 'cancelled',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('enqueue_pending', 'file_resolution_pending', 'downloading', 'completed', 'selecting_files')
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: SetDownloadFileSelection :one
UPDATE download_files
SET selected = sqlc.arg(selected),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;
