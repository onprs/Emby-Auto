-- name: MarkOutdatedRSSSubscriptionProgressDirty :execrows
UPDATE rss_subscription_progress
SET source_revision = source_revision + 1,
    dirty = true,
    dirtied_transaction_id = txid_current(),
    dirtied_at = clock_timestamp()
WHERE model_version < sqlc.arg(model_version)
  AND NOT dirty;

-- name: MarkOutdatedRSSSubscriptionProgressByIDsDirty :execrows
UPDATE rss_subscription_progress
SET source_revision = source_revision + 1,
    dirty = true,
    dirtied_transaction_id = txid_current(),
    dirtied_at = clock_timestamp()
WHERE subscription_id = ANY(sqlc.arg(subscription_ids)::uuid[])
  AND model_version < sqlc.arg(model_version)
  AND NOT dirty;

-- name: LockCurrentTransactionRSSSubscriptionProgress :many
SELECT
    progress.subscription_id,
    progress.source_revision,
    progress.model_version,
    subscription.completed_at
FROM rss_subscription_progress AS progress
JOIN rss_subscriptions AS subscription ON subscription.id = progress.subscription_id
WHERE progress.dirty
  AND progress.dirtied_transaction_id = txid_current()
ORDER BY progress.subscription_id
FOR UPDATE OF progress;

-- name: LockDirtyRSSSubscriptionProgress :many
SELECT
    progress.subscription_id,
    progress.source_revision,
    progress.model_version,
    subscription.completed_at
FROM rss_subscription_progress AS progress
JOIN rss_subscriptions AS subscription ON subscription.id = progress.subscription_id
WHERE progress.dirty
ORDER BY progress.dirtied_at, progress.subscription_id
LIMIT sqlc.arg(batch_size)
FOR UPDATE OF progress SKIP LOCKED;

-- name: LockDirtyRSSSubscriptionProgressByIDs :many
SELECT
    progress.subscription_id,
    progress.source_revision,
    progress.model_version,
    subscription.completed_at
FROM rss_subscription_progress AS progress
JOIN rss_subscriptions AS subscription ON subscription.id = progress.subscription_id
WHERE progress.subscription_id = ANY(sqlc.arg(subscription_ids)::uuid[])
  AND progress.dirty
ORDER BY progress.subscription_id
FOR UPDATE OF progress;

-- name: UpdateRSSSubscriptionProgress :one
UPDATE rss_subscription_progress
SET overall_progress = sqlc.arg(overall_progress),
    task_count = sqlc.arg(task_count),
    completed_task_count = sqlc.arg(completed_task_count),
    attention_task_count = sqlc.arg(attention_task_count),
    calculated_revision = source_revision,
    model_version = sqlc.arg(model_version),
    dirty = false,
    calculated_at = clock_timestamp()
WHERE subscription_id = sqlc.arg(subscription_id)
  AND source_revision = sqlc.arg(expected_source_revision)
RETURNING *;

-- name: GetRSSSubscriptionProgress :one
SELECT *
FROM rss_subscription_progress
WHERE subscription_id = sqlc.arg(subscription_id);

-- name: ListRSSSubscriptionProgressByIDs :many
SELECT *
FROM rss_subscription_progress
WHERE subscription_id = ANY(sqlc.arg(subscription_ids)::uuid[])
ORDER BY subscription_id;

-- name: GetRSSSubscriptionProgressReadiness :one
SELECT
    EXISTS (
        SELECT 1
        FROM rss_subscription_progress AS dirty_progress
        WHERE dirty_progress.dirty
    ) AS has_dirty,
    EXISTS (
        SELECT 1
        FROM rss_subscription_progress AS outdated_progress
        WHERE NOT outdated_progress.dirty
          AND outdated_progress.model_version < sqlc.arg(target_model_version)
    ) AS has_outdated_model,
    EXISTS (
        SELECT 1
        FROM rss_subscription_progress AS newer_progress
        WHERE newer_progress.model_version > sqlc.arg(target_model_version)
    ) AS has_newer_model;

-- name: ListRSSSubscriptionRetryableTaskCountsByIDs :many
SELECT
    requested.id AS subscription_id,
    count(task.id)::bigint AS retryable_task_count
