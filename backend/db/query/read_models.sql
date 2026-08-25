-- name: ListDownloads :many
SELECT download.*
FROM downloads AS download
WHERE download.deleted_at IS NULL
  AND (sqlc.narg(status)::text IS NULL OR download.status = sqlc.narg(status)::text)
  AND (
      sqlc.narg(phase)::text IS NULL
      OR (sqlc.narg(phase)::text = 'active' AND download.status IN ('enqueue_pending', 'file_resolution_pending', 'downloading'))
      OR (
          sqlc.narg(phase)::text = 'waiting'
          AND (
              download.status IN ('enqueue_pending', 'file_resolution_pending')
              OR (download.status = 'downloading' AND download.client_state IN ('metaDL', 'queuedDL', 'stalledDL'))
          )
      )
      OR (
          sqlc.narg(phase)::text = 'downloading'
          AND download.status = 'downloading'
          AND (
              download.client_state IS NULL
              OR download.client_state NOT IN ('metaDL', 'queuedDL', 'stalledDL', 'pausedDL', 'stoppedDL')
          )
      )
      OR (
          sqlc.narg(phase)::text = 'paused'
          AND download.status = 'downloading'
          AND download.client_state IN ('pausedDL', 'stoppedDL')
      )
      OR (
          sqlc.narg(phase)::text = 'completed'
          AND (
              download.status IN ('completed', 'selecting_files', 'materialized')
              OR (download.status = 'failed' AND download.failure_stage = 'materialize' AND download.progress = 1)
          )
      )
      OR (
          sqlc.narg(phase)::text = 'failed'
          AND download.status = 'failed'
          AND download.failure_stage IS DISTINCT FROM 'materialize'
      )
  )
  AND (
      sqlc.narg(query)::text IS NULL
      OR download.id::text ILIKE '%' || sqlc.narg(query)::text || '%'
      OR COALESCE(download.save_path, '') ILIKE '%' || sqlc.narg(query)::text || '%'
      OR COALESCE(download.torrent_hash, '') ILIKE '%' || sqlc.narg(query)::text || '%'
      OR EXISTS (
          SELECT 1
          FROM acquisitions AS acquisition
          JOIN media_series AS media ON media.id = acquisition.series_id
          WHERE acquisition.id = download.acquisition_id
            AND (media.title ILIKE '%' || sqlc.narg(query)::text || '%' OR COALESCE(media.original_title, '') ILIKE '%' || sqlc.narg(query)::text || '%')
      )
  )
  AND (
      sqlc.narg(cursor)::uuid IS NULL
      OR (
          COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest'
          AND (download.created_at, download.id) > (
              SELECT cursor_download.created_at, cursor_download.id
              FROM downloads AS cursor_download
              WHERE cursor_download.id = sqlc.narg(cursor)::uuid
          )
      )
      OR (
          COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest'
          AND (download.created_at, download.id) < (
              SELECT cursor_download.created_at, cursor_download.id
              FROM downloads AS cursor_download
              WHERE cursor_download.id = sqlc.narg(cursor)::uuid
          )
      )
  )
ORDER BY
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest' THEN download.created_at END ASC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest' THEN download.created_at END DESC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest' THEN download.id END ASC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest' THEN download.id END DESC
LIMIT sqlc.arg(row_limit);

-- name: GetDownloadByID :one
SELECT *
FROM downloads
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: GetDownloadByIDIncludingDeleted :one
SELECT *
FROM downloads
WHERE id = sqlc.arg(id);

-- name: LockDownloadByID :one
SELECT *
FROM downloads
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: ListDownloadFiles :many
SELECT *
FROM download_files
WHERE download_id = sqlc.arg(download_id)
ORDER BY file_index;

-- name: GetLatestDownloadByAcquisition :one
SELECT *
FROM downloads
WHERE acquisition_id = sqlc.arg(acquisition_id)
  AND deleted_at IS NULL
ORDER BY (status = 'cancelled'), attempt DESC
LIMIT 1;

-- name: ListLatestDownloadsByAcquisitionIDs :many
SELECT DISTINCT ON (acquisition_id) *
FROM downloads
WHERE acquisition_id = ANY(sqlc.arg(acquisition_ids)::uuid[])
  AND deleted_at IS NULL
ORDER BY acquisition_id, (status = 'cancelled'), attempt DESC;

-- name: ListAcquisitionSourceTitles :many
SELECT
    acquisition.id AS acquisition_id,
    COALESCE(candidate.title, entry.title, '')::text AS source_title
