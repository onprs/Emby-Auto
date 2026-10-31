CREATE VIEW download_list_items AS
SELECT download.id, download.status, download.attempt,
    download.progress::double precision AS progress,
    COALESCE(download.client_state, '') AS client_state,
    download.failure_stage, download.updated_at,
    COALESCE(download.save_path, '') AS save_path,
    COALESCE(download.torrent_hash, '') AS torrent_hash,
    series.title AS media_title, COALESCE(series.original_title, '') AS original_title
FROM downloads AS download
LEFT JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
LEFT JOIN media_series AS series ON series.id = acquisition.series_id
WHERE download.deleted_at IS NULL;

CREATE VIEW acquisition_list_items AS
SELECT projection.acquisition_id AS id, projection.*, series.tmdb_series_id
FROM acquisition_list_projections AS projection
JOIN acquisitions AS acquisition ON acquisition.id = projection.acquisition_id
JOIN media_series AS series ON series.id = acquisition.series_id
WHERE NOT projection.dirty AND projection.list_visible;

CREATE VIEW rss_entry_list_items AS
SELECT projection.entry_id AS id, projection.*, entry.status
FROM rss_entry_list_projections AS projection
JOIN rss_entries AS entry ON entry.id = projection.entry_id
WHERE NOT projection.dirty;

CREATE INDEX download_list_updated_idx ON downloads (updated_at, id) WHERE deleted_at IS NULL;
CREATE INDEX download_list_status_updated_idx ON downloads (status, updated_at, id) WHERE deleted_at IS NULL;
CREATE INDEX download_list_attempt_idx ON downloads (attempt, id) WHERE deleted_at IS NULL;
CREATE INDEX download_list_progress_idx ON downloads ((progress::double precision), id) WHERE deleted_at IS NULL;
CREATE INDEX download_list_client_idx ON downloads ((COALESCE(client_state, '') COLLATE "C"), id) WHERE deleted_at IS NULL;
CREATE INDEX download_list_status_idx ON downloads ((status COLLATE "C"), id) WHERE deleted_at IS NULL;

-- 返回 NULL 表示游标不属于当前筛选集合；空数组表示有效的空页。
-- 排序表达式仅取自固定列表，筛选值通过 USING 绑定；EXECUTE 按每次调用
-- 的实际筛选与排序生成计划，避免通用 CASE 排序失去对应索引。
CREATE FUNCTION list_download_page_ids(
    filter_status text, filter_phase text, filter_query text,
    sort_field text, descending_order boolean, cursor_id uuid, page_size integer
)
RETURNS uuid[] LANGUAGE plpgsql STABLE AS $function$
DECLARE
    keys text[];
    direction text := CASE WHEN descending_order THEN 'DESC' ELSE 'ASC' END;
    comparison text := CASE WHEN descending_order THEN '<' ELSE '>' END;
    key_sql text;
    order_sql text;
    result uuid[];
BEGIN
    keys := CASE sort_field
        WHEN 'status' THEN ARRAY['status COLLATE "C"', 'id']
        WHEN 'attempt' THEN ARRAY['attempt', 'id']
        WHEN 'progress' THEN ARRAY['progress', 'id']
        WHEN 'client_state' THEN ARRAY['client_state COLLATE "C"', 'id']
        ELSE ARRAY['updated_at', 'id']
    END;
    key_sql := array_to_string(keys, ', ');
    order_sql := array_to_string(keys, ' ' || direction || ', ') || ' ' || direction;
    EXECUTE format($query$
        WITH filtered AS NOT MATERIALIZED (
            SELECT * FROM download_list_items
            WHERE ($1::text IS NULL OR status = $1)
              AND (
      $2::text IS NULL
      OR ($2::text = 'active' AND status IN ('enqueue_pending', 'file_resolution_pending', 'downloading'))
      OR (
          $2::text = 'waiting'
          AND (
              status IN ('enqueue_pending', 'file_resolution_pending')
              OR (status = 'downloading' AND client_state IN ('metaDL', 'queuedDL', 'stalledDL'))
          )
      )
      OR (
          $2::text = 'downloading'
          AND status = 'downloading'
          AND (
              client_state IS NULL
              OR client_state NOT IN ('metaDL', 'queuedDL', 'stalledDL', 'pausedDL', 'stoppedDL')
          )
      )
      OR (
          $2::text = 'paused'
          AND status = 'downloading'
          AND client_state IN ('pausedDL', 'stoppedDL')
      )
      OR (
          $2::text = 'completed'
          AND (
              status IN ('completed', 'selecting_files', 'materialized')
              OR (status = 'failed' AND failure_stage = 'materialize' AND progress = 1)
          )
      )
      OR (
          $2::text = 'failed'
          AND status = 'failed'
          AND failure_stage IS DISTINCT FROM 'materialize'
      )
              )
              AND ($3::text IS NULL
                   OR id::text ILIKE '%%' || $3 || '%%'
                   OR save_path ILIKE '%%' || $3 || '%%'
                   OR torrent_hash ILIKE '%%' || $3 || '%%'
                   OR media_title ILIKE '%%' || $3 || '%%'
                   OR original_title ILIKE '%%' || $3 || '%%')
        ), anchor AS (
            SELECT * FROM filtered WHERE id = $4::uuid
        ), page AS (
            SELECT * FROM filtered
            WHERE $4::uuid IS NULL OR (%1$s) %2$s (SELECT %1$s FROM anchor)
            ORDER BY %3$s LIMIT $5
        )
        SELECT CASE WHEN $4::uuid IS NOT NULL AND NOT EXISTS (SELECT 1 FROM anchor)
            THEN NULL::uuid[] ELSE ARRAY(SELECT id FROM page ORDER BY %3$s)::uuid[] END
    $query$, key_sql, comparison, order_sql)
    INTO result USING filter_status, filter_phase, filter_query, cursor_id, page_size;
    RETURN result;
