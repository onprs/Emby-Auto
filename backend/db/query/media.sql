-- name: LockMaterializeAcquisitionForDownload :one
SELECT acquisition.id
FROM downloads AS download
JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
WHERE download.id = sqlc.arg(download_id)
FOR UPDATE OF acquisition;

-- name: LockDownloadForMaterialize :one
SELECT
    download.*,
    acquisition.id AS workflow_acquisition_id,
    acquisition.mapping_profile_id,
    media.id AS media_id,
    media.media_type,
    media.title AS media_title,
    media.release_year,
    media.tmdb_movie_id
FROM downloads AS download
JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
JOIN media_series AS media ON media.id = acquisition.series_id
WHERE download.id = sqlc.arg(id)
FOR UPDATE OF download;

-- name: GetDefaultTranscodeProfile :one
SELECT *
FROM transcode_profiles
WHERE active
  AND is_default
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListMaterializeVideos :many
SELECT
    file.id AS file_id,
    file.relative_path,
    file.size_bytes,
    file.source_season,
    file.source_episode,
    mapping.id AS mapping_id,
    mapping.mapping_status,
    mapping.error_code AS mapping_error_code,
    episode.id AS target_episode_id,
    episode.episode_number AS target_episode_number,
    episode.title AS target_episode_title,
    season.season_number AS target_season_number,
    series.title AS series_title,
    series.media_type,
    series.release_year,
    series.tmdb_movie_id
FROM download_files AS file
JOIN downloads AS download ON download.id = file.download_id
JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN episode_mappings AS mapping
    ON mapping.profile_id = acquisition.mapping_profile_id
   AND mapping.source_season = file.source_season
   AND mapping.source_episode = file.source_episode
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE file.download_id = sqlc.arg(download_id)
  AND file.selected
  AND file.media_kind = 'video'
ORDER BY file.source_season, file.source_episode, file.file_index;

-- name: MarkDownloadSelectingFiles :one
UPDATE downloads
SET status = 'selecting_files',
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'completed'
RETURNING *;

-- name: CreateEpisodeTask :one
INSERT INTO episode_tasks (
    id,
    acquisition_id,
    source_video_file_id,
    mapping_id,
    transcode_profile_id,
    media_type
) VALUES (
    sqlc.arg(id),
    sqlc.arg(acquisition_id),
    sqlc.arg(source_video_file_id),
    sqlc.narg(mapping_id),
    sqlc.arg(transcode_profile_id),
    sqlc.arg(media_type)
)
ON CONFLICT (acquisition_id, source_video_file_id) DO NOTHING
RETURNING *;

-- name: GetEpisodeTaskBySource :one
SELECT *
FROM episode_tasks
WHERE acquisition_id = sqlc.arg(acquisition_id)
  AND source_video_file_id = sqlc.arg(source_video_file_id);

-- name: MarkDownloadMaterialized :one
UPDATE downloads
SET status = 'materialized',
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'selecting_files'
RETURNING *;

-- name: LockEpisodeTask :one
SELECT *
FROM episode_tasks
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: StartTaskVideo :one
UPDATE episode_tasks
SET state = CASE WHEN state = 'media_queued' THEN 'processing' ELSE state END,
    video_state = 'transcoding',
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state IN ('media_queued', 'processing', 'failed')
  AND video_state IN ('transcode_queued', 'transcoding')
RETURNING *;

-- name: StartTaskSubtitle :one
UPDATE episode_tasks
SET state = CASE WHEN state = 'media_queued' THEN 'processing' ELSE state END,
    subtitle_state = 'extracting_or_converting',
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state IN ('media_queued', 'processing', 'failed')
  AND subtitle_state IN ('subtitle_queued', 'extracting_or_converting')
RETURNING *;

-- name: GetTaskMediaCommand :one
SELECT
    task.id AS task_id,
    task.state,
    task.video_state,
    task.subtitle_state,
    task.transcode_profile_id,
    task.media_type,
    video.id AS source_video_file_id,
    video.relative_path AS source_video_relative_path,
    video.source_season,
    video.source_episode,
    download.id AS download_id,
    download.save_path,
    mapping.id AS mapping_id,
    episode.episode_number AS target_episode_number,
    episode.title AS target_episode_title,
    season.season_number AS target_season_number,
    series.title AS series_title,
    series.release_year,
    series.tmdb_movie_id,
    profile.name AS profile_name,
    profile.video_codec,
    profile.encoder,
    profile.container,
    profile.file_extension,
    profile.quality_mode,
    profile.quality_value,
    profile.audio_policy,
    profile.audio_codec,
    profile.preset,
    profile.pixel_format,
    profile.thread_count,
    profile.max_concurrency
