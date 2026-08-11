-- name: CreateOperation :execrows
INSERT INTO operations (
    id,
    kind,
    resource_type,
    resource_id,
    idempotency_key,
    max_attempts,
    timeout_seconds,
    payload
) VALUES (
    sqlc.arg(id),
    sqlc.arg(kind),
    sqlc.narg(resource_type),
    sqlc.narg(resource_id),
    sqlc.arg(idempotency_key),
    sqlc.arg(max_attempts),
    sqlc.arg(timeout_seconds),
    sqlc.arg(payload)
)
ON CONFLICT (idempotency_key) DO NOTHING;

-- name: GetOperation :one
SELECT *
FROM operations
WHERE id = sqlc.arg(id);

-- name: GetOperationByIdempotencyKey :one
SELECT *
FROM operations
WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: LockOperation :one
SELECT *
FROM operations
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: AttachRiverJob :one
UPDATE operations
SET river_job_id = sqlc.arg(river_job_id),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'queued'
  AND river_job_id IS NULL
RETURNING *;

-- name: AbandonRunningOperationAttempts :execrows
UPDATE operation_attempts
SET status = 'failed',
    error_code = 'worker_interrupted',
    error_message = 'the previous worker stopped before finishing the attempt',
    heartbeat_at = now(),
    finished_at = now()
WHERE operation_id = sqlc.arg(operation_id)
  AND status = 'running';

-- name: StartOperationAttempt :one
UPDATE operations
SET status = 'running',
    attempt_count = attempt_count + 1,
    started_at = COALESCE(started_at, now()),
    heartbeat_at = now(),
    error_code = NULL,
    error_message = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
  AND cancel_requested_at IS NULL
  AND attempt_count < max_attempts
RETURNING *;

-- name: RequestOperationCancellation :one
UPDATE operations
SET cancel_requested_at = COALESCE(cancel_requested_at, now()),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: ListCancellableResourceOperations :many
SELECT *
FROM operations
WHERE resource_type = sqlc.arg(resource_type)
  AND resource_id = sqlc.arg(resource_id)
  AND status IN ('queued', 'running')
ORDER BY created_at, id
FOR UPDATE;

-- name: CountOtherActiveResourceOperations :one
SELECT count(*)::bigint
FROM operations
WHERE resource_type = sqlc.arg(resource_type)
  AND resource_id = sqlc.arg(resource_id)
  AND id <> sqlc.arg(operation_id)
  AND status IN ('queued', 'running');

-- name: HeartbeatOperation :execrows
UPDATE operations
SET heartbeat_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND cancel_requested_at IS NULL;

-- name: CompleteOperation :one
UPDATE operations
SET status = 'succeeded',
    heartbeat_at = now(),
    finished_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND cancel_requested_at IS NULL
RETURNING *;

-- name: RetryOperation :one
UPDATE operations
SET status = 'queued',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    heartbeat_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND cancel_requested_at IS NULL
  AND attempt_count < max_attempts
RETURNING *;

-- name: FailOperation :one
UPDATE operations
SET status = 'failed',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    heartbeat_at = now(),
    finished_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: CancelOperation :one
UPDATE operations
SET status = 'cancelled',
    cancel_requested_at = COALESCE(cancel_requested_at, now()),
    heartbeat_at = CASE WHEN status = 'running' THEN now() ELSE heartbeat_at END,
    finished_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('queued', 'running')
RETURNING *;

-- name: CreateOperationAttempt :one
INSERT INTO operation_attempts (
    id,
    operation_id,
    attempt,
    status,
    worker_id
) VALUES (
    sqlc.arg(id),
    sqlc.arg(operation_id),
    sqlc.arg(attempt),
    'running',
    sqlc.narg(worker_id)
)
RETURNING *;

-- name: HeartbeatOperationAttempt :execrows
UPDATE operation_attempts
SET heartbeat_at = now()
WHERE operation_id = sqlc.arg(operation_id)
  AND attempt = sqlc.arg(attempt)
  AND status = 'running';

-- name: DeleteSnoozedOperationAttempt :execrows
DELETE FROM operation_attempts
WHERE operation_id = sqlc.arg(operation_id)
  AND attempt = sqlc.arg(attempt)
  AND status = 'running';

-- name: SnoozeOperation :one
UPDATE operations
SET status = 'queued',
    attempt_count = attempt_count - 1,
    heartbeat_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND cancel_requested_at IS NULL
  AND attempt_count > 0
RETURNING *;

-- name: FinishOperationAttempt :one
UPDATE operation_attempts
SET status = sqlc.arg(status),
    error_code = sqlc.narg(error_code),
    error_message = sqlc.narg(error_message),
    heartbeat_at = now(),
    finished_at = now()
WHERE operation_id = sqlc.arg(operation_id)
  AND attempt = sqlc.arg(attempt)
  AND status = 'running'
RETURNING *;
