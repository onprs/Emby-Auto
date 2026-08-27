-- name: CountInstallationState :one
SELECT count(*)
FROM installation_state;

-- name: GetInstallationState :one
SELECT *
FROM installation_state
WHERE id = true;

-- name: GetFirstAdminUser :one
SELECT *
FROM admin_users
ORDER BY created_at, id
LIMIT 1;

-- name: CompleteInstallation :one
INSERT INTO installation_state (id, completed_by)
VALUES (true, sqlc.arg(completed_by))
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: UpsertTMDbSeries :one
INSERT INTO media_series (
    id,
    tmdb_series_id,
    title,
    original_title,
    metadata,
    updated_at
) VALUES (
    sqlc.arg(id),
    sqlc.arg(tmdb_series_id),
    sqlc.arg(title),
    sqlc.narg(original_title),
    sqlc.arg(metadata),
    now()
)
ON CONFLICT (tmdb_series_id) DO UPDATE
SET title = EXCLUDED.title,
    original_title = EXCLUDED.original_title,
    metadata = EXCLUDED.metadata,
    updated_at = now()
RETURNING *;

-- name: GetMediaSeriesByID :one
SELECT *
FROM media_series
WHERE id = sqlc.arg(id);

-- name: ListMediaSeriesByIDs :many
SELECT *
FROM media_series
WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: GetMediaSeriesByTMDbID :one
SELECT *
FROM media_series
WHERE tmdb_series_id = sqlc.arg(tmdb_series_id);

-- name: UpsertTMDbSeason :one
INSERT INTO tmdb_seasons (
    id,
    series_id,
    tmdb_season_id,
    season_number,
    name,
    episode_count,
    fetched_at,
    upstream_payload
) VALUES (
    sqlc.arg(id),
    sqlc.arg(series_id),
    sqlc.narg(tmdb_season_id),
    sqlc.arg(season_number),
    sqlc.narg(name),
    sqlc.arg(episode_count),
    sqlc.arg(fetched_at),
    sqlc.arg(upstream_payload)
)
ON CONFLICT (series_id, season_number) DO UPDATE
SET tmdb_season_id = EXCLUDED.tmdb_season_id,
    name = EXCLUDED.name,
    episode_count = EXCLUDED.episode_count,
    fetched_at = EXCLUDED.fetched_at,
    upstream_payload = EXCLUDED.upstream_payload,
    updated_at = now()
RETURNING *;

-- name: UpsertTMDbEpisode :one
INSERT INTO media_episodes (
    id,
    season_id,
    tmdb_episode_id,
    episode_number,
    title,
    air_date,
    upstream_payload
) VALUES (
    sqlc.arg(id),
    sqlc.arg(season_id),
    sqlc.narg(tmdb_episode_id),
    sqlc.arg(episode_number),
    sqlc.arg(title),
    sqlc.narg(air_date),
    sqlc.arg(upstream_payload)
)
ON CONFLICT (season_id, episode_number) DO UPDATE
SET tmdb_episode_id = EXCLUDED.tmdb_episode_id,
    title = EXCLUDED.title,
    air_date = EXCLUDED.air_date,
    upstream_payload = EXCLUDED.upstream_payload,
    updated_at = now()
RETURNING *;

-- name: DeleteStaleTMDbEpisodes :execrows
DELETE FROM media_episodes
WHERE season_id = sqlc.arg(season_id)
  AND episode_number <> ALL(sqlc.arg(episode_numbers)::integer[]);

-- name: DeleteStaleTMDbSeasons :execrows
DELETE FROM tmdb_seasons
WHERE series_id = sqlc.arg(series_id)
  AND season_number <> ALL(sqlc.arg(season_numbers)::integer[]);

-- name: GetAcquisitionMappingContext :one
SELECT acquisition.*, series.tmdb_series_id, entry.subscription_id AS rss_subscription_id
FROM acquisitions AS acquisition
JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE acquisition.id = sqlc.arg(id);

-- name: LockRSSSubscriptionForMappingAcquisition :one
SELECT subscription.id
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE acquisition.id = sqlc.arg(acquisition_id)
FOR UPDATE OF subscription;

-- name: LockAcquisitionForMapping :one
SELECT acquisition.*, series.tmdb_series_id, entry.subscription_id AS rss_subscription_id
FROM acquisitions AS acquisition
JOIN media_series AS series ON series.id = acquisition.series_id
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE acquisition.id = sqlc.arg(id)
FOR UPDATE OF acquisition;

-- name: LockLatestDownloadForMapping :one
SELECT download.*
FROM downloads AS download
WHERE download.acquisition_id = sqlc.arg(acquisition_id)
  AND download.deleted_at IS NULL
ORDER BY (download.status = 'cancelled'), download.attempt DESC
LIMIT 1
FOR UPDATE OF download;

-- name: AcquisitionHasEpisodeTasks :one
SELECT EXISTS (
    SELECT 1
    FROM episode_tasks AS task
    WHERE task.acquisition_id = sqlc.arg(acquisition_id)
) AS episode_task_exists;

