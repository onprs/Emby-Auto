-- name: CreateRSSFeedCatalogLookup :one
WITH expired AS (
    DELETE FROM rss_feed_catalog_lookups
    WHERE expires_at <= now()
)
INSERT INTO rss_feed_catalog_lookups (
    id,
    feed_title,
    suggested_queries,
    sample_titles,
    expires_at
) VALUES (
    sqlc.arg(id),
    sqlc.arg(feed_title),
    sqlc.arg(suggested_queries),
    sqlc.arg(sample_titles),
    sqlc.arg(expires_at)
)
RETURNING *;

-- name: GetAgentRSSFeedCatalogLookup :one
SELECT *
FROM rss_feed_catalog_lookups
WHERE id = sqlc.arg(id)
  AND expires_at > now();

-- name: GetAgentRSSContext :one
SELECT
    entry.id,
    entry.subscription_id,
    entry.title,
    (entry.download_uri IS NOT NULL)::boolean AS has_download_uri,
    entry.rejection_reasons,
    entry.source_season,
    entry.source_episode,
    entry.discovered_at,
    entry.updated_at,
    subscription.source_season AS default_source_season,
    subscription.include_keywords,
    subscription.exclude_keywords,
    subscription.enabled,
    subscription.mapping_profile_id,
    subscription.version AS subscription_version,
    subscription.completed_at
FROM rss_entries AS entry
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE entry.id = sqlc.arg(id);

-- name: ListAgentResolvableRSSEntries :many
SELECT entry.id
FROM rss_entries AS entry
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND subscription.enabled
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND entry.status IN ('discovered', 'enqueue_failed')
  AND NOT EXISTS (
      SELECT 1 FROM rss_entry_adjudications AS adjudication
      WHERE adjudication.entry_id = entry.id
  )
  AND NOT entry.downloadable
  AND entry.download_uri IS NOT NULL
  AND cardinality(entry.rejection_reasons) > 0
  AND entry.rejection_reasons <@ ARRAY['episode_not_detected', 'episode_ambiguous']::text[]
ORDER BY entry.discovered_at, entry.id
LIMIT 100;

-- name: LockRSSEntryForAgentCoordinate :one
SELECT entry.*
FROM rss_entries AS entry
WHERE entry.id = sqlc.arg(id)
FOR UPDATE OF entry;

-- name: ApplyAgentRSSCoordinate :one
UPDATE rss_entries
SET source_season = sqlc.arg(source_season),
    source_episode = sqlc.arg(source_episode),
    downloadable = true,
    rejection_reasons = ARRAY[]::text[],
    coordinate_source = sqlc.arg(coordinate_source),
    agent_resolution_id = sqlc.arg(agent_resolution_id),
    last_error_code = NULL,
    last_error_message = NULL,
    last_error_retryable = false,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ListAgentRSSNeighbors :many
SELECT id, title, source_season, source_episode
FROM rss_entries
WHERE subscription_id = sqlc.arg(subscription_id)
  AND id <> sqlc.arg(entry_id)
  AND source_season IS NOT NULL
  AND source_episode IS NOT NULL
ORDER BY abs(EXTRACT(EPOCH FROM (discovered_at - sqlc.arg(discovered_at)::timestamptz))), id
LIMIT 12;

-- name: GetAgentRSSAdjudicationBatch :one
SELECT
    batch.*,
    subscription.source_season AS default_source_season,
    subscription.include_keywords,
    subscription.exclude_keywords,
    subscription.enabled AS subscription_enabled,
    subscription.completed_at AS subscription_completed_at,
    subscription.deleted_at AS subscription_deleted_at,
    subscription.series_id,
    subscription.mapping_profile_id
FROM rss_adjudication_batches AS batch
JOIN rss_subscriptions AS subscription ON subscription.id = batch.subscription_id
WHERE batch.id = sqlc.arg(id);

-- name: ListAgentRSSAdjudicationEntries :many
SELECT
    entry.id,
    entry.title,
    entry.published_at,
    entry.status,
    entry.downloadable,
    entry.rejection_reasons,
    entry.source_season,
    entry.source_episode,
    entry.imported_at,
    entry.discovered_at,
    entry.updated_at
FROM rss_entries AS entry
JOIN rss_entry_adjudications AS adjudication ON adjudication.entry_id = entry.id
WHERE adjudication.batch_id = sqlc.arg(batch_id)
ORDER BY entry.discovered_at, entry.id;

