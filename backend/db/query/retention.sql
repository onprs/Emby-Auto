-- 保留候选规则集中在迁移创建的视图中；这里只负责按截止时间计数与分批删除。

-- name: CountExpiredOperations :one
SELECT count(*)::bigint
FROM operation_retention_candidates AS candidate
WHERE candidate.finished_at < sqlc.arg(before);

-- name: CountExpiredAgentResolutions :one
SELECT count(*)::bigint
FROM agent_resolution_retention_candidates AS candidate
WHERE COALESCE(candidate.completed_at, candidate.updated_at) < sqlc.arg(before);

-- name: DeleteExpiredAgentResolutions :execrows
WITH expired AS (
    SELECT candidate.id
    FROM agent_resolution_retention_candidates AS candidate
    WHERE COALESCE(candidate.completed_at, candidate.updated_at) < sqlc.arg(before)
    ORDER BY candidate.completed_at, candidate.id
    LIMIT sqlc.arg(max_rows)
    FOR UPDATE OF candidate SKIP LOCKED
)
DELETE FROM agent_resolutions
USING expired
WHERE agent_resolutions.id = expired.id;

-- name: DeleteExpiredOperations :execrows
WITH expired AS (
    SELECT candidate.id
    FROM operation_retention_candidates AS candidate
    WHERE candidate.finished_at < sqlc.arg(before)
    ORDER BY candidate.finished_at, candidate.id
    LIMIT sqlc.arg(max_rows)
    FOR UPDATE OF candidate SKIP LOCKED
)
DELETE FROM operations
USING expired
WHERE operations.id = expired.id;