-- name: LockMediaSeries :one
SELECT *
FROM media_series
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: GetLatestDownloadForAcquisition :one
SELECT *
FROM downloads
WHERE acquisition_id = sqlc.arg(acquisition_id)
ORDER BY attempt DESC
LIMIT 1;

-- name: ListAcquisitionSelectedVideos :many
SELECT
    file.id,
    file.relative_path,
    file.source_season,
    file.source_episode,
    file.source_episode_fraction_hundredths,
    download.file_resolution_source
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
ORDER BY file.source_season, file.source_episode, file.source_episode_fraction_hundredths, file.file_index
FOR UPDATE OF file;

-- name: ListSeriesMappingCatalog :many
SELECT
    season.season_number,
    season.episode_count,
    episode.id AS episode_id,
    episode.episode_number,
    episode.title
FROM tmdb_seasons AS season
LEFT JOIN media_episodes AS episode ON episode.season_id = season.id
WHERE season.series_id = sqlc.arg(series_id)
ORDER BY season.season_number, episode.episode_number;

-- name: GetCompatibleActiveMappingProfile :one
SELECT profile.id
FROM episode_mapping_profiles AS profile
WHERE profile.id = sqlc.arg(profile_id)
  AND profile.series_id = sqlc.arg(series_id)
  AND profile.active
  AND (
      (
          profile.anchor_source_season = sqlc.arg(source_season)
          AND profile.anchor_target_episode_id IS NOT NULL
          AND EXISTS (
              SELECT 1
              FROM episode_mappings AS mapping
              JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
              JOIN tmdb_seasons AS season ON season.id = episode.season_id
              WHERE mapping.profile_id = profile.id
                AND mapping.source_season = sqlc.arg(source_season)
                AND mapping.source_episode_fraction_hundredths = 0
                AND mapping.mapping_status = 'mapped'
                AND season.series_id = profile.series_id
          )
          AND NOT EXISTS (
              SELECT 1
              FROM episode_mappings AS mapping
              WHERE mapping.profile_id = profile.id
                AND mapping.source_season = sqlc.arg(source_season)
                AND mapping.source_episode_fraction_hundredths = 0
                AND mapping.mapping_status <> 'mapped'
          )
      )
      OR (
          profile.anchor_source_season IS NULL
          AND cardinality(profile.source_season_lengths) >= sqlc.arg(source_season)::integer
          AND profile.source_season_lengths[sqlc.arg(source_season)::integer] > 0
          AND NOT EXISTS (
              SELECT 1
              FROM generate_series(
                  1,
                  profile.source_season_lengths[sqlc.arg(source_season)::integer]
              ) AS expected(source_episode)
              WHERE NOT EXISTS (
                  SELECT 1
                  FROM episode_mappings AS mapping
                  JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
                  JOIN tmdb_seasons AS season ON season.id = episode.season_id
                  WHERE mapping.profile_id = profile.id
                    AND mapping.source_season = sqlc.arg(source_season)
                    AND mapping.source_episode = expected.source_episode
                    AND mapping.source_episode_fraction_hundredths = 0
                    AND mapping.mapping_status = 'mapped'
                    AND season.series_id = profile.series_id
              )
          )
          AND NOT EXISTS (
              SELECT 1
              FROM episode_mappings AS mapping
              WHERE mapping.profile_id = profile.id
                AND mapping.source_season = sqlc.arg(source_season)
                AND mapping.source_episode_fraction_hundredths = 0
                AND (
                    mapping.source_episode < 1
                    OR mapping.source_episode > profile.source_season_lengths[sqlc.arg(source_season)::integer]
                    OR mapping.mapping_status <> 'mapped'
                )
          )
      )
  );

-- name: GetMappedTargetEpisodeForSource :one
SELECT profile.series_id, mapping.target_episode_id
FROM episode_mapping_profiles AS profile
JOIN media_series AS series ON series.id = profile.series_id
JOIN episode_mappings AS mapping ON mapping.profile_id = profile.id
JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE profile.id = sqlc.arg(profile_id)
  AND series.tmdb_series_id = sqlc.arg(tmdb_series_id)
  AND profile.active
  AND mapping.source_season = sqlc.arg(source_season)
  AND mapping.source_episode = sqlc.arg(source_episode)
  AND mapping.source_episode_fraction_hundredths = 0
  AND mapping.mapping_status = 'mapped'
  AND mapping.target_episode_id IS NOT NULL
  AND season.series_id = profile.series_id;

