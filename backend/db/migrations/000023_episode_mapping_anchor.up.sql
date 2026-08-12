ALTER TABLE episode_mapping_profiles
    ADD COLUMN anchor_source_season integer,
    ADD COLUMN anchor_source_episode integer,
    ADD COLUMN anchor_target_episode_id uuid REFERENCES media_episodes (id) ON DELETE RESTRICT,
    ADD COLUMN target_episode_offset integer,
    ADD CONSTRAINT episode_mapping_profiles_anchor_valid CHECK (
        (
            anchor_source_season IS NULL
            AND anchor_source_episode IS NULL
            AND anchor_target_episode_id IS NULL
            AND target_episode_offset IS NULL
        )
        OR (
            anchor_source_season > 0
            AND anchor_source_episode > 0
            AND anchor_target_episode_id IS NOT NULL
            AND target_episode_offset IS NOT NULL
        )
    );

ALTER TABLE episode_mappings
    DROP CONSTRAINT episode_mappings_match_source_valid,
    ADD CONSTRAINT episode_mappings_match_source_valid CHECK (
        match_source IN ('anchor', 'explicit', 'absolute', 'pending')
    );