FROM acquisitions AS acquisition
LEFT JOIN release_candidates AS candidate ON candidate.id = acquisition.release_candidate_id
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE acquisition.id = ANY(sqlc.arg(acquisition_ids)::uuid[])
ORDER BY acquisition.id;

-- name: ListAcquisitions :many
SELECT *
FROM acquisitions
WHERE acquisitions.deletion_requested_at IS NULL
  AND (
      NOT EXISTS (SELECT 1 FROM downloads WHERE downloads.acquisition_id = acquisitions.id)
      OR EXISTS (
          SELECT 1 FROM downloads
          WHERE downloads.acquisition_id = acquisitions.id
            AND downloads.deleted_at IS NULL
            AND downloads.status <> 'cancelled'
      )
      OR EXISTS (
          SELECT 1
          FROM episode_tasks AS task
          JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
          JOIN downloads AS source_download ON source_download.id = source_file.download_id
          WHERE task.acquisition_id = acquisitions.id
            AND source_download.deleted_at IS NULL
      )
  )
  AND (sqlc.narg(source_kind)::text IS NULL OR source_kind = sqlc.narg(source_kind)::text)
  AND (
      sqlc.narg(tmdb_series_id)::bigint IS NULL
      OR series_id IN (SELECT id FROM media_series WHERE tmdb_series_id = sqlc.narg(tmdb_series_id)::bigint)
  )
  AND (
      sqlc.narg(phase)::text IS NULL
      OR (
          sqlc.narg(phase)::text = 'mapping_pending'
          AND acquisitions.series_id IN (SELECT id FROM media_series WHERE media_type = 'tv')
          AND EXISTS (
              SELECT 1
              FROM downloads AS download
              WHERE download.id = (
                    SELECT candidate.id
                    FROM downloads AS candidate
                    WHERE candidate.acquisition_id = acquisitions.id
                      AND candidate.deleted_at IS NULL
                    ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
                    LIMIT 1
                )
                AND (
                    download.status IN ('completed', 'selecting_files', 'materialized')
                    OR (
                        download.status = 'failed'
                        AND download.failure_stage = 'materialize'
                        AND download.error_code IN (
                            'mapping_profile_required',
                            'episode_mapping_required',
                            'mapping_source_invalid',
                            'mapping_source_out_of_range',
                            'mapping_context_incomplete',
                            'mapping_target_out_of_range',
                            'mapping_title_missing'
                        )
                    )
                )
                AND (
                    NOT EXISTS (
                        SELECT 1 FROM download_files AS file
                        WHERE file.download_id = download.id AND file.selected AND file.media_kind = 'video'
                    )
                    OR EXISTS (
                        SELECT 1
                        FROM download_files AS file
                        WHERE file.download_id = download.id
                          AND file.selected
                          AND file.media_kind = 'video'
                          AND NOT EXISTS (
                              SELECT 1
                              FROM episode_mappings AS mapping
                              WHERE mapping.profile_id = acquisitions.mapping_profile_id
                                AND mapping.source_season = file.source_season
                                AND mapping.source_episode = file.source_episode
                                AND mapping.mapping_status = 'mapped'
                                AND mapping.target_episode_id IS NOT NULL
                          )
                    )
                )
          )
      )
  )
  AND (
      sqlc.narg(cursor)::uuid IS NULL
      OR (
          COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest'
          AND (acquisitions.created_at, acquisitions.id) > (
              SELECT cursor_acquisition.created_at, cursor_acquisition.id
              FROM acquisitions AS cursor_acquisition
              WHERE cursor_acquisition.id = sqlc.narg(cursor)::uuid
          )
      )
      OR (
          COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest'
          AND (acquisitions.created_at, acquisitions.id) < (
              SELECT cursor_acquisition.created_at, cursor_acquisition.id
              FROM acquisitions AS cursor_acquisition
              WHERE cursor_acquisition.id = sqlc.narg(cursor)::uuid
          )
      )
  )
ORDER BY
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest' THEN acquisitions.created_at END ASC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest' THEN acquisitions.created_at END DESC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest' THEN acquisitions.id END ASC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest' THEN acquisitions.id END DESC
LIMIT sqlc.arg(row_limit);

-- name: GetAcquisitionByID :one
SELECT *
FROM acquisitions
WHERE acquisitions.id = sqlc.arg(id)
  AND (
      NOT EXISTS (SELECT 1 FROM downloads WHERE downloads.acquisition_id = acquisitions.id)
      OR EXISTS (SELECT 1 FROM downloads WHERE downloads.acquisition_id = acquisitions.id AND downloads.deleted_at IS NULL)
  );

