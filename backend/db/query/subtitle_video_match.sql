-- name: CreateSubtitleVideoMatchScope :one
INSERT INTO subtitle_video_match_scopes (id, task_id)
VALUES (sqlc.arg(id), sqlc.arg(task_id))
ON CONFLICT (task_id) DO UPDATE
SET status = 'pending',
    selected_candidate_id = NULL,
    agent_resolution_id = NULL,
    updated_at = now()
WHERE subtitle_video_match_scopes.status = 'expired'
RETURNING *;

-- name: DeleteSubtitleVideoMatchCandidates :exec
DELETE FROM subtitle_video_match_candidates
WHERE scope_id = sqlc.arg(scope_id);

-- name: ExpireSubtitleVideoMatchScope :one
UPDATE subtitle_video_match_scopes
SET status = 'expired',
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'pending'
RETURNING task_id;

-- name: InsertSubtitleVideoMatchCandidate :exec
INSERT INTO subtitle_video_match_candidates (
    scope_id, candidate_id, source, stream_index, format, language, title, path
) VALUES (
    sqlc.arg(scope_id), sqlc.arg(candidate_id), sqlc.arg(source),
    sqlc.arg(stream_index), sqlc.arg(format), sqlc.arg(language), sqlc.arg(title), sqlc.arg(path)
);

-- name: GetSubtitleVideoMatchContext :one
SELECT
    scope.id AS scope_id,
    scope.status AS scope_status,
    scope.selected_candidate_id,
    task.id AS task_id,
    task.media_type,
    acquisition.series_id,
    series.title AS series_title,
    mapping.id AS mapping_id,
    episode.episode_number AS target_episode_number,
    episode.title AS target_episode_title,
    season.season_number AS target_season_number,
    video.relative_path AS source_video_relative_path,
    download.save_path,
    task.version AS task_version
FROM subtitle_video_match_scopes AS scope
JOIN episode_tasks AS task ON task.id = scope.task_id
JOIN download_files AS video ON video.id = task.source_video_file_id
JOIN downloads AS download ON download.id = video.download_id
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id AND mapping.mapping_status = 'mapped'
LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
LEFT JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE scope.id = sqlc.arg(scope_id)
FOR UPDATE OF scope, task;

-- name: ListSubtitleVideoMatchCandidates :many
SELECT
    candidate_id,
    source,
    stream_index,
    format,
    language,
    title,
    path
FROM subtitle_video_match_candidates
WHERE scope_id = sqlc.arg(scope_id)
ORDER BY created_at, candidate_id;

-- name: ApplySubtitleVideoMatchSelection :one
UPDATE subtitle_video_match_scopes
SET status = 'applied',
    selected_candidate_id = sqlc.arg(selected_candidate_id),
    agent_resolution_id = sqlc.arg(agent_resolution_id),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'pending'
RETURNING *;

-- name: GetSubtitleVideoMatchSelection :one
SELECT
    scope.task_id,
    scope.selected_candidate_id,
    scope.status,
    selected.path AS selected_candidate_path
FROM subtitle_video_match_scopes AS scope
LEFT JOIN subtitle_video_match_candidates AS selected
  ON selected.scope_id = scope.id
 AND selected.candidate_id = scope.selected_candidate_id
WHERE scope.task_id = sqlc.arg(task_id)
  AND scope.status = 'applied';

-- name: GetSubtitleVideoMatchSchedulingState :one
SELECT
    scope.id AS scope_id,
    scope.status AS scope_status,
    task.version AS task_version,
    resolution.id AS resolution_id,
    COALESCE(resolution.status, '')::text AS resolution_status,
    COALESCE(resolution.error_code, '')::text AS resolution_error_code,
    COALESCE(resolution.version, 0)::integer AS resolution_version,
    resolution.resource_version AS resolution_task_version,
    operation.id AS operation_id,
    COALESCE(operation.status, '')::text AS operation_status,
    COALESCE(operation.cancel_requested_at IS NOT NULL, false)::boolean AS cancellation_requested
FROM subtitle_video_match_scopes AS scope
JOIN episode_tasks AS task ON task.id = scope.task_id
LEFT JOIN LATERAL (
    SELECT candidate.*
    FROM agent_resolutions AS candidate
    WHERE candidate.resource_type = 'episode_task'
      AND candidate.resource_id = scope.id
      AND candidate.capability = 'subtitle_video_match'
    ORDER BY candidate.created_at DESC, candidate.id DESC
    LIMIT 1
) AS resolution ON true
LEFT JOIN operations AS operation ON operation.id = resolution.operation_id
WHERE scope.task_id = sqlc.arg(task_id);

-- name: GetSubtitleVideoMatchTaskVersion :one
SELECT task.version
FROM subtitle_video_match_scopes AS scope
JOIN episode_tasks AS task ON task.id = scope.task_id
WHERE scope.id = sqlc.arg(scope_id);

-- 作废已被新候选集或新任务版本取代的活动决议，避免旧操作在后续失败时
-- 回收新 generation 的范围与任务。
-- name: SupersedeSubtitleVideoMatchResolutions :execrows
UPDATE agent_resolutions
SET status = 'cancelled',
    error_code = 'agent_resolution_superseded',
    error_message = 'a newer subtitle video match scope generation replaced this resolution',
    completed_at = now(),
    version = version + 1,
    updated_at = now()
WHERE resource_type = 'episode_task'
  AND resource_id = sqlc.arg(scope_id)
  AND capability = 'subtitle_video_match'
  AND status IN ('queued', 'running', 'proposed', 'review_required')
  AND (sqlc.narg(task_version)::integer IS NULL OR resource_version IS NULL OR resource_version <> sqlc.narg(task_version));
