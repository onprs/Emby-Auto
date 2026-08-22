-- name: CreateSearchRun :one
INSERT INTO search_runs (
    id,
    query,
    requested_by
) VALUES (
    sqlc.arg(id),
    sqlc.arg(query),
    sqlc.narg(requested_by)
)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: GetSearchRun :one
SELECT *
FROM search_runs
WHERE id = sqlc.arg(id);

-- name: LockSearchRun :one
SELECT *
FROM search_runs
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: MarkSearchRunRunning :one
UPDATE search_runs
SET status = 'running',
    started_at = COALESCE(started_at, now()),
    error_code = NULL,
    error_message = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'queued'
RETURNING *;

-- name: UpsertReleaseCandidate :one
INSERT INTO release_candidates (
    id,
    search_run_id,
    provider,
    identity_key,
    title,
    download_uri,
    published_at,
    size_bytes,
    seeders,
    upstream_payload
) VALUES (
    sqlc.arg(id),
    sqlc.arg(search_run_id),
    sqlc.arg(provider),
    sqlc.arg(identity_key),
    sqlc.arg(title),
    sqlc.narg(download_uri),
    sqlc.narg(published_at),
    sqlc.narg(size_bytes),
    sqlc.narg(seeders),
    sqlc.arg(upstream_payload)
)
ON CONFLICT (search_run_id, provider, identity_key) DO UPDATE
SET title = EXCLUDED.title,
    download_uri = EXCLUDED.download_uri,
    published_at = EXCLUDED.published_at,
    size_bytes = EXCLUDED.size_bytes,
    seeders = EXCLUDED.seeders,
    upstream_payload = EXCLUDED.upstream_payload
RETURNING *;

-- name: MarkSearchRunCompleted :one
UPDATE search_runs
SET status = 'completed',
    started_at = COALESCE(started_at, now()),
    completed_at = COALESCE(completed_at, now()),
    error_code = NULL,
    error_message = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: MarkSearchRunTerminalFailure :one
UPDATE search_runs
SET status = 'failed',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    completed_at = COALESCE(completed_at, now()),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: MarkSearchRunCancelled :one
UPDATE search_runs
SET status = 'cancelled',
    completed_at = COALESCE(completed_at, now()),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: ListReleaseCandidates :many
SELECT *
FROM release_candidates
WHERE search_run_id = sqlc.arg(search_run_id)
ORDER BY created_at, id;

-- name: ListRecentReleaseCandidates :many
SELECT rc.*
FROM release_candidates AS rc
JOIN search_runs AS sr ON sr.id = rc.search_run_id
ORDER BY sr.created_at DESC, sr.id DESC, rc.created_at ASC, rc.id ASC
LIMIT sqlc.arg(row_limit);

-- name: GetReleaseCandidate :one
SELECT *
FROM release_candidates
WHERE id = sqlc.arg(id);

-- name: UpsertSearchMediaSeries :one
INSERT INTO media_series (
    id,
    tmdb_series_id,
    title
) VALUES (
    sqlc.arg(id),
    sqlc.arg(tmdb_series_id),
    sqlc.arg(title)
)
ON CONFLICT (tmdb_series_id) DO UPDATE
SET title = EXCLUDED.title,
    updated_at = now()
RETURNING *;

-- name: UpsertSearchMediaMovie :one
INSERT INTO media_series (
    id,
    tmdb_movie_id,
    title,
    media_type,
    release_year
) VALUES (
    sqlc.arg(id),
    sqlc.arg(tmdb_movie_id),
    sqlc.arg(title),
    'movie',
    sqlc.arg(release_year)
)
ON CONFLICT (tmdb_movie_id) WHERE tmdb_movie_id IS NOT NULL DO UPDATE
SET title = EXCLUDED.title,
    release_year = EXCLUDED.release_year,
    updated_at = now()
RETURNING *;

-- name: CreateSearchAcquisition :one
INSERT INTO acquisitions (
    id,
    series_id,
    mapping_profile_id,
    source_kind,
    release_candidate_id,
    source_payload,
    created_by
) VALUES (
    sqlc.arg(id),
    sqlc.arg(series_id),
    sqlc.narg(mapping_profile_id),
    'search',
    sqlc.arg(release_candidate_id),
    sqlc.arg(source_payload),
    sqlc.narg(created_by)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetSearchAcquisitionByCandidate :one
SELECT *
FROM acquisitions
WHERE release_candidate_id = sqlc.arg(release_candidate_id);

-- name: CreateSearchDownload :one
INSERT INTO downloads (
    id,
    acquisition_id,
    attempt
)
SELECT
    sqlc.arg(id),
    sqlc.arg(acquisition_id),
    1
WHERE NOT EXISTS (
    SELECT 1
    FROM downloads
    WHERE acquisition_id = sqlc.arg(acquisition_id)
)
RETURNING *;

-- name: GetSearchAcquisitionDownload :one
SELECT *
FROM downloads
WHERE acquisition_id = sqlc.arg(acquisition_id)
ORDER BY attempt DESC
LIMIT 1;
