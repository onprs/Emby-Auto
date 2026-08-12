CREATE TABLE legacy_migration_runs (
    id uuid PRIMARY KEY,
    source_kind text NOT NULL,
    source_fingerprint bytea NOT NULL,
    status text NOT NULL DEFAULT 'running',
    counts jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code text,
    error_message text,
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CONSTRAINT legacy_migration_runs_source_kind_not_blank CHECK (btrim(source_kind) <> ''),
    CONSTRAINT legacy_migration_runs_fingerprint_length CHECK (octet_length(source_fingerprint) = 32),
    CONSTRAINT legacy_migration_runs_status_valid CHECK (status IN ('running', 'completed', 'failed')),
    CONSTRAINT legacy_migration_runs_counts_object CHECK (jsonb_typeof(counts) = 'object'),
    CONSTRAINT legacy_migration_runs_terminal_time CHECK (
        (status = 'running' AND completed_at IS NULL)
        OR (status IN ('completed', 'failed') AND completed_at IS NOT NULL)
    ),
    CONSTRAINT legacy_migration_runs_failure_error CHECK (
        status <> 'failed'
        OR (
            error_code IS NOT NULL AND btrim(error_code) <> ''
            AND error_message IS NOT NULL AND btrim(error_message) <> ''
        )
    )
);

CREATE INDEX legacy_migration_runs_started_idx
    ON legacy_migration_runs (started_at DESC, id DESC);

CREATE TABLE legacy_migration_items (
    id uuid PRIMARY KEY,
    source_kind text NOT NULL,
    legacy_id text NOT NULL,
    fingerprint bytea NOT NULL,
    migration_run_id uuid NOT NULL REFERENCES legacy_migration_runs (id) ON DELETE RESTRICT,
    status text NOT NULL,
    resource_type text,
    resource_id uuid,
    error_code text,
    error_message text,
    legacy_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT legacy_migration_items_source_kind_not_blank CHECK (btrim(source_kind) <> ''),
    CONSTRAINT legacy_migration_items_legacy_id_not_blank CHECK (btrim(legacy_id) <> ''),
    CONSTRAINT legacy_migration_items_fingerprint_length CHECK (octet_length(fingerprint) = 32),
    CONSTRAINT legacy_migration_items_status_valid CHECK (status IN ('imported', 'skipped', 'invalid')),
    CONSTRAINT legacy_migration_items_resource_pair CHECK ((resource_type IS NULL) = (resource_id IS NULL)),
    CONSTRAINT legacy_migration_items_imported_resource CHECK (
        status <> 'imported'
        OR (resource_type IS NOT NULL AND resource_id IS NOT NULL)
    ),
    CONSTRAINT legacy_migration_items_error_pair CHECK ((error_code IS NULL) = (error_message IS NULL)),
    CONSTRAINT legacy_migration_items_payload_object CHECK (jsonb_typeof(legacy_payload) = 'object'),
    UNIQUE (source_kind, legacy_id)
);

CREATE INDEX legacy_migration_items_run_idx
    ON legacy_migration_items (migration_run_id, status, source_kind, legacy_id);
