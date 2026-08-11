DROP INDEX downloads_visible_list_idx;
DROP INDEX downloads_torrent_hash_unique;

CREATE UNIQUE INDEX downloads_torrent_hash_unique
    ON downloads (lower(torrent_hash))
    WHERE torrent_hash IS NOT NULL
      AND status <> 'cancelled';

ALTER TABLE downloads
    DROP COLUMN deleted_at,
    DROP COLUMN deletion_requested_at;
