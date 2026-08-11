DROP INDEX IF EXISTS rss_subscriptions_poll_due_idx;
DROP INDEX IF EXISTS rss_entries_eligible_idx;

ALTER TABLE rss_entries
    DROP CONSTRAINT IF EXISTS rss_entries_downloadable_fields_required,
    DROP CONSTRAINT IF EXISTS rss_entries_source_coordinates_valid,
    DROP CONSTRAINT IF EXISTS rss_entries_rejection_reasons_valid,
    DROP CONSTRAINT IF EXISTS rss_entries_download_uri_not_blank,
    DROP COLUMN IF EXISTS source_episode,
    DROP COLUMN IF EXISTS source_season,
    DROP COLUMN IF EXISTS rejection_reasons,
    DROP COLUMN IF EXISTS downloadable,
    DROP COLUMN IF EXISTS download_uri;

ALTER TABLE rss_subscriptions
    DROP CONSTRAINT IF EXISTS rss_subscriptions_source_season_positive,
    DROP COLUMN IF EXISTS deleted_at,
    DROP COLUMN IF EXISTS source_season;