-- name: ListAgentRSSAdjudicationHistory :many
SELECT
    entry.id,
    entry.title,
    entry.published_at,
    entry.status,
    COALESCE(adjudication.state, 'not_required')::text AS adjudication_state,
    entry.source_season,
    entry.source_episode,
    entry.imported_at,
    entry.discovered_at
FROM rss_entries AS entry
LEFT JOIN rss_entry_adjudications AS adjudication ON adjudication.entry_id = entry.id
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND entry.discovered_at < sqlc.arg(discovered_before)
  AND (adjudication.batch_id IS NULL OR adjudication.batch_id <> sqlc.arg(batch_id))
ORDER BY entry.discovered_at DESC, entry.id DESC
LIMIT 50;

-- name: LockRSSSubscriptionForAdjudicationBatch :one
SELECT subscription.*
FROM rss_adjudication_batches AS batch
JOIN rss_subscriptions AS subscription ON subscription.id = batch.subscription_id
WHERE batch.id = sqlc.arg(id)
FOR UPDATE OF subscription;

-- name: LockRSSAdjudicationBatch :one
SELECT batch.*
FROM rss_adjudication_batches AS batch
WHERE batch.id = sqlc.arg(id)
FOR UPDATE OF batch;

-- name: LockRSSAdjudicationEntries :many
SELECT
    entry.*,
    adjudication.state AS adjudication_state,
    adjudication.source AS adjudication_source,
    adjudication.resolution_id AS adjudication_resolution_id,
    adjudication.related_entry_id
FROM rss_entries AS entry
JOIN rss_entry_adjudications AS adjudication ON adjudication.entry_id = entry.id
WHERE adjudication.batch_id = sqlc.arg(batch_id)
ORDER BY entry.discovered_at, entry.id
FOR UPDATE OF entry, adjudication;

-- name: ApplyAgentRSSAdjudicationEntry :one
WITH decision AS (
    UPDATE rss_entry_adjudications AS adjudication
    SET state = sqlc.arg(adjudication_state),
        source = sqlc.arg(adjudication_source),
        resolution_id = sqlc.arg(adjudication_resolution_id),
        related_entry_id = sqlc.narg(related_entry_id),
        updated_at = now()
    WHERE adjudication.entry_id = sqlc.arg(id)
      AND adjudication.batch_id = sqlc.arg(adjudication_batch_id)
      AND adjudication.state = 'pending'
    RETURNING adjudication.entry_id
)
UPDATE rss_entries AS entry
SET source_season = CASE WHEN sqlc.arg(adjudication_state)::text = 'selected' THEN sqlc.narg(source_season) ELSE entry.source_season END,
    source_episode = CASE WHEN sqlc.arg(adjudication_state)::text = 'selected' THEN sqlc.narg(source_episode) ELSE entry.source_episode END,
    downloadable = sqlc.arg(adjudication_state)::text = 'selected',
    rejection_reasons = CASE
        WHEN sqlc.arg(adjudication_state)::text = 'selected' THEN ARRAY[]::text[]
        ELSE sqlc.arg(rejection_reasons)::text[]
    END,
    coordinate_source = CASE WHEN sqlc.arg(adjudication_state)::text = 'selected' THEN sqlc.arg(adjudication_source) ELSE entry.coordinate_source END,
    agent_resolution_id = CASE WHEN sqlc.arg(adjudication_state)::text = 'selected' THEN sqlc.arg(adjudication_resolution_id) ELSE entry.agent_resolution_id END,
    last_error_code = NULL,
    last_error_message = NULL,
    last_error_retryable = false,
    updated_at = now()
FROM decision
WHERE entry.id = decision.entry_id
  AND entry.status IN ('discovered', 'enqueue_failed')
RETURNING entry.*;

-- name: CompleteRSSAdjudicationBatch :one
UPDATE rss_adjudication_batches
SET status = 'applied',
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'pending'
RETURNING *;

-- name: GetAgentDownloadContext :one
SELECT
    download.id,
    download.acquisition_id,
    download.version,
    download.status,
    download.failure_stage,
    download.file_resolution_source,
    download.updated_at,
    acquisition.source_kind,
    COALESCE(entry.source_season, 1)::integer AS default_source_season,
    entry.source_episode AS default_source_episode
FROM downloads AS download
JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE download.id = sqlc.arg(id)
  AND download.deleted_at IS NULL;

