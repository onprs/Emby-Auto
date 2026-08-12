ALTER TABLE agent_resolutions
    DROP CONSTRAINT agent_resolutions_capability_valid,
    ADD CONSTRAINT agent_resolutions_capability_valid CHECK (
        capability IN (
            'rss_coordinate',
            'rss_release_adjudication',
            'rss_preacquisition_mapping',
            'download_file_resolution',
            'catalog_candidate',
            'episode_mapping'
        )
    );

CREATE TABLE rss_preacquisition_mapping_scopes (
    id uuid PRIMARY KEY,
    subscription_id uuid NOT NULL REFERENCES rss_subscriptions (id) ON DELETE CASCADE,
    subscription_version integer NOT NULL,
    source_fingerprint bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    applied_profile_id uuid REFERENCES episode_mapping_profiles (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rss_preacquisition_mapping_scopes_version_positive CHECK (subscription_version > 0),
    CONSTRAINT rss_preacquisition_mapping_scopes_fingerprint_length CHECK (octet_length(source_fingerprint) = 32),
    CONSTRAINT rss_preacquisition_mapping_scopes_status_valid CHECK (status IN ('pending', 'applied', 'expired')),
    CONSTRAINT rss_preacquisition_mapping_scopes_applied_consistent CHECK (
        (status = 'applied' AND applied_profile_id IS NOT NULL)
        OR (status <> 'applied' AND applied_profile_id IS NULL)
    ),
    CONSTRAINT rss_preacquisition_mapping_scopes_identity_unique UNIQUE (
        subscription_id, subscription_version, source_fingerprint
    )
);

CREATE INDEX rss_preacquisition_mapping_scopes_pending_idx
    ON rss_preacquisition_mapping_scopes (created_at, id)
    WHERE status = 'pending';

CREATE TABLE rss_preacquisition_mapping_sources (
    scope_id uuid NOT NULL REFERENCES rss_preacquisition_mapping_scopes (id) ON DELETE CASCADE,
    entry_id uuid NOT NULL REFERENCES rss_entries (id) ON DELETE CASCADE,
    source_season integer NOT NULL,
    source_episode integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope_id, entry_id),
    CONSTRAINT rss_preacquisition_mapping_sources_season_positive CHECK (source_season > 0),
    CONSTRAINT rss_preacquisition_mapping_sources_episode_positive CHECK (source_episode > 0)
);

CREATE INDEX rss_preacquisition_mapping_sources_coordinate_idx
    ON rss_preacquisition_mapping_sources (scope_id, source_season, source_episode, entry_id);
