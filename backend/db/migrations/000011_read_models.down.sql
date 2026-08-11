DROP TABLE connectivity_test_results;

ALTER TABLE rss_entries
    DROP CONSTRAINT rss_entries_duplicate_count_nonnegative,
    DROP COLUMN duplicate_count;