-- name: ListCompatibleActiveMappingProfiles :many
SELECT profile.id
FROM episode_mapping_profiles AS profile
WHERE profile.series_id = sqlc.arg(series_id)
  AND profile.active
  AND (
      (
          profile.anchor_source_season = sqlc.arg(source_season)
          AND profile.anchor_target_episode_id IS NOT NULL
          AND EXISTS (
              SELECT 1
              FROM episode_mappings AS mapping
              JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
              JOIN tmdb_seasons AS season ON season.id = episode.season_id
              WHERE mapping.profile_id = profile.id
                AND mapping.source_season = sqlc.arg(source_season)
                AND mapping.source_episode_fraction_hundredths = 0
                AND mapping.mapping_status = 'mapped'
                AND season.series_id = profile.series_id
          )
          AND NOT EXISTS (
              SELECT 1
              FROM episode_mappings AS mapping
              WHERE mapping.profile_id = profile.id
                AND mapping.source_season = sqlc.arg(source_season)
                AND mapping.source_episode_fraction_hundredths = 0
                AND mapping.mapping_status <> 'mapped'
          )
      )
      OR (
          profile.anchor_source_season IS NULL
          AND cardinality(profile.source_season_lengths) >= sqlc.arg(source_season)::integer
          AND profile.source_season_lengths[sqlc.arg(source_season)::integer] > 0
          AND NOT EXISTS (
              SELECT 1
              FROM generate_series(
                  1,
                  profile.source_season_lengths[sqlc.arg(source_season)::integer]
              ) AS expected(source_episode)
              WHERE NOT EXISTS (
                  SELECT 1
                  FROM episode_mappings AS mapping
                  JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
                  JOIN tmdb_seasons AS season ON season.id = episode.season_id
                  WHERE mapping.profile_id = profile.id
                    AND mapping.source_season = sqlc.arg(source_season)
                    AND mapping.source_episode = expected.source_episode
                    AND mapping.source_episode_fraction_hundredths = 0
                    AND mapping.mapping_status = 'mapped'
                    AND season.series_id = profile.series_id
              )
          )
          AND NOT EXISTS (
              SELECT 1
              FROM episode_mappings AS mapping
              WHERE mapping.profile_id = profile.id
                AND mapping.source_season = sqlc.arg(source_season)
                AND mapping.source_episode_fraction_hundredths = 0
                AND (
                    mapping.source_episode < 1
                    OR mapping.source_episode > profile.source_season_lengths[sqlc.arg(source_season)::integer]
                    OR mapping.mapping_status <> 'mapped'
                )
          )
      )
  )
ORDER BY profile.created_at DESC, profile.id
LIMIT 2;

-- name: NextMappingProfileVersion :one
SELECT COALESCE(max(version), 0)::integer + 1
FROM episode_mapping_profiles
WHERE series_id = sqlc.arg(series_id)
  AND name = sqlc.arg(name);

-- name: DeactivateMappingProfiles :execrows
UPDATE episode_mapping_profiles
SET active = false
WHERE series_id = sqlc.arg(series_id)
  AND name = sqlc.arg(name)
  AND active;

-- name: CreateMappingProfile :one
INSERT INTO episode_mapping_profiles (
    id,
    series_id,
    name,
    version,
    source_season_lengths,
    anchor_source_season,
    anchor_source_episode,
    anchor_target_episode_id,
    target_episode_offset,
    active,
    created_by,
    decision_source,
    agent_resolution_id
) VALUES (
    sqlc.arg(id),
    sqlc.arg(series_id),
    sqlc.arg(name),
    sqlc.arg(version),
    NULL,
    sqlc.arg(anchor_source_season),
    sqlc.arg(anchor_source_episode),
    sqlc.arg(anchor_target_episode_id),
    sqlc.arg(target_episode_offset),
    true,
    sqlc.narg(created_by),
    sqlc.arg(decision_source),
    sqlc.narg(agent_resolution_id)
)
RETURNING *;

-- name: CreateEpisodeMapping :one
INSERT INTO episode_mappings (
    id,
    profile_id,
    source_season,
    source_episode,
    source_episode_fraction_hundredths,
    absolute_episode,
    target_episode_id,
    mapping_status,
    match_source,
    error_code
) VALUES (
    sqlc.arg(id),
    sqlc.arg(profile_id),
    sqlc.arg(source_season),
    sqlc.arg(source_episode),
    sqlc.arg(source_episode_fraction_hundredths),
    sqlc.narg(absolute_episode),
    sqlc.narg(target_episode_id),
    sqlc.arg(mapping_status),
    sqlc.arg(match_source),
    sqlc.narg(error_code)
)
RETURNING *;

-- name: ListExplicitMappingFilesForDownload :many
SELECT
    file.id,
    file.relative_path,
    file.media_kind,
    file.source_season,
    file.source_episode,
    file.source_episode_fraction_hundredths,
    download.file_resolution_source
FROM download_files AS file
JOIN downloads AS download ON download.id = file.download_id
WHERE file.download_id = sqlc.arg(download_id)
  AND file.selected
  AND file.media_kind IN ('video', 'subtitle')
ORDER BY file.file_index, file.id
FOR UPDATE OF file;

