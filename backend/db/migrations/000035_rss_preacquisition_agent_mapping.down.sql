DROP INDEX rss_preacquisition_mapping_sources_coordinate_idx;
DROP TABLE rss_preacquisition_mapping_sources;

DROP INDEX rss_preacquisition_mapping_scopes_pending_idx;
DROP TABLE rss_preacquisition_mapping_scopes;

UPDATE agent_resolutions
SET capability = 'episode_mapping'
WHERE capability = 'rss_preacquisition_mapping';

ALTER TABLE agent_resolutions
    DROP CONSTRAINT agent_resolutions_capability_valid,
    ADD CONSTRAINT agent_resolutions_capability_valid CHECK (
        capability IN (
            'rss_coordinate',
            'rss_release_adjudication',
            'download_file_resolution',
            'catalog_candidate',
            'episode_mapping'
        )
    );
