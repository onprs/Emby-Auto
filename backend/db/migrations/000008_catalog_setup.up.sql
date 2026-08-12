CREATE TABLE installation_state (
    id boolean PRIMARY KEY DEFAULT true,
    completed_by uuid NOT NULL REFERENCES admin_users (id) ON DELETE RESTRICT,
    completed_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT installation_state_singleton CHECK (id)
);

CREATE TABLE episode_mapping_saves (
    id uuid PRIMARY KEY,
    acquisition_id uuid NOT NULL REFERENCES acquisitions (id) ON DELETE CASCADE,
    profile_id uuid NOT NULL REFERENCES episode_mapping_profiles (id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL UNIQUE,
    request_fingerprint bytea NOT NULL,
    result_payload jsonb NOT NULL,
    created_by uuid NOT NULL REFERENCES admin_users (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT episode_mapping_saves_key_not_blank CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT episode_mapping_saves_fingerprint_length CHECK (octet_length(request_fingerprint) = 32),
    CONSTRAINT episode_mapping_saves_result_object CHECK (jsonb_typeof(result_payload) = 'object')
);

CREATE INDEX episode_mapping_saves_acquisition_idx
    ON episode_mapping_saves (acquisition_id, created_at DESC, id DESC);

CREATE TABLE emby_scan_runs (
    id uuid PRIMARY KEY,
    operation_id uuid NOT NULL UNIQUE REFERENCES operations (id) ON DELETE RESTRICT,
    status text NOT NULL DEFAULT 'queued',
    library_count integer NOT NULL DEFAULT 0,
    item_count integer NOT NULL DEFAULT 0,
    error_code text,
    error_message text,
    started_at timestamptz,
    completed_at timestamptz,
    created_by uuid NOT NULL REFERENCES admin_users (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT emby_scan_runs_status_valid CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT emby_scan_runs_counts_nonnegative CHECK (library_count >= 0 AND item_count >= 0),
    CONSTRAINT emby_scan_runs_error_pair CHECK ((error_code IS NULL) = (error_message IS NULL)),
    CONSTRAINT emby_scan_runs_terminal_time CHECK (
        (status IN ('succeeded', 'failed', 'cancelled') AND completed_at IS NOT NULL)
        OR (status IN ('queued', 'running') AND completed_at IS NULL)
    ),
    CONSTRAINT emby_scan_runs_failure_error CHECK (
        status NOT IN ('failed', 'cancelled')
        OR (error_code IS NOT NULL AND btrim(error_code) <> '' AND error_message IS NOT NULL AND btrim(error_message) <> '')
    )
);

CREATE INDEX emby_scan_runs_created_idx
    ON emby_scan_runs (created_at DESC, id DESC);

CREATE UNIQUE INDEX emby_scan_runs_one_active
    ON emby_scan_runs ((true))
    WHERE status IN ('queued', 'running');

CREATE TABLE emby_libraries (
    id uuid PRIMARY KEY,
    emby_id text NOT NULL UNIQUE,
    name text NOT NULL,
    collection_type text,
    locations text[] NOT NULL DEFAULT '{}'::text[],
    present boolean NOT NULL DEFAULT true,
    last_scan_run_id uuid NOT NULL REFERENCES emby_scan_runs (id) ON DELETE RESTRICT,
    last_seen_at timestamptz NOT NULL,
    upstream_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT emby_libraries_emby_id_not_blank CHECK (btrim(emby_id) <> ''),
    CONSTRAINT emby_libraries_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT emby_libraries_locations_valid CHECK (array_position(locations, NULL) IS NULL),
    CONSTRAINT emby_libraries_payload_object CHECK (jsonb_typeof(upstream_payload) = 'object')
);

CREATE INDEX emby_libraries_present_name_idx
    ON emby_libraries (present, lower(name), id);

CREATE TABLE emby_library_items (
    id uuid PRIMARY KEY,
    emby_id text NOT NULL UNIQUE,
    library_id uuid NOT NULL REFERENCES emby_libraries (id) ON DELETE CASCADE,
    parent_emby_id text,
    item_type text NOT NULL,
    name text NOT NULL,
    file_path text,
    provider_ids jsonb NOT NULL DEFAULT '{}'::jsonb,
    season_number integer,
    episode_number integer,
    present boolean NOT NULL DEFAULT true,
    last_scan_run_id uuid NOT NULL REFERENCES emby_scan_runs (id) ON DELETE RESTRICT,
    last_seen_at timestamptz NOT NULL,
    upstream_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT emby_library_items_emby_id_not_blank CHECK (btrim(emby_id) <> ''),
    CONSTRAINT emby_library_items_parent_not_blank CHECK (parent_emby_id IS NULL OR btrim(parent_emby_id) <> ''),
    CONSTRAINT emby_library_items_type_valid CHECK (item_type IN ('Series', 'Season', 'Episode')),
    CONSTRAINT emby_library_items_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT emby_library_items_path_not_blank CHECK (file_path IS NULL OR btrim(file_path) <> ''),
    CONSTRAINT emby_library_items_provider_ids_object CHECK (jsonb_typeof(provider_ids) = 'object'),
    CONSTRAINT emby_library_items_season_nonnegative CHECK (season_number IS NULL OR season_number >= 0),
    CONSTRAINT emby_library_items_episode_positive CHECK (episode_number IS NULL OR episode_number > 0),
    CONSTRAINT emby_library_items_payload_object CHECK (jsonb_typeof(upstream_payload) = 'object')
);

CREATE INDEX emby_library_items_library_cursor_idx
    ON emby_library_items (library_id, present, item_type, id);

CREATE INDEX emby_library_items_provider_ids_gin
    ON emby_library_items USING gin (provider_ids);
