ALTER TABLE connectivity_test_results
    DROP CONSTRAINT connectivity_test_results_target_valid;

ALTER TABLE connectivity_test_results
    ADD CONSTRAINT connectivity_test_results_target_valid CHECK (
        target IN ('qbittorrent', 'tmdb', 'emby', 'media_tools', 'network_proxy')
    );
