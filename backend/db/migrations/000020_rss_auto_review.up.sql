ALTER TABLE rss_subscriptions
    ADD COLUMN auto_review boolean NOT NULL DEFAULT false;
