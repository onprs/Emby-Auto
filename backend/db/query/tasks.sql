-- name: GetTaskView :one
-- sqlc nullable projection: optional workflow rows use zero scalar sentinels.
SELECT
    task.*,
    acquisition.id AS workflow_acquisition_id,
    source_file.download_id,
    source_file.source_season,
    source_file.source_episode,
    series.title AS series_title,
    series.release_year,
    season.season_number AS target_season,
    episode.episode_number AS target_episode,
    episode.title AS target_episode_title,
    artifact_set.id AS artifact_set_id,
    artifact_set.basename AS artifact_basename,
    video.id AS video_artifact_id,
    video.file_path AS video_file_path,
    video.format AS video_format,
    video.size_bytes AS video_size_bytes,
    video.checksum_sha256 AS video_checksum_sha256,
    subtitle.id AS subtitle_artifact_id,
    subtitle.file_path AS subtitle_file_path,
    subtitle.format AS subtitle_format,
    subtitle.size_bytes AS subtitle_size_bytes,
    subtitle.checksum_sha256 AS subtitle_checksum_sha256,
    review.id AS review_id,
    review.decision AS review_decision,
    review.notes AS review_notes,
    review.reviewed_by,
    review.reviewed_at,
    latest_import.id AS import_id,
    COALESCE(latest_import.attempt, 0)::integer AS import_attempt,
    COALESCE(latest_import.status, '')::text AS import_status,
    latest_import.destination_video_path,
    latest_import.destination_subtitle_path,
    latest_import.error_code AS import_error_code,
    latest_import.error_message AS import_error_message,
    latest_import.started_at AS import_started_at,
    latest_import.completed_at AS import_completed_at,
    latest_import.created_at AS import_created_at,
    latest_import.updated_at AS import_updated_at,
    latest_cleanup.id AS cleanup_id,
    COALESCE(latest_cleanup.attempt, 0)::integer AS cleanup_attempt,
    COALESCE(latest_cleanup.status, '')::text AS cleanup_status,
    COALESCE(latest_cleanup.torrent_removed, false)::boolean AS torrent_removed,
    COALESCE(latest_cleanup.staged_files_removed, false)::boolean AS staged_files_removed,
    latest_cleanup.error_code AS cleanup_error_code,
    latest_cleanup.error_message AS cleanup_error_message,
    latest_cleanup.started_at AS cleanup_started_at,
    latest_cleanup.completed_at AS cleanup_completed_at,
    latest_cleanup.created_at AS cleanup_created_at,
    latest_cleanup.updated_at AS cleanup_updated_at,
    related_emby_item.id AS emby_item_id,
    related_emby_item.library_id AS emby_library_id
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
LEFT JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
LEFT JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN artifact_sets AS artifact_set ON artifact_set.task_id = task.id
LEFT JOIN media_artifacts AS video ON video.id = artifact_set.video_artifact_id
LEFT JOIN media_artifacts AS subtitle ON subtitle.id = artifact_set.subtitle_artifact_id
LEFT JOIN reviews AS review ON review.task_id = task.id
LEFT JOIN LATERAL (
    SELECT import.*
    FROM imports AS import
    WHERE import.task_id = task.id
    ORDER BY import.attempt DESC
    LIMIT 1
) AS latest_import ON true
LEFT JOIN LATERAL (
    SELECT cleanup.*
    FROM cleanup_runs AS cleanup
    WHERE cleanup.task_id = task.id
    ORDER BY cleanup.attempt DESC
    LIMIT 1
) AS latest_cleanup ON true
LEFT JOIN LATERAL (
    SELECT item.id, item.library_id
    FROM emby_library_items AS item
    WHERE item.present
      AND (
          item.file_path = latest_import.destination_video_path
          OR (
              episode.tmdb_episode_id IS NOT NULL
              AND EXISTS (
                  SELECT 1
                  FROM jsonb_each_text(item.provider_ids) AS provider
                  WHERE lower(provider.key) IN ('tmdb', 'themoviedb')
                    AND provider.value = episode.tmdb_episode_id::text
              )
          )
      )
    ORDER BY (item.file_path = latest_import.destination_video_path) DESC, item.id
    LIMIT 1
) AS related_emby_item ON true
WHERE task.id = sqlc.arg(id)
  AND EXISTS (
      SELECT 1 FROM downloads AS source_download
      WHERE source_download.id = source_file.download_id
        AND source_download.deleted_at IS NULL
  );

