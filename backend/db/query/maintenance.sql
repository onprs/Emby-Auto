-- name: ListTaskIDsForStagingMigration :many
SELECT id
FROM episode_tasks
ORDER BY id;

-- name: ListActiveDownloadHashes :many
SELECT torrent_hash
FROM downloads
WHERE status IN ('enqueue_pending', 'file_resolution_pending', 'downloading', 'completed', 'selecting_files')
  AND torrent_hash IS NOT NULL
ORDER BY id;
