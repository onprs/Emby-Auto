-- name: GetAppSetting :one
SELECT *
FROM app_settings
WHERE name = sqlc.arg(name);

-- name: ListAppSecrets :many
SELECT *
FROM app_secrets
ORDER BY name;

-- name: GetAppSecret :one
SELECT *
FROM app_secrets
WHERE name = sqlc.arg(name);

-- name: SaveAppSetting :one
WITH updated AS (
    UPDATE app_settings AS current_setting
    SET value = sqlc.arg(value),
        version = current_setting.version + 1,
        updated_by = sqlc.narg(updated_by),
        updated_at = now()
    WHERE current_setting.name = sqlc.arg(name)
      AND current_setting.version = sqlc.arg(expected_version)
      AND sqlc.arg(expected_version)::integer > 0
    RETURNING current_setting.*
), inserted AS (
    INSERT INTO app_settings (
        name,
        value,
        version,
        updated_by
    )
    SELECT
        sqlc.arg(name),
        sqlc.arg(value),
        1,
        sqlc.narg(updated_by)
    WHERE sqlc.arg(expected_version)::integer = 0
      AND NOT EXISTS (
          SELECT 1
          FROM app_settings AS current_setting
          WHERE current_setting.name = sqlc.arg(name)
      )
    ON CONFLICT (name) DO NOTHING
    RETURNING *
)
SELECT * FROM updated
UNION ALL
SELECT * FROM inserted
LIMIT 1;

-- name: UpsertAppSecret :one
INSERT INTO app_secrets (
    name,
    ciphertext,
    nonce,
    masked_hint,
    updated_by
) VALUES (
    sqlc.arg(name),
    sqlc.arg(ciphertext),
    sqlc.arg(nonce),
    sqlc.arg(masked_hint),
    sqlc.narg(updated_by)
)
ON CONFLICT (name) DO UPDATE
SET ciphertext = EXCLUDED.ciphertext,
    nonce = EXCLUDED.nonce,
    masked_hint = EXCLUDED.masked_hint,
    version = app_secrets.version + 1,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: DeleteAppSecret :execrows
DELETE FROM app_secrets
WHERE name = sqlc.arg(name);

-- name: UpsertConnectivityTestResult :one
INSERT INTO connectivity_test_results (
    target,
    success,
    code,
    message,
    tested_at
) VALUES (
    sqlc.arg(target),
    sqlc.arg(success),
    sqlc.arg(code),
    sqlc.arg(message),
    sqlc.arg(tested_at)
)
ON CONFLICT (target) DO UPDATE
SET success = EXCLUDED.success,
    code = EXCLUDED.code,
    message = EXCLUDED.message,
    tested_at = EXCLUDED.tested_at
RETURNING *;

-- name: ListConnectivityTestResults :many
SELECT *
FROM connectivity_test_results
ORDER BY target;

-- name: ReplaceDefaultTranscodeProfile :one
WITH deactivated AS (
    UPDATE transcode_profiles
    SET active = false,
        is_default = false
    WHERE active
    RETURNING id
), next_version AS (
    SELECT COALESCE(max(version), 0) + 1 AS version
    FROM transcode_profiles
    WHERE name = sqlc.arg(name)
)
INSERT INTO transcode_profiles (
    id,
    name,
    version,
    active,
    is_default,
    video_codec,
    encoder,
    container,
    file_extension,
    quality_mode,
    quality_value,
    audio_policy,
    audio_codec,
    preset,
    pixel_format,
    thread_count,
    max_concurrency,
    created_by
)
SELECT
    sqlc.arg(id),
    sqlc.arg(name),
    next_version.version,
    true,
    true,
    sqlc.arg(video_codec),
    sqlc.arg(encoder),
    sqlc.arg(container),
    sqlc.arg(file_extension),
    sqlc.arg(quality_mode),
    sqlc.arg(quality_value_milli)::bigint::numeric / 1000,
    sqlc.arg(audio_policy),
    sqlc.narg(audio_codec),
    sqlc.arg(preset),
    sqlc.arg(pixel_format),
    sqlc.arg(thread_count),
    sqlc.arg(max_concurrency),
    sqlc.narg(created_by)
FROM next_version
CROSS JOIN (SELECT count(*) FROM deactivated) AS applied
RETURNING *;
