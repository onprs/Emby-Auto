DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_resolutions
        WHERE resource_type = 'rss_feed_lookup'
    ) THEN
        RAISE EXCEPTION 'cannot remove RSS feed catalog lookup resources while Agent resolution audit records reference them';
    END IF;
END
$$;

DROP INDEX rss_feed_catalog_lookups_expiry_idx;
DROP TABLE rss_feed_catalog_lookups;