FROM rss_subscriptions AS requested
LEFT JOIN rss_entries AS entry ON entry.subscription_id = requested.id
LEFT JOIN acquisitions AS acquisition ON acquisition.rss_entry_id = entry.id
LEFT JOIN episode_tasks AS task
  ON task.acquisition_id = acquisition.id
 AND task.state = 'failed'
WHERE requested.id = ANY(sqlc.arg(subscription_ids)::uuid[])
GROUP BY requested.id
ORDER BY requested.id;

-- name: ListRSSSubscriptionsByProgressAsc :many
SELECT
    subscription.id,
    subscription.series_id,
    subscription.mapping_profile_id,
    subscription.name,
    subscription.feed_url,
    subscription.enabled,
    subscription.poll_interval_seconds,
    subscription.last_polled_at,
    subscription.next_poll_at,
    subscription.version,
    subscription.created_at,
    subscription.updated_at,
    subscription.source_season,
    subscription.deleted_at,
    subscription.auto_review,
    subscription.cleanup_source_on_completion,
    subscription.completed_at,
    subscription.include_keywords,
    subscription.exclude_keywords,
    subscription.auto_episode_mapping,
    series.title AS series_title,
    series.tmdb_series_id,
    progress.overall_progress,
    progress.task_count,
    progress.completed_task_count,
    progress.attention_task_count
FROM rss_subscription_progress AS progress
JOIN rss_subscriptions AS subscription ON subscription.id = progress.subscription_id
JOIN media_series AS series ON series.id = subscription.series_id
WHERE subscription.deleted_at IS NULL
  AND NOT progress.dirty
  AND progress.model_version = sqlc.arg(model_version)
  AND (
      sqlc.narg(query)::text IS NULL
      OR strpos(LOWER(subscription.name), LOWER(sqlc.narg(query)::text)) > 0
      OR strpos(LOWER(series.title), LOWER(sqlc.narg(query)::text)) > 0
  )
  AND (
      sqlc.narg(cursor_id)::uuid IS NULL
      OR (progress.overall_progress, progress.subscription_id) > (
          sqlc.narg(cursor_progress)::double precision,
          sqlc.narg(cursor_id)::uuid
      )
  )
ORDER BY progress.overall_progress ASC, progress.subscription_id ASC
LIMIT sqlc.arg(page_size);

-- name: ListRSSSubscriptionsByProgressDesc :many
SELECT
    subscription.id,
    subscription.series_id,
    subscription.mapping_profile_id,
    subscription.name,
    subscription.feed_url,
    subscription.enabled,
    subscription.poll_interval_seconds,
    subscription.last_polled_at,
    subscription.next_poll_at,
    subscription.version,
    subscription.created_at,
    subscription.updated_at,
    subscription.source_season,
    subscription.deleted_at,
    subscription.auto_review,
    subscription.cleanup_source_on_completion,
    subscription.completed_at,
    subscription.include_keywords,
    subscription.exclude_keywords,
    subscription.auto_episode_mapping,
    series.title AS series_title,
    series.tmdb_series_id,
    progress.overall_progress,
    progress.task_count,
    progress.completed_task_count,
    progress.attention_task_count
FROM rss_subscription_progress AS progress
JOIN rss_subscriptions AS subscription ON subscription.id = progress.subscription_id
JOIN media_series AS series ON series.id = subscription.series_id
WHERE subscription.deleted_at IS NULL
  AND NOT progress.dirty
  AND progress.model_version = sqlc.arg(model_version)
  AND (
      sqlc.narg(query)::text IS NULL
      OR strpos(LOWER(subscription.name), LOWER(sqlc.narg(query)::text)) > 0
      OR strpos(LOWER(series.title), LOWER(sqlc.narg(query)::text)) > 0
  )
  AND (
      sqlc.narg(cursor_id)::uuid IS NULL
      OR (progress.overall_progress, progress.subscription_id) < (
          sqlc.narg(cursor_progress)::double precision,
          sqlc.narg(cursor_id)::uuid
      )
  )
ORDER BY progress.overall_progress DESC, progress.subscription_id DESC
LIMIT sqlc.arg(page_size);
