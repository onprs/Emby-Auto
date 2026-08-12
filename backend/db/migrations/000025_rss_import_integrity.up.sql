ALTER TABLE rss_entries
    ADD COLUMN imported_at timestamptz;

CREATE INDEX rss_entries_subscription_imported_idx
    ON rss_entries (subscription_id, source_season, source_episode)
    WHERE imported_at IS NOT NULL;

-- Acquisition cleanup removes task rows but retains events. Rebuild durable
-- per-entry import evidence by following the original enqueue download ID to
-- task.created and task.imported.
WITH imported_entries AS (
    SELECT
        enqueue.resource_id AS entry_id,
        max(imported.occurred_at) AS imported_at
    FROM events AS enqueue
    JOIN events AS created
      ON created.resource_type = 'episode_task'
     AND created.topic = 'task.created'
     AND created.data->>'downloadId' = enqueue.data->>'downloadId'
    JOIN events AS imported
      ON imported.resource_type = 'episode_task'
     AND imported.resource_id = created.resource_id
     AND imported.topic = 'task.imported'
    WHERE enqueue.resource_type = 'rss_entry'
      AND enqueue.topic = 'rss.entry.enqueueing'
      AND enqueue.resource_id IS NOT NULL
      AND enqueue.data ? 'downloadId'
    GROUP BY enqueue.resource_id
)
UPDATE rss_entries AS entry
SET imported_at = imported.imported_at
FROM imported_entries AS imported
WHERE entry.id = imported.entry_id;

-- Migration 24 treated completion cleanup/history restoration as proof that a
-- full source season was imported. Reopen any subscription for which at least
-- one expected mapped source episode lacks durable import evidence.
WITH completion_bounds AS (
    SELECT
        subscription.id AS subscription_id,
        subscription.source_season,
        CASE
            WHEN profile.anchor_source_season IS NOT NULL THEN (
                SELECT max(mapping.source_episode)
                FROM episode_mappings AS mapping
                WHERE mapping.profile_id = profile.id
                  AND mapping.source_season = subscription.source_season
                  AND mapping.mapping_status = 'mapped'
            )
            ELSE profile.source_season_lengths[subscription.source_season]
        END AS final_source_episode
    FROM rss_subscriptions AS subscription
    JOIN episode_mapping_profiles AS profile ON profile.id = subscription.mapping_profile_id
    WHERE subscription.completed_at IS NOT NULL
      AND subscription.deleted_at IS NULL
), completion_stats AS (
    SELECT
        bound.subscription_id,
        bound.source_season,
        bound.final_source_episode AS expected_count,
        count(DISTINCT entry.source_episode) FILTER (WHERE entry.imported_at IS NOT NULL) AS imported_count
    FROM completion_bounds AS bound
    LEFT JOIN rss_entries AS entry
      ON entry.subscription_id = bound.subscription_id
     AND entry.source_season = bound.source_season
     AND entry.source_episode BETWEEN 1 AND bound.final_source_episode
    WHERE bound.final_source_episode IS NOT NULL
      AND bound.final_source_episode > 0
    GROUP BY bound.subscription_id, bound.source_season, bound.final_source_episode
), invalidated AS (
    UPDATE rss_subscriptions AS subscription
    SET completed_at = NULL,
        enabled = false,
        next_poll_at = NULL,
        version = version + 1,
        updated_at = now()
    FROM completion_stats AS stats
    WHERE subscription.id = stats.subscription_id
      AND stats.imported_count < stats.expected_count
    RETURNING subscription.id, subscription.version
)
INSERT INTO events (
    id,
    topic,
    resource_type,
    resource_id,
    data
)
SELECT
    gen_random_uuid(),
    'rss.subscription.completion_invalidated',
    'rss_subscription',
    invalidated.id,
    jsonb_build_object(
        'reason', 'incomplete_import_evidence',
        'expectedEntryCount', stats.expected_count,
        'importedEntryCount', stats.imported_count,
        'sourceSeason', stats.source_season,
        'version', invalidated.version
    )
FROM invalidated
JOIN completion_stats AS stats ON stats.subscription_id = invalidated.id;