FROM episode_tasks AS task
JOIN download_files AS video ON video.id = task.source_video_file_id
JOIN downloads AS download ON download.id = video.download_id
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id AND mapping.mapping_status = 'mapped'
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
JOIN transcode_profiles AS profile ON profile.id = task.transcode_profile_id
WHERE task.id = sqlc.arg(id);

-- name: ListTaskExternalSubtitles :many
SELECT
    subtitle.id,
    subtitle.relative_path,
    subtitle.language
FROM episode_tasks AS task
JOIN download_files AS video ON video.id = task.source_video_file_id
JOIN download_files AS subtitle
    ON subtitle.download_id = video.download_id
   AND subtitle.source_season = video.source_season
   AND subtitle.source_episode = video.source_episode
WHERE task.id = sqlc.arg(task_id)
  AND subtitle.selected
  AND subtitle.media_kind = 'subtitle'
ORDER BY subtitle.file_index;

-- name: UpsertMediaArtifact :one
INSERT INTO media_artifacts (
    id,
    task_id,
    source_file_id,
    transcode_profile_id,
    kind,
    basename,
    file_path,
    format,
    size_bytes,
    checksum_sha256,
    metadata
) VALUES (
    sqlc.arg(id),
    sqlc.arg(task_id),
    sqlc.narg(source_file_id),
    sqlc.narg(transcode_profile_id),
    sqlc.arg(kind),
    sqlc.arg(basename),
    sqlc.arg(file_path),
    sqlc.arg(format),
    sqlc.arg(size_bytes),
    sqlc.arg(checksum_sha256),
    sqlc.arg(metadata)
)
ON CONFLICT (task_id, kind) DO UPDATE
SET source_file_id = EXCLUDED.source_file_id,
    transcode_profile_id = EXCLUDED.transcode_profile_id,
    basename = EXCLUDED.basename,
    file_path = EXCLUDED.file_path,
    format = EXCLUDED.format,
    size_bytes = EXCLUDED.size_bytes,
    checksum_sha256 = EXCLUDED.checksum_sha256,
    metadata = EXCLUDED.metadata
RETURNING *;

-- name: MarkTaskVideoReady :one
UPDATE episode_tasks
SET video_state = 'video_ready',
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND video_state IN ('transcode_queued', 'transcoding')
RETURNING *;

-- name: MarkTaskSubtitleReady :one
UPDATE episode_tasks
SET subtitle_state = 'ass_ready',
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND subtitle_state IN ('subtitle_queued', 'extracting_or_converting')
RETURNING *;

-- name: MarkTaskFinalizingIfReady :one
UPDATE episode_tasks
SET state = 'finalizing',
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state = 'processing'
  AND video_state = 'video_ready'
  AND subtitle_state = 'ass_ready'
RETURNING *;

-- name: GetTaskArtifact :one
SELECT *
FROM media_artifacts
WHERE task_id = sqlc.arg(task_id)
  AND kind = sqlc.arg(kind);

-- name: GetTaskFinalizeCommand :one
SELECT
    task.id AS task_id,
    task.state,
    task.transcode_profile_id,
    video.id AS video_artifact_id,
    video.basename AS video_basename,
    video.file_path AS video_file_path,
    video.size_bytes AS video_size_bytes,
    video.checksum_sha256 AS video_checksum_sha256,
    subtitle.id AS subtitle_artifact_id,
    subtitle.basename AS subtitle_basename,
    subtitle.file_path AS subtitle_file_path,
    subtitle.size_bytes AS subtitle_size_bytes,
    subtitle.checksum_sha256 AS subtitle_checksum_sha256
FROM episode_tasks AS task
JOIN media_artifacts AS video ON video.task_id = task.id AND video.kind = 'video'
JOIN media_artifacts AS subtitle ON subtitle.task_id = task.id AND subtitle.kind = 'subtitle'
WHERE task.id = sqlc.arg(id);

-- name: GetTaskRSSAutoReview :one
SELECT
    subscription.id AS subscription_id,
    subscription.auto_review
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE task.id = sqlc.arg(id)
  AND subscription.deleted_at IS NULL;

-- name: CreateArtifactSet :one
INSERT INTO artifact_sets (
    id,
    task_id,
    transcode_profile_id,
    basename,
    video_artifact_id,
    subtitle_artifact_id
) VALUES (
    sqlc.arg(id),
    sqlc.arg(task_id),
    sqlc.arg(transcode_profile_id),
    sqlc.arg(basename),
    sqlc.arg(video_artifact_id),
    sqlc.arg(subtitle_artifact_id)
)
ON CONFLICT (task_id) DO UPDATE
SET transcode_profile_id = EXCLUDED.transcode_profile_id,
    basename = EXCLUDED.basename,
    video_artifact_id = EXCLUDED.video_artifact_id,
    subtitle_artifact_id = EXCLUDED.subtitle_artifact_id
