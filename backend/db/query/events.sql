-- name: AppendEvent :one
INSERT INTO events (
    id,
    topic,
    resource_type,
    resource_id,
    operation_id,
    actor_user_id,
    data
) VALUES (
    sqlc.arg(id),
    sqlc.arg(topic),
    sqlc.narg(resource_type),
    sqlc.narg(resource_id),
    sqlc.narg(operation_id),
    sqlc.narg(actor_user_id),
    sqlc.arg(data)
)
RETURNING *;

-- name: GetEvent :one
SELECT *
FROM events
WHERE id = sqlc.arg(id);

-- name: GetEventStats :one
SELECT
    count(*)::bigint AS event_count,
    min(occurred_at)::timestamptz AS earliest_occurred_at
FROM events;

-- name: ListEvents :many
SELECT *
FROM events
ORDER BY event_sequence
LIMIT sqlc.arg(page_size);

-- name: ListEventsAfter :many
SELECT event.*
FROM events AS event
JOIN events AS cursor_event ON cursor_event.id = sqlc.arg(cursor_id)
WHERE event.event_sequence > cursor_event.event_sequence
ORDER BY event.event_sequence
LIMIT sqlc.arg(page_size);

-- name: DeleteExpiredEvents :execrows
-- 结构化 provenance 已独立保留业务事实，事件本身仍按 fail-closed allowlist 清理；
-- 未知、新增 topic 默认保留。
WITH expired AS (
    SELECT events.id
    FROM events
    WHERE events.occurred_at < sqlc.arg(before)
      AND event_is_discardable(events.topic)
    ORDER BY events.occurred_at, events.event_sequence
    LIMIT sqlc.arg(max_rows)
)
DELETE FROM events
USING expired
WHERE events.id = expired.id;
