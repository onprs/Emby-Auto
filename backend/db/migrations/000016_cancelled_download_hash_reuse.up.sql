DROP INDEX downloads_torrent_hash_unique;

CREATE UNIQUE INDEX downloads_torrent_hash_unique
    ON downloads (lower(torrent_hash))
    WHERE torrent_hash IS NOT NULL
      AND status <> 'cancelled';
