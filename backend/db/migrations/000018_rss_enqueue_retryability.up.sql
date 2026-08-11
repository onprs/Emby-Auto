ALTER TABLE rss_entries
    ADD COLUMN last_error_retryable boolean NOT NULL DEFAULT false;

UPDATE rss_entries
SET last_error_retryable = true
WHERE status = 'enqueue_failed'
  AND last_error_code IN (
      'configuration_unavailable',
      'download_storage_unavailable',
      'operation_failed',
      'operation_interrupted',
      'qbittorrent_category_failed',
      'qbittorrent_compensation_failed',
      'qbittorrent_enqueue_failed',
      'qbittorrent_files_unavailable',
      'qbittorrent_file_priority_failed',
      'qbittorrent_resume_failed',
      'qbittorrent_unavailable',
      'rss_schedule_failed'
  );
