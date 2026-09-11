CREATE TABLE rss_subtitle_groups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    normalized_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rss_subtitle_groups_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT rss_subtitle_groups_normalized_name_not_blank CHECK (btrim(normalized_name) <> ''),
    CONSTRAINT rss_subtitle_groups_normalized_name_unique UNIQUE (normalized_name)
);

CREATE INDEX rss_subtitle_groups_name_idx
    ON rss_subtitle_groups (normalized_name, id);
