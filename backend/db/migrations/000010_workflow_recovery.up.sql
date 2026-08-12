ALTER TABLE downloads
    ADD COLUMN failure_stage text,
    ADD COLUMN client_state text,
    ADD COLUMN last_synced_at timestamptz,
    ADD CONSTRAINT downloads_failure_stage_valid CHECK (
        failure_stage IS NULL OR failure_stage IN ('enqueue', 'sync', 'materialize')
    );

ALTER TABLE episode_tasks
    ADD COLUMN failure_stage text,
    ADD CONSTRAINT episode_tasks_failure_stage_valid CHECK (
        failure_stage IS NULL OR failure_stage IN ('video', 'subtitle', 'finalize', 'import')
    );

UPDATE downloads
SET failure_stage = CASE
    WHEN torrent_hash IS NULL THEN 'enqueue'
    WHEN completed_at IS NULL THEN 'sync'
    ELSE 'materialize'
END
WHERE status = 'failed';

UPDATE episode_tasks AS task
SET failure_stage = CASE
    WHEN video_state = 'failed' THEN 'video'
    WHEN subtitle_state = 'failed' THEN 'subtitle'
    WHEN EXISTS (SELECT 1 FROM imports WHERE task_id = task.id AND status = 'failed') THEN 'import'
    ELSE 'finalize'
END
WHERE state = 'failed';

ALTER TABLE downloads
    ADD CONSTRAINT downloads_failure_consistent CHECK (
        (status = 'failed' AND failure_stage IS NOT NULL)
        OR (status <> 'failed' AND failure_stage IS NULL)
    );

ALTER TABLE episode_tasks
    ADD CONSTRAINT episode_tasks_failure_consistent CHECK (
        (state = 'failed' AND failure_stage IS NOT NULL)
        OR (state <> 'failed' AND failure_stage IS NULL)
    );
