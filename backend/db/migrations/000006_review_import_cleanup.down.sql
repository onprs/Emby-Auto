DROP INDEX IF EXISTS cleanup_runs_one_active_per_task;
DROP INDEX IF EXISTS imports_one_active_per_task;
ALTER TABLE imports
    DROP CONSTRAINT IF EXISTS imports_destination_paths_paired;
ALTER TABLE reviews
    DROP CONSTRAINT IF EXISTS reviews_idempotency_key_unique,
    DROP CONSTRAINT IF EXISTS reviews_expected_version_positive,
    DROP CONSTRAINT IF EXISTS reviews_idempotency_key_not_blank,
    DROP COLUMN IF EXISTS expected_task_version,
    DROP COLUMN IF EXISTS idempotency_key;
