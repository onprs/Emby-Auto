-- Reopen anchor subscriptions that were completed while the TMDb series was still ongoing.
WITH reopened AS (
    UPDATE rss_subscriptions AS subscription
    SET enabled = true,
        next_poll_at = now(),
        completed_at = NULL,
        version = subscription.version + 1,
        updated_at = now()
    FROM episode_mapping_profiles AS profile
    CROSS JOIN media_series AS series
    WHERE subscription.mapping_profile_id = profile.id
      AND series.id = subscription.series_id
      AND profile.anchor_source_season IS NOT NULL
      AND subscription.completed_at IS NOT NULL
      AND subscription.deleted_at IS NULL
      AND lower(series.metadata->>'status') NOT IN ('ended', 'canceled')
      AND series.metadata ? 'status'
    RETURNING subscription.id, subscription.version
)
INSERT INTO events (topic, resource_type, resource_id, data)
SELECT
    'rss.subscription.completion_invalidated',
    'rss_subscription',
    reopened.id,
    jsonb_build_object(
        'reason', 'series_not_ended',
        'version', reopened.version
    )
FROM reopened;
