DROP TRIGGER IF EXISTS rss_subscription_progress_media_series_changes ON media_series;
DROP TRIGGER IF EXISTS rss_subscription_progress_mapping_changes ON episode_mappings;
DROP TRIGGER IF EXISTS rss_subscription_progress_operation_changes ON operations;
DROP TRIGGER IF EXISTS rss_subscription_progress_cleanup_changes ON cleanup_runs;
DROP TRIGGER IF EXISTS rss_subscription_progress_import_changes ON imports;
DROP TRIGGER IF EXISTS rss_subscription_progress_review_changes ON reviews;
DROP TRIGGER IF EXISTS rss_subscription_progress_artifact_set_changes ON artifact_sets;
DROP TRIGGER IF EXISTS rss_subscription_progress_task_changes ON episode_tasks;
DROP TRIGGER IF EXISTS rss_subscription_progress_download_file_changes ON download_files;
DROP TRIGGER IF EXISTS rss_subscription_progress_download_changes ON downloads;
DROP TRIGGER IF EXISTS rss_subscription_progress_acquisition_changes ON acquisitions;
DROP TRIGGER IF EXISTS rss_subscription_progress_entry_changes ON rss_entries;
DROP TRIGGER IF EXISTS rss_subscription_progress_subscription_changes ON rss_subscriptions;

DROP FUNCTION IF EXISTS mark_rss_subscription_progress_from_change();
DROP FUNCTION IF EXISTS mark_rss_subscription_progress_for_mapping_profiles(uuid[]);
DROP FUNCTION IF EXISTS mark_rss_subscription_progress_for_tasks(uuid[]);
DROP FUNCTION IF EXISTS mark_rss_subscription_progress_for_downloads(uuid[]);
DROP FUNCTION IF EXISTS mark_rss_subscription_progress_for_acquisitions(uuid[]);
DROP FUNCTION IF EXISTS mark_rss_subscription_progress_for_entries(uuid[]);
DROP FUNCTION IF EXISTS mark_rss_subscription_progress_dirty_many(uuid[]);
DROP FUNCTION IF EXISTS mark_rss_subscription_progress_dirty(uuid);

DROP TABLE IF EXISTS rss_subscription_progress;