-- name: GetArchivedRSSAcquisitionByID :one
WITH successful_history AS (
    SELECT
        provenance.rss_entry_id AS entry_id,
        provenance.acquisition_id,
        provenance.download_id,
        provenance.acquisition_created_at,
        provenance.task_id,
        provenance.task_created_at,
        provenance.video_ready_at,
        provenance.subtitle_ready_at,
        provenance.artifact_ready_at,
        provenance.reviewed_at,
        provenance.imported_at,
        provenance.archived_at
    FROM rss_acquisition_provenance AS provenance
    JOIN rss_entries AS entry ON entry.id = provenance.rss_entry_id
    WHERE provenance.acquisition_id = sqlc.arg(id)::uuid
      AND entry.imported_at IS NOT NULL
      AND provenance.task_id IS NOT NULL
      AND provenance.imported_at IS NOT NULL
)
SELECT
    history.acquisition_id,
    history.entry_id,
    entry.subscription_id,
    subscription.series_id,
    series.tmdb_series_id,
    series.title AS series_title,
    entry.title AS source_title,
    subscription.mapping_profile_id,
    history.task_id,
    history.download_id,
    entry.source_season,
    entry.source_episode,
    target_season.season_number AS target_season,
    target_episode.episode_number AS target_episode,
    target_episode.title AS target_episode_title,
    history.acquisition_created_at,
    history.task_created_at,
    history.video_ready_at,
    history.subtitle_ready_at,
    history.artifact_ready_at,
    history.reviewed_at,
    history.imported_at,
    COALESCE(subscription.completed_at, history.archived_at, history.imported_at)::timestamptz AS archived_at
FROM successful_history AS history
JOIN rss_entries AS entry ON entry.id = history.entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
JOIN media_series AS series ON series.id = subscription.series_id
LEFT JOIN episode_mappings AS mapping
  ON mapping.profile_id = subscription.mapping_profile_id
 AND mapping.source_season = entry.source_season
 AND mapping.source_episode = entry.source_episode
 AND mapping.mapping_status = 'mapped'
LEFT JOIN media_episodes AS target_episode ON target_episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS target_season ON target_season.id = target_episode.season_id
LIMIT 1;

-- name: ListAcquisitionTaskSummaries :many
SELECT
    task.id,
    task.media_type,
    source_file.download_id,
    source_file.source_season,
    source_file.source_episode,
    season.season_number AS target_season,
    episode.episode_number AS target_episode,
    episode.title AS target_episode_title,
    task.state,
    task.video_state,
    task.subtitle_state,
    artifact_set.basename AS artifact_basename,
    review.decision AS review_decision,
    review.reviewed_at,
    COALESCE(latest_import.status, '')::text AS import_status,
    latest_import.destination_video_path,
    latest_import.destination_subtitle_path,
    COALESCE(latest_refresh.status, '')::text AS emby_refresh_status,
    COALESCE(latest_cleanup.status, '')::text AS cleanup_status,
    task.failure_stage,
    task.error_code,
    task.error_message,
    GREATEST(
        task.updated_at,
        review.reviewed_at,
        latest_import.updated_at,
        latest_refresh.updated_at,
        latest_cleanup.updated_at
    )::timestamptz AS updated_at
FROM episode_tasks AS task
JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
JOIN downloads AS source_download ON source_download.id = source_file.download_id AND source_download.deleted_at IS NULL
LEFT JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
LEFT JOIN artifact_sets AS artifact_set ON artifact_set.task_id = task.id
LEFT JOIN reviews AS review ON review.task_id = task.id
LEFT JOIN LATERAL (
    SELECT import.*
    FROM imports AS import
    WHERE import.task_id = task.id
    ORDER BY import.attempt DESC
    LIMIT 1
) AS latest_import ON true
LEFT JOIN LATERAL (
    SELECT operation.*
    FROM operations AS operation
    WHERE operation.resource_type = 'episode_task'
      AND operation.resource_id = task.id
      AND operation.kind = 'emby.refresh'
    ORDER BY operation.created_at DESC, operation.id DESC
    LIMIT 1
) AS latest_refresh ON true
LEFT JOIN LATERAL (
    SELECT cleanup.*
    FROM cleanup_runs AS cleanup
    WHERE cleanup.task_id = task.id
    ORDER BY cleanup.attempt DESC
    LIMIT 1
) AS latest_cleanup ON true
WHERE task.acquisition_id = sqlc.arg(acquisition_id)
ORDER BY source_file.source_season, source_file.source_episode, task.id;

