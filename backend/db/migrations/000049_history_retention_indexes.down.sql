DROP VIEW IF EXISTS agent_resolution_retention_candidates;
DROP VIEW IF EXISTS operation_retention_candidates;

DROP INDEX IF EXISTS agent_resolutions_completed_at_idx;
DROP INDEX IF EXISTS operations_finished_at_idx;
DROP INDEX IF EXISTS imports_completed_at_idx;
DROP INDEX IF EXISTS episode_tasks_updated_at_idx;
DROP INDEX IF EXISTS operations_updated_at_idx;
DROP INDEX IF EXISTS operations_created_at_idx;
