ALTER TABLE rss_subscriptions
    DROP CONSTRAINT rss_subscriptions_series_id_feed_url_key;

CREATE UNIQUE INDEX rss_subscriptions_active_series_feed_unique
    ON rss_subscriptions (series_id, feed_url)
    WHERE deleted_at IS NULL;
