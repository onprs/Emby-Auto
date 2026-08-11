ALTER TABLE media_series
    DROP CONSTRAINT media_series_type_valid,
    ADD COLUMN tmdb_movie_id bigint,
    ADD COLUMN release_year integer,
    ADD CONSTRAINT media_series_type_valid CHECK (media_type IN ('tv', 'movie')),
    ADD CONSTRAINT media_series_tmdb_movie_id_positive CHECK (tmdb_movie_id IS NULL OR tmdb_movie_id > 0),
    ADD CONSTRAINT media_series_release_year_valid CHECK (release_year IS NULL OR release_year BETWEEN 1870 AND 9999),
    ADD CONSTRAINT media_series_movie_metadata_valid CHECK (
        (media_type = 'tv' AND tmdb_movie_id IS NULL AND release_year IS NULL)
        OR
        (media_type = 'movie' AND tmdb_series_id IS NULL AND tmdb_movie_id IS NOT NULL AND release_year IS NOT NULL)
    );

CREATE UNIQUE INDEX media_series_tmdb_movie_id_unique
    ON media_series (tmdb_movie_id)
    WHERE tmdb_movie_id IS NOT NULL;

ALTER TABLE episode_tasks
    ADD COLUMN media_type text NOT NULL DEFAULT 'episode',
    ADD CONSTRAINT episode_tasks_media_type_valid CHECK (media_type IN ('episode', 'movie'));
