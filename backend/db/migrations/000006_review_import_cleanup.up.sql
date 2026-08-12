ALTER TABLE reviews
    ADD COLUMN idempotency_key text,
    ADD COLUMN expected_task_version integer;

UPDATE reviews
SET idempotency_key = 'legacy-review:' || id::text,
    expected_task_version = 1
WHERE idempotency_key IS NULL
   OR expected_task_version IS NULL;

ALTER TABLE reviews
    ALTER COLUMN idempotency_key SET NOT NULL,
    ALTER COLUMN expected_task_version SET NOT NULL,
    ADD CONSTRAINT reviews_idempotency_key_not_blank CHECK (btrim(idempotency_key) <> ''),
    ADD CONSTRAINT reviews_expected_version_positive CHECK (expected_task_version > 0),
    ADD CONSTRAINT reviews_idempotency_key_unique UNIQUE (idempotency_key);

ALTER TABLE imports
    ADD CONSTRAINT imports_destination_paths_paired CHECK (
        (destination_video_path IS NULL) = (destination_subtitle_path IS NULL)
    );

CREATE UNIQUE INDEX imports_one_active_per_task
    ON imports (task_id)
    WHERE status IN ('queued', 'running');

CREATE UNIQUE INDEX cleanup_runs_one_active_per_task
    ON cleanup_runs (task_id)
    WHERE status IN ('queued', 'running');