-- name: UpdateExplicitMappingFileCoordinate :execrows
UPDATE download_files
SET source_season = sqlc.arg(source_season),
    source_episode = sqlc.arg(source_episode),
    source_episode_fraction_hundredths = sqlc.arg(source_episode_fraction_hundredths),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND download_id = sqlc.arg(download_id)
  AND selected
  AND (
      source_season IS DISTINCT FROM sqlc.arg(source_season)
      OR source_episode IS DISTINCT FROM sqlc.arg(source_episode)
      OR source_episode_fraction_hundredths IS DISTINCT FROM sqlc.arg(source_episode_fraction_hundredths)
  );

-- name: ExcludeExplicitMappingFile :execrows
UPDATE download_files
SET selected = false,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND download_id = sqlc.arg(download_id)
  AND selected;

-- name: GetEpisodeMappingTargetOccupancy :one
WITH target AS (
    SELECT
        episode.id AS target_episode_id,
        episode.tmdb_episode_id,
        episode.episode_number AS target_episode,
        season.season_number AS target_season,
        series.tmdb_series_id
    FROM media_episodes AS episode
    JOIN tmdb_seasons AS season ON season.id = episode.season_id
    JOIN media_series AS series ON series.id = season.series_id
    WHERE episode.id = sqlc.arg(target_episode_id)
      AND series.id = sqlc.arg(series_id)
)
SELECT
    EXISTS (
        SELECT 1
        FROM target
        JOIN emby_library_items AS item
          ON item.present
         AND item.item_type = 'Episode'
         AND item.file_path IS NOT NULL
         AND (
             (
                 target.tmdb_episode_id IS NOT NULL
                 AND EXISTS (
                     SELECT 1
                     FROM jsonb_each_text(item.provider_ids) AS provider
                     WHERE lower(provider.key) IN ('tmdb', 'themoviedb')
                       AND provider.value = target.tmdb_episode_id::text
                 )
             )
             OR (
                 item.season_number = target.target_season
                 AND item.episode_number = target.target_episode
                 AND EXISTS (
                     SELECT 1
                     FROM emby_library_items AS series_item
                     WHERE series_item.present
                       AND series_item.item_type = 'Series'
                       AND EXISTS (
                           SELECT 1
                           FROM jsonb_each_text(series_item.provider_ids) AS provider
                           WHERE lower(provider.key) IN ('tmdb', 'themoviedb')
                             AND provider.value = target.tmdb_series_id::text
                       )
                       AND (
                           item.parent_emby_id = series_item.emby_id
                           OR EXISTS (
                               SELECT 1
                               FROM emby_library_items AS season_item
                               WHERE season_item.present
                                 AND season_item.item_type = 'Season'
                                 AND season_item.emby_id = item.parent_emby_id
                                 AND season_item.parent_emby_id = series_item.emby_id
                           )
                       )
                 )
             )
         )
    ) AS catalog_present,
    EXISTS (
        SELECT 1
        FROM target
        JOIN episode_mappings AS mapping ON mapping.target_episode_id = target.target_episode_id
        JOIN episode_tasks AS task ON task.mapping_id = mapping.id
        JOIN imports AS imported ON imported.task_id = task.id AND imported.status = 'succeeded'
        WHERE task.acquisition_id <> sqlc.arg(excluded_acquisition_id)
    ) AS managed_import_present,
    EXISTS (
        SELECT 1
        FROM target
        JOIN episode_mappings AS mapping ON mapping.target_episode_id = target.target_episode_id
        JOIN episode_tasks AS task ON task.mapping_id = mapping.id
        WHERE task.acquisition_id <> sqlc.arg(excluded_acquisition_id)
          AND (
              task.state NOT IN ('imported', 'rejected', 'cancelled')
              OR (
                  task.state = 'cancelled'
                  AND (task.video_state = 'failed' OR task.subtitle_state = 'failed')
                  AND task.video_state IN ('failed', 'video_ready')
                  AND task.subtitle_state IN ('failed', 'ass_ready')
              )
          )
        UNION ALL
        SELECT 1
        FROM target
        JOIN episode_mappings AS mapping ON mapping.target_episode_id = target.target_episode_id
        JOIN acquisitions AS acquisition ON acquisition.mapping_profile_id = mapping.profile_id
        LEFT JOIN rss_entries AS owner_entry ON owner_entry.id = acquisition.rss_entry_id
        JOIN LATERAL (
            SELECT candidate.id, candidate.status
            FROM downloads AS candidate
            WHERE candidate.acquisition_id = acquisition.id
              AND candidate.deleted_at IS NULL
            ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
            LIMIT 1
        ) AS download ON true
        WHERE acquisition.id <> sqlc.arg(excluded_acquisition_id)
          AND acquisition.deletion_requested_at IS NULL
          AND mapping.mapping_status = 'mapped'
          AND mapping.source_season::bigint = COALESCE(
              owner_entry.source_season::bigint,
              CASE
                  WHEN acquisition.rss_entry_id IS NULL
                   AND acquisition.source_payload->'singleEpisode' = 'true'::jsonb
                   AND COALESCE(acquisition.source_payload->>'sourceSeason', '') ~ '^[1-9][0-9]{0,9}$'
                  THEN (acquisition.source_payload->>'sourceSeason')::bigint
              END
          )
          AND mapping.source_episode::bigint = COALESCE(
              owner_entry.source_episode::bigint,
              CASE
                  WHEN acquisition.rss_entry_id IS NULL
                   AND acquisition.source_payload->'singleEpisode' = 'true'::jsonb
                   AND COALESCE(acquisition.source_payload->>'sourceEpisode', '') ~ '^[1-9][0-9]{0,9}$'
                  THEN (acquisition.source_payload->>'sourceEpisode')::bigint
              END
          )
          AND mapping.source_episode_fraction_hundredths = COALESCE(
              owner_entry.source_episode_fraction_hundredths,
              CASE
                  WHEN acquisition.rss_entry_id IS NULL
                   AND acquisition.source_payload->'singleEpisode' = 'true'::jsonb
                   AND COALESCE(acquisition.source_payload->>'sourceEpisodeFractionHundredths', '0') ~ '^(?:0|[1-9][0-9]?)$'
                  THEN COALESCE((acquisition.source_payload->>'sourceEpisodeFractionHundredths')::integer, 0)
              END,
              0
          )
          AND download.status <> 'cancelled'
          AND NOT EXISTS (
              SELECT 1 FROM episode_tasks AS task WHERE task.acquisition_id = acquisition.id
          )
        UNION ALL
        SELECT 1
        FROM target
        JOIN episode_mappings AS mapping ON mapping.target_episode_id = target.target_episode_id
        JOIN acquisitions AS acquisition ON acquisition.mapping_profile_id = mapping.profile_id
        JOIN LATERAL (
            SELECT candidate.id, candidate.status
            FROM downloads AS candidate
            WHERE candidate.acquisition_id = acquisition.id
              AND candidate.deleted_at IS NULL
            ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
            LIMIT 1
        ) AS download ON true
        JOIN download_files AS source_file
          ON source_file.download_id = download.id
         AND source_file.selected
         AND source_file.media_kind = 'video'
         AND source_file.source_season = mapping.source_season
         AND source_file.source_episode = mapping.source_episode
         AND source_file.source_episode_fraction_hundredths = mapping.source_episode_fraction_hundredths
        WHERE acquisition.id <> sqlc.arg(excluded_acquisition_id)
          AND acquisition.deletion_requested_at IS NULL
          AND mapping.mapping_status = 'mapped'
          AND download.status <> 'cancelled'
          AND NOT EXISTS (
              SELECT 1 FROM episode_tasks AS task WHERE task.acquisition_id = acquisition.id
          )
    ) AS processing_present;

