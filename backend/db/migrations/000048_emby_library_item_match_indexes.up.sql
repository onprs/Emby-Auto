-- 任务列表、dashboard 与 RSS 目标占用查询按 TMDb 标识匹配媒体库条目。
-- 原先每次匹配都对 provider_ids 执行 jsonb_each_text，无法使用 GIN 索引，
-- 单行扫描成本随媒体库条目数线性增长。归一化为生成列后按索引精确匹配，
-- 任何写入 provider_ids 的路径都会自动得到一致的列值。
CREATE FUNCTION emby_tmdb_provider_id(provider_ids jsonb) RETURNS text
LANGUAGE sql
IMMUTABLE
AS $function$
    SELECT COALESCE((
        SELECT btrim(provider.value)
        FROM jsonb_each_text(provider_ids) AS provider
        WHERE lower(provider.key) IN ('tmdb', 'themoviedb')
          AND btrim(provider.value) <> ''
        ORDER BY lower(provider.key), provider.value
        LIMIT 1
    ), '')
$function$;

ALTER TABLE emby_library_items
    ADD COLUMN tmdb_provider_id text
    GENERATED ALWAYS AS (emby_tmdb_provider_id(provider_ids)) STORED;

CREATE INDEX emby_library_items_tmdb_episode_idx
    ON emby_library_items (tmdb_provider_id)
    WHERE present AND item_type = 'Episode' AND tmdb_provider_id <> '';

CREATE INDEX emby_library_items_episode_coordinate_idx
    ON emby_library_items (season_number, episode_number)
    WHERE present AND item_type = 'Episode';

CREATE INDEX emby_library_items_tmdb_series_idx
    ON emby_library_items (tmdb_provider_id)
    WHERE present AND item_type = 'Series' AND tmdb_provider_id <> '';

CREATE INDEX emby_library_items_file_path_idx
    ON emby_library_items (file_path)
    WHERE present AND file_path IS NOT NULL;