END
$function$;

CREATE FUNCTION list_acquisition_page_ids(
    filter_source text, filter_tmdb bigint, filter_phase text,
    sort_field text, descending_order boolean, cursor_id uuid, page_size integer
)
RETURNS uuid[] LANGUAGE plpgsql STABLE AS $function$
DECLARE
    keys text[];
    direction text := CASE WHEN descending_order THEN 'DESC' ELSE 'ASC' END;
    comparison text := CASE WHEN descending_order THEN '<' ELSE '>' END;
    key_sql text;
    order_sql text;
    result uuid[];
BEGIN
    keys := CASE sort_field
        WHEN 'content' THEN ARRAY[
            'content_title COLLATE "C"', 'media_type COLLATE "C"', 'release_year',
            'source_season', 'source_episode', 'id'
        ]
        WHEN 'source_kind' THEN ARRAY['source_kind COLLATE "C"', 'id']
        WHEN 'progress' THEN ARRAY['overall_progress', 'id']
        ELSE ARRAY['resource_updated_at', 'id']
    END;
    key_sql := array_to_string(keys, ', ');
    order_sql := array_to_string(keys, ' ' || direction || ', ') || ' ' || direction;
    EXECUTE format($query$
        WITH filtered AS NOT MATERIALIZED (
            SELECT * FROM acquisition_list_items
            WHERE ($1::text IS NULL OR source_kind = $1)
              AND ($2::bigint IS NULL OR tmdb_series_id = $2)
              AND ($3::text IS NULL OR phases @> ARRAY[$3])
        ), anchor AS (
            SELECT * FROM filtered WHERE id = $4::uuid
        ), page AS (
            SELECT * FROM filtered
            WHERE $4::uuid IS NULL OR (%1$s) %2$s (SELECT %1$s FROM anchor)
            ORDER BY %3$s LIMIT $5
        )
        SELECT CASE WHEN $4::uuid IS NOT NULL AND NOT EXISTS (SELECT 1 FROM anchor)
            THEN NULL::uuid[] ELSE ARRAY(SELECT id FROM page ORDER BY %3$s)::uuid[] END
    $query$, key_sql, comparison, order_sql)
    INTO result USING filter_source, filter_tmdb, filter_phase, cursor_id, page_size;
    RETURN result;
END
$function$;

CREATE FUNCTION list_rss_entry_page_ids(
    filter_subscription uuid, filter_status text, filter_group text,
    filter_query text, filter_reject_reason text,
    sort_field text, descending_order boolean, cursor_id uuid, page_size integer
)
RETURNS uuid[] LANGUAGE plpgsql STABLE AS $function$
DECLARE
    keys text[];
    direction text := CASE WHEN descending_order THEN 'DESC' ELSE 'ASC' END;
    comparison text := CASE WHEN descending_order THEN '<' ELSE '>' END;
    key_sql text;
    order_sql text;
    result uuid[];
BEGIN
    keys := CASE sort_field
        WHEN 'title' THEN ARRAY['title_key COLLATE "C"', 'id']
        WHEN 'episode' THEN ARRAY['source_season', 'source_episode', 'id']
        WHEN 'progress' THEN ARRAY['overall_progress', 'id']
        ELSE ARRAY['discovered_at', 'id']
    END;
    key_sql := array_to_string(keys, ', ');
    order_sql := array_to_string(keys, ' ' || direction || ', ') || ' ' || direction;
    EXECUTE format($query$
        WITH filtered AS NOT MATERIALIZED (
            SELECT * FROM rss_entry_list_items
            WHERE subscription_id = $1
              AND ($2::text IS NULL OR status = $2)
              AND ($3::text IS NULL OR entry_group = $3)
              AND ($4::text IS NULL OR strpos(title_key, $4) > 0)
              AND ($5::text IS NULL OR reject_reasons @> ARRAY[$5])
        ), anchor AS (
            SELECT * FROM filtered WHERE id = $6::uuid
        ), page AS (
            SELECT * FROM filtered
            WHERE $6::uuid IS NULL OR (%1$s) %2$s (SELECT %1$s FROM anchor)
            ORDER BY %3$s LIMIT $7
        )
        SELECT CASE WHEN $6::uuid IS NOT NULL AND NOT EXISTS (SELECT 1 FROM anchor)
            THEN NULL::uuid[] ELSE ARRAY(SELECT id FROM page ORDER BY %3$s)::uuid[] END
    $query$, key_sql, comparison, order_sql)
    INTO result USING filter_subscription, filter_status, filter_group,
        filter_query, filter_reject_reason, cursor_id, page_size;
    RETURN result;
END
$function$;
