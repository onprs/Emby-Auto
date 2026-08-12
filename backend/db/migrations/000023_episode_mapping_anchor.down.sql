UPDATE episode_mappings
SET match_source = 'absolute'
WHERE match_source = 'anchor';

ALTER TABLE episode_mappings
    DROP CONSTRAINT episode_mappings_match_source_valid,
    ADD CONSTRAINT episode_mappings_match_source_valid CHECK (
        match_source IN ('explicit', 'absolute', 'pending')
    );

ALTER TABLE episode_mapping_profiles
    DROP CONSTRAINT episode_mapping_profiles_anchor_valid,
    DROP COLUMN target_episode_offset,
    DROP COLUMN anchor_target_episode_id,
    DROP COLUMN anchor_source_episode,
    DROP COLUMN anchor_source_season;