-- name: ListAgentDownloadFiles :many
SELECT id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode, language
FROM download_files
WHERE download_id = sqlc.arg(download_id)
ORDER BY file_index;

-- name: ListAgentMappingAcquisitionsBySeries :many
SELECT DISTINCT ON (COALESCE(entry.subscription_id, acquisition.id)) acquisition.id
FROM acquisitions AS acquisition
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
LEFT JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE acquisition.series_id = sqlc.arg(series_id)
  AND acquisition.mapping_profile_id IS NULL
  AND acquisition.deletion_requested_at IS NULL
  AND (
      entry.id IS NULL
      OR (
          subscription.auto_episode_mapping
          AND subscription.deleted_at IS NULL
      )
  )
  AND EXISTS (
      SELECT 1
      FROM downloads AS download
      JOIN download_files AS file ON file.download_id = download.id
      WHERE download.acquisition_id = acquisition.id
        AND download.deleted_at IS NULL
        AND file.selected
        AND file.media_kind = 'video'
  )
ORDER BY COALESCE(entry.subscription_id, acquisition.id), acquisition.created_at, acquisition.id
LIMIT 100;

-- name: ListAgentMappingAcquisitionsByRSSSubscription :many
SELECT acquisition.id
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND subscription.auto_episode_mapping
  AND subscription.deleted_at IS NULL
  AND acquisition.mapping_profile_id IS NULL
  AND acquisition.deletion_requested_at IS NULL
  AND EXISTS (
      SELECT 1
      FROM downloads AS download
      JOIN download_files AS file ON file.download_id = download.id
      WHERE download.acquisition_id = acquisition.id
        AND download.deleted_at IS NULL
        AND file.selected
        AND file.media_kind = 'video'
  )
ORDER BY acquisition.created_at, acquisition.id
LIMIT 1;

-- name: ListAutomaticEpisodeMappingAcquisitions :many
WITH reconciliation_window AS (
    SELECT COALESCE(
        sqlc.narg(window_created_before)::timestamptz,
        statement_timestamp()
    )::timestamptz AS created_before
),
canonical AS (
    SELECT DISTINCT ON (COALESCE(subscription.id, acquisition.id))
        acquisition.id AS acquisition_id,
        COALESCE(subscription.id, acquisition.id) AS group_key,
        acquisition.created_at
    FROM acquisitions AS acquisition
    JOIN media_series AS series ON series.id = acquisition.series_id
    LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
    LEFT JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
    CROSS JOIN reconciliation_window
    WHERE acquisition.created_at < reconciliation_window.created_before
      AND series.media_type = 'tv'
      AND acquisition.mapping_profile_id IS NULL
      AND acquisition.deletion_requested_at IS NULL
      AND (
          acquisition.source_kind <> 'rss'
          OR (
              subscription.auto_episode_mapping
              AND subscription.deleted_at IS NULL
          )
      )
      AND EXISTS (
          SELECT 1
          FROM downloads AS download
          JOIN download_files AS file ON file.download_id = download.id
          WHERE download.acquisition_id = acquisition.id
            AND download.deleted_at IS NULL
            AND file.selected
            AND file.media_kind = 'video'
      )
    ORDER BY COALESCE(subscription.id, acquisition.id), acquisition.created_at, acquisition.id
),
window_bounds AS (
    SELECT
        reconciliation_window.created_before,
        COALESCE(sqlc.narg(window_high_group_key)::uuid, initial_high.group_key)::uuid AS high_group_key,
        COALESCE(sqlc.narg(window_high_created_at)::timestamptz, initial_high.created_at)::timestamptz AS high_created_at,
        COALESCE(sqlc.narg(window_high_acquisition_id)::uuid, initial_high.acquisition_id)::uuid AS high_acquisition_id
    FROM reconciliation_window
    LEFT JOIN LATERAL (
        SELECT group_key, created_at, acquisition_id
        FROM canonical
        ORDER BY group_key DESC, created_at DESC, acquisition_id DESC
        LIMIT 1
    ) AS initial_high ON true
)
SELECT
    canonical.acquisition_id,
    canonical.group_key,
    canonical.created_at,
    window_bounds.created_before AS window_created_before,
    window_bounds.high_group_key AS window_high_group_key,
    window_bounds.high_created_at AS window_high_created_at,
    window_bounds.high_acquisition_id AS window_high_acquisition_id