-- name: ListTaskViews :many
-- sqlc nullable projection: optional workflow rows use zero scalar sentinels.
SELECT
    task.*,
    acquisition.id AS workflow_acquisition_id,
    source_file.download_id,
    source_file.source_season,
    source_file.source_episode,
    series.title AS series_title,
    series.release_year,
    season.season_number AS target_season,
    episode.episode_number AS target_episode,
    episode.title AS target_episode_title,
    artifact_set.id AS artifact_set_id,
    artifact_set.basename AS artifact_basename,
    video.id AS video_artifact_id,
    video.file_path AS video_file_path,
    video.format AS video_format,
    video.size_bytes AS video_size_bytes,
    video.checksum_sha256 AS video_checksum_sha256,
    subtitle.id AS subtitle_artifact_id,
    subtitle.file_path AS subtitle_file_path,
    subtitle.format AS subtitle_format,
    subtitle.size_bytes AS subtitle_size_bytes,
    subtitle.checksum_sha256 AS subtitle_checksum_sha256,
    review.id AS review_id,
    review.decision AS review_decision,
    review.notes AS review_notes,
    review.reviewed_by,
    review.reviewed_at,
    latest_import.id AS import_id,
    COALESCE(latest_import.attempt, 0)::integer AS import_attempt,
    COALESCE(latest_import.status, '')::text AS import_status,
    latest_import.destination_video_path,
    latest_import.destination_subtitle_path,
    latest_import.error_code AS import_error_code,
    latest_import.error_message AS import_error_message,
    latest_import.started_at AS import_started_at,
    latest_import.completed_at AS import_completed_at,
    latest_import.created_at AS import_created_at,
    latest_import.updated_at AS import_updated_at,
    latest_cleanup.id AS cleanup_id,
    COALESCE(latest_cleanup.attempt, 0)::integer AS cleanup_attempt,
    COALESCE(latest_cleanup.status, '')::text AS cleanup_status,
    COALESCE(latest_cleanup.torrent_removed, false)::boolean AS torrent_removed,
    COALESCE(latest_cleanup.staged_files_removed, false)::boolean AS staged_files_removed,
    latest_cleanup.error_code AS cleanup_error_code,
    latest_cleanup.error_message AS cleanup_error_message,
    latest_cleanup.started_at AS cleanup_started_at,
    latest_cleanup.completed_at AS cleanup_completed_at,
    latest_cleanup.created_at AS cleanup_created_at,
    latest_cleanup.updated_at AS cleanup_updated_at,
    related_emby_item.id AS emby_item_id,
    related_emby_item.library_id AS emby_library_id
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
LEFT JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
LEFT JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN artifact_sets AS artifact_set ON artifact_set.task_id = task.id
LEFT JOIN media_artifacts AS video ON video.id = artifact_set.video_artifact_id
LEFT JOIN media_artifacts AS subtitle ON subtitle.id = artifact_set.subtitle_artifact_id
LEFT JOIN reviews AS review ON review.task_id = task.id
LEFT JOIN LATERAL (
    SELECT import.*
    FROM imports AS import
    WHERE import.task_id = task.id
    ORDER BY import.attempt DESC
    LIMIT 1
) AS latest_import ON true
LEFT JOIN LATERAL (
    SELECT cleanup.*
    FROM cleanup_runs AS cleanup
    WHERE cleanup.task_id = task.id
    ORDER BY cleanup.attempt DESC
    LIMIT 1
) AS latest_cleanup ON true
LEFT JOIN LATERAL (
    SELECT item.id, item.library_id
    FROM emby_library_items AS item
    WHERE item.present
      AND (
          item.file_path = latest_import.destination_video_path
          OR (
              episode.tmdb_episode_id IS NOT NULL
              AND EXISTS (
                  SELECT 1
                  FROM jsonb_each_text(item.provider_ids) AS provider
                  WHERE lower(provider.key) IN ('tmdb', 'themoviedb')
                    AND provider.value = episode.tmdb_episode_id::text
              )
          )
      )
    ORDER BY (item.file_path = latest_import.destination_video_path) DESC, item.id
    LIMIT 1
) AS related_emby_item ON true
WHERE EXISTS (
      SELECT 1 FROM downloads AS source_download
      WHERE source_download.id = source_file.download_id
        AND source_download.deleted_at IS NULL
  )
  AND (sqlc.narg(state_filter)::text IS NULL OR task.state = sqlc.narg(state_filter))
  AND (
      sqlc.narg(phase_filter)::text IS NULL
      OR (sqlc.narg(phase_filter)::text = 'processing' AND task.state IN ('media_queued', 'processing', 'finalizing'))
      OR (sqlc.narg(phase_filter)::text = 'awaiting_review' AND task.state IN ('awaiting_review', 'approved', 'rejected'))
      OR (sqlc.narg(phase_filter)::text = 'importing' AND task.state IN ('import_queued', 'importing'))
      OR (sqlc.narg(phase_filter)::text = 'failed' AND task.state = 'failed')
      OR (
          sqlc.narg(phase_filter)::text = 'cleanup_failed'
          AND task.state = 'imported'
          AND latest_cleanup.status = 'failed'
      )
  )
  AND (
      sqlc.narg(cursor_id)::uuid IS NULL
      OR (task.created_at, task.id) < (
          SELECT cursor.created_at, cursor.id
          FROM episode_tasks AS cursor
          WHERE cursor.id = sqlc.narg(cursor_id)
      )
  )
