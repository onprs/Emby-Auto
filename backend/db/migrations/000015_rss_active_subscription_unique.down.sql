DROP INDEX IF EXISTS rss_subscriptions_active_series_feed_unique;

ALTER TABLE rss_subscriptions
    ADD CONSTRAINT rss_subscriptions_series_id_feed_url_key UNIQUE (series_id, feed_url);