RETURNING *;

-- name: MarkTaskAwaitingReview :one
UPDATE episode_tasks
SET state = 'awaiting_review',
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state = 'finalizing'
  AND video_state = 'video_ready'
  AND subtitle_state = 'ass_ready'
RETURNING *;

-- name: MarkTaskVideoTerminalFailure :one
UPDATE episode_tasks
SET state = 'failed',
    video_state = 'failed',
    failure_stage = 'video',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND video_state IN ('transcode_queued', 'transcoding')
RETURNING *;

-- name: MarkTaskSubtitleTerminalFailure :one
UPDATE episode_tasks
SET state = 'failed',
    subtitle_state = 'failed',
    failure_stage = 'subtitle',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND subtitle_state IN ('subtitle_queued', 'extracting_or_converting')
RETURNING *;

-- name: MarkTaskFinalizeTerminalFailure :one
UPDATE episode_tasks
SET state = 'failed',
    failure_stage = 'finalize',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state = 'finalizing'
RETURNING *;

-- name: MarkTaskVideoCancelled :one
UPDATE episode_tasks
SET state = 'cancelled',
    video_state = 'cancelled',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND video_state IN ('transcode_queued', 'transcoding')
RETURNING *;

-- name: MarkTaskSubtitleCancelled :one
UPDATE episode_tasks
SET state = 'cancelled',
    subtitle_state = 'cancelled',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND subtitle_state IN ('subtitle_queued', 'extracting_or_converting')
RETURNING *;

-- name: MarkTaskFinalizeCancelled :one
UPDATE episode_tasks
SET state = 'cancelled',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state = 'finalizing'
RETURNING *;

-- name: GetArtifactByID :one
SELECT *
FROM media_artifacts
WHERE id = sqlc.arg(id);

-- name: RequeueTaskVideoBranch :one
UPDATE episode_tasks
SET state = 'processing',
    video_state = 'transcode_queued',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state = 'failed'
  AND video_state = 'failed'
  AND failure_stage = 'video'
RETURNING *;

-- name: RequeueTaskSubtitleBranch :one
UPDATE episode_tasks
SET state = 'processing',
    subtitle_state = 'subtitle_queued',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state = 'failed'
  AND subtitle_state = 'failed'
  AND failure_stage = 'subtitle'
RETURNING *;

-- name: RequeueTaskFinalizeBranch :one
UPDATE episode_tasks
SET state = 'finalizing',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state = 'failed'
  AND video_state = 'video_ready'
  AND subtitle_state = 'ass_ready'
  AND failure_stage = 'finalize'
RETURNING *;

-- name: RequeueTaskImportBranch :one
UPDATE episode_tasks
SET state = 'import_queued',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE episode_tasks.id = sqlc.arg(id)
  AND episode_tasks.state = 'failed'
  AND episode_tasks.failure_stage = 'import'
  AND EXISTS (
      SELECT 1 FROM imports
      WHERE imports.task_id = episode_tasks.id
        AND imports.status = 'failed'
  )
RETURNING episode_tasks.*;

-- name: RequeueTaskFailedMediaBranches :one
UPDATE episode_tasks
SET state = CASE WHEN state IN ('failed', 'cancelled') THEN 'processing' ELSE state END,
    video_state = CASE WHEN video_state = 'failed' THEN 'transcode_queued' ELSE video_state END,
    subtitle_state = CASE WHEN subtitle_state = 'failed' THEN 'subtitle_queued' ELSE subtitle_state END,
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND (
      (state = 'failed' AND (video_state = 'failed' OR subtitle_state = 'failed'))
      OR (state = 'processing' AND (video_state = 'failed' OR subtitle_state = 'failed'))
      OR (state = 'cancelled' AND (video_state = 'failed' OR subtitle_state = 'failed') AND video_state IN ('failed', 'video_ready') AND subtitle_state IN ('failed', 'ass_ready'))
  )
RETURNING *;

-- name: CancelEpisodeTaskIfActive :one
UPDATE episode_tasks
SET state = 'cancelled',
    video_state = CASE WHEN video_state IN ('transcode_queued', 'transcoding') THEN 'cancelled' ELSE video_state END,
    subtitle_state = CASE WHEN subtitle_state IN ('subtitle_queued', 'extracting_or_converting') THEN 'cancelled' ELSE subtitle_state END,
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND state NOT IN ('imported', 'failed', 'cancelled', 'rejected')
  AND version = sqlc.arg(expected_version)
RETURNING *;
