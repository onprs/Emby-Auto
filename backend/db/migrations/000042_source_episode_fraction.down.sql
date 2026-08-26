DROP TRIGGER rss_subscription_progress_download_file_fraction_changes ON download_files;
DROP TRIGGER rss_subscription_progress_entry_fraction_changes ON rss_entries;
DROP FUNCTION mark_rss_subscription_progress_from_source_fraction_change();

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM download_files WHERE source_episode_fraction_hundredths <> 0
    ) OR EXISTS (
        SELECT 1 FROM episode_mappings WHERE source_episode_fraction_hundredths <> 0
    ) OR EXISTS (
        SELECT 1 FROM rss_entries WHERE source_episode_fraction_hundredths <> 0
    ) THEN
        RAISE EXCEPTION 'cannot remove source episode fractions while fractional coordinates exist';
    END IF;
END
$$;

DROP INDEX download_files_selected_media_idx;
CREATE INDEX download_files_selected_media_idx
    ON download_files (download_id, media_kind, source_season, source_episode, file_index)
    WHERE selected;

DROP INDEX rss_entries_subscription_imported_idx;
CREATE INDEX rss_entries_subscription_imported_idx
    ON rss_entries (subscription_id, source_season, source_episode)
    WHERE imported_at IS NOT NULL;

ALTER TABLE rss_entries
    DROP CONSTRAINT rss_entries_source_episode_fraction_consistent,
    DROP CONSTRAINT rss_entries_source_episode_fraction_valid,
    DROP COLUMN source_episode_fraction_hundredths;

ALTER TABLE episode_mappings
    DROP CONSTRAINT episode_mappings_source_coordinate_unique,
    DROP CONSTRAINT episode_mappings_source_episode_fraction_valid,
    DROP COLUMN source_episode_fraction_hundredths,
    ADD CONSTRAINT episode_mappings_profile_id_source_season_source_episode_key UNIQUE (
        profile_id,
        source_season,
        source_episode
    );

ALTER TABLE download_files
    DROP CONSTRAINT download_files_source_episode_fraction_consistent,
    DROP CONSTRAINT download_files_source_episode_fraction_valid,
    DROP COLUMN source_episode_fraction_hundredths;