FROM canonical
CROSS JOIN window_bounds
WHERE window_bounds.high_group_key IS NOT NULL
  AND (
      sqlc.narg(cursor_group_key)::uuid IS NULL
      OR (canonical.group_key, canonical.created_at, canonical.acquisition_id) > (
          sqlc.narg(cursor_group_key)::uuid,
          sqlc.narg(cursor_created_at)::timestamptz,
          sqlc.narg(cursor_acquisition_id)::uuid
      )
  )
  AND (canonical.group_key, canonical.created_at, canonical.acquisition_id) <= (
      window_bounds.high_group_key,
      window_bounds.high_created_at,
      window_bounds.high_acquisition_id
  )
ORDER BY canonical.group_key, canonical.created_at, canonical.acquisition_id
LIMIT sqlc.arg(page_size);

-- name: IsAutomaticEpisodeMappingEnabled :one
SELECT CASE
    WHEN acquisition.source_kind <> 'rss' THEN true
    ELSE COALESCE(subscription.auto_episode_mapping AND subscription.deleted_at IS NULL, false)
END::boolean AS enabled
FROM acquisitions AS acquisition
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
LEFT JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE acquisition.id = sqlc.arg(acquisition_id);

-- name: IsCanonicalRSSAgentMappingAcquisition :one
WITH source AS (
    SELECT acquisition.id, entry.subscription_id
    FROM acquisitions AS acquisition
    LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
    WHERE acquisition.id = sqlc.arg(acquisition_id)
)
SELECT COALESCE(
    source.subscription_id IS NULL
    OR source.id = (
        SELECT candidate.id
        FROM acquisitions AS candidate
        JOIN rss_entries AS candidate_entry ON candidate_entry.id = candidate.rss_entry_id
        WHERE candidate_entry.subscription_id = source.subscription_id
          AND candidate.mapping_profile_id IS NULL
          AND candidate.deletion_requested_at IS NULL
          AND EXISTS (
              SELECT 1
              FROM downloads AS download
              JOIN download_files AS file ON file.download_id = download.id
              WHERE download.acquisition_id = candidate.id
                AND download.deleted_at IS NULL
                AND file.selected
                AND file.media_kind = 'video'
          )
        ORDER BY candidate.created_at, candidate.id
        LIMIT 1
    ),
    false
)::boolean AS canonical
FROM source;

-- name: GetAgentMappingContext :one
SELECT
    acquisition.id,
    acquisition.series_id,
    acquisition.mapping_profile_id,
    acquisition.updated_at,
    series.title,
    series.tmdb_series_id,
    COALESCE(candidate.title, entry.title, '')::text AS source_title
FROM acquisitions AS acquisition
JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN release_candidates AS candidate ON candidate.id = acquisition.release_candidate_id
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE acquisition.id = sqlc.arg(id);

-- name: ListAgentMappingFiles :many
SELECT file.id, file.relative_path, file.source_season, file.source_episode
FROM download_files AS file
JOIN downloads AS download ON download.id = file.download_id
WHERE download.id = (
    SELECT candidate.id
    FROM downloads AS candidate
    WHERE candidate.acquisition_id = sqlc.arg(acquisition_id)
      AND candidate.deleted_at IS NULL
    ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
    LIMIT 1
)
  AND file.selected
  AND file.media_kind = 'video'
ORDER BY file.file_index;

-- name: ListAgentTMDbEpisodes :many
SELECT
    episode.id,
    season.season_number,
    episode.episode_number,
    episode.title
FROM acquisitions AS acquisition
JOIN tmdb_seasons AS season ON season.series_id = acquisition.series_id
JOIN media_episodes AS episode ON episode.season_id = season.id
WHERE acquisition.id = sqlc.arg(acquisition_id)
ORDER BY season.season_number, episode.episode_number;

-- name: GetAgentCatalogContext :one
SELECT
    acquisition.id,
    acquisition.updated_at,
    acquisition.source_kind,
    series.title AS current_title,
    COALESCE(candidate.title, entry.title, series.title)::text AS source_title
FROM acquisitions AS acquisition
JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN release_candidates AS candidate ON candidate.id = acquisition.release_candidate_id
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE acquisition.id = sqlc.arg(id);

