ALTER TABLE rss_entries
    ADD COLUMN fulfillment_source text;

UPDATE rss_entries
SET fulfillment_source = 'managed_import'
WHERE imported_at IS NOT NULL;

ALTER TABLE rss_entries
    ADD CONSTRAINT rss_entries_fulfillment_consistent CHECK (
        (imported_at IS NULL AND fulfillment_source IS NULL)
        OR (
            imported_at IS NOT NULL
            AND fulfillment_source IN ('managed_import', 'emby_catalog')
        )
    );

ALTER TABLE rss_entry_adjudications
    DROP CONSTRAINT rss_entry_adjudications_source_valid,
    ADD CONSTRAINT rss_entry_adjudications_source_valid CHECK (
        source IS NULL OR source IN ('deterministic', 'agent_auto', 'agent_accepted')
    );
