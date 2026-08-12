-- name: CheckDatabase :one
SELECT now()::timestamptz AS checked_at;
