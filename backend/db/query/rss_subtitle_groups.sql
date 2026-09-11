-- name: GetRSSSubtitleGroup :one
SELECT *
FROM rss_subtitle_groups
WHERE id = sqlc.arg(id);

-- name: ListRSSSubtitleGroups :many
SELECT *
FROM rss_subtitle_groups
ORDER BY normalized_name, id;

-- name: ListRSSSubtitleGroupNames :many
SELECT name
FROM rss_subtitle_groups
ORDER BY normalized_name, id;

-- name: CreateRSSSubtitleGroup :one
INSERT INTO rss_subtitle_groups (
    id,
    name,
    normalized_name
) VALUES (
    sqlc.arg(id),
    sqlc.arg(name),
    sqlc.arg(normalized_name)
)
RETURNING *;

-- name: UpdateRSSSubtitleGroup :one
UPDATE rss_subtitle_groups
SET name = sqlc.arg(name),
    normalized_name = sqlc.arg(normalized_name),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteRSSSubtitleGroup :execrows
DELETE FROM rss_subtitle_groups
WHERE id = sqlc.arg(id);
