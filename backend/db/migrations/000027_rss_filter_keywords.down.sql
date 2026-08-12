ALTER TABLE rss_subscriptions
    DROP CONSTRAINT IF EXISTS rss_subscriptions_exclude_keywords_no_nulls,
    DROP CONSTRAINT IF EXISTS rss_subscriptions_include_keywords_no_nulls,
    DROP CONSTRAINT IF EXISTS rss_subscriptions_exclude_keywords_count_valid,
    DROP CONSTRAINT IF EXISTS rss_subscriptions_include_keywords_count_valid,
    DROP COLUMN IF EXISTS exclude_keywords,
    DROP COLUMN IF EXISTS include_keywords;
