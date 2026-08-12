CREATE TABLE rss_feed_catalog_lookups (
    id uuid PRIMARY KEY,
    feed_title text NOT NULL DEFAULT '',
    suggested_queries text[] NOT NULL DEFAULT ARRAY[]::text[],
    sample_titles text[] NOT NULL DEFAULT ARRAY[]::text[],
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    CONSTRAINT rss_feed_catalog_lookups_queries_bounded CHECK (
        cardinality(suggested_queries) BETWEEN 1 AND 8
    ),
    CONSTRAINT rss_feed_catalog_lookups_samples_bounded CHECK (
        cardinality(sample_titles) <= 5
    ),
    CONSTRAINT rss_feed_catalog_lookups_expiry_valid CHECK (expires_at > created_at)
);

CREATE INDEX rss_feed_catalog_lookups_expiry_idx
    ON rss_feed_catalog_lookups (expires_at, id);
