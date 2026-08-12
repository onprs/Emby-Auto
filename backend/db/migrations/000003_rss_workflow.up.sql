ALTER TABLE rss_subscriptions
    ADD COLUMN source_season integer NOT NULL DEFAULT 1,
    ADD COLUMN deleted_at timestamptz,
    ADD CONSTRAINT rss_subscriptions_source_season_positive CHECK (source_season > 0);

ALTER TABLE rss_entries
    ADD COLUMN download_uri text,
    ADD COLUMN downloadable boolean NOT NULL DEFAULT false,
    ADD COLUMN rejection_reasons text[] NOT NULL DEFAULT '{}'::text[],
    ADD COLUMN source_season integer,
    ADD COLUMN source_episode integer,
    ADD CONSTRAINT rss_entries_download_uri_not_blank CHECK (
        download_uri IS NULL OR btrim(download_uri) <> ''
    ),
    ADD CONSTRAINT rss_entries_rejection_reasons_valid CHECK (
        array_position(rejection_reasons, NULL) IS NULL
    ),
    ADD CONSTRAINT rss_entries_source_coordinates_valid CHECK (
        (source_season IS NULL AND source_episode IS NULL)
        OR (
            source_season IS NOT NULL
            AND source_episode IS NOT NULL
            AND source_season > 0
            AND source_episode > 0
        )
    ),
    ADD CONSTRAINT rss_entries_downloadable_fields_required CHECK (
        NOT downloadable
        OR (
            download_uri IS NOT NULL
            AND source_season IS NOT NULL
            AND source_episode IS NOT NULL
            AND cardinality(rejection_reasons) = 0
        )
    );

CREATE INDEX rss_entries_eligible_idx
    ON rss_entries (subscription_id, status, discovered_at, id)
    WHERE downloadable AND status IN ('discovered', 'enqueue_failed');

CREATE INDEX rss_subscriptions_poll_due_idx
    ON rss_subscriptions (next_poll_at, id)
    WHERE enabled AND deleted_at IS NULL;
