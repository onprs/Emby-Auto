ALTER TABLE rss_entries
    ADD COLUMN duplicate_count integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT rss_entries_duplicate_count_nonnegative CHECK (duplicate_count >= 0);

CREATE TABLE connectivity_test_results (
    target text PRIMARY KEY,
    success boolean NOT NULL,
    code text NOT NULL,
    message text NOT NULL,
    tested_at timestamptz NOT NULL,
    CONSTRAINT connectivity_test_results_target_valid CHECK (
        target IN ('qbittorrent', 'tmdb', 'emby', 'media_tools')
    ),
    CONSTRAINT connectivity_test_results_code_not_blank CHECK (btrim(code) <> '')
);

CREATE INDEX connectivity_test_results_tested_at_idx
    ON connectivity_test_results (tested_at DESC);
