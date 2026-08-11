DROP TABLE subtitle_video_match_candidates;

DROP INDEX subtitle_video_match_scopes_pending_idx;
DROP TABLE subtitle_video_match_scopes;

UPDATE agent_resolutions
SET capability = 'episode_mapping'
WHERE capability = 'subtitle_video_match';

ALTER TABLE agent_resolutions
    DROP CONSTRAINT agent_resolutions_capability_valid,
    ADD CONSTRAINT agent_resolutions_capability_valid CHECK (
        capability IN (
            'rss_coordinate',
            'rss_release_adjudication',
            'rss_preacquisition_mapping',
            'download_file_resolution',
            'catalog_candidate',
            'episode_mapping'
        )
    );