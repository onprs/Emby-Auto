-- name: UpsertRSSMediaSeries :one
INSERT INTO media_series (
    id,
    tmdb_series_id,
    title
) VALUES (
    sqlc.arg(id),
    sqlc.arg(tmdb_series_id),
    sqlc.arg(title)
)
ON CONFLICT (tmdb_series_id) DO UPDATE
SET title = EXCLUDED.title,
    updated_at = now()
RETURNING *;

-- name: CreateRSSSubscription :one
INSERT INTO rss_subscriptions (
    id,
    series_id,
    mapping_profile_id,
    name,
    feed_url,
    include_keywords,
    exclude_keywords,
    enabled,
    auto_episode_mapping,
    auto_review,
    cleanup_source_on_completion,
    poll_interval_seconds,
    source_season,
    next_poll_at
) VALUES (
    sqlc.arg(id),
    sqlc.arg(series_id),
    sqlc.narg(mapping_profile_id),
    sqlc.arg(name),
    sqlc.arg(feed_url),
    sqlc.arg(include_keywords),
    sqlc.arg(exclude_keywords),
    sqlc.arg(enabled),
    sqlc.arg(auto_episode_mapping),
    sqlc.arg(auto_review),
    sqlc.arg(cleanup_source_on_completion),
    sqlc.arg(poll_interval_seconds),
    sqlc.arg(source_season),
    CASE WHEN sqlc.arg(enabled)::boolean THEN now() ELSE NULL END
)
RETURNING *;

-- name: RestoreRSSSubscriptionHistory :one
INSERT INTO rss_subscriptions (
    id,
    series_id,
    mapping_profile_id,
    name,
    feed_url,
    enabled,
    auto_review,
    cleanup_source_on_completion,
    poll_interval_seconds,
    last_polled_at,
    next_poll_at,
    version,
    source_season,
    deleted_at,
    completed_at,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(id),
    sqlc.arg(series_id),
    sqlc.arg(mapping_profile_id),
    sqlc.arg(name),
    sqlc.arg(feed_url),
    false,
    sqlc.arg(auto_review),
    sqlc.arg(cleanup_source_on_completion),
    sqlc.arg(poll_interval_seconds),
    sqlc.narg(last_polled_at),
    NULL,
    GREATEST(sqlc.arg(version)::integer, 1) + 1,
    sqlc.arg(source_season),
    NULL,
    NULL,
    sqlc.arg(created_at),
    now()
)
RETURNING *;

-- name: RestoreCompletedRSSEntry :one
INSERT INTO rss_entries (
    id,
    subscription_id,
    identity_key,
    guid,
    btih,
    canonical_url,
    title,
    published_at,
    status,
    enqueue_attempts,
    last_error_code,
    last_error_message,
    last_error_retryable,
    upstream_payload,
    discovered_at,
    enqueued_at,
    updated_at,
    download_uri,
    downloadable,
    rejection_reasons,
    source_season,
    source_episode,
    duplicate_count
) VALUES (
    sqlc.arg(id),
    sqlc.arg(subscription_id),
    sqlc.arg(identity_key),
    sqlc.narg(guid),
    sqlc.narg(btih),
    sqlc.narg(canonical_url),
    sqlc.arg(title),
    sqlc.narg(published_at),
    'enqueued',
    GREATEST(sqlc.arg(enqueue_attempts)::integer, 1),
    NULL,
    NULL,
    false,
    sqlc.arg(upstream_payload),
    sqlc.arg(discovered_at),
    now(),
    now(),
    sqlc.arg(download_uri),
    true,
    ARRAY[]::text[],
    sqlc.arg(source_season),
    sqlc.arg(source_episode),
    sqlc.arg(duplicate_count)
)
RETURNING *;

-- name: GetRSSSubscription :one
SELECT
    subscription.*,
    series.title AS series_title,
    series.tmdb_series_id
FROM rss_subscriptions AS subscription
JOIN media_series AS series ON series.id = subscription.series_id
WHERE subscription.id = sqlc.arg(id)
  AND subscription.deleted_at IS NULL;

-- name: ListRSSSubscriptions :many
SELECT
    subscription.*,
    series.title AS series_title,
    series.tmdb_series_id,
    (
        SELECT count(*)
        FROM episode_tasks AS task
        JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE entry.subscription_id = subscription.id
          AND task.state = 'failed'
    ) AS retryable_task_count
FROM rss_subscriptions AS subscription
JOIN media_series AS series ON series.id = subscription.series_id
WHERE subscription.deleted_at IS NULL
  AND (
      sqlc.narg(cursor_id)::uuid IS NULL
      OR (
          COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest'
          AND (subscription.created_at, subscription.id) > (
              SELECT cursor.created_at, cursor.id
              FROM rss_subscriptions AS cursor
              WHERE cursor.id = sqlc.narg(cursor_id)
          )
      )
      OR (
          COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest'
          AND (subscription.created_at, subscription.id) < (
              SELECT cursor.created_at, cursor.id
              FROM rss_subscriptions AS cursor
              WHERE cursor.id = sqlc.narg(cursor_id)
          )
      )
  )
ORDER BY
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest' THEN subscription.created_at END ASC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest' THEN subscription.created_at END DESC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'oldest' THEN subscription.id END ASC,
    CASE WHEN COALESCE(sqlc.narg(sort)::text, 'newest') = 'newest' THEN subscription.id END DESC
LIMIT sqlc.arg(page_size);

-- name: ListRSSSubscriptionsSorted :many
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
    (
        SELECT count(*)
        FROM episode_tasks AS task
        JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE entry.subscription_id = subscription.id
          AND task.state = 'failed'
    ) AS retryable_task_count