-- name: CreateAgentResolution :one
INSERT INTO agent_resolutions (
    id,
    operation_id,
    resource_version,
    capability,
    resource_type,
    resource_id,
    trigger,
    input_fingerprint,
    configuration_version,
    protocol,
    provider_origin,
    model,
    prompt_version,
    toolset_version,
    created_by
) VALUES (
    sqlc.arg(id),
    sqlc.arg(operation_id),
    sqlc.narg(resource_version),
    sqlc.arg(capability),
    sqlc.arg(resource_type),
    sqlc.arg(resource_id),
    sqlc.arg(trigger),
    sqlc.arg(input_fingerprint),
    sqlc.arg(configuration_version),
    sqlc.arg(protocol),
    sqlc.arg(provider_origin),
    sqlc.arg(model),
    sqlc.arg(prompt_version),
    sqlc.arg(toolset_version),
    sqlc.narg(created_by)
)
ON CONFLICT (id) DO UPDATE
SET id = agent_resolutions.id
RETURNING *;

-- name: GetAgentResolutionDashboardStats :one
SELECT
    count(*)::bigint AS total,
    count(*) FILTER (WHERE status = 'review_required')::bigint AS review_pending,
    count(*) FILTER (WHERE status = 'applied')::bigint AS applied,
    count(*) FILTER (WHERE status = 'applied' AND review_decision IS NULL)::bigint AS auto_applied,
    count(*) FILTER (WHERE status = 'applied' AND review_decision = 'accepted')::bigint AS accepted,
    count(*) FILTER (WHERE status = 'rejected')::bigint AS rejected,
    count(*) FILTER (WHERE status = 'failed')::bigint AS failed,
    COALESCE(sum(input_tokens), 0)::bigint AS input_tokens,
    COALESCE(sum(output_tokens), 0)::bigint AS output_tokens,
    COALESCE(avg(latency_milliseconds), 0)::bigint AS average_latency_milliseconds
FROM agent_resolutions;

-- name: GetAgentResolution :one
SELECT *
FROM agent_resolutions
WHERE id = sqlc.arg(id);

-- name: GetAgentResolutionByOperation :one
SELECT *
FROM agent_resolutions
WHERE operation_id = sqlc.arg(operation_id);

-- name: LockAgentResolution :one
SELECT *
FROM agent_resolutions
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: ListAgentResolutions :many
SELECT resolution.*
FROM agent_resolutions AS resolution
WHERE (sqlc.narg(status)::text IS NULL OR resolution.status = sqlc.narg(status))
  AND (sqlc.narg(capability)::text IS NULL OR resolution.capability = sqlc.narg(capability))
  AND (sqlc.narg(resource_type)::text IS NULL OR resolution.resource_type = sqlc.narg(resource_type))
  AND (sqlc.narg(resource_id)::uuid IS NULL OR resolution.resource_id = sqlc.narg(resource_id))
  AND (
      sqlc.narg(cursor_id)::uuid IS NULL
      OR (resolution.created_at, resolution.id) < (
          SELECT cursor.created_at, cursor.id
          FROM agent_resolutions AS cursor
          WHERE cursor.id = sqlc.narg(cursor_id)
      )
  )
ORDER BY resolution.created_at DESC, resolution.id DESC
LIMIT sqlc.arg(page_size);

-- name: StartAgentResolution :one
UPDATE agent_resolutions
SET status = 'running',
    started_at = COALESCE(started_at, now()),
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: SaveAgentResolutionProposal :one
UPDATE agent_resolutions
SET status = 'proposed',
    proposal = sqlc.arg(proposal),
    input_tokens = sqlc.narg(input_tokens),
    output_tokens = sqlc.narg(output_tokens),
    tool_call_count = sqlc.arg(tool_call_count),
    latency_milliseconds = sqlc.narg(latency_milliseconds),
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
RETURNING *;

-- name: CompleteAgentResolution :one
UPDATE agent_resolutions
SET status = sqlc.arg(status),
    validation = sqlc.arg(validation),
    completed_at = now(),
    applied_at = CASE WHEN sqlc.arg(status)::text = 'applied' THEN now() ELSE NULL END,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('running', 'proposed', 'review_required')
RETURNING *;

-- name: FailAgentResolution :one
UPDATE agent_resolutions
SET status = sqlc.arg(status),
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    completed_at = now(),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running', 'proposed')
RETURNING *;

-- name: FailValidatedAgentResolution :one
UPDATE agent_resolutions
SET status = 'failed',
    validation = sqlc.arg(validation),
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    completed_at = now(),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('running', 'proposed')
RETURNING *;

