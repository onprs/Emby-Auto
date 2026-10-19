-- name: GetRSSMappedTargetEpisodeID :one
SELECT mapping.target_episode_id
FROM rss_subscriptions AS subscription
JOIN episode_mappings AS mapping
  ON mapping.profile_id = subscription.mapping_profile_id
 AND mapping.source_season = sqlc.arg(source_season)
 AND mapping.source_episode = sqlc.arg(source_episode)
 AND mapping.source_episode_fraction_hundredths = 0
 AND mapping.mapping_status = 'mapped'
JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE subscription.id = sqlc.arg(subscription_id)
  AND season.season_number > 0;

-- name: LockRSSSubscriptionsForEntries :many
SELECT subscription.*
FROM rss_subscriptions AS subscription
WHERE subscription.id IN (
    SELECT entry.subscription_id
    FROM rss_entries AS entry
    WHERE entry.id = ANY(sqlc.arg(entry_ids)::uuid[])
)
ORDER BY subscription.id
FOR UPDATE OF subscription;

-- name: ListRSSEntriesByIDs :many
SELECT entry.*
FROM rss_entries AS entry
WHERE entry.id = ANY(sqlc.arg(entry_ids)::uuid[])
ORDER BY entry.id;

-- name: ListRSSEntryMappedRealtimeTargets :many
SELECT
    entry.id AS entry_id,
    mapping.source_season,
    mapping.source_episode,
    mapping.target_episode_id,
    season.season_number AS target_season,
    episode.episode_number AS target_episode,
    episode.tmdb_episode_id,
    series.tmdb_series_id
FROM rss_entries AS entry
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
JOIN media_series AS series ON series.id = subscription.series_id
JOIN episode_mappings AS mapping
  ON mapping.profile_id = subscription.mapping_profile_id
 AND mapping.source_season = entry.source_season
 AND mapping.source_episode = entry.source_episode
 AND mapping.source_episode_fraction_hundredths = entry.source_episode_fraction_hundredths
 AND mapping.mapping_status = 'mapped'
JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE entry.id = ANY(sqlc.arg(entry_ids)::uuid[])
  AND season.season_number > 0
ORDER BY entry.id;
