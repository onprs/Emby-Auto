ALTER TABLE acquisitions
    ADD COLUMN deletion_requested_at timestamptz;

CREATE INDEX acquisitions_visible_created_idx
    ON acquisitions (created_at, id)
    WHERE deletion_requested_at IS NULL;