-- name: UpdateAcquisitionMappingProfile :one
UPDATE acquisitions
SET mapping_profile_id = sqlc.arg(mapping_profile_id),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetMappingProfileAudit :one
SELECT decision_source, agent_resolution_id
FROM episode_mapping_profiles
WHERE id = sqlc.arg(id);

-- name: UpdateSelectedFileCoordinateFamily :execrows
WITH source_file AS (
    SELECT
        selected_file.download_id,
        selected_file.source_season,
        selected_file.source_episode,
        selected_file.source_episode_fraction_hundredths
    FROM download_files AS selected_file
    WHERE selected_file.id = sqlc.arg(source_file_id)
      AND selected_file.selected
      AND selected_file.media_kind = 'video'
)
UPDATE download_files AS related
SET source_season = sqlc.arg(new_source_season),
    source_episode = sqlc.arg(new_source_episode),
    source_episode_fraction_hundredths = sqlc.arg(new_source_episode_fraction_hundredths)
FROM source_file
WHERE related.download_id = source_file.download_id
  AND related.selected
  AND related.media_kind IN ('video', 'subtitle')
  AND related.source_season IS NOT DISTINCT FROM source_file.source_season
  AND related.source_episode IS NOT DISTINCT FROM source_file.source_episode
  AND related.source_episode_fraction_hundredths = source_file.source_episode_fraction_hundredths
  AND (
      related.source_season IS DISTINCT FROM sqlc.arg(new_source_season)
      OR related.source_episode IS DISTINCT FROM sqlc.arg(new_source_episode)
      OR related.source_episode_fraction_hundredths IS DISTINCT FROM sqlc.arg(new_source_episode_fraction_hundredths)
  );

-- name: ApplyMappingProfileToRSSSubscription :one
UPDATE rss_subscriptions AS subscription
SET mapping_profile_id = sqlc.arg(mapping_profile_id),
    version = version + 1,
    updated_at = now()
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE acquisition.id = sqlc.arg(acquisition_id)
  AND subscription.id = entry.subscription_id
  AND subscription.deleted_at IS NULL
RETURNING subscription.id;

