ALTER TABLE episode_mapping_saves
    DROP CONSTRAINT episode_mapping_saves_decision_source_consistent,
    DROP CONSTRAINT episode_mapping_saves_decision_source_valid,
    DROP COLUMN agent_resolution_id,
    DROP COLUMN decision_source,
    ALTER COLUMN created_by SET NOT NULL;

ALTER TABLE episode_mapping_profiles
    DROP CONSTRAINT episode_mapping_profiles_decision_source_consistent,
    DROP CONSTRAINT episode_mapping_profiles_decision_source_valid,
    DROP COLUMN agent_resolution_id,
    DROP COLUMN decision_source;

UPDATE downloads
SET status = 'failed',
    failure_stage = 'enqueue',
    error_code = COALESCE(error_code, 'download_file_resolution_required'),
    error_message = COALESCE(error_message, 'Download file resolution requires the newer application version'),
    version = version + 1,
    updated_at = now()
WHERE status = 'file_resolution_pending'
   OR failure_stage = 'file_resolution';

ALTER TABLE downloads
    DROP CONSTRAINT downloads_failure_stage_valid,
    ADD CONSTRAINT downloads_failure_stage_valid CHECK (
        failure_stage IS NULL OR failure_stage IN ('enqueue', 'sync', 'materialize')
    );

ALTER TABLE downloads
    DROP CONSTRAINT downloads_status_valid,
    ADD CONSTRAINT downloads_status_valid CHECK (status IN (
        'enqueue_pending', 'downloading', 'completed', 'selecting_files', 'materialized', 'failed', 'cancelled'
    ));

ALTER TABLE downloads
    DROP CONSTRAINT downloads_file_resolution_agent_consistent,
    DROP CONSTRAINT downloads_file_resolution_source_valid,
    DROP COLUMN agent_resolution_id,
    DROP COLUMN file_resolution_source;

ALTER TABLE rss_entries
    DROP CONSTRAINT rss_entries_coordinate_agent_consistent,
    DROP CONSTRAINT rss_entries_coordinate_source_valid,
    DROP COLUMN agent_resolution_id,
    DROP COLUMN coordinate_source;

DROP TABLE agent_resolution_steps;
DROP TABLE agent_resolutions;

DELETE FROM connectivity_test_results
WHERE target = 'agent';

ALTER TABLE connectivity_test_results
    DROP CONSTRAINT connectivity_test_results_target_valid;

ALTER TABLE connectivity_test_results
    ADD CONSTRAINT connectivity_test_results_target_valid CHECK (
        target IN ('qbittorrent', 'tmdb', 'emby', 'media_tools', 'network_proxy')
    );