ORDER BY task.created_at DESC, task.id DESC
LIMIT sqlc.arg(page_size);

-- name: ListTaskOperationSummaries :many
SELECT *
FROM operations
WHERE resource_type = 'episode_task'
  AND resource_id = ANY(sqlc.arg(task_ids)::uuid[])
ORDER BY resource_id, created_at DESC, id DESC;

-- name: GetTaskReviewByTask :one
SELECT *
FROM reviews
WHERE task_id = sqlc.arg(task_id);

-- name: CreateTaskReview :one
INSERT INTO reviews (
    id,
    task_id,
    decision,
    notes,
    reviewed_by,
    idempotency_key,
    expected_task_version
) VALUES (
    sqlc.arg(id),
    sqlc.arg(task_id),
    sqlc.arg(decision),
    sqlc.arg(notes),
    sqlc.arg(reviewed_by),
    sqlc.arg(idempotency_key),
    sqlc.arg(expected_task_version)
)
RETURNING *;

-- name: MarkTaskReviewed :one
UPDATE episode_tasks
SET state = sqlc.arg(decision),
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE episode_tasks.id = sqlc.arg(id)
  AND state = 'awaiting_review'
  AND version = sqlc.arg(expected_version)
  AND video_state = 'video_ready'
  AND subtitle_state = 'ass_ready'
  AND EXISTS (SELECT 1 FROM artifact_sets WHERE task_id = episode_tasks.id)
RETURNING *;

-- name: GetArtifactSetForTask :one
SELECT
    artifact_set.*,
    video.file_path AS video_file_path,
    video.size_bytes AS video_size_bytes,
    video.checksum_sha256 AS video_checksum_sha256,
    subtitle.file_path AS subtitle_file_path,
    subtitle.size_bytes AS subtitle_size_bytes,
    subtitle.checksum_sha256 AS subtitle_checksum_sha256
FROM artifact_sets AS artifact_set
JOIN media_artifacts AS video ON video.id = artifact_set.video_artifact_id AND video.kind = 'video'
JOIN media_artifacts AS subtitle ON subtitle.id = artifact_set.subtitle_artifact_id AND subtitle.kind = 'subtitle' AND lower(subtitle.format) = 'ass'
WHERE artifact_set.task_id = sqlc.arg(task_id);

-- name: CreateTaskImport :one
WITH next_attempt AS (
    SELECT COALESCE(max(attempt), 0) + 1 AS value
    FROM imports
    WHERE task_id = sqlc.arg(task_id)
)
INSERT INTO imports (id, task_id, attempt)
SELECT sqlc.arg(id), sqlc.arg(task_id), next_attempt.value
FROM next_attempt
RETURNING *;

-- name: MarkTaskImportQueued :one
UPDATE episode_tasks
SET state = 'import_queued',
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE episode_tasks.id = sqlc.arg(id)
  AND state = 'approved'
  AND version = sqlc.arg(expected_version)
  AND EXISTS (
      SELECT 1
      FROM reviews
      WHERE task_id = episode_tasks.id
        AND decision = 'approved'
  )
  AND EXISTS (SELECT 1 FROM artifact_sets WHERE task_id = episode_tasks.id)
RETURNING *;

