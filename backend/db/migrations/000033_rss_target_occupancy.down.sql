UPDATE rss_entry_adjudications
SET source = 'agent_auto'
WHERE source = 'deterministic';

ALTER TABLE rss_entry_adjudications
    DROP CONSTRAINT rss_entry_adjudications_source_valid,
    ADD CONSTRAINT rss_entry_adjudications_source_valid CHECK (
        source IS NULL OR source IN ('agent_auto', 'agent_accepted')
    );

ALTER TABLE rss_entries
    DROP CONSTRAINT rss_entries_fulfillment_consistent,
    DROP COLUMN fulfillment_source;
