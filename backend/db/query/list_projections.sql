-- name: GetListProjectionReadiness :one
SELECT
    CASE sqlc.arg(kind)::text
        WHEN 'acquisition' THEN (EXISTS (SELECT 1 FROM acquisition_list_projections AS p WHERE p.dirty) OR EXISTS (SELECT 1 FROM acquisition_list_projections AS p WHERE p.model_version < sqlc.arg(target_model_version)))
        ELSE (EXISTS (SELECT 1 FROM rss_entry_list_projections AS p WHERE p.dirty) OR EXISTS (SELECT 1 FROM rss_entry_list_projections AS p WHERE p.model_version < sqlc.arg(target_model_version)))
    END::boolean AS needs_refresh,
    CASE sqlc.arg(kind)::text
        WHEN 'acquisition' THEN EXISTS (SELECT 1 FROM acquisition_list_projections AS p WHERE p.model_version > sqlc.arg(target_model_version))
        ELSE EXISTS (SELECT 1 FROM rss_entry_list_projections AS p WHERE p.model_version > sqlc.arg(target_model_version))
    END::boolean AS has_newer_model;

-- name: MarkOutdatedListProjectionsDirty :one
WITH acquisitions_updated AS (
    UPDATE acquisition_list_projections AS projection
    SET dirty = true, source_revision = projection.source_revision + 1,
        dirtied_transaction_id = txid_current(), dirtied_at = clock_timestamp()
    WHERE sqlc.arg(kind)::text = 'acquisition' AND NOT projection.dirty AND projection.model_version < sqlc.arg(target_model_version)
    RETURNING acquisition_id
), entries_updated AS (
    UPDATE rss_entry_list_projections AS projection
    SET dirty = true, source_revision = projection.source_revision + 1,
        dirtied_transaction_id = txid_current(), dirtied_at = clock_timestamp()
    WHERE sqlc.arg(kind)::text = 'rss_entry' AND NOT projection.dirty AND projection.model_version < sqlc.arg(target_model_version)
    RETURNING entry_id
)
SELECT ((SELECT count(*) FROM acquisitions_updated) + (SELECT count(*) FROM entries_updated))::bigint AS updated_count;

-- name: LockAcquisitionListProjectionBatch :many
SELECT * FROM acquisition_list_projections
WHERE dirty
  AND (NOT sqlc.arg(transaction_only)::boolean OR dirtied_transaction_id = txid_current())
ORDER BY acquisition_id
LIMIT sqlc.arg(batch_size)
FOR UPDATE;

-- name: ListDirtyRSSEntryListProjectionIDs :many
SELECT entry_id FROM rss_entry_list_projections
WHERE dirty
  AND (NOT sqlc.arg(transaction_only)::boolean OR dirtied_transaction_id = txid_current())
ORDER BY entry_id
LIMIT sqlc.arg(batch_size);

-- name: LockRSSEntryParentListProjections :many
SELECT projection.*
FROM acquisition_list_projections AS projection
JOIN acquisitions AS acquisition ON acquisition.id = projection.acquisition_id
WHERE acquisition.rss_entry_id = ANY(sqlc.arg(entry_ids)::uuid[])
ORDER BY projection.acquisition_id
FOR UPDATE OF projection;

-- name: LockRSSEntryListProjectionsByIDs :many
SELECT * FROM rss_entry_list_projections
WHERE entry_id = ANY(sqlc.arg(entry_ids)::uuid[]) AND dirty
ORDER BY entry_id
FOR UPDATE;

