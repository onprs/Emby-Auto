ALTER TABLE episode_tasks
    DROP CONSTRAINT episode_tasks_failure_consistent,
    DROP CONSTRAINT episode_tasks_failure_stage_valid,
    DROP COLUMN failure_stage;

ALTER TABLE downloads
    DROP CONSTRAINT downloads_failure_consistent,
    DROP CONSTRAINT downloads_failure_stage_valid,
    DROP COLUMN last_synced_at,
    DROP COLUMN client_state,
    DROP COLUMN failure_stage;
