CREATE TABLE rss_target_realtime_checks (
    target_episode_id uuid NOT NULL REFERENCES media_episodes (id) ON DELETE CASCADE,
    check_id uuid NOT NULL,
    present boolean NOT NULL,
    match_source text NOT NULL,
    checked_at timestamptz NOT NULL,
    PRIMARY KEY (target_episode_id, check_id),
    CONSTRAINT rss_target_realtime_checks_match_source_valid CHECK (
        match_source IN ('tmdb_episode', 'target_coordinate', 'absent')
    )
);

CREATE INDEX rss_target_realtime_checks_checked_at_idx
    ON rss_target_realtime_checks (checked_at);