-- name: GetTaskImportCommand :one
SELECT
    task.id AS task_id,
    task.state,
    import.id AS import_id,
    import.status AS import_status,
    task.media_type,
    series.title AS series_title,
    series.release_year,
    season.season_number AS target_season,
    artifact_set.basename,
    video.id AS video_artifact_id,
    video.file_path AS video_file_path,
    video.format AS video_format,
    video.size_bytes AS video_size_bytes,
    video.checksum_sha256 AS video_checksum_sha256,
    subtitle.id AS subtitle_artifact_id,
    subtitle.file_path AS subtitle_file_path,
    subtitle.format AS subtitle_format,
    subtitle.size_bytes AS subtitle_size_bytes,
    subtitle.checksum_sha256 AS subtitle_checksum_sha256
FROM episode_tasks AS task
JOIN imports AS import ON import.task_id = task.id
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
JOIN artifact_sets AS artifact_set ON artifact_set.task_id = task.id
JOIN media_artifacts AS video ON video.id = artifact_set.video_artifact_id AND video.kind = 'video'
JOIN media_artifacts AS subtitle ON subtitle.id = artifact_set.subtitle_artifact_id AND subtitle.kind = 'subtitle' AND lower(subtitle.format) = 'ass'
WHERE task.id = sqlc.arg(task_id)
  AND import.id = sqlc.arg(import_id);

-- name: StartTaskImport :one
UPDATE episode_tasks
SET state = 'importing',
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state IN ('import_queued', 'importing')
RETURNING *;

-- name: StartImport :one
UPDATE imports
SET status = 'running',
    started_at = COALESCE(started_at, now()),
    error_code = NULL,
    error_message = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: CompleteImport :one
UPDATE imports
SET status = 'succeeded',
    destination_video_path = sqlc.arg(destination_video_path),
    destination_subtitle_path = sqlc.arg(destination_subtitle_path),
    error_code = NULL,
    error_message = NULL,
    completed_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
RETURNING *;

-- name: MarkTaskImported :one
UPDATE episode_tasks
SET state = 'imported',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state = 'importing'
RETURNING *;

-- name: GetTaskDownloadID :one
SELECT download.id
FROM episode_tasks AS task
JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
JOIN downloads AS download ON download.id = source_file.download_id
WHERE task.id = sqlc.arg(task_id);

-- name: CreateTaskCleanup :one
WITH next_attempt AS (
    SELECT COALESCE(max(attempt), 0) + 1 AS value
    FROM cleanup_runs
    WHERE task_id = sqlc.arg(task_id)
)
INSERT INTO cleanup_runs (id, task_id, download_id, attempt)
SELECT sqlc.arg(id), sqlc.arg(task_id), sqlc.arg(download_id), next_attempt.value
FROM next_attempt
RETURNING *;

-- name: GetTaskCleanupCommand :one
SELECT
    task.id AS task_id,
    task.state AS task_state,
    cleanup.id AS cleanup_id,
    cleanup.status AS cleanup_status,
    download.id AS download_id,
    download.torrent_hash,
    download.save_path,
    video.file_path AS staged_video_path,
    subtitle.file_path AS staged_subtitle_path,
    NOT EXISTS (
        SELECT 1
        FROM episode_tasks AS sibling
        JOIN download_files AS sibling_file ON sibling_file.id = sibling.source_video_file_id
        WHERE sibling_file.download_id = download.id
          AND sibling.state NOT IN ('imported', 'rejected', 'cancelled')
    ) AS download_removable
FROM episode_tasks AS task
JOIN cleanup_runs AS cleanup ON cleanup.task_id = task.id
JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
JOIN downloads AS download ON download.id = source_file.download_id
JOIN artifact_sets AS artifact_set ON artifact_set.task_id = task.id
JOIN media_artifacts AS video ON video.id = artifact_set.video_artifact_id
JOIN media_artifacts AS subtitle ON subtitle.id = artifact_set.subtitle_artifact_id
JOIN imports AS import ON import.task_id = task.id AND import.status = 'succeeded'
WHERE task.id = sqlc.arg(task_id)
  AND cleanup.id = sqlc.arg(cleanup_id);

-- name: StartCleanup :one
UPDATE cleanup_runs
SET status = 'running',
    started_at = COALESCE(started_at, now()),
    error_code = NULL,
    error_message = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: CompleteCleanup :one
UPDATE cleanup_runs
SET status = 'completed',
    torrent_removed = sqlc.arg(torrent_removed),
    staged_files_removed = sqlc.arg(staged_files_removed),
    error_code = NULL,
    error_message = NULL,
    completed_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
