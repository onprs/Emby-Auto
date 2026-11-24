-- name: UpsertOperationSubtitleBinding :one
INSERT INTO operation_subtitle_bindings (
    operation_id,
    binding_version,
    phase,
    attempt,
    attempt_id,
    operation_attempt_count,
    binding
) VALUES (
    sqlc.arg(operation_id),
    sqlc.arg(binding_version),
    sqlc.arg(phase),
    sqlc.arg(attempt),
    sqlc.arg(attempt_id),
    sqlc.arg(operation_attempt_count),
    sqlc.arg(binding)
)
ON CONFLICT (operation_id) DO UPDATE
SET binding_version = EXCLUDED.binding_version,
    phase = EXCLUDED.phase,
    attempt = EXCLUDED.attempt,
    attempt_id = EXCLUDED.attempt_id,
    operation_attempt_count = EXCLUDED.operation_attempt_count,
    binding = EXCLUDED.binding,
    updated_at = now()
RETURNING *;

-- name: AdvanceOperationSubtitleBinding :one
UPDATE operation_subtitle_bindings
SET phase = sqlc.arg(next_phase),
    binding = sqlc.arg(binding),
    updated_at = now()
WHERE operation_id = sqlc.arg(operation_id)
  AND binding_version = sqlc.arg(binding_version)
  AND phase = sqlc.arg(expected_phase)
  AND attempt = sqlc.arg(attempt)
  AND attempt_id = sqlc.arg(attempt_id)
  AND operation_attempt_count = sqlc.arg(operation_attempt_count)
RETURNING *;

-- name: GetOperationSubtitleBinding :one
SELECT *
FROM operation_subtitle_bindings
WHERE operation_id = sqlc.arg(operation_id);

-- name: FinishOperationAttemptByID :one
UPDATE operation_attempts
SET status = sqlc.arg(status),
    error_code = sqlc.narg(error_code),
    error_message = sqlc.narg(error_message),
    heartbeat_at = now(),
    finished_at = now()
WHERE id = sqlc.arg(id)
  AND operation_id = sqlc.arg(operation_id)
  AND attempt = sqlc.arg(attempt)
  AND status = 'running'
RETURNING *;

-- name: HeartbeatOperationAttemptByID :execrows
UPDATE operation_attempts
SET heartbeat_at = now()
WHERE id = sqlc.arg(id)
  AND operation_id = sqlc.arg(operation_id)
  AND attempt = sqlc.arg(attempt)
  AND status = 'running';

-- name: DeleteOperationAttemptByID :execrows
DELETE FROM operation_attempts
WHERE id = sqlc.arg(id)
  AND operation_id = sqlc.arg(operation_id)
  AND attempt = sqlc.arg(attempt)
  AND status = 'running';

-- name: CountOtherActiveSubtitlePrepareOperations :one
SELECT count(*)::bigint
FROM operations
WHERE resource_type = 'episode_task'
  AND resource_id = sqlc.arg(resource_id)
  AND kind = 'subtitle.prepare'
  AND status IN ('queued', 'running')
  AND id <> sqlc.arg(operation_id);

-- name: ListOtherActiveSubtitlePrepareOperations :many
SELECT
    operation.id,
    operation.status,
    operation.attempt_count,
    operation.max_attempts,
    operation.cancel_requested_at,
    binding.binding_version,
    binding.phase,
    binding.attempt,
    binding.attempt_id,
    binding.operation_attempt_count,
    binding.binding
FROM operations AS operation
LEFT JOIN operation_subtitle_bindings AS binding ON binding.operation_id = operation.id
WHERE operation.resource_type = 'episode_task'
  AND operation.resource_id = sqlc.arg(resource_id)
  AND operation.kind = 'subtitle.prepare'
  AND operation.status IN ('queued', 'running')
  AND operation.id <> sqlc.arg(operation_id)
ORDER BY operation.created_at, operation.id;
