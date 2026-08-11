ALTER TABLE rss_subscriptions
    ADD COLUMN delete_imported_on_completion boolean NOT NULL DEFAULT false;