FROM rss_subscriptions AS subscription
JOIN media_series AS series ON series.id = subscription.series_id
WHERE subscription.deleted_at IS NULL
  AND (
      sqlc.narg(cursor_id)::uuid IS NULL
      OR (
          sqlc.narg(sort_order)::text = 'asc'
          AND (
              (
                  CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(subscription.name)::text
                      WHEN 'series_title' THEN LOWER(series.title)::text
                      WHEN 'source_season' THEN lpad(subscription.source_season::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN subscription.enabled THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(subscription.next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(subscription.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END IS NULL
              ),
              COALESCE(
                  CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(subscription.name)::text
                      WHEN 'series_title' THEN LOWER(series.title)::text
                      WHEN 'source_season' THEN lpad(subscription.source_season::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN subscription.enabled THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(subscription.next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(subscription.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END,
                  ''
              ),
              subscription.id
          ) > (
              (
                  CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(sqlc.narg(cursor_name)::text)
                      WHEN 'series_title' THEN LOWER(sqlc.narg(cursor_series_title)::text)
                      WHEN 'source_season' THEN lpad(sqlc.narg(cursor_source_season)::int::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN sqlc.narg(cursor_enabled)::bool THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(sqlc.narg(cursor_next_poll_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(sqlc.narg(cursor_created_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END IS NULL
              ),
              COALESCE(
                  CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(sqlc.narg(cursor_name)::text)
                      WHEN 'series_title' THEN LOWER(sqlc.narg(cursor_series_title)::text)
                      WHEN 'source_season' THEN lpad(sqlc.narg(cursor_source_season)::int::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN sqlc.narg(cursor_enabled)::bool THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(sqlc.narg(cursor_next_poll_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(sqlc.narg(cursor_created_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END,
                  ''
              ),
              sqlc.narg(cursor_id)::uuid
          )
      )
      OR (
          sqlc.narg(sort_order)::text = 'desc'
          AND (
              (
                  CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(subscription.name)::text
                      WHEN 'series_title' THEN LOWER(series.title)::text
                      WHEN 'source_season' THEN lpad(subscription.source_season::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN subscription.enabled THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(subscription.next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(subscription.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END IS NOT NULL
              ) > (
                  CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(sqlc.narg(cursor_name)::text)
                      WHEN 'series_title' THEN LOWER(sqlc.narg(cursor_series_title)::text)
                      WHEN 'source_season' THEN lpad(sqlc.narg(cursor_source_season)::int::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN sqlc.narg(cursor_enabled)::bool THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(sqlc.narg(cursor_next_poll_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(sqlc.narg(cursor_created_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END IS NOT NULL
              )
              OR (
                  (CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(subscription.name)::text
                      WHEN 'series_title' THEN LOWER(series.title)::text
                      WHEN 'source_season' THEN lpad(subscription.source_season::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN subscription.enabled THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(subscription.next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(subscription.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END IS NOT NULL) = (CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(sqlc.narg(cursor_name)::text)
                      WHEN 'series_title' THEN LOWER(sqlc.narg(cursor_series_title)::text)
                      WHEN 'source_season' THEN lpad(sqlc.narg(cursor_source_season)::int::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN sqlc.narg(cursor_enabled)::bool THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(sqlc.narg(cursor_next_poll_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(sqlc.narg(cursor_created_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END IS NOT NULL)
                  AND COALESCE(
                      CASE sqlc.narg(sort_key)::text
                          WHEN 'name' THEN LOWER(subscription.name)::text
                          WHEN 'series_title' THEN LOWER(series.title)::text
                          WHEN 'source_season' THEN lpad(subscription.source_season::text, 12, '0')
                          WHEN 'enabled' THEN CASE WHEN subscription.enabled THEN '1' ELSE '0' END
                          WHEN 'next_poll_at' THEN to_char(subscription.next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                          WHEN 'created_at' THEN to_char(subscription.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      END,
                      ''
                  ) < COALESCE(
                      CASE sqlc.narg(sort_key)::text
                          WHEN 'name' THEN LOWER(sqlc.narg(cursor_name)::text)
                          WHEN 'series_title' THEN LOWER(sqlc.narg(cursor_series_title)::text)
                          WHEN 'source_season' THEN lpad(sqlc.narg(cursor_source_season)::int::text, 12, '0')
                          WHEN 'enabled' THEN CASE WHEN sqlc.narg(cursor_enabled)::bool THEN '1' ELSE '0' END
                          WHEN 'next_poll_at' THEN to_char(sqlc.narg(cursor_next_poll_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                          WHEN 'created_at' THEN to_char(sqlc.narg(cursor_created_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      END,
                      ''
                  )
              )
              OR (
                  (CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(subscription.name)::text
                      WHEN 'series_title' THEN LOWER(series.title)::text
                      WHEN 'source_season' THEN lpad(subscription.source_season::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN subscription.enabled THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(subscription.next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(subscription.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END IS NOT NULL) = (CASE sqlc.narg(sort_key)::text
                      WHEN 'name' THEN LOWER(sqlc.narg(cursor_name)::text)
                      WHEN 'series_title' THEN LOWER(sqlc.narg(cursor_series_title)::text)
                      WHEN 'source_season' THEN lpad(sqlc.narg(cursor_source_season)::int::text, 12, '0')
                      WHEN 'enabled' THEN CASE WHEN sqlc.narg(cursor_enabled)::bool THEN '1' ELSE '0' END
                      WHEN 'next_poll_at' THEN to_char(sqlc.narg(cursor_next_poll_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      WHEN 'created_at' THEN to_char(sqlc.narg(cursor_created_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                  END IS NOT NULL)
                  AND COALESCE(
                      CASE sqlc.narg(sort_key)::text
                          WHEN 'name' THEN LOWER(subscription.name)::text
                          WHEN 'series_title' THEN LOWER(series.title)::text
                          WHEN 'source_season' THEN lpad(subscription.source_season::text, 12, '0')
                          WHEN 'enabled' THEN CASE WHEN subscription.enabled THEN '1' ELSE '0' END
                          WHEN 'next_poll_at' THEN to_char(subscription.next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                          WHEN 'created_at' THEN to_char(subscription.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      END,
                      ''
                  ) = COALESCE(
                      CASE sqlc.narg(sort_key)::text
                          WHEN 'name' THEN LOWER(sqlc.narg(cursor_name)::text)
                          WHEN 'series_title' THEN LOWER(sqlc.narg(cursor_series_title)::text)
                          WHEN 'source_season' THEN lpad(sqlc.narg(cursor_source_season)::int::text, 12, '0')
                          WHEN 'enabled' THEN CASE WHEN sqlc.narg(cursor_enabled)::bool THEN '1' ELSE '0' END
                          WHEN 'next_poll_at' THEN to_char(sqlc.narg(cursor_next_poll_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                          WHEN 'created_at' THEN to_char(sqlc.narg(cursor_created_at)::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
                      END,
                      ''
                  )
                  AND subscription.id < sqlc.narg(cursor_id)::uuid
              )
          )
      )
  )
ORDER BY
    CASE WHEN sqlc.narg(sort_order)::text = 'asc' THEN
        CASE sqlc.narg(sort_key)::text
            WHEN 'name' THEN LOWER(subscription.name)::text
            WHEN 'series_title' THEN LOWER(series.title)::text
            WHEN 'source_season' THEN lpad(subscription.source_season::text, 12, '0')
            WHEN 'enabled' THEN CASE WHEN subscription.enabled THEN '1' ELSE '0' END
            WHEN 'next_poll_at' THEN to_char(subscription.next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
            WHEN 'created_at' THEN to_char(subscription.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
        END
    END ASC NULLS LAST,
    CASE WHEN sqlc.narg(sort_order)::text = 'desc' THEN
        CASE sqlc.narg(sort_key)::text
            WHEN 'name' THEN LOWER(subscription.name)::text
            WHEN 'series_title' THEN LOWER(series.title)::text
            WHEN 'source_season' THEN lpad(subscription.source_season::text, 12, '0')
            WHEN 'enabled' THEN CASE WHEN subscription.enabled THEN '1' ELSE '0' END
            WHEN 'next_poll_at' THEN to_char(subscription.next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
            WHEN 'created_at' THEN to_char(subscription.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')
        END
    END DESC NULLS FIRST,
    CASE WHEN sqlc.narg(sort_order)::text = 'asc' THEN subscription.id END ASC,
    CASE WHEN sqlc.narg(sort_order)::text = 'desc' THEN subscription.id END DESC
LIMIT sqlc.arg(page_size);

-- name: ListRSSSubscriptionAcquisitions :many
SELECT acquisition.*
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND acquisition.deletion_requested_at IS NULL
  AND (
      NOT EXISTS (
          SELECT 1
          FROM downloads AS download
          WHERE download.acquisition_id = acquisition.id
      )
      OR EXISTS (
          SELECT 1
          FROM downloads AS download
          WHERE download.acquisition_id = acquisition.id
            AND download.deleted_at IS NULL
            AND download.status <> 'cancelled'
      )
      OR EXISTS (
          SELECT 1
          FROM episode_tasks AS task
          JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
          JOIN downloads AS source_download ON source_download.id = source_file.download_id
          WHERE task.acquisition_id = acquisition.id
            AND source_download.deleted_at IS NULL
      )
  )
ORDER BY acquisition.created_at, acquisition.id;

-- name: ListRSSSubscriptionAcquisitionsBySubscriptionIDs :many
SELECT acquisition.*, entry.subscription_id
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
WHERE entry.subscription_id = ANY(sqlc.arg(subscription_ids)::uuid[])
  AND acquisition.deletion_requested_at IS NULL
  AND (
      NOT EXISTS (
          SELECT 1
          FROM downloads AS download
          WHERE download.acquisition_id = acquisition.id
      )
      OR EXISTS (
          SELECT 1
          FROM downloads AS download
          WHERE download.acquisition_id = acquisition.id
            AND download.deleted_at IS NULL
            AND download.status <> 'cancelled'
      )
      OR EXISTS (
          SELECT 1
          FROM episode_tasks AS task
          JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
          JOIN downloads AS source_download ON source_download.id = source_file.download_id
          WHERE task.acquisition_id = acquisition.id
            AND source_download.deleted_at IS NULL
      )
  )
ORDER BY acquisition.created_at, acquisition.id;

-- name: UpdateRSSSubscription :one
UPDATE rss_subscriptions
SET name = sqlc.arg(name),
    feed_url = sqlc.arg(feed_url),
    include_keywords = sqlc.arg(include_keywords),
    exclude_keywords = sqlc.arg(exclude_keywords),
    enabled = sqlc.arg(enabled),
    auto_episode_mapping = sqlc.arg(auto_episode_mapping),
    auto_review = sqlc.arg(auto_review),
    cleanup_source_on_completion = sqlc.arg(cleanup_source_on_completion),
    poll_interval_seconds = sqlc.arg(poll_interval_seconds),
    source_season = sqlc.arg(source_season),
    mapping_profile_id = sqlc.narg(mapping_profile_id),
    next_poll_at = CASE
        WHEN sqlc.arg(enabled)::boolean THEN COALESCE(next_poll_at, now())
        ELSE NULL
    END,
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
  AND (completed_at IS NULL OR NOT sqlc.arg(enabled)::boolean)
RETURNING *;

-- name: ListRSSAutoReviewPendingTasks :many
SELECT
    task.id,
    task.version,
    entry.subscription_id
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE subscription.auto_review
  AND subscription.deleted_at IS NULL
  AND task.state = 'awaiting_review'
  AND task.video_state = 'video_ready'
  AND task.subtitle_state = 'ass_ready'
  AND EXISTS (SELECT 1 FROM artifact_sets WHERE task_id = task.id)
  AND NOT EXISTS (SELECT 1 FROM reviews WHERE task_id = task.id)
  AND (
      sqlc.narg(subscription_id)::uuid IS NULL
      OR entry.subscription_id = sqlc.narg(subscription_id)
  )
ORDER BY task.created_at, task.id
FOR UPDATE OF task;

-- name: PropagateRSSMappingProfile :execrows
UPDATE acquisitions AS acquisition
SET mapping_profile_id = sqlc.narg(mapping_profile_id),
    updated_at = now()
FROM rss_entries AS entry
WHERE acquisition.rss_entry_id = entry.id
  AND entry.subscription_id = sqlc.arg(subscription_id)
  AND acquisition.deletion_requested_at IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM episode_tasks AS task
      WHERE task.acquisition_id = acquisition.id
  );

-- name: MarkRSSSubscriptionCompleted :one
UPDATE rss_subscriptions
SET enabled = false,
    next_poll_at = NULL,
    completed_at = COALESCE(completed_at, now()),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
  AND completed_at IS NULL
RETURNING *;

-- name: RetainArchivedRSSSubscriptionCompletion :one
UPDATE rss_subscriptions
SET enabled = false,
    next_poll_at = NULL,
    deleted_at = NULL,
    completed_at = COALESCE(completed_at, now()),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND deleted_at IS NOT NULL
RETURNING *;

-- name: ArchiveRSSSubscription :one
UPDATE rss_subscriptions
SET enabled = false,
    next_poll_at = NULL,
    deleted_at = now(),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
RETURNING *;

-- name: GetRSSPollCommand :one
SELECT
    subscription.id,
    subscription.feed_url,
    subscription.include_keywords,
    subscription.exclude_keywords,
    subscription.enabled,
    subscription.auto_episode_mapping,
    subscription.poll_interval_seconds,
    subscription.source_season,
    subscription.version,
    subscription.deleted_at,
    subscription.completed_at
FROM rss_subscriptions AS subscription
WHERE subscription.id = sqlc.arg(id);

-- name: LockRSSPollMappingContext :one
SELECT
    subscription.id,
    subscription.series_id,
    subscription.mapping_profile_id,
    subscription.auto_episode_mapping,
    subscription.enabled,
    subscription.include_keywords,
    subscription.exclude_keywords,
    subscription.source_season,
    subscription.poll_interval_seconds,
    subscription.version,
    subscription.deleted_at,
    subscription.completed_at
FROM rss_subscriptions AS subscription
WHERE subscription.id = sqlc.arg(id)
FOR UPDATE OF subscription;

-- name: ApplyDeterministicRSSPollMappingProfile :one
UPDATE rss_subscriptions
SET mapping_profile_id = sqlc.arg(mapping_profile_id),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND mapping_profile_id IS NULL
  AND enabled
  AND auto_episode_mapping
  AND deleted_at IS NULL
  AND completed_at IS NULL
RETURNING version;

-- name: ListRSSPreAcquisitionMappingRecoveryCandidates :many
SELECT subscription.id, subscription.version
FROM rss_subscriptions AS subscription
WHERE subscription.enabled
  AND subscription.auto_episode_mapping
  AND subscription.mapping_profile_id IS NULL
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND EXISTS (
      SELECT 1
      FROM tmdb_seasons AS season
      JOIN media_episodes AS episode ON episode.season_id = season.id
      WHERE season.series_id = subscription.series_id
        AND season.season_number > 0
        AND episode.episode_number > 0
  )
  AND EXISTS (
      SELECT 1
      FROM operations AS failed_poll
      WHERE failed_poll.kind = 'rss.poll'
        AND failed_poll.resource_type = 'rss_subscription'
        AND failed_poll.resource_id = subscription.id
        AND failed_poll.status = 'failed'
        AND failed_poll.error_code = 'rss_realtime_mapping_unavailable'
  )
  AND NOT EXISTS (
      SELECT 1
      FROM operations AS recovery
      WHERE recovery.idempotency_key =
          'rss.poll:recovery:preacquisition-mapping-v1:' || subscription.id::text
  )
ORDER BY subscription.created_at, subscription.id
LIMIT 100;

-- name: ListRSSMappedRealtimeTargets :many
SELECT
    mapping.source_season,
    mapping.source_episode,
    mapping.target_episode_id,
    season.season_number AS target_season,
    episode.episode_number AS target_episode,
    episode.tmdb_episode_id,
    series.tmdb_series_id
FROM rss_subscriptions AS subscription
JOIN media_series AS series ON series.id = subscription.series_id
JOIN episode_mappings AS mapping
  ON mapping.profile_id = subscription.mapping_profile_id
 AND mapping.mapping_status = 'mapped'
JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE subscription.id = sqlc.arg(subscription_id)
  AND season.season_number > 0
ORDER BY mapping.source_season, mapping.source_episode;

-- name: GetRSSEntryMappedRealtimeTarget :one
SELECT
    entry.subscription_id,
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
 AND mapping.mapping_status = 'mapped'
JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
JOIN tmdb_seasons AS season ON season.id = episode.season_id
WHERE entry.id = sqlc.arg(entry_id)
  AND season.season_number > 0;

-- name: UpsertRSSRealtimeTargetCheck :exec
INSERT INTO rss_target_realtime_checks (
    target_episode_id,
    check_id,
    present,
    match_source,
    checked_at
) VALUES (
    sqlc.arg(target_episode_id),
    sqlc.arg(check_id),
    sqlc.arg(present),
    sqlc.arg(match_source),
    sqlc.arg(checked_at)
)
ON CONFLICT (target_episode_id, check_id) DO UPDATE
SET present = EXCLUDED.present,
    match_source = EXCLUDED.match_source,
    checked_at = EXCLUDED.checked_at;

-- name: DeleteExpiredRSSRealtimeTargetChecks :exec
DELETE FROM rss_target_realtime_checks
WHERE checked_at < now() - interval '10 minutes';

-- name: GetRSSMappedTargetOccupancy :one
WITH target AS (
    SELECT
        mapping.target_episode_id,
        season.season_number AS target_season,
        episode.episode_number AS target_episode,
        episode.tmdb_episode_id,
        series.tmdb_series_id
    FROM rss_subscriptions AS subscription
    JOIN media_series AS series ON series.id = subscription.series_id
    JOIN episode_mappings AS mapping
      ON mapping.profile_id = subscription.mapping_profile_id
     AND mapping.source_season = sqlc.arg(source_season)
     AND mapping.source_episode = sqlc.arg(source_episode)
     AND mapping.mapping_status = 'mapped'
    JOIN media_episodes AS episode ON episode.id = mapping.target_episode_id
    JOIN tmdb_seasons AS season ON season.id = episode.season_id
    WHERE subscription.id = sqlc.arg(subscription_id)
      AND season.season_number > 0
)
SELECT
    target.target_episode_id,
    target.target_season,
    target.target_episode,
    EXISTS (
        SELECT 1
        FROM rss_target_realtime_checks AS realtime
        WHERE realtime.target_episode_id = target.target_episode_id
          AND realtime.check_id = sqlc.narg(realtime_check_id)::uuid
          AND realtime.checked_at >= now() - interval '30 seconds'
    ) AS realtime_check_valid,
    COALESCE((
        SELECT realtime.present
        FROM rss_target_realtime_checks AS realtime
        WHERE realtime.target_episode_id = target.target_episode_id
          AND realtime.check_id = sqlc.narg(realtime_check_id)::uuid
          AND realtime.checked_at >= now() - interval '30 seconds'
    ), false)::boolean AS realtime_catalog_present,
    EXISTS (
        SELECT 1
        FROM emby_library_items AS item
        WHERE item.present
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
        FROM episode_tasks AS task
        JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
        JOIN episode_mappings AS task_mapping ON task_mapping.id = task.mapping_id
        JOIN imports AS imported ON imported.task_id = task.id AND imported.status = 'succeeded'
        WHERE task_mapping.target_episode_id = target.target_episode_id
          AND acquisition.rss_entry_id IS DISTINCT FROM sqlc.narg(excluded_rss_entry_id)::uuid
    ) AS managed_import_present,
    EXISTS (
        SELECT 1
        FROM acquisitions AS acquisition
        WHERE acquisition.deletion_requested_at IS NULL
          AND acquisition.rss_entry_id IS DISTINCT FROM sqlc.narg(excluded_rss_entry_id)::uuid
          AND (
              EXISTS (
                  SELECT 1
                  FROM episode_tasks AS task
                  JOIN episode_mappings AS task_mapping ON task_mapping.id = task.mapping_id
                  WHERE task.acquisition_id = acquisition.id
                    AND task_mapping.target_episode_id = target.target_episode_id
                    AND task.state NOT IN ('imported', 'rejected', 'cancelled', 'failed')
              )
              OR EXISTS (
                  SELECT 1
                  FROM rss_entries AS owner_entry
                  JOIN episode_mappings AS owner_mapping
                    ON owner_mapping.profile_id = acquisition.mapping_profile_id
                   AND owner_mapping.source_season = owner_entry.source_season
                   AND owner_mapping.source_episode = owner_entry.source_episode
                   AND owner_mapping.mapping_status = 'mapped'
                  JOIN downloads AS download ON download.acquisition_id = acquisition.id
                  WHERE owner_entry.id = acquisition.rss_entry_id
                    AND owner_mapping.target_episode_id = target.target_episode_id
                    AND download.deleted_at IS NULL
                    AND download.status NOT IN ('failed', 'cancelled')
                    AND NOT EXISTS (
                        SELECT 1 FROM episode_tasks AS task WHERE task.acquisition_id = acquisition.id
                    )
              )
          )
    ) AS processing_present
FROM target;

-- name: ListRSSEmbyCatalogFulfilledEntries :many
SELECT id, source_season, source_episode
FROM rss_entries
WHERE subscription_id = sqlc.arg(subscription_id)
  AND imported_at IS NOT NULL
  AND fulfillment_source = 'emby_catalog'
ORDER BY id;

-- name: ClearRSSEmbyCatalogFulfillment :one
UPDATE rss_entries
SET imported_at = NULL,
    fulfillment_source = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND imported_at IS NOT NULL
  AND fulfillment_source = 'emby_catalog'
RETURNING *;

-- name: MarkRSSEntryTargetOccupied :one
UPDATE rss_entries
SET downloadable = false,
    rejection_reasons = ARRAY[sqlc.arg(rejection_reason)::text],
    imported_at = CASE
        WHEN sqlc.arg(fulfilled)::boolean THEN COALESCE(imported_at, now())
        ELSE imported_at
    END,
    fulfillment_source = CASE
        WHEN sqlc.arg(fulfilled)::boolean THEN COALESCE(fulfillment_source, sqlc.arg(fulfillment_source)::text)
        ELSE fulfillment_source
    END,
    last_error_code = NULL,
    last_error_message = NULL,
    last_error_retryable = false,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND NOT (
      imported_at IS NOT NULL
      AND fulfillment_source IS NOT NULL
      AND fulfillment_source = 'managed_import'
  )
  AND (
      downloadable
      OR rejection_reasons IS DISTINCT FROM ARRAY[sqlc.arg(rejection_reason)::text]
      OR (sqlc.arg(fulfilled)::boolean AND imported_at IS NULL)
  )
RETURNING *;

-- name: ListRSSImportConflictReconciliationCandidates :many
SELECT DISTINCT
    acquisition.id AS acquisition_id,
    entry.id AS entry_id,
    entry.source_season,
    entry.source_episode
FROM rss_entries AS entry
JOIN acquisitions AS acquisition ON acquisition.rss_entry_id = entry.id
JOIN episode_tasks AS task ON task.acquisition_id = acquisition.id
JOIN LATERAL (
    SELECT imported.status, imported.error_code
    FROM imports AS imported
    WHERE imported.task_id = task.id
    ORDER BY imported.attempt DESC
    LIMIT 1
) AS latest_import ON true
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND acquisition.deletion_requested_at IS NULL
  AND task.state = 'failed'
  AND task.failure_stage = 'import'
  AND task.error_code = 'library_destination_conflict'
  AND latest_import.status = 'failed'
  AND latest_import.error_code = 'library_destination_conflict'
  AND NOT EXISTS (
      SELECT 1
      FROM episode_tasks AS sibling
      LEFT JOIN LATERAL (
          SELECT imported.status, imported.error_code
          FROM imports AS imported
          WHERE imported.task_id = sibling.id
          ORDER BY imported.attempt DESC
          LIMIT 1
      ) AS sibling_import ON true
      WHERE sibling.acquisition_id = acquisition.id
        AND (
            sibling.state <> 'failed'
            OR sibling.failure_stage IS DISTINCT FROM 'import'
            OR sibling.error_code IS DISTINCT FROM 'library_destination_conflict'
            OR sibling_import.status IS DISTINCT FROM 'failed'
            OR sibling_import.error_code IS DISTINCT FROM 'library_destination_conflict'
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM episode_tasks AS sibling
      JOIN imports AS imported ON imported.task_id = sibling.id
      WHERE sibling.acquisition_id = acquisition.id
        AND imported.status = 'succeeded'
  )
ORDER BY acquisition.id;

-- name: LockRSSSubscriptionForTaskImport :one
SELECT subscription.id
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE task.id = sqlc.arg(task_id)
  AND subscription.deleted_at IS NULL
FOR UPDATE OF subscription;

-- name: MarkRSSEntryImportedForTask :one
UPDATE rss_entries AS entry
SET imported_at = COALESCE(entry.imported_at, now()),
    fulfillment_source = 'managed_import',
    updated_at = now()
FROM acquisitions AS acquisition
JOIN episode_tasks AS task ON task.acquisition_id = acquisition.id
WHERE task.id = sqlc.arg(task_id)
  AND task.state = 'imported'
  AND entry.id = acquisition.rss_entry_id
RETURNING entry.*;

-- name: LockRSSSubscriptionAtCompleteImport :one
SELECT
    subscription.id,
    subscription.version,
    subscription.cleanup_source_on_completion,
    subscription.source_season,
    completion.final_source_episode::integer AS source_episode
FROM episode_tasks AS task
JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
JOIN episode_mapping_profiles AS profile ON profile.id = subscription.mapping_profile_id
CROSS JOIN LATERAL (
    SELECT CASE
        WHEN profile.anchor_source_season IS NOT NULL THEN (
            SELECT max(mapping.source_episode)
            FROM episode_mappings AS mapping
            WHERE mapping.profile_id = profile.id
              AND mapping.source_season = subscription.source_season
              AND mapping.mapping_status = 'mapped'
        )
        ELSE profile.source_season_lengths[subscription.source_season]
    END AS final_source_episode
) AS completion
WHERE task.id = sqlc.arg(task_id)
  AND task.state = 'imported'
  AND entry.imported_at IS NOT NULL
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND completion.final_source_episode IS NOT NULL
  AND completion.final_source_episode > 0
  AND NOT EXISTS (
      SELECT 1
      FROM generate_series(1, completion.final_source_episode) AS expected(source_episode)
      WHERE NOT EXISTS (
          SELECT 1
          FROM rss_entries AS imported_entry
          WHERE imported_entry.subscription_id = subscription.id
            AND imported_entry.source_season = subscription.source_season
            AND imported_entry.source_episode = expected.source_episode
            AND imported_entry.imported_at IS NOT NULL
      )
  )
FOR UPDATE OF subscription;

-- name: LockRSSSubscriptionAtFulfillment :one
SELECT
    subscription.id,
    subscription.version,
    subscription.cleanup_source_on_completion,
    subscription.source_season,
    completion.final_source_episode::integer AS source_episode
FROM rss_subscriptions AS subscription
JOIN episode_mapping_profiles AS profile ON profile.id = subscription.mapping_profile_id
CROSS JOIN LATERAL (
    SELECT CASE
        WHEN profile.anchor_source_season IS NOT NULL THEN (
            SELECT max(mapping.source_episode)
            FROM episode_mappings AS mapping
            WHERE mapping.profile_id = profile.id
              AND mapping.source_season = subscription.source_season
              AND mapping.mapping_status = 'mapped'
        )
        ELSE profile.source_season_lengths[subscription.source_season]
    END AS final_source_episode
) AS completion
WHERE subscription.id = sqlc.arg(subscription_id)
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND completion.final_source_episode IS NOT NULL
  AND completion.final_source_episode > 0
  AND NOT EXISTS (
      SELECT 1
      FROM generate_series(1, completion.final_source_episode) AS expected(source_episode)
      WHERE NOT EXISTS (
          SELECT 1
          FROM rss_entries AS fulfilled_entry
          WHERE fulfilled_entry.subscription_id = subscription.id
            AND fulfilled_entry.source_season = subscription.source_season
            AND fulfilled_entry.source_episode = expected.source_episode
            AND fulfilled_entry.imported_at IS NOT NULL
      )
  )
FOR UPDATE OF subscription;

-- name: ListRSSCompletionCleanupCandidates :many
SELECT
    task.id AS task_id,
    import_record.id AS import_id,
    download.id AS download_id
FROM rss_entries AS entry
JOIN acquisitions AS acquisition ON acquisition.rss_entry_id = entry.id
JOIN episode_tasks AS task ON task.acquisition_id = acquisition.id
JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
JOIN downloads AS download ON download.id = source_file.download_id
JOIN imports AS import_record ON import_record.task_id = task.id AND import_record.status = 'succeeded'
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND task.state = 'imported'
  AND NOT EXISTS (
      SELECT 1
      FROM cleanup_runs AS cleanup
      WHERE cleanup.task_id = task.id
  )
ORDER BY task.created_at, task.id;

-- name: ListRSSImportedEntryAcquisitions :many
WITH successful_history AS (
    SELECT DISTINCT ON (enqueue.resource_id)
        enqueue.resource_id AS entry_id,
        (enqueue.data->>'acquisitionId')::uuid AS acquisition_id,
        imported.occurred_at AS imported_at
    FROM events AS enqueue
    JOIN rss_entries AS entry
      ON entry.id = enqueue.resource_id
     AND entry.imported_at IS NOT NULL
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
      AND entry.subscription_id = sqlc.arg(subscription_id)
      AND enqueue.data ? 'acquisitionId'
      AND enqueue.data ? 'downloadId'
    ORDER BY enqueue.resource_id, imported.occurred_at DESC, enqueue.occurred_at DESC
)
SELECT entry_id, acquisition_id, imported_at
FROM successful_history
ORDER BY entry_id;

-- name: GetRSSSubscriptionImportedCount :one
SELECT
    count(DISTINCT entry.source_episode) FILTER (WHERE entry.imported_at IS NOT NULL)::bigint AS imported_count
FROM rss_subscriptions AS subscription
LEFT JOIN rss_entries AS entry
  ON entry.subscription_id = subscription.id
 AND entry.source_season = subscription.source_season
WHERE subscription.id = sqlc.arg(id)
GROUP BY subscription.id;

-- name: ListRSSSubscriptionImportedCountsBySubscriptionIDs :many
SELECT
    subscription.id,
    count(DISTINCT entry.source_episode) FILTER (WHERE entry.imported_at IS NOT NULL)::bigint AS imported_count
FROM rss_subscriptions AS subscription
LEFT JOIN rss_entries AS entry
  ON entry.subscription_id = subscription.id
 AND entry.source_season = subscription.source_season
WHERE subscription.id = ANY(sqlc.arg(subscription_ids)::uuid[])
GROUP BY subscription.id;

-- name: LockRSSIncompleteRecoveryEntries :many
SELECT
    entry.*,
    acquisition.id AS acquisition_id
FROM rss_entries AS entry
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
LEFT JOIN LATERAL (
    SELECT candidate.id
    FROM acquisitions AS candidate
    WHERE candidate.rss_entry_id = entry.id
      AND candidate.deletion_requested_at IS NULL
    ORDER BY candidate.created_at DESC, candidate.id DESC
    LIMIT 1
) AS acquisition ON true
WHERE subscription.id = sqlc.arg(subscription_id)
  AND NOT subscription.enabled
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND entry.source_season = subscription.source_season
  AND entry.source_episode = ANY(sqlc.arg(source_episodes)::integer[])
FOR UPDATE OF entry, subscription;

-- name: ResetRSSIncompleteRecoveryEntries :many
UPDATE rss_entries AS entry
SET status = 'discovered',
    last_error_code = NULL,
    last_error_message = NULL,
    last_error_retryable = false,
    updated_at = now()
FROM rss_subscriptions AS subscription
WHERE subscription.id = sqlc.arg(subscription_id)
  AND NOT subscription.enabled
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND entry.subscription_id = subscription.id
  AND entry.source_season = subscription.source_season
  AND entry.source_episode = ANY(sqlc.arg(source_episodes)::integer[])
  AND entry.imported_at IS NULL
  AND entry.downloadable
  AND entry.status IN ('enqueued', 'enqueue_failed')
  AND NOT EXISTS (
      SELECT 1
      FROM acquisitions AS acquisition
      WHERE acquisition.rss_entry_id = entry.id
        AND acquisition.deletion_requested_at IS NULL
  )
RETURNING entry.*;

-- name: GetRSSRecoveryScheduleState :one
SELECT
    entry.status,
    acquisition.id AS acquisition_id,
    download.id AS download_id,
    operation.id AS operation_id
FROM rss_entries AS entry
JOIN acquisitions AS acquisition
  ON acquisition.rss_entry_id = entry.id
 AND acquisition.deletion_requested_at IS NULL
JOIN LATERAL (
    SELECT candidate.id
    FROM downloads AS candidate
    WHERE candidate.acquisition_id = acquisition.id
      AND candidate.deleted_at IS NULL
    ORDER BY candidate.attempt DESC
    LIMIT 1
) AS download ON true
JOIN operations AS operation
  ON operation.resource_type = 'download'
 AND operation.resource_id = download.id
 AND operation.kind = 'download.enqueue'
WHERE entry.id = sqlc.arg(entry_id)
  AND entry.imported_at IS NULL
ORDER BY operation.created_at DESC
LIMIT 1;

-- name: CreateRSSAdjudicationBatch :one
INSERT INTO rss_adjudication_batches (id, subscription_id)
VALUES (sqlc.arg(id), sqlc.arg(subscription_id))
RETURNING *;

-- name: FinalizeRSSAdjudicationBatch :one
UPDATE rss_adjudication_batches
SET entry_count = sqlc.arg(entry_count),
    updated_at = now()
WHERE rss_adjudication_batches.id = sqlc.arg(batch_id)
  AND status = 'pending'
RETURNING *;

-- name: DeleteEmptyRSSAdjudicationBatch :execrows
DELETE FROM rss_adjudication_batches
WHERE rss_adjudication_batches.id = sqlc.arg(batch_id)
  AND status = 'pending'
  AND entry_count = 0
  AND NOT EXISTS (
      SELECT 1
      FROM rss_entry_adjudications AS adjudication
      WHERE adjudication.batch_id = rss_adjudication_batches.id
  );

-- name: ExpirePendingRSSAdjudicationBatches :many
WITH expired AS (
    UPDATE rss_adjudication_batches
    SET status = 'expired',
        updated_at = now()
    WHERE rss_adjudication_batches.subscription_id = sqlc.arg(target_subscription_id)
      AND status = 'pending'
    RETURNING id
), removed AS (
    DELETE FROM rss_entry_adjudications AS adjudication
    USING expired
    WHERE adjudication.batch_id = expired.id
      AND adjudication.state = 'pending'
    RETURNING adjudication.entry_id
)
SELECT entry_id
FROM removed;

-- name: ListUnresolvedRSSAdjudicationBatches :many
SELECT batch.id
FROM rss_adjudication_batches AS batch
JOIN rss_subscriptions AS subscription ON subscription.id = batch.subscription_id
WHERE batch.subscription_id = sqlc.arg(subscription_id)
  AND batch.status = 'pending'
  AND subscription.enabled
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM agent_resolutions AS resolution
      WHERE resolution.capability = 'rss_release_adjudication'
        AND resolution.resource_type = 'rss_adjudication_batch'
        AND resolution.resource_id = batch.id
        AND resolution.status NOT IN ('cancelled', 'expired', 'failed', 'review_required')
  )
ORDER BY batch.created_at, batch.id
LIMIT 100;

-- name: ListAutomaticRSSAdjudicationBatches :many
SELECT batch.id
FROM rss_adjudication_batches AS batch
JOIN rss_subscriptions AS subscription ON subscription.id = batch.subscription_id
WHERE batch.status = 'pending'
  AND subscription.enabled
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM agent_resolutions AS resolution
      WHERE resolution.capability = 'rss_release_adjudication'
        AND resolution.resource_type = 'rss_adjudication_batch'
        AND resolution.resource_id = batch.id
        AND resolution.status NOT IN ('cancelled', 'expired', 'failed', 'review_required')
  )
ORDER BY batch.created_at, batch.id
LIMIT 100;

-- name: InsertRSSEntry :one
INSERT INTO rss_entries (
    id,
    subscription_id,
    identity_key,
    guid,
    btih,
    canonical_url,
    download_uri,
    title,
    published_at,
    downloadable,
    rejection_reasons,
    source_season,
    source_episode,
    upstream_payload
) VALUES (
    sqlc.arg(id),
    sqlc.arg(subscription_id),
    sqlc.arg(identity_key),
    sqlc.narg(guid),
    sqlc.narg(btih),
    sqlc.narg(canonical_url),
    sqlc.narg(download_uri),
    sqlc.arg(title),
    sqlc.narg(published_at),
    sqlc.arg(downloadable),
    sqlc.arg(rejection_reasons),
    sqlc.narg(source_season),
    sqlc.narg(source_episode),
    sqlc.arg(upstream_payload)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: CreatePendingRSSAdjudication :one
INSERT INTO rss_entry_adjudications (entry_id, subscription_id, batch_id)
VALUES (sqlc.arg(entry_id), sqlc.arg(subscription_id), sqlc.arg(batch_id))
RETURNING *;

-- name: GetRSSEntryBySignals :one
SELECT *
FROM rss_entries
WHERE subscription_id = sqlc.arg(subscription_id)
  AND (
      identity_key = sqlc.arg(identity_key)
      OR (
          sqlc.narg(btih)::text IS NOT NULL
          AND lower(btih) = lower(sqlc.narg(btih)::text)
      )
  )
ORDER BY discovered_at, id
LIMIT 1;

-- name: UpdateRSSEntryMetadata :one
UPDATE rss_entries AS entry
SET guid = COALESCE(sqlc.narg(guid), entry.guid),
    btih = COALESCE(sqlc.narg(btih), entry.btih),
    canonical_url = COALESCE(sqlc.narg(canonical_url), entry.canonical_url),
    download_uri = COALESCE(sqlc.narg(download_uri), entry.download_uri),
    title = sqlc.arg(title),
    published_at = COALESCE(sqlc.narg(published_at), entry.published_at),
    downloadable = CASE
        WHEN EXISTS (
            SELECT 1 FROM rss_entry_adjudications AS adjudication
            WHERE adjudication.entry_id = entry.id AND adjudication.state IN ('selected', 'ignored')
        ) THEN entry.downloadable
        ELSE sqlc.arg(downloadable)
    END,
    rejection_reasons = CASE
        WHEN EXISTS (
            SELECT 1 FROM rss_entry_adjudications AS adjudication
            WHERE adjudication.entry_id = entry.id AND adjudication.state IN ('selected', 'ignored')
        ) THEN entry.rejection_reasons
        ELSE sqlc.arg(rejection_reasons)
    END,
    source_season = CASE
        WHEN EXISTS (
            SELECT 1 FROM rss_entry_adjudications AS adjudication
            WHERE adjudication.entry_id = entry.id AND adjudication.state IN ('selected', 'ignored')
        ) THEN entry.source_season
        ELSE sqlc.narg(source_season)
    END,
    source_episode = CASE
        WHEN EXISTS (
            SELECT 1 FROM rss_entry_adjudications AS adjudication
            WHERE adjudication.entry_id = entry.id AND adjudication.state IN ('selected', 'ignored')
        ) THEN entry.source_episode
        ELSE sqlc.narg(source_episode)
    END,
    upstream_payload = sqlc.arg(upstream_payload),
    duplicate_count = entry.duplicate_count + 1,
    updated_at = now()
WHERE entry.id = sqlc.arg(id)
RETURNING entry.*;

-- name: RecordRSSPoll :one
UPDATE rss_subscriptions
SET last_polled_at = now(),
    next_poll_at = CASE
        WHEN enabled THEN now() + make_interval(secs => poll_interval_seconds)
        ELSE NULL
    END,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND completed_at IS NULL
RETURNING *;

-- name: RecordRSSPollFailure :one
UPDATE rss_subscriptions
SET next_poll_at = CASE
        WHEN enabled THEN now() + make_interval(secs => poll_interval_seconds)
        ELSE NULL
    END,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND completed_at IS NULL
RETURNING *;

-- name: ListEligibleRSSEntries :many
SELECT entry.id, entry.status, entry.downloadable
FROM rss_entries AS entry
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND subscription.enabled
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND entry.downloadable
  AND NOT EXISTS (
      SELECT 1
      FROM rss_entry_adjudications AS adjudication
      WHERE adjudication.entry_id = entry.id
        AND adjudication.state IN ('pending', 'ignored')
  )
  AND (
      entry.status = 'discovered'
      OR (entry.status = 'enqueue_failed' AND entry.last_error_retryable)
  )
ORDER BY entry.discovered_at, entry.id;

-- name: LockRSSEntryForEnqueue :one
SELECT
    entry.*,
    subscription.series_id,
    subscription.mapping_profile_id
FROM rss_entries AS entry
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE entry.id = sqlc.arg(id)
  AND subscription.enabled = NOT sqlc.arg(recovery)::boolean
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
FOR UPDATE OF entry;

-- name: MarkRSSEntryEnqueueing :one
UPDATE rss_entries
SET status = 'enqueueing',
    enqueue_attempts = enqueue_attempts + 1,
    last_error_code = NULL,
    last_error_message = NULL,
    last_error_retryable = false,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND downloadable
  AND status IN ('discovered', 'enqueue_failed')
RETURNING *;

-- name: UpsertRSSAcquisition :one
INSERT INTO acquisitions (
    id,
    series_id,
    mapping_profile_id,
    source_kind,
    rss_entry_id,
    source_payload
) VALUES (
    sqlc.arg(id),
    sqlc.arg(series_id),
    sqlc.narg(mapping_profile_id),
    'rss',
    sqlc.arg(rss_entry_id),
    sqlc.arg(source_payload)
)
ON CONFLICT (rss_entry_id) WHERE rss_entry_id IS NOT NULL DO UPDATE
SET mapping_profile_id = EXCLUDED.mapping_profile_id,
    source_payload = EXCLUDED.source_payload,
    updated_at = now()
RETURNING *;

-- name: CreateRSSDownloadAttempt :one
INSERT INTO downloads (
    id,
    acquisition_id,
    attempt
)
SELECT
    sqlc.arg(id),
    sqlc.arg(acquisition_id),
    COALESCE(max(attempt), 0) + 1
FROM downloads
WHERE acquisition_id = sqlc.arg(acquisition_id)
RETURNING *;

-- name: MarkRSSEntryScheduleFailed :one
UPDATE rss_entries
SET status = 'enqueue_failed',
    enqueue_attempts = enqueue_attempts + CASE WHEN status = 'enqueueing' THEN 0 ELSE 1 END,
    last_error_code = sqlc.arg(error_code),
    last_error_message = sqlc.arg(error_message),
    last_error_retryable = true,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('discovered', 'enqueueing', 'enqueue_failed')
RETURNING *;

-- name: MarkDownloadEnqueueTerminalFailure :one
UPDATE downloads
SET status = 'failed',
    failure_stage = 'enqueue',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'enqueue_pending'
RETURNING *;

-- name: MarkDownloadRSSEntryEnqueueFailed :execrows
UPDATE rss_entries AS entry
SET status = 'enqueue_failed',
    last_error_code = sqlc.arg(error_code),
    last_error_message = sqlc.arg(error_message),
    last_error_retryable = sqlc.arg(error_retryable),
    updated_at = now()
FROM acquisitions AS acquisition
JOIN downloads AS download ON download.acquisition_id = acquisition.id
WHERE download.id = sqlc.arg(download_id)
  AND entry.id = acquisition.rss_entry_id
  AND entry.status = 'enqueueing';

-- name: ListSubscriptionCascadeAcquisitions :many
SELECT
    acquisition.id AS acquisition_id,
    download.id AS download_id,
    download.status AS download_status,
    download.torrent_hash,
    download.save_path,
    download.version AS download_version,
    COALESCE(task_counts.active_tasks, 0) AS active_tasks,
    COALESCE(task_counts.imported_tasks, 0) AS imported_tasks
FROM acquisitions AS acquisition
JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
LEFT JOIN LATERAL (
    SELECT candidate.id, candidate.status, candidate.torrent_hash, candidate.save_path, candidate.version
    FROM downloads AS candidate
    WHERE candidate.acquisition_id = acquisition.id
      AND candidate.deleted_at IS NULL
    ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
    LIMIT 1
) AS download ON true
LEFT JOIN LATERAL (
    SELECT
        count(*) FILTER (WHERE task.state NOT IN ('imported', 'rejected', 'cancelled')) AS active_tasks,
        count(*) FILTER (WHERE task.state = 'imported') AS imported_tasks
    FROM episode_tasks AS task
    WHERE task.acquisition_id = acquisition.id
) AS task_counts ON true
WHERE entry.subscription_id = sqlc.arg(subscription_id)
ORDER BY acquisition.id;

-- name: ListRSSPreacquisitionMappingSourceCandidates :many
SELECT entry.id, entry.source_season, entry.source_episode
FROM rss_entries AS entry
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND subscription.enabled
  AND subscription.auto_episode_mapping
  AND subscription.mapping_profile_id IS NULL
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND entry.status IN ('discovered', 'enqueue_failed')
  AND entry.downloadable
  AND entry.source_season IS NOT NULL
  AND entry.source_episode IS NOT NULL
  AND entry.source_season > 0
  AND entry.source_episode > 0
  AND NOT EXISTS (
      SELECT 1
      FROM rss_entry_adjudications AS adjudication
      WHERE adjudication.entry_id = entry.id
        AND adjudication.state IN ('pending', 'ignored')
  )
ORDER BY entry.source_season, entry.source_episode, entry.discovered_at, entry.id
LIMIT 100;

-- name: ExpireRSSPreacquisitionMappingScopes :execrows
UPDATE rss_preacquisition_mapping_scopes
SET status = 'expired',
    updated_at = now()
WHERE subscription_id = sqlc.arg(subscription_id)
  AND status = 'pending'
  AND (
      subscription_version <> sqlc.arg(subscription_version)
      OR source_fingerprint <> sqlc.arg(source_fingerprint)
  );

-- name: CreateRSSPreacquisitionMappingScope :one
INSERT INTO rss_preacquisition_mapping_scopes (
    id, subscription_id, subscription_version, source_fingerprint
) VALUES (
    sqlc.arg(id), sqlc.arg(subscription_id), sqlc.arg(subscription_version), sqlc.arg(source_fingerprint)
)
ON CONFLICT (subscription_id, subscription_version, source_fingerprint) DO UPDATE
SET id = rss_preacquisition_mapping_scopes.id
RETURNING *;

-- name: CreateRSSPreacquisitionMappingSource :exec
INSERT INTO rss_preacquisition_mapping_sources (
    scope_id, entry_id, source_season, source_episode
) VALUES (
    sqlc.arg(scope_id), sqlc.arg(entry_id), sqlc.arg(source_season), sqlc.arg(source_episode)
)
ON CONFLICT (scope_id, entry_id) DO NOTHING;

-- name: GetRSSPreacquisitionMappingScope :one
SELECT *
FROM rss_preacquisition_mapping_scopes
WHERE id = sqlc.arg(id);

-- name: ListRSSPreacquisitionMappingSources :many
SELECT source.*
FROM rss_preacquisition_mapping_sources AS source
WHERE source.scope_id = sqlc.arg(scope_id)
ORDER BY source.source_season, source.source_episode, source.entry_id;

-- name: LockRSSPreacquisitionMappingContext :one
SELECT
    scope.id AS scope_id,
    scope.subscription_version,
    scope.source_fingerprint,
    scope.status AS scope_status,
    subscription.id AS subscription_id,
    subscription.series_id,
    subscription.source_season,
    subscription.mapping_profile_id,
    subscription.version AS current_subscription_version,
    subscription.enabled,
    subscription.auto_episode_mapping,
    subscription.deleted_at,
    subscription.completed_at
FROM rss_preacquisition_mapping_scopes AS scope
JOIN rss_subscriptions AS subscription ON subscription.id = scope.subscription_id
WHERE scope.id = sqlc.arg(id)
FOR UPDATE OF scope, subscription;

-- name: ApplyRSSPreacquisitionMappingProfile :one
UPDATE rss_subscriptions AS subscription
SET mapping_profile_id = sqlc.arg(mapping_profile_id),
    version = version + 1,
    updated_at = now()
FROM rss_preacquisition_mapping_scopes AS scope
WHERE scope.id = sqlc.arg(scope_id)
  AND scope.subscription_id = subscription.id
  AND scope.status = 'pending'
  AND scope.subscription_version = subscription.version
  AND subscription.version = sqlc.arg(expected_version)
  AND subscription.mapping_profile_id IS NULL
  AND subscription.enabled
  AND subscription.auto_episode_mapping
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
RETURNING subscription.version;

-- name: CompleteRSSPreacquisitionMappingScope :one
UPDATE rss_preacquisition_mapping_scopes
SET status = 'applied',
    applied_profile_id = sqlc.arg(applied_profile_id),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'pending'
RETURNING *;

-- name: ExpireOtherRSSPreacquisitionMappingScopes :execrows
UPDATE rss_preacquisition_mapping_scopes
SET status = 'expired',
    updated_at = now()
WHERE subscription_id = sqlc.arg(subscription_id)
  AND id <> sqlc.arg(applied_scope_id)
  AND status = 'pending';

-- name: IsRSSPreacquisitionMappingEnabled :one
SELECT subscription.auto_episode_mapping
       AND subscription.enabled
       AND subscription.mapping_profile_id IS NULL
       AND subscription.deleted_at IS NULL
       AND subscription.completed_at IS NULL
       AND scope.status = 'pending'
       AND scope.subscription_version = subscription.version AS enabled
FROM rss_preacquisition_mapping_scopes AS scope
JOIN rss_subscriptions AS subscription ON subscription.id = scope.subscription_id
WHERE scope.id = sqlc.arg(scope_id);

-- name: IsCurrentRSSPreacquisitionMappingScope :one
SELECT scope.status = 'pending'
       AND subscription.enabled
       AND subscription.auto_episode_mapping
       AND subscription.mapping_profile_id IS NULL
       AND subscription.deleted_at IS NULL
       AND subscription.completed_at IS NULL
       AND scope.subscription_version = subscription.version AS current
FROM rss_preacquisition_mapping_scopes AS scope
JOIN rss_subscriptions AS subscription ON subscription.id = scope.subscription_id
WHERE scope.id = sqlc.arg(scope_id);

-- name: ExpireInactiveRSSPreacquisitionMappingScopes :execrows
UPDATE rss_preacquisition_mapping_scopes AS scope
SET status = 'expired',
    updated_at = now()
FROM rss_subscriptions AS subscription
WHERE scope.subscription_id = subscription.id
  AND scope.status = 'pending'
  AND (
      subscription.mapping_profile_id IS NOT NULL
      OR NOT subscription.enabled
      OR NOT subscription.auto_episode_mapping
      OR subscription.deleted_at IS NOT NULL
      OR subscription.completed_at IS NOT NULL
      OR scope.subscription_version <> subscription.version
  );

-- name: ListAutomaticRSSPreacquisitionMappingScopes :many
SELECT scope.id
FROM rss_preacquisition_mapping_scopes AS scope
JOIN rss_subscriptions AS subscription ON subscription.id = scope.subscription_id
WHERE scope.status = 'pending'
  AND subscription.enabled
  AND subscription.auto_episode_mapping
  AND subscription.mapping_profile_id IS NULL
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND scope.subscription_version = subscription.version
  AND NOT EXISTS (
      SELECT 1
      FROM agent_resolutions AS resolution
      WHERE resolution.capability = 'rss_preacquisition_mapping'
        AND resolution.resource_type = 'rss_preacquisition_mapping_scope'
        AND resolution.resource_id = scope.id
        AND resolution.status NOT IN ('cancelled', 'expired', 'failed', 'review_required')
  )
ORDER BY scope.created_at, scope.id
LIMIT 100;

-- name: ListAutomaticRSSPreacquisitionMappingScopesBySeries :many
SELECT scope.id
FROM rss_preacquisition_mapping_scopes AS scope
JOIN rss_subscriptions AS subscription ON subscription.id = scope.subscription_id
WHERE subscription.series_id = sqlc.arg(series_id)
  AND scope.status = 'pending'
  AND subscription.enabled
  AND subscription.auto_episode_mapping
  AND subscription.mapping_profile_id IS NULL
  AND subscription.deleted_at IS NULL
  AND subscription.completed_at IS NULL
  AND scope.subscription_version = subscription.version
  AND NOT EXISTS (
      SELECT 1
      FROM agent_resolutions AS resolution
      WHERE resolution.capability = 'rss_preacquisition_mapping'
        AND resolution.resource_type = 'rss_preacquisition_mapping_scope'
        AND resolution.resource_id = scope.id
        AND resolution.status NOT IN ('cancelled', 'expired', 'failed', 'review_required')
  )
ORDER BY scope.created_at, scope.id
LIMIT 100;
