DROP INDEX IF EXISTS emby_library_items_file_path_idx;
DROP INDEX IF EXISTS emby_library_items_tmdb_series_idx;
DROP INDEX IF EXISTS emby_library_items_episode_coordinate_idx;
DROP INDEX IF EXISTS emby_library_items_tmdb_episode_idx;

ALTER TABLE emby_library_items
    DROP COLUMN IF EXISTS tmdb_provider_id;

DROP FUNCTION IF EXISTS emby_tmdb_provider_id(jsonb);
