DROP TRIGGER rss_entry_list_projection_change ON acquisition_list_projections;
DROP TRIGGER list_projection_delete_lock ON acquisitions;
DROP TRIGGER list_projection_delete_lock ON rss_entries;

DO $block$
DECLARE source_table text;
BEGIN
    FOREACH source_table IN ARRAY ARRAY[
        'acquisitions', 'downloads', 'download_files', 'episode_tasks',
        'episode_mapping_profiles', 'episode_mappings', 'media_series',
        'artifact_sets', 'reviews', 'imports', 'cleanup_runs', 'operations',
        'rss_entries', 'rss_entry_adjudications', 'rss_acquisition_provenance'
    ] LOOP
        EXECUTE format('DROP TRIGGER list_projection_change ON %I', source_table);
    END LOOP;
END
$block$;

DROP FUNCTION mark_rss_entry_list_projection_from_change();
DROP FUNCTION mark_acquisition_list_projection_from_change();
DROP FUNCTION mark_rss_entry_list_projections_dirty(uuid[]);
DROP FUNCTION mark_acquisition_list_projections_dirty(uuid[]);
DROP FUNCTION lock_list_projection_subscriptions(uuid[], uuid[], uuid[]);
DROP TABLE rss_entry_list_projections;
DROP TABLE acquisition_list_projections;