RETURNING *;

-- name: MarkActiveImportTerminalFailure :one
UPDATE imports
SET status = 'failed',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    completed_at = now(),
    updated_at = now()
WHERE imports.id = (
    SELECT candidate.id
    FROM imports AS candidate
    WHERE candidate.task_id = sqlc.arg(task_id)
      AND candidate.status IN ('queued', 'running')
    ORDER BY candidate.attempt DESC
    LIMIT 1
)
RETURNING *;

-- name: MarkTaskImportTerminalFailure :one
UPDATE episode_tasks
SET state = 'failed',
    failure_stage = 'import',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    version = version + 1,
    updated_at = now()
WHERE episode_tasks.id = sqlc.arg(id)
  AND state IN ('import_queued', 'importing')
RETURNING *;

-- name: MarkActiveCleanupTerminalFailure :one
UPDATE cleanup_runs
SET status = 'failed',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    completed_at = now(),
    updated_at = now()
WHERE cleanup_runs.id = (
    SELECT candidate.id
    FROM cleanup_runs AS candidate
    WHERE candidate.task_id = sqlc.arg(task_id)
      AND candidate.status IN ('queued', 'running')
    ORDER BY candidate.attempt DESC
    LIMIT 1
)
RETURNING *;

-- name: MarkTaskCleanupRetryRequested :one
UPDATE episode_tasks
SET version = version + 1,
    updated_at = now()
WHERE episode_tasks.id = sqlc.arg(id)
  AND episode_tasks.state = 'imported'
  AND episode_tasks.version = sqlc.arg(expected_version)
  AND EXISTS (
      SELECT 1 FROM cleanup_runs
      WHERE cleanup_runs.task_id = episode_tasks.id
        AND cleanup_runs.status = 'failed'
  )
RETURNING *;

-- name: RequeueLatestFailedTaskCleanup :one
UPDATE cleanup_runs
SET status = 'queued',
    error_code = NULL,
    error_message = NULL,
    started_at = NULL,
    completed_at = NULL,
    updated_at = now()
WHERE cleanup_runs.id = (
    SELECT candidate.id
    FROM cleanup_runs AS candidate
    WHERE candidate.task_id = sqlc.arg(task_id)
      AND candidate.status = 'failed'
    ORDER BY candidate.attempt DESC
    LIMIT 1
)
RETURNING *;

-- name: RequeueLatestFailedTaskImport :one
UPDATE imports
SET status = 'queued',
    error_code = NULL,
    error_message = NULL,
    started_at = NULL,
    completed_at = NULL,
    updated_at = now()
WHERE imports.id = (
    SELECT candidate.id
    FROM imports AS candidate
    WHERE candidate.task_id = sqlc.arg(task_id)
      AND candidate.status = 'failed'
    ORDER BY candidate.attempt DESC
    LIMIT 1
)
RETURNING *;

-- name: CancelActiveImport :one
UPDATE imports
SET status = 'cancelled',
    completed_at = now(),
    updated_at = now()
WHERE imports.id = (
    SELECT candidate.id
    FROM imports AS candidate
    WHERE candidate.task_id = sqlc.arg(task_id)
      AND candidate.status IN ('queued', 'running')
    ORDER BY candidate.attempt DESC
    LIMIT 1
)
RETURNING *;

-- name: CancelTaskImport :one
UPDATE episode_tasks
SET state = 'cancelled',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE episode_tasks.id = sqlc.arg(id)
  AND state IN ('import_queued', 'importing')
RETURNING *;

-- name: CancelActiveCleanup :one
UPDATE cleanup_runs
SET status = 'cancelled',
    completed_at = now(),
    updated_at = now()
WHERE cleanup_runs.id = (
    SELECT candidate.id
    FROM cleanup_runs AS candidate
    WHERE candidate.task_id = sqlc.arg(task_id)
      AND candidate.status IN ('queued', 'running')
    ORDER BY candidate.attempt DESC
    LIMIT 1
)
RETURNING *;

-- name: ListActiveTaskIDsForAcquisition :many
SELECT task.id
FROM episode_tasks AS task
WHERE task.acquisition_id = sqlc.arg(acquisition_id)
  AND task.state NOT IN ('imported', 'rejected', 'cancelled')
ORDER BY task.id;

-- name: GetEpisodeTaskVersion :one
SELECT task.version
FROM episode_tasks AS task
WHERE task.id = sqlc.arg(id);
