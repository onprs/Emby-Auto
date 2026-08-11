ALTER TABLE rss_subscriptions
    ADD COLUMN include_keywords text[] NOT NULL DEFAULT ARRAY[]::text[],
    ADD COLUMN exclude_keywords text[] NOT NULL DEFAULT ARRAY[]::text[],
    ADD CONSTRAINT rss_subscriptions_include_keywords_count_valid
        CHECK (cardinality(include_keywords) <= 20),
    ADD CONSTRAINT rss_subscriptions_exclude_keywords_count_valid
        CHECK (cardinality(exclude_keywords) <= 20),
    ADD CONSTRAINT rss_subscriptions_include_keywords_no_nulls
        CHECK (array_position(include_keywords, NULL) IS NULL),
    ADD CONSTRAINT rss_subscriptions_exclude_keywords_no_nulls
        CHECK (array_position(exclude_keywords, NULL) IS NULL);
