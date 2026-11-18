ALTER TABLE media_episodes
    DROP COLUMN IF EXISTS present;

ALTER TABLE tmdb_seasons
    DROP COLUMN IF EXISTS present;