-- name: ListAcquisitionTaskSummariesByAcquisitionIDs :many
SELECT
    task.acquisition_id,
    task.id,
    task.media_type,
    source_file.download_id,
    source_file.source_season,
    source_file.source_episode,
    season.season_number AS target_season,
    episode.episode_number AS target_episode,
    episode.title AS target_episode_title,
    task.state,
    task.video_state,
    task.subtitle_state,
    artifact_set.basename AS artifact_basename,
    review.decision AS review_decision,
    review.reviewed_at,
    COALESCE(latest_import.status, '')::text AS import_status,
    latest_import.destination_video_path,
    latest_import.destination_subtitle_path,
    COALESCE(latest_refresh.status, '')::text AS emby_refresh_status,
    COALESCE(latest_cleanup.status, '')::text AS cleanup_status,
    task.failure_stage,
    task.error_code,
    task.error_message,
    GREATEST(
        task.updated_at,
        review.reviewed_at,
        latest_import.updated_at,
        latest_refresh.updated_at,
        latest_cleanup.updated_at
    )::timestamptz AS updated_at
FROM episode_tasks AS task
JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
JOIN downloads AS source_download ON source_download.id = source_file.download_id AND source_download.deleted_at IS NULL
LEFT JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
LEFT JOIN artifact_sets AS artifact_set ON artifact_set.task_id = task.id
LEFT JOIN reviews AS review ON review.task_id = task.id
LEFT JOIN LATERAL (
    SELECT import.*
    FROM imports AS import
    WHERE import.task_id = task.id
    ORDER BY import.attempt DESC
    LIMIT 1
) AS latest_import ON true
LEFT JOIN LATERAL (
    SELECT operation.*
    FROM operations AS operation
    WHERE operation.resource_type = 'episode_task'
      AND operation.resource_id = task.id
      AND operation.kind = 'emby.refresh'
    ORDER BY operation.created_at DESC, operation.id DESC
    LIMIT 1
) AS latest_refresh ON true
LEFT JOIN LATERAL (
    SELECT cleanup.*
    FROM cleanup_runs AS cleanup
    WHERE cleanup.task_id = task.id
    ORDER BY cleanup.attempt DESC
    LIMIT 1
) AS latest_cleanup ON true
WHERE task.acquisition_id = ANY(sqlc.arg(acquisition_ids)::uuid[])
ORDER BY task.acquisition_id, source_file.source_season, source_file.source_episode, task.id;

-- name: GetAcquisitionMappingCompleteness :one
SELECT
    count(file.id)::bigint AS selected_video_count,
    CASE WHEN media.media_type = 'movie' THEN count(file.id)::bigint ELSE
        count(mapping.id) FILTER (
            WHERE mapping.mapping_status = 'mapped' AND mapping.target_episode_id IS NOT NULL
        )::bigint
    END AS mapped_video_count
FROM acquisitions AS acquisition
JOIN media_series AS media ON media.id = acquisition.series_id
LEFT JOIN downloads AS download
    ON download.id = (
        SELECT candidate.id
        FROM downloads AS candidate
        WHERE candidate.acquisition_id = acquisition.id
          AND candidate.deleted_at IS NULL
        ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
        LIMIT 1
    )
LEFT JOIN download_files AS file
    ON file.download_id = download.id
   AND file.selected
   AND file.media_kind = 'video'
LEFT JOIN episode_mappings AS mapping
    ON mapping.profile_id = acquisition.mapping_profile_id
   AND mapping.source_season = file.source_season
   AND mapping.source_episode = file.source_episode
WHERE acquisition.id = sqlc.arg(acquisition_id)
GROUP BY media.media_type;

-- name: GetAcquisitionMappingCompletenessByAcquisitionIDs :many
SELECT
    acquisition.id,
    count(file.id)::bigint AS selected_video_count,
    CASE WHEN media.media_type = 'movie' THEN count(file.id)::bigint ELSE
        count(mapping.id) FILTER (
            WHERE mapping.mapping_status = 'mapped' AND mapping.target_episode_id IS NOT NULL
        )::bigint
    END AS mapped_video_count
