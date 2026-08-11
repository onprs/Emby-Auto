ALTER TABLE downloads
    ADD COLUMN version integer NOT NULL DEFAULT 1;

ALTER TABLE downloads
    ADD CONSTRAINT downloads_version_positive CHECK (version > 0);