-- name: ListAcquisitionProjectionSources :many
SELECT sqlc.embed(acquisition),
    (acquisition.deletion_requested_at IS NULL
  AND (
      NOT EXISTS (SELECT 1 FROM downloads WHERE downloads.acquisition_id = acquisition.id)
      OR EXISTS (
          SELECT 1 FROM downloads
          WHERE downloads.acquisition_id = acquisition.id
            AND downloads.deleted_at IS NULL
            AND downloads.status <> 'cancelled'
      )
      OR EXISTS (
          SELECT 1
          FROM episode_tasks AS task
          JOIN download_files AS source_file ON source_file.id = task.source_video_file_id
          JOIN downloads AS source_download ON source_download.id = source_file.download_id
          WHERE task.acquisition_id = acquisition.id
            AND source_download.deleted_at IS NULL
      )
  ))::boolean AS list_visible,
    (acquisition.series_id IN (SELECT id FROM media_series WHERE media_type = 'tv')
          AND EXISTS (
              SELECT 1
              FROM downloads AS download
              WHERE download.id = (
                    SELECT candidate.id
                    FROM downloads AS candidate
                    WHERE candidate.acquisition_id = acquisition.id
                      AND candidate.deleted_at IS NULL
                    ORDER BY (candidate.status = 'cancelled'), candidate.attempt DESC
                    LIMIT 1
                )
                AND (
                    download.status IN ('completed', 'selecting_files', 'materialized')
                    OR (
                        download.status = 'failed'
                        AND download.failure_stage = 'materialize'
                        AND download.error_code IN (
                            'mapping_profile_required',
                            'episode_mapping_required',
                            'mapping_source_invalid',
                            'mapping_source_out_of_range',
                            'mapping_context_incomplete',
                            'mapping_target_out_of_range',
                            'mapping_title_missing'
                        )
                    )
                )
                AND (
                    NOT EXISTS (
                        SELECT 1 FROM download_files AS file
                        WHERE file.download_id = download.id AND file.selected AND file.media_kind = 'video'
                    )
                    OR EXISTS (
                        SELECT 1
                        FROM download_files AS file
                        WHERE file.download_id = download.id
                          AND file.selected
                          AND file.media_kind = 'video'
                          AND NOT EXISTS (
                              SELECT 1
                              FROM episode_mappings AS mapping
                              WHERE mapping.profile_id = acquisition.mapping_profile_id
                                AND mapping.source_season = file.source_season
                                AND mapping.source_episode = file.source_episode
                                AND mapping.source_episode_fraction_hundredths = file.source_episode_fraction_hundredths
                                AND mapping.mapping_status = 'mapped'
                                AND mapping.target_episode_id IS NOT NULL
                          )
                    )
                )
          ))::boolean AS mapping_filter_match
FROM acquisitions AS acquisition
WHERE acquisition.id = ANY(sqlc.arg(acquisition_ids)::uuid[])
ORDER BY acquisition.id;

-- name: ListRSSEntryParentListProjections :many
SELECT entry.id AS entry_id, acquisition.id AS acquisition_id,
       COALESCE(projection.overall_progress, 0)::double precision AS overall_progress,
       COALESCE(projection.model_version, 0)::integer AS model_version,
       COALESCE(projection.dirty, true)::boolean AS dirty
FROM rss_entries AS entry
LEFT JOIN acquisitions AS acquisition ON acquisition.rss_entry_id = entry.id
LEFT JOIN acquisition_list_projections AS projection ON projection.acquisition_id = acquisition.id
WHERE entry.id = ANY(sqlc.arg(entry_ids)::uuid[])
ORDER BY entry.id;

-- name: UpdateAcquisitionListProjections :execrows
UPDATE acquisition_list_projections AS projection
SET list_visible = calculated.list_visible,
    phases = calculated.phases,
    overall_progress = calculated.overall_progress,
    content_title = calculated.content_title,
    media_type = calculated.media_type,
    release_year = calculated.release_year,
    source_season = calculated.source_season,
    source_episode = calculated.source_episode,
    source_kind = calculated.source_kind,
    resource_updated_at = calculated.resource_updated_at,
    model_version = calculated.model_version,
    dirty = false
FROM jsonb_to_recordset(sqlc.arg(calculations)::jsonb) AS calculated(
    acquisition_id uuid, source_revision bigint, model_version integer,
    list_visible boolean, phases text[],
    overall_progress double precision,
    content_title text, media_type text, release_year integer,
    source_season bigint, source_episode bigint, source_kind text,
    resource_updated_at timestamptz
)
WHERE projection.acquisition_id = calculated.acquisition_id
  AND projection.source_revision = calculated.source_revision;

-- name: UpdateRSSEntryListProjections :execrows
UPDATE rss_entry_list_projections AS projection
SET subscription_id = calculated.subscription_id,
    title_key = calculated.title_key,
    entry_group = calculated.entry_group,
    reject_reasons = calculated.reject_reasons,
    overall_progress = calculated.overall_progress,
    source_season = calculated.source_season,
    source_episode = calculated.source_episode,
    discovered_at = calculated.discovered_at,
    model_version = calculated.model_version,
    dirty = false
FROM jsonb_to_recordset(sqlc.arg(calculations)::jsonb) AS calculated(
    entry_id uuid, subscription_id uuid, source_revision bigint, model_version integer,
    title_key text, entry_group text, reject_reasons text[],
    overall_progress double precision, source_season bigint, source_episode bigint,
    discovered_at timestamptz
)
WHERE projection.entry_id = calculated.entry_id
  AND projection.source_revision = calculated.source_revision;