-- name: ListMappingScopeSelectedVideos :many
WITH source_subscription AS (
    SELECT entry.subscription_id
    FROM acquisitions AS source_acquisition
    JOIN rss_entries AS entry ON entry.id = source_acquisition.rss_entry_id
    JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
    WHERE source_acquisition.id = sqlc.arg(acquisition_id)
      AND subscription.deleted_at IS NULL
)
SELECT
    file.id,
    file.relative_path,
    file.source_season,
    file.source_episode,
    file.source_episode_fraction_hundredths,
    download.file_resolution_source
FROM acquisitions AS acquisition
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN LATERAL (
    SELECT candidate.*
    FROM downloads AS candidate
    WHERE candidate.acquisition_id = acquisition.id
      AND candidate.deleted_at IS NULL
    ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
    LIMIT 1
) AS download ON true
JOIN download_files AS file ON file.download_id = download.id
WHERE (
      acquisition.id = sqlc.arg(acquisition_id)
      OR entry.subscription_id = (SELECT subscription_id FROM source_subscription)
  )
  AND acquisition.deletion_requested_at IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM episode_tasks AS task
      WHERE task.acquisition_id = acquisition.id
  )
  AND file.selected
  AND file.media_kind = 'video'
ORDER BY acquisition.created_at, acquisition.id, file.file_index;

-- name: SyncMappingScopeSourceFacts :execrows
WITH source_subscription AS (
    SELECT entry.subscription_id
    FROM acquisitions AS source_acquisition
    JOIN rss_entries AS entry ON entry.id = source_acquisition.rss_entry_id
    JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
    WHERE source_acquisition.id = sqlc.arg(acquisition_id)
      AND subscription.deleted_at IS NULL
),
source_facts AS (
    SELECT
        acquisition.id AS acquisition_id,
        acquisition.rss_entry_id,
        file.source_season::integer AS source_season,
        file.source_episode::integer AS source_episode,
        file.source_episode_fraction_hundredths::integer AS source_episode_fraction_hundredths
    FROM acquisitions AS acquisition
    LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
    JOIN LATERAL (
        SELECT candidate.*
        FROM downloads AS candidate
        WHERE candidate.acquisition_id = acquisition.id
          AND candidate.deleted_at IS NULL
        ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
        LIMIT 1
    ) AS download ON true
    JOIN download_files AS file
      ON file.download_id = download.id
     AND file.selected
     AND file.media_kind = 'video'
    WHERE (
          acquisition.id = sqlc.arg(acquisition_id)
          OR entry.subscription_id = (SELECT subscription_id FROM source_subscription)
      )
      AND acquisition.deletion_requested_at IS NULL
      AND file.source_season IS NOT NULL
      AND file.source_episode IS NOT NULL
      AND NOT EXISTS (
          SELECT 1
          FROM download_files AS sibling
          WHERE sibling.download_id = download.id
            AND sibling.selected
            AND sibling.media_kind = 'video'
            AND sibling.id <> file.id
      )
),
updated_acquisitions AS (
    UPDATE acquisitions AS acquisition
    SET source_payload = jsonb_set(
            jsonb_set(
                jsonb_set(acquisition.source_payload, '{sourceSeason}', to_jsonb(source_facts.source_season), true),
                '{sourceEpisode}', to_jsonb(source_facts.source_episode), true
            ),
            '{sourceEpisodeFractionHundredths}', to_jsonb(source_facts.source_episode_fraction_hundredths), true
        ),
        updated_at = now()
    FROM source_facts
    WHERE acquisition.id = source_facts.acquisition_id
    RETURNING
        acquisition.rss_entry_id,
        source_facts.source_season,
        source_facts.source_episode,
        source_facts.source_episode_fraction_hundredths
)
UPDATE rss_entries AS entry
SET source_season = updated_acquisitions.source_season,
    source_episode = updated_acquisitions.source_episode,
    source_episode_fraction_hundredths = updated_acquisitions.source_episode_fraction_hundredths,
    updated_at = now()
FROM updated_acquisitions
WHERE entry.id = updated_acquisitions.rss_entry_id;

