-- name: GetOperationAttemptByID :one
SELECT *
FROM operation_attempts
WHERE id = sqlc.arg(id)
  AND operation_id = sqlc.arg(operation_id)
  AND attempt = sqlc.arg(attempt);