FROM acquisitions AS acquisition
JOIN media_series AS media ON media.id = acquisition.series_id
LEFT JOIN downloads AS download
    ON download.id = (
        SELECT candidate.id
        FROM downloads AS candidate
        WHERE candidate.acquisition_id = acquisition.id
          AND candidate.deleted_at IS NULL
        ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
        LIMIT 1
    )
LEFT JOIN download_files AS file
    ON file.download_id = download.id
   AND file.selected
   AND file.media_kind = 'video'
LEFT JOIN episode_mappings AS mapping
    ON mapping.profile_id = acquisition.mapping_profile_id
   AND mapping.source_season = file.source_season
   AND mapping.source_episode = file.source_episode
WHERE acquisition.id = ANY(sqlc.arg(acquisition_ids)::uuid[])
GROUP BY acquisition.id, media.media_type;

-- name: ListRSSEntries :many
SELECT
    entry.*,
    adjudication.batch_id AS adjudication_batch_id,
    COALESCE(adjudication.state, 'not_required')::text AS adjudication_state,
    adjudication.source AS adjudication_source,
    adjudication.resolution_id AS adjudication_resolution_id,
    adjudication.related_entry_id,
    EXISTS (
        SELECT 1
        FROM rss_acquisition_provenance AS provenance
        WHERE provenance.rss_entry_id = entry.id
          AND provenance.task_id IS NOT NULL
          AND provenance.imported_at IS NOT NULL
    ) AS successful_import_present
FROM rss_entries AS entry
LEFT JOIN rss_entry_adjudications AS adjudication ON adjudication.entry_id = entry.id
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND (sqlc.narg(status)::text IS NULL OR entry.status = sqlc.narg(status)::text)
  AND (
      sqlc.narg(cursor)::uuid IS NULL
      OR (
          COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest'
          AND (entry.discovered_at, entry.id) > (
              SELECT cursor_entry.discovered_at, cursor_entry.id
              FROM rss_entries AS cursor_entry
              WHERE cursor_entry.id = sqlc.narg(cursor)::uuid
          )
      )
      OR (
          COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest'
          AND (entry.discovered_at, entry.id) < (
              SELECT cursor_entry.discovered_at, cursor_entry.id
              FROM rss_entries AS cursor_entry
              WHERE cursor_entry.id = sqlc.narg(cursor)::uuid
          )
      )
  )
ORDER BY
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest' THEN entry.discovered_at END ASC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest' THEN entry.discovered_at END DESC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest' THEN entry.id END ASC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest' THEN entry.id END DESC
LIMIT sqlc.arg(row_limit);

-- name: GetRSSEntryRelations :one
SELECT
    acquisition.id AS acquisition_id,
    latest_download.id AS download_id
FROM acquisitions AS acquisition
LEFT JOIN LATERAL (
    SELECT download.id
    FROM downloads AS download
    WHERE download.acquisition_id = acquisition.id
      AND download.deleted_at IS NULL
    ORDER BY (download.status = 'cancelled'), download.attempt DESC
    LIMIT 1
) AS latest_download ON true
WHERE acquisition.rss_entry_id = sqlc.arg(rss_entry_id)
  AND latest_download.id IS NOT NULL;

