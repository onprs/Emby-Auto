DROP INDEX acquisitions_visible_created_idx;

ALTER TABLE acquisitions
    DROP COLUMN deletion_requested_at;
