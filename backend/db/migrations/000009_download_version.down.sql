ALTER TABLE downloads
    DROP CONSTRAINT downloads_version_positive;

ALTER TABLE downloads
    DROP COLUMN version;
