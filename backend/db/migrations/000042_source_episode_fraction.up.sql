ALTER TABLE download_files
    ADD COLUMN source_episode_fraction_hundredths integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT download_files_source_episode_fraction_valid CHECK (
        source_episode_fraction_hundredths BETWEEN 0 AND 99
    ),
    ADD CONSTRAINT download_files_source_episode_fraction_consistent CHECK (
        source_episode IS NOT NULL OR source_episode_fraction_hundredths = 0
    );

ALTER TABLE episode_mappings
    ADD COLUMN source_episode_fraction_hundredths integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT episode_mappings_source_episode_fraction_valid CHECK (
        source_episode_fraction_hundredths BETWEEN 0 AND 99
    ),
    DROP CONSTRAINT episode_mappings_profile_id_source_season_source_episode_key,
    ADD CONSTRAINT episode_mappings_source_coordinate_unique UNIQUE (
        profile_id,
        source_season,
        source_episode,
        source_episode_fraction_hundredths
    );

ALTER TABLE rss_entries
    ADD COLUMN source_episode_fraction_hundredths integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT rss_entries_source_episode_fraction_valid CHECK (
        source_episode_fraction_hundredths BETWEEN 0 AND 99
    ),
    ADD CONSTRAINT rss_entries_source_episode_fraction_consistent CHECK (
        source_episode IS NOT NULL OR source_episode_fraction_hundredths = 0
    );

DROP INDEX download_files_selected_media_idx;
CREATE INDEX download_files_selected_media_idx
    ON download_files (
        download_id,
        media_kind,
        source_season,
        source_episode,
        source_episode_fraction_hundredths,
        file_index
    )
    WHERE selected;

DROP INDEX rss_entries_subscription_imported_idx;
CREATE INDEX rss_entries_subscription_imported_idx
    ON rss_entries (
        subscription_id,
        source_season,
        source_episode,
        source_episode_fraction_hundredths
    )
    WHERE imported_at IS NOT NULL;

CREATE FUNCTION mark_rss_subscription_progress_from_source_fraction_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_TABLE_NAME = 'rss_entries' THEN
        PERFORM mark_rss_subscription_progress_dirty_many(ARRAY[OLD.subscription_id, NEW.subscription_id]);
    ELSIF TG_TABLE_NAME = 'download_files' THEN
        PERFORM mark_rss_subscription_progress_for_downloads(ARRAY[OLD.download_id, NEW.download_id]);
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER rss_subscription_progress_entry_fraction_changes
    AFTER UPDATE OF source_episode_fraction_hundredths ON rss_entries
    FOR EACH ROW
    WHEN (
        NEW.source_episode_fraction_hundredths IS DISTINCT FROM OLD.source_episode_fraction_hundredths
        AND NEW.subscription_id IS NOT DISTINCT FROM OLD.subscription_id
        AND NEW.source_season IS NOT DISTINCT FROM OLD.source_season
        AND NEW.source_episode IS NOT DISTINCT FROM OLD.source_episode
        AND NEW.imported_at IS NOT DISTINCT FROM OLD.imported_at
    )
    EXECUTE FUNCTION mark_rss_subscription_progress_from_source_fraction_change();

CREATE TRIGGER rss_subscription_progress_download_file_fraction_changes
    AFTER UPDATE OF source_episode_fraction_hundredths ON download_files
    FOR EACH ROW
    WHEN (
        NEW.source_episode_fraction_hundredths IS DISTINCT FROM OLD.source_episode_fraction_hundredths
        AND NEW.download_id IS NOT DISTINCT FROM OLD.download_id
        AND NEW.selected IS NOT DISTINCT FROM OLD.selected
        AND NEW.media_kind IS NOT DISTINCT FROM OLD.media_kind
        AND NEW.source_season IS NOT DISTINCT FROM OLD.source_season
        AND NEW.source_episode IS NOT DISTINCT FROM OLD.source_episode
    )
    EXECUTE FUNCTION mark_rss_subscription_progress_from_source_fraction_change();