-- name: ListOperations :many
SELECT *
FROM operations
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(resource_type)::text IS NULL OR resource_type = sqlc.narg(resource_type)::text)
  AND (sqlc.narg(resource_id)::uuid IS NULL OR resource_id = sqlc.narg(resource_id)::uuid)
  AND (
      sqlc.narg(cursor)::uuid IS NULL
      OR (created_at, id) < (
          SELECT cursor_operation.created_at, cursor_operation.id
          FROM operations AS cursor_operation
          WHERE cursor_operation.id = sqlc.narg(cursor)::uuid
      )
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetOperationByID :one
SELECT *
FROM operations
WHERE id = sqlc.arg(id);

-- name: ListOperationAttempts :many
SELECT *
FROM operation_attempts
WHERE operation_id = sqlc.arg(operation_id)
ORDER BY attempt;

-- name: ListResourceEvents :many
SELECT event.*
FROM events AS event
WHERE event.resource_type = sqlc.arg(resource_type)
  AND event.resource_id = sqlc.arg(resource_id)::uuid
  AND (
      sqlc.narg(cursor)::uuid IS NULL
      OR event.event_sequence < (
          SELECT cursor_event.event_sequence
          FROM events AS cursor_event
          WHERE cursor_event.id = sqlc.narg(cursor)::uuid
      )
  )
ORDER BY event.event_sequence DESC
LIMIT sqlc.arg(row_limit);

-- name: DashboardTaskCounts :one
SELECT
    count(*) FILTER (WHERE state IN ('media_queued', 'processing', 'finalizing'))::bigint AS processing,
    count(*) FILTER (WHERE state = 'awaiting_review')::bigint AS awaiting_review,
    count(*) FILTER (WHERE state IN ('import_queued', 'importing'))::bigint AS importing,
    count(*) FILTER (WHERE state = 'failed')::bigint AS failed
FROM episode_tasks AS task
JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
JOIN downloads AS source_download ON source_download.id = source_file.download_id
WHERE source_download.deleted_at IS NULL;

-- name: DashboardDownloadCounts :one
SELECT
    count(*) FILTER (WHERE status IN ('enqueue_pending', 'file_resolution_pending', 'downloading'))::bigint AS downloading,
    count(*) FILTER (
        WHERE status = 'failed'
          AND NOT COALESCE(
              failure_stage = 'materialize'
              AND error_code IN (
                  'mapping_profile_required',
                  'episode_mapping_required',
                  'mapping_source_invalid',
                  'mapping_source_out_of_range',
                  'mapping_context_incomplete',
                  'mapping_target_out_of_range',
                  'mapping_title_missing'
              ),
              false
          )
    )::bigint AS failed
FROM downloads
WHERE deleted_at IS NULL;

-- name: DashboardCleanupFailedCount :one
SELECT count(*)::bigint
FROM cleanup_runs
WHERE status = 'failed';

-- name: DashboardMappingPendingCount :one
SELECT count(*)::bigint
FROM acquisitions AS acquisition
JOIN media_series AS media ON media.id = acquisition.series_id AND media.media_type = 'tv'
WHERE EXISTS (
    SELECT 1
    FROM downloads AS download
    WHERE download.id = (
          SELECT candidate.id
          FROM downloads AS candidate
          WHERE candidate.acquisition_id = acquisition.id
            AND candidate.deleted_at IS NULL
          ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
          LIMIT 1
      )
      AND (
          download.status IN ('completed', 'selecting_files', 'materialized')
          OR (
              download.status = 'failed'
              AND download.failure_stage = 'materialize'
              AND download.error_code IN (
                  'mapping_profile_required',
                  'episode_mapping_required',
                  'mapping_source_invalid',
                  'mapping_source_out_of_range',
                  'mapping_context_incomplete',
                  'mapping_target_out_of_range',
                  'mapping_title_missing'
              )
          )
      )
      AND (
          NOT EXISTS (
              SELECT 1 FROM download_files AS file
              WHERE file.download_id = download.id AND file.selected AND file.media_kind = 'video'
          )
          OR EXISTS (
              SELECT 1
              FROM download_files AS file
              WHERE file.download_id = download.id
                AND file.selected
                AND file.media_kind = 'video'
                AND NOT EXISTS (
                    SELECT 1
                    FROM episode_mappings AS mapping
                    WHERE mapping.profile_id = acquisition.mapping_profile_id
                      AND mapping.source_season = file.source_season
                      AND mapping.source_episode = file.source_episode
                      AND mapping.mapping_status = 'mapped'
                      AND mapping.target_episode_id IS NOT NULL
                )
          )
      )
);

-- name: DashboardRecentOperations :many
SELECT *
FROM operations
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: DashboardRecentImports :many
SELECT
    task.id AS task_id,
    acquisition.id AS acquisition_id,
    task.media_type,
    series.title AS series_title,
    series.release_year,
    season.season_number,
    episode.episode_number,
    latest_import.destination_video_path,
    latest_import.completed_at
FROM imports AS latest_import
JOIN episode_tasks AS task ON task.id = latest_import.task_id
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE latest_import.status = 'succeeded'
ORDER BY latest_import.completed_at DESC, latest_import.id DESC
LIMIT sqlc.arg(row_limit);

-- name: DashboardRecentScans :many
SELECT *
FROM emby_scan_runs
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListSearchRuns :many
SELECT *
FROM search_runs
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(query)::text IS NULL OR query ILIKE '%' || sqlc.narg(query)::text || '%')
  AND (
      sqlc.narg(cursor)::uuid IS NULL
      OR (created_at, id) < (
          SELECT cursor_search.created_at, cursor_search.id
          FROM search_runs AS cursor_search
          WHERE cursor_search.id = sqlc.narg(cursor)::uuid
      )
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);
