ALTER TABLE episode_tasks
    DROP CONSTRAINT episode_tasks_media_type_valid,
    DROP COLUMN media_type;

DROP INDEX media_series_tmdb_movie_id_unique;

ALTER TABLE media_series
    DROP CONSTRAINT media_series_movie_metadata_valid,
    DROP CONSTRAINT media_series_release_year_valid,
    DROP CONSTRAINT media_series_tmdb_movie_id_positive,
    DROP CONSTRAINT media_series_type_valid,
    DROP COLUMN release_year,
    DROP COLUMN tmdb_movie_id,
    ADD CONSTRAINT media_series_type_valid CHECK (media_type IN ('tv'));
