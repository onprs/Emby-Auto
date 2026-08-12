DROP INDEX rss_entry_adjudications_batch_idx;
DROP TABLE rss_entry_adjudications;

ALTER TABLE rss_entries
    DROP CONSTRAINT rss_entries_adjudication_owner_unique;

DROP INDEX rss_adjudication_batches_pending_idx;
DROP INDEX rss_adjudication_batches_subscription_idx;
DROP TABLE rss_adjudication_batches;

DELETE FROM agent_resolution_steps
WHERE resolution_id IN (
    SELECT id
    FROM agent_resolutions
    WHERE capability = 'rss_release_adjudication'
);

DELETE FROM agent_resolutions
WHERE capability = 'rss_release_adjudication';

ALTER TABLE agent_resolutions
    DROP CONSTRAINT agent_resolutions_capability_valid,
    ADD CONSTRAINT agent_resolutions_capability_valid CHECK (
        capability IN ('rss_coordinate', 'download_file_resolution', 'catalog_candidate', 'episode_mapping')
    );
