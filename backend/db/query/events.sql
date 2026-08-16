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
-- 分批删除可安全丢弃的事件：仅清理流式/操作审计类事件，
-- 必须保护被 read model 作为事实来源的 provenance 事件。
-- 保护集合必须与 read_models.sql / rss.sql 中对 events 的引用保持一致：
--   rss.entry.enqueueing, task.created/imported/video_ready/subtitle_ready/
--   awaiting_review/reviewed, acquisition.delete_completed
WITH expired AS (
    SELECT events.id
    FROM events
    WHERE events.occurred_at < sqlc.arg(before)
      AND events.topic NOT IN (
          'rss.entry.enqueueing',
          'task.created',
          'task.imported',
          'task.video_ready',
          'task.subtitle_ready',
          'task.awaiting_review',
          'task.reviewed',
          'acquisition.delete_completed'
      )
    ORDER BY events.event_sequence
    LIMIT sqlc.arg(max_rows)
)
DELETE FROM events
WHERE events.id IN (SELECT expired.id FROM expired);