-- name: CountAcquisitionListAttention :one
SELECT count(*)::bigint FROM acquisition_list_projections
WHERE NOT dirty AND list_visible AND phases @> ARRAY['attention'];

-- name: ListDownloadPageIDs :one
SELECT list_download_page_ids(
    sqlc.narg(status)::text, sqlc.narg(phase)::text, sqlc.narg(query)::text,
    sqlc.arg(sort_by)::text, sqlc.arg(descending_order)::boolean,
    sqlc.narg(cursor)::uuid, sqlc.arg(row_limit)::integer
)::uuid[] AS ids;

-- name: ListAcquisitionPageIDs :one
SELECT list_acquisition_page_ids(
    sqlc.narg(source_kind)::text, sqlc.narg(tmdb_series_id)::bigint, sqlc.narg(phase)::text,
    sqlc.arg(sort_by)::text, sqlc.arg(descending_order)::boolean,
    sqlc.narg(cursor)::uuid, sqlc.arg(row_limit)::integer
)::uuid[] AS ids;

-- name: ListRSSEntryPageIDs :one
SELECT list_rss_entry_page_ids(
    sqlc.arg(subscription_id)::uuid, sqlc.narg(status)::text, sqlc.narg(entry_group)::text,
    sqlc.narg(query)::text, sqlc.narg(reject_reason)::text,
    sqlc.arg(sort_by)::text, sqlc.arg(descending_order)::boolean,
    sqlc.narg(cursor)::uuid, sqlc.arg(row_limit)::integer
)::uuid[] AS ids;

-- name: ListDownloadsByIDs :many
SELECT * FROM downloads WHERE id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY id;

-- name: ListDownloadFilesByDownloadIDs :many
SELECT * FROM download_files
WHERE download_id = ANY(sqlc.arg(download_ids)::uuid[])
ORDER BY download_id, file_index, id;

-- name: ListAcquisitionsByIDs :many
SELECT * FROM acquisitions WHERE id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY id;

-- name: ListAcquisitionsByRSSEntryIDs :many
SELECT * FROM acquisitions WHERE rss_entry_id = ANY(sqlc.arg(entry_ids)::uuid[]) ORDER BY id;

-- name: ListMappingProfileAuditsByIDs :many
SELECT id, decision_source, agent_resolution_id
FROM episode_mapping_profiles WHERE id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY id;

-- name: ListRSSEntryReadModelsByIDs :many
SELECT
    entry.*,
    adjudication.batch_id AS adjudication_batch_id,
    COALESCE(adjudication.state, 'not_required')::text AS adjudication_state,
    adjudication.source AS adjudication_source,
    adjudication.resolution_id AS adjudication_resolution_id,
    adjudication.related_entry_id,
    EXISTS (
        SELECT 1
        FROM rss_acquisition_provenance AS provenance
        WHERE provenance.rss_entry_id = entry.id
          AND provenance.task_id IS NOT NULL
          AND provenance.imported_at IS NOT NULL
    ) AS successful_import_present
FROM rss_entries AS entry
LEFT JOIN rss_entry_adjudications AS adjudication ON adjudication.entry_id = entry.id
WHERE entry.id = ANY(sqlc.arg(entry_ids)::uuid[])
ORDER BY entry.id;

-- name: ListArchivedRSSEntryAcquisitionsByEntryIDs :many
SELECT DISTINCT ON (provenance.rss_entry_id)
    provenance.rss_entry_id AS entry_id,
    provenance.acquisition_id,
    provenance.imported_at
FROM rss_acquisition_provenance AS provenance
JOIN rss_entries AS entry ON entry.id = provenance.rss_entry_id
WHERE entry.id = ANY(sqlc.arg(entry_ids)::uuid[])
  AND entry.imported_at IS NOT NULL
  AND provenance.task_id IS NOT NULL
  AND provenance.imported_at IS NOT NULL
ORDER BY provenance.rss_entry_id, provenance.imported_at DESC, provenance.acquisition_created_at DESC, provenance.acquisition_id;

-- name: ListPendingRSSEntryRetriesByEntryIDs :many
SELECT DISTINCT entry.id
FROM rss_entries AS entry
JOIN operations AS operation
  ON operation.resource_type = 'rss_subscription'
 AND operation.resource_id = entry.subscription_id
 AND operation.kind = 'rss.poll' AND operation.payload->>'command' = 'retry-entry'
 AND operation.payload->>'entryId' = entry.id::text
 AND operation.status IN ('queued', 'running')
WHERE entry.id = ANY(sqlc.arg(entry_ids)::uuid[]);
