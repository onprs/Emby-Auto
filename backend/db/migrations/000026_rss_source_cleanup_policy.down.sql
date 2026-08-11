ALTER TABLE rss_subscriptions
    RENAME COLUMN cleanup_source_on_completion TO delete_imported_on_completion;
