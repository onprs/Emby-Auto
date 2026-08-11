ALTER TABLE downloads
    ADD COLUMN deletion_requested_at timestamptz,
    ADD COLUMN deleted_at timestamptz;

DROP INDEX downloads_torrent_hash_unique;

CREATE UNIQUE INDEX downloads_torrent_hash_unique
    ON downloads (lower(torrent_hash))
    WHERE torrent_hash IS NOT NULL
      AND status <> 'cancelled'
      AND deleted_at IS NULL;

CREATE INDEX downloads_visible_list_idx
    ON downloads (id)
    WHERE deleted_at IS NULL;
