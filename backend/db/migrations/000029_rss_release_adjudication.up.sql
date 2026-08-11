ALTER TABLE agent_resolutions
    DROP CONSTRAINT agent_resolutions_capability_valid,
    ADD CONSTRAINT agent_resolutions_capability_valid CHECK (
        capability IN (
            'rss_coordinate',
            'rss_release_adjudication',
            'download_file_resolution',
            'catalog_candidate',
            'episode_mapping'
        )
    );

CREATE TABLE rss_adjudication_batches (
    id uuid PRIMARY KEY,
    subscription_id uuid NOT NULL REFERENCES rss_subscriptions (id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending',
    entry_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rss_adjudication_batches_status_valid CHECK (
        status IN ('pending', 'applied', 'expired')
    ),
    CONSTRAINT rss_adjudication_batches_entry_count_nonnegative CHECK (entry_count >= 0),
    CONSTRAINT rss_adjudication_batches_owner_unique UNIQUE (id, subscription_id)
);

CREATE INDEX rss_adjudication_batches_subscription_idx
    ON rss_adjudication_batches (subscription_id, created_at DESC, id DESC);

CREATE INDEX rss_adjudication_batches_pending_idx
    ON rss_adjudication_batches (created_at, id)
    WHERE status = 'pending';

ALTER TABLE rss_entries
    ADD CONSTRAINT rss_entries_adjudication_owner_unique UNIQUE (id, subscription_id);

CREATE TABLE rss_entry_adjudications (
    entry_id uuid PRIMARY KEY,
    subscription_id uuid NOT NULL,
    batch_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    source text,
    resolution_id uuid REFERENCES agent_resolutions (id) ON DELETE RESTRICT,
    related_entry_id uuid REFERENCES rss_entries (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rss_entry_adjudications_entry_owner_fk FOREIGN KEY (entry_id, subscription_id)
        REFERENCES rss_entries (id, subscription_id) ON DELETE CASCADE,
    CONSTRAINT rss_entry_adjudications_batch_owner_fk FOREIGN KEY (batch_id, subscription_id)
        REFERENCES rss_adjudication_batches (id, subscription_id) ON DELETE RESTRICT,
    CONSTRAINT rss_entry_adjudications_state_valid CHECK (
        state IN ('pending', 'selected', 'ignored')
    ),
    CONSTRAINT rss_entry_adjudications_source_valid CHECK (
        source IS NULL OR source IN ('agent_auto', 'agent_accepted')
    ),
    CONSTRAINT rss_entry_adjudications_consistent CHECK (
        (state = 'pending' AND source IS NULL AND resolution_id IS NULL AND related_entry_id IS NULL)
        OR (state IN ('selected', 'ignored') AND source IS NOT NULL AND resolution_id IS NOT NULL)
    ),
    CONSTRAINT rss_entry_adjudications_related_not_self CHECK (
        related_entry_id IS NULL OR related_entry_id <> entry_id
    )
);

CREATE INDEX rss_entry_adjudications_batch_idx
    ON rss_entry_adjudications (batch_id, entry_id);