-- name: ListMappingMaterializationCandidates :many
WITH source_subscription AS (
    SELECT entry.subscription_id
    FROM acquisitions AS source_acquisition
    JOIN rss_entries AS entry ON entry.id = source_acquisition.rss_entry_id
    JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
    WHERE source_acquisition.id = sqlc.arg(acquisition_id)
      AND subscription.deleted_at IS NULL
)
SELECT download.id, download.status, download.version, download.failure_stage, download.error_code
FROM acquisitions AS acquisition
LEFT JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN LATERAL (
    SELECT candidate.*
    FROM downloads AS candidate
    WHERE candidate.acquisition_id = acquisition.id
      AND candidate.deleted_at IS NULL
    ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
    LIMIT 1
) AS download ON true
WHERE (
      acquisition.id = sqlc.arg(acquisition_id)
      OR entry.subscription_id = (SELECT subscription_id FROM source_subscription)
  )
  AND acquisition.mapping_profile_id = sqlc.arg(mapping_profile_id)
  AND acquisition.deletion_requested_at IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM episode_tasks AS task
      WHERE task.acquisition_id = acquisition.id
  )
  AND (
      download.status = 'completed'
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
  AND EXISTS (
      SELECT 1
      FROM download_files AS selected_video
      WHERE selected_video.download_id = download.id
        AND selected_video.selected
        AND selected_video.media_kind = 'video'
  )
  AND NOT EXISTS (
      SELECT 1
      FROM download_files AS selected_video
      LEFT JOIN episode_mappings AS mapping
        ON mapping.profile_id = sqlc.arg(mapping_profile_id)
       AND mapping.source_season = selected_video.source_season
       AND mapping.source_episode = selected_video.source_episode
       AND mapping.source_episode_fraction_hundredths = selected_video.source_episode_fraction_hundredths
      WHERE selected_video.download_id = download.id
        AND selected_video.selected
        AND selected_video.media_kind = 'video'
        AND (mapping.id IS NULL OR mapping.mapping_status <> 'mapped')
  )
ORDER BY acquisition.created_at, acquisition.id, download.attempt;

-- name: RequeueMappingMaterialization :one
UPDATE downloads
SET status = 'completed',
    failure_stage = NULL,
    error_code = NULL,
    error_message = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND status = 'failed'
  AND failure_stage = 'materialize'
  AND error_code IN (
      'mapping_profile_required',
      'episode_mapping_required',
      'mapping_source_invalid',
      'mapping_source_out_of_range',
      'mapping_context_incomplete',
      'mapping_target_out_of_range',
      'mapping_title_missing'
  )
RETURNING *;

-- name: GetMappingSaveByIdempotencyKey :one
SELECT save.*, profile.version
FROM episode_mapping_saves AS save
JOIN episode_mapping_profiles AS profile ON profile.id = save.profile_id
WHERE save.idempotency_key = sqlc.arg(idempotency_key);

-- name: CreateMappingSave :one
INSERT INTO episode_mapping_saves (
    id,
    acquisition_id,
    profile_id,
    idempotency_key,
    request_fingerprint,
    result_payload,
    created_by,
    decision_source,
    agent_resolution_id
) VALUES (
    sqlc.arg(id),
    sqlc.arg(acquisition_id),
    sqlc.arg(profile_id),
    sqlc.arg(idempotency_key),
    sqlc.arg(request_fingerprint),
    sqlc.arg(result_payload),
    sqlc.narg(created_by),
    sqlc.arg(decision_source),
    sqlc.narg(agent_resolution_id)
)
RETURNING *;

-- name: CreateEmbyScanRun :one
INSERT INTO emby_scan_runs (
    id,
    operation_id,
    created_by
) VALUES (
    sqlc.arg(id),
    sqlc.arg(operation_id),
    sqlc.arg(created_by)
)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: GetEmbyScanRun :one
SELECT *
FROM emby_scan_runs
WHERE id = sqlc.arg(id);

-- name: GetEmbyScanRunByOperation :one
SELECT *
FROM emby_scan_runs
WHERE operation_id = sqlc.arg(operation_id);

-- name: ListEmbyScanRuns :many
SELECT scan.*
FROM emby_scan_runs AS scan
WHERE sqlc.narg(cursor)::uuid IS NULL
   OR (scan.created_at, scan.id) < (
       SELECT cursor_scan.created_at, cursor_scan.id
       FROM emby_scan_runs AS cursor_scan
       WHERE cursor_scan.id = sqlc.narg(cursor)::uuid
   )
ORDER BY scan.created_at DESC, scan.id DESC
LIMIT sqlc.arg(page_size);

-- name: StartEmbyScanRun :one
UPDATE emby_scan_runs
SET status = 'running',
    started_at = COALESCE(started_at, now()),
    error_code = NULL,
    error_message = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: UpsertEmbyLibrary :one
INSERT INTO emby_libraries (
    id,
    emby_id,
    name,
    collection_type,
    locations,
    present,
    last_scan_run_id,
    last_seen_at,
    upstream_payload
) VALUES (
    sqlc.arg(id),
    sqlc.arg(emby_id),
    sqlc.arg(name),
    sqlc.narg(collection_type),
    sqlc.arg(locations),
    true,
    sqlc.arg(last_scan_run_id),
    sqlc.arg(last_seen_at),
    sqlc.arg(upstream_payload)
)
ON CONFLICT (emby_id) DO UPDATE
SET name = EXCLUDED.name,
    collection_type = EXCLUDED.collection_type,
    locations = EXCLUDED.locations,
    present = true,
    last_scan_run_id = EXCLUDED.last_scan_run_id,
    last_seen_at = EXCLUDED.last_seen_at,
    upstream_payload = EXCLUDED.upstream_payload,
    updated_at = now()
RETURNING *;

-- name: UpsertEmbyLibraryItem :one
INSERT INTO emby_library_items (
    id,
    emby_id,
    library_id,
    parent_emby_id,
    item_type,
    name,
    file_path,
    provider_ids,
    season_number,
    episode_number,
    present,
    last_scan_run_id,
    last_seen_at,
    upstream_payload
) VALUES (
    sqlc.arg(id),
    sqlc.arg(emby_id),
    sqlc.arg(library_id),
    sqlc.narg(parent_emby_id),
    sqlc.arg(item_type),
    sqlc.arg(name),
    sqlc.narg(file_path),
    sqlc.arg(provider_ids),
    sqlc.narg(season_number),
    sqlc.narg(episode_number),
    true,
    sqlc.arg(last_scan_run_id),
    sqlc.arg(last_seen_at),
    sqlc.arg(upstream_payload)
)
ON CONFLICT (emby_id) DO UPDATE
SET library_id = EXCLUDED.library_id,
    parent_emby_id = EXCLUDED.parent_emby_id,
    item_type = EXCLUDED.item_type,
    name = EXCLUDED.name,
    file_path = EXCLUDED.file_path,
    provider_ids = EXCLUDED.provider_ids,
    season_number = EXCLUDED.season_number,
    episode_number = EXCLUDED.episode_number,
    present = true,
    last_scan_run_id = EXCLUDED.last_scan_run_id,
    last_seen_at = EXCLUDED.last_seen_at,
    upstream_payload = EXCLUDED.upstream_payload,
    updated_at = now()
RETURNING *;

-- name: MarkEmbyLibrariesAbsent :execrows
UPDATE emby_libraries
SET present = false,
    updated_at = now()
WHERE last_scan_run_id <> sqlc.arg(scan_run_id)
  AND present;

-- name: MarkEmbyLibraryItemsAbsent :execrows
UPDATE emby_library_items
SET present = false,
    updated_at = now()
WHERE last_scan_run_id <> sqlc.arg(scan_run_id)
  AND present;

-- name: CompleteEmbyScanRun :one
UPDATE emby_scan_runs
SET status = 'succeeded',
    library_count = sqlc.arg(library_count),
    item_count = sqlc.arg(item_count),
    error_code = NULL,
    error_message = NULL,
    completed_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
RETURNING *;

-- name: FailEmbyScanRun :one
UPDATE emby_scan_runs
SET status = sqlc.arg(status),
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    completed_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: ListEmbyLibraries :many
SELECT *
FROM emby_libraries
ORDER BY present DESC, lower(name), id;

-- name: GetEmbyLibrary :one
SELECT *
FROM emby_libraries
WHERE id = sqlc.arg(id);

-- name: ListEmbyLibraryItems :many
SELECT
    item.*,
    related_task.id AS imported_task_id
FROM emby_library_items AS item
LEFT JOIN LATERAL (
    SELECT task.id
    FROM episode_tasks AS task
    LEFT JOIN LATERAL (
        SELECT import.destination_video_path
        FROM imports AS import
        WHERE import.task_id = task.id
          AND import.status = 'succeeded'
        ORDER BY import.attempt DESC
        LIMIT 1
    ) AS latest_import ON true
    LEFT JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
    LEFT JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
    WHERE latest_import.destination_video_path = item.file_path
       OR (
           episode.tmdb_episode_id IS NOT NULL
           AND EXISTS (
               SELECT 1
               FROM jsonb_each_text(item.provider_ids) AS provider
               WHERE lower(provider.key) IN ('tmdb', 'themoviedb')
                 AND provider.value = episode.tmdb_episode_id::text
           )
       )
    ORDER BY (latest_import.destination_video_path = item.file_path) DESC, task.updated_at DESC, task.id
    LIMIT 1
) AS related_task ON true
WHERE item.library_id = sqlc.arg(library_id)
  AND (sqlc.narg(item_type)::text IS NULL OR item.item_type = sqlc.narg(item_type))
  AND (sqlc.narg(name)::text IS NULL OR item.name ILIKE '%' || sqlc.narg(name)::text || '%')
  AND (sqlc.narg(present)::boolean IS NULL OR item.present = sqlc.narg(present)::boolean)
  AND (
      sqlc.narg(provider_id)::text IS NULL
      OR EXISTS (
          SELECT 1
          FROM jsonb_each_text(item.provider_ids) AS provider
          WHERE provider.value = sqlc.narg(provider_id)::text
      )
  )
  AND (sqlc.narg(cursor)::uuid IS NULL OR item.id > sqlc.narg(cursor))
ORDER BY item.id
LIMIT sqlc.arg(page_size);

-- name: ListSeriesSeasons :many
SELECT *
FROM tmdb_seasons
WHERE series_id = sqlc.arg(series_id)
ORDER BY season_number;

-- name: ListSeasonEpisodes :many
SELECT *
FROM media_episodes
WHERE season_id = sqlc.arg(season_id)
ORDER BY episode_number;

-- name: GetSeriesLastSync :one
SELECT max(fetched_at)
FROM tmdb_seasons
WHERE series_id = sqlc.arg(series_id);
