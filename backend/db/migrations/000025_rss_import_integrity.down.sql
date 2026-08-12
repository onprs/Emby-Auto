DROP INDEX rss_entries_subscription_imported_idx;

ALTER TABLE rss_entries
    DROP COLUMN imported_at;
