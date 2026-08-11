ALTER TABLE rss_subscriptions
    DROP CONSTRAINT rss_subscriptions_completed_disabled,
    DROP COLUMN completed_at;