-- name: RequeueAgentResolution :one
UPDATE agent_resolutions
SET status = CASE
        WHEN status = 'failed' AND proposal <> '{}'::jsonb THEN 'proposed'
        ELSE 'queued'
    END,
    operation_id = sqlc.arg(operation_id),
    proposal = CASE WHEN status = 'rejected' THEN '{}'::jsonb ELSE proposal END,
    validation = '{}'::jsonb,
    error_code = NULL,
    error_message = NULL,
    input_tokens = CASE WHEN status = 'rejected' THEN NULL ELSE input_tokens END,
    output_tokens = CASE WHEN status = 'rejected' THEN NULL ELSE output_tokens END,
    tool_call_count = CASE WHEN status = 'rejected' THEN 0 ELSE tool_call_count END,
    latency_milliseconds = CASE WHEN status = 'rejected' THEN NULL ELSE latency_milliseconds END,
    reviewed_by = NULL,
    review_decision = NULL,
    reviewed_at = NULL,
    completed_at = NULL,
    started_at = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND status IN ('failed', 'rejected')
RETURNING *;

-- name: MarkAgentResolutionOperationTerminal :one
UPDATE agent_resolutions
SET status = sqlc.arg(status),
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    completed_at = now(),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running', 'proposed')
RETURNING *;

-- name: CreateAgentResolutionStep :one
INSERT INTO agent_resolution_steps (
    id,
    resolution_id,
    sequence,
    tool_name,
    status,
    arguments_digest,
    result_digest,
    duration_milliseconds,
    error_code
) VALUES (
    sqlc.arg(id),
    sqlc.arg(resolution_id),
    sqlc.arg(sequence),
    sqlc.arg(tool_name),
    sqlc.arg(status),
    sqlc.narg(arguments_digest),
    sqlc.narg(result_digest),
    sqlc.narg(duration_milliseconds),
    sqlc.narg(error_code)
)
ON CONFLICT (resolution_id, sequence) DO UPDATE
SET tool_name = EXCLUDED.tool_name,
    status = EXCLUDED.status,
    arguments_digest = EXCLUDED.arguments_digest,
    result_digest = EXCLUDED.result_digest,
    duration_milliseconds = EXCLUDED.duration_milliseconds,
    error_code = EXCLUDED.error_code
RETURNING *;

-- name: ListAgentResolutionSteps :many
SELECT *
FROM agent_resolution_steps
WHERE resolution_id = sqlc.arg(resolution_id)
ORDER BY sequence;

-- name: GetAgentRSSPreacquisitionMappingContext :one
SELECT
    scope.id,
    scope.subscription_id,
    scope.subscription_version,
    scope.source_fingerprint,
    scope.status,
    subscription.series_id,
    subscription.mapping_profile_id,
    subscription.enabled AS subscription_enabled,
    subscription.auto_episode_mapping,
    subscription.deleted_at AS subscription_deleted_at,
    subscription.completed_at AS subscription_completed_at,
    series.tmdb_series_id,
    series.title AS series_title
FROM rss_preacquisition_mapping_scopes AS scope
JOIN rss_subscriptions AS subscription ON subscription.id = scope.subscription_id
JOIN media_series AS series ON series.id = subscription.series_id
WHERE scope.id = sqlc.arg(id);

-- name: ListAgentRSSPreacquisitionMappingSources :many
SELECT
    source.entry_id,
    source.source_season,
    source.source_episode,
    entry.title
FROM rss_preacquisition_mapping_sources AS source
JOIN rss_entries AS entry ON entry.id = source.entry_id
WHERE source.scope_id = sqlc.arg(scope_id)
ORDER BY source.source_season, source.source_episode, source.entry_id;

-- name: ListAgentRSSPreacquisitionTMDbEpisodes :many
SELECT
    episode.id,
    season.season_number,
    episode.episode_number,
    episode.title
FROM rss_preacquisition_mapping_scopes AS scope
JOIN rss_subscriptions AS subscription ON subscription.id = scope.subscription_id
JOIN tmdb_seasons AS season ON season.series_id = subscription.series_id
JOIN media_episodes AS episode ON episode.season_id = season.id
WHERE scope.id = sqlc.arg(scope_id)
  AND season.season_number > 0
  AND episode.episode_number > 0
ORDER BY season.season_number, episode.episode_number;
