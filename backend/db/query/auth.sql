-- name: CountAdminUsers :one
SELECT count(*)
FROM admin_users;

-- name: CreateAdminUser :one
INSERT INTO admin_users (
    id,
    username,
    password_hash
) VALUES (
    sqlc.arg(id),
    sqlc.arg(username),
    sqlc.arg(password_hash)
)
RETURNING *;

-- name: GetAdminUserByUsername :one
SELECT *
FROM admin_users
WHERE lower(username) = lower(sqlc.arg(username));

-- name: CreateSession :one
INSERT INTO sessions (
    id,
    admin_user_id,
    token_hash,
    expires_at
) VALUES (
    sqlc.arg(id),
    sqlc.arg(admin_user_id),
    sqlc.arg(token_hash),
    sqlc.arg(expires_at)
)
RETURNING *;

-- name: GetActiveSessionByTokenHash :one
SELECT
    session.id AS session_id,
    session.expires_at,
    admin_user.id AS admin_user_id,
    admin_user.username
FROM sessions AS session
JOIN admin_users AS admin_user ON admin_user.id = session.admin_user_id
WHERE session.token_hash = sqlc.arg(token_hash)
  AND session.revoked_at IS NULL
  AND session.expires_at > now()
  AND NOT admin_user.disabled;

-- name: TouchSession :execrows
UPDATE sessions
SET last_seen_at = now()
WHERE id = sqlc.arg(id)
  AND revoked_at IS NULL
  AND expires_at > now()
  AND last_seen_at < now() - interval '5 minutes';

-- name: RevokeSessionByTokenHash :execrows
UPDATE sessions
SET revoked_at = COALESCE(revoked_at, now())
WHERE token_hash = sqlc.arg(token_hash)
  AND revoked_at IS NULL;
