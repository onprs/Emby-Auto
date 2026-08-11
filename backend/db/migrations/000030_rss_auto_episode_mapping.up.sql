ALTER TABLE rss_subscriptions
    ADD COLUMN auto_episode_mapping boolean NOT NULL DEFAULT false;
