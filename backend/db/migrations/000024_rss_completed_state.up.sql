ALTER TABLE rss_subscriptions
    ADD COLUMN completed_at timestamptz;

WITH completed_subscriptions AS (
    SELECT
        event.resource_id AS subscription_id,
        max(event.occurred_at) AS completed_at
    FROM events AS event
    WHERE event.resource_type = 'rss_subscription'
      AND event.topic IN (
          'rss.subscription.completion_cleanup_completed',
          'rss.subscription.history_restored'
      )
    GROUP BY event.resource_id
)
UPDATE rss_subscriptions AS subscription
SET completed_at = completed.completed_at,
    enabled = false,
    next_poll_at = NULL,
    version = version + 1,
    updated_at = now()
FROM completed_subscriptions AS completed
WHERE subscription.id = completed.subscription_id
  AND subscription.deleted_at IS NULL;

ALTER TABLE rss_subscriptions
    ADD CONSTRAINT rss_subscriptions_completed_disabled CHECK (
        completed_at IS NULL
        OR (NOT enabled AND next_poll_at IS NULL)
    );
