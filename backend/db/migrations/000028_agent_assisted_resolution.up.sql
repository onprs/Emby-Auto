ALTER TABLE connectivity_test_results
    DROP CONSTRAINT connectivity_test_results_target_valid;

ALTER TABLE connectivity_test_results
    ADD CONSTRAINT connectivity_test_results_target_valid CHECK (
        target IN ('qbittorrent', 'tmdb', 'emby', 'media_tools', 'network_proxy', 'agent')
    );

CREATE TABLE agent_resolutions (
    id uuid PRIMARY KEY,
    operation_id uuid NOT NULL UNIQUE REFERENCES operations (id) ON DELETE RESTRICT,
    version integer NOT NULL DEFAULT 1,
    capability text NOT NULL,
    resource_type text NOT NULL,
    resource_id uuid NOT NULL,
    resource_version integer,
    trigger text NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    input_fingerprint bytea NOT NULL,
    configuration_version integer NOT NULL,
    protocol text NOT NULL,
    provider_origin text NOT NULL,
    model text NOT NULL,
    prompt_version text NOT NULL,
    toolset_version text NOT NULL,
    proposal jsonb NOT NULL DEFAULT '{}'::jsonb,
    validation jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code text,
    error_message text,
    input_tokens bigint,
    output_tokens bigint,
    tool_call_count integer NOT NULL DEFAULT 0,
    latency_milliseconds bigint,
    created_by uuid REFERENCES admin_users (id) ON DELETE RESTRICT,
    reviewed_by uuid REFERENCES admin_users (id) ON DELETE RESTRICT,
    review_decision text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    reviewed_at timestamptz,
    applied_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_resolutions_version_positive CHECK (version > 0),
    CONSTRAINT agent_resolutions_capability_valid CHECK (
        capability IN ('rss_coordinate', 'download_file_resolution', 'catalog_candidate', 'episode_mapping')
    ),
    CONSTRAINT agent_resolutions_resource_type_not_blank CHECK (btrim(resource_type) <> ''),
    CONSTRAINT agent_resolutions_resource_version_positive CHECK (resource_version IS NULL OR resource_version > 0),
    CONSTRAINT agent_resolutions_trigger_valid CHECK (trigger IN ('automatic', 'user', 'retry')),
    CONSTRAINT agent_resolutions_status_valid CHECK (
        status IN ('queued', 'running', 'proposed', 'review_required', 'applied', 'rejected', 'failed', 'cancelled', 'expired')
    ),
    CONSTRAINT agent_resolutions_fingerprint_length CHECK (octet_length(input_fingerprint) = 32),
    CONSTRAINT agent_resolutions_configuration_version_nonnegative CHECK (configuration_version >= 0),
    CONSTRAINT agent_resolutions_protocol_valid CHECK (protocol = 'openai_chat_completions'),
    CONSTRAINT agent_resolutions_provider_origin_not_blank CHECK (btrim(provider_origin) <> ''),
    CONSTRAINT agent_resolutions_model_not_blank CHECK (btrim(model) <> ''),
    CONSTRAINT agent_resolutions_prompt_version_not_blank CHECK (btrim(prompt_version) <> ''),
    CONSTRAINT agent_resolutions_toolset_version_not_blank CHECK (btrim(toolset_version) <> ''),
    CONSTRAINT agent_resolutions_proposal_object CHECK (jsonb_typeof(proposal) = 'object'),
    CONSTRAINT agent_resolutions_validation_object CHECK (jsonb_typeof(validation) = 'object'),
    CONSTRAINT agent_resolutions_error_pair CHECK ((error_code IS NULL) = (error_message IS NULL)),
    CONSTRAINT agent_resolutions_usage_nonnegative CHECK (
        (input_tokens IS NULL OR input_tokens >= 0)
        AND (output_tokens IS NULL OR output_tokens >= 0)
        AND tool_call_count >= 0
        AND (latency_milliseconds IS NULL OR latency_milliseconds >= 0)
    ),
    CONSTRAINT agent_resolutions_review_pair CHECK (
        (reviewed_by IS NULL AND review_decision IS NULL AND reviewed_at IS NULL)
        OR (reviewed_by IS NOT NULL AND review_decision IN ('accepted', 'rejected') AND reviewed_at IS NOT NULL)
    ),
    CONSTRAINT agent_resolutions_applied_time CHECK (
        (status = 'applied' AND applied_at IS NOT NULL)
        OR (status <> 'applied' AND applied_at IS NULL)
    )
);

CREATE UNIQUE INDEX agent_resolutions_input_unique
    ON agent_resolutions (
        capability,
        resource_type,
        resource_id,
        COALESCE(resource_version, 0),
        input_fingerprint,
        configuration_version,
        protocol,
        provider_origin,
        prompt_version,
        toolset_version,
        model
    )
    WHERE status <> 'cancelled';

CREATE INDEX agent_resolutions_list_idx
    ON agent_resolutions (created_at DESC, id DESC);

CREATE INDEX agent_resolutions_resource_idx
    ON agent_resolutions (resource_type, resource_id, created_at DESC, id DESC);

CREATE TABLE agent_resolution_steps (
    id uuid PRIMARY KEY,
    resolution_id uuid NOT NULL REFERENCES agent_resolutions (id) ON DELETE CASCADE,
    sequence integer NOT NULL,
    tool_name text NOT NULL,
    status text NOT NULL,
    arguments_digest bytea,
    result_digest bytea,
    duration_milliseconds bigint,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_resolution_steps_sequence_positive CHECK (sequence > 0),
    CONSTRAINT agent_resolution_steps_tool_not_blank CHECK (btrim(tool_name) <> ''),
    CONSTRAINT agent_resolution_steps_status_valid CHECK (status IN ('succeeded', 'failed', 'rejected')),
    CONSTRAINT agent_resolution_steps_arguments_digest_length CHECK (arguments_digest IS NULL OR octet_length(arguments_digest) = 32),
    CONSTRAINT agent_resolution_steps_result_digest_length CHECK (result_digest IS NULL OR octet_length(result_digest) = 32),
    CONSTRAINT agent_resolution_steps_duration_nonnegative CHECK (duration_milliseconds IS NULL OR duration_milliseconds >= 0),
    CONSTRAINT agent_resolution_steps_error_not_blank CHECK (error_code IS NULL OR btrim(error_code) <> ''),
    UNIQUE (resolution_id, sequence)
);

ALTER TABLE rss_entries
    ADD COLUMN coordinate_source text,
    ADD COLUMN agent_resolution_id uuid REFERENCES agent_resolutions (id) ON DELETE RESTRICT,
    ADD CONSTRAINT rss_entries_coordinate_source_valid CHECK (
        coordinate_source IS NULL OR coordinate_source IN ('deterministic', 'agent_auto', 'agent_accepted', 'user')
    ),
    ADD CONSTRAINT rss_entries_coordinate_agent_consistent CHECK (
        (coordinate_source IN ('agent_auto', 'agent_accepted') AND agent_resolution_id IS NOT NULL)
        OR (coordinate_source IS NULL OR coordinate_source IN ('deterministic', 'user')) AND agent_resolution_id IS NULL
    );

UPDATE rss_entries
SET coordinate_source = 'deterministic'
WHERE source_season IS NOT NULL AND source_episode IS NOT NULL;

ALTER TABLE downloads
    ADD COLUMN file_resolution_source text,
    ADD COLUMN agent_resolution_id uuid REFERENCES agent_resolutions (id) ON DELETE RESTRICT,
    ADD CONSTRAINT downloads_file_resolution_source_valid CHECK (
        file_resolution_source IS NULL OR file_resolution_source IN ('deterministic', 'agent_auto', 'agent_accepted', 'user')
    ),
    ADD CONSTRAINT downloads_file_resolution_agent_consistent CHECK (
        (file_resolution_source IN ('agent_auto', 'agent_accepted') AND agent_resolution_id IS NOT NULL)
        OR (file_resolution_source IS NULL OR file_resolution_source IN ('deterministic', 'user')) AND agent_resolution_id IS NULL
    );

UPDATE downloads AS download
SET file_resolution_source = 'deterministic'
WHERE EXISTS (
    SELECT 1 FROM download_files AS file
    WHERE file.download_id = download.id AND file.selected
);

ALTER TABLE downloads
    DROP CONSTRAINT downloads_status_valid,
    ADD CONSTRAINT downloads_status_valid CHECK (status IN (
        'enqueue_pending', 'file_resolution_pending', 'downloading', 'completed', 'selecting_files', 'materialized', 'failed', 'cancelled'
    ));

ALTER TABLE downloads
    DROP CONSTRAINT downloads_failure_stage_valid,
    ADD CONSTRAINT downloads_failure_stage_valid CHECK (
        failure_stage IS NULL OR failure_stage IN ('enqueue', 'file_resolution', 'sync', 'materialize')
    );

ALTER TABLE episode_mapping_profiles
    ADD COLUMN decision_source text,
    ADD COLUMN agent_resolution_id uuid REFERENCES agent_resolutions (id) ON DELETE RESTRICT;

UPDATE episode_mapping_profiles
SET decision_source = CASE WHEN created_by IS NULL THEN 'legacy' ELSE 'user' END;

ALTER TABLE episode_mapping_profiles
    ALTER COLUMN decision_source SET NOT NULL,
    ADD CONSTRAINT episode_mapping_profiles_decision_source_valid CHECK (
        decision_source IN ('user', 'agent_auto', 'agent_accepted', 'legacy')
    ),
    ADD CONSTRAINT episode_mapping_profiles_decision_source_consistent CHECK (
        (decision_source = 'user' AND created_by IS NOT NULL AND agent_resolution_id IS NULL)
        OR (decision_source = 'agent_auto' AND created_by IS NULL AND agent_resolution_id IS NOT NULL)
        OR (decision_source = 'agent_accepted' AND created_by IS NOT NULL AND agent_resolution_id IS NOT NULL)
        OR (decision_source = 'legacy' AND agent_resolution_id IS NULL)
    );

ALTER TABLE episode_mapping_saves
    ALTER COLUMN created_by DROP NOT NULL,
    ADD COLUMN decision_source text NOT NULL DEFAULT 'user',
    ADD COLUMN agent_resolution_id uuid REFERENCES agent_resolutions (id) ON DELETE RESTRICT,
    ADD CONSTRAINT episode_mapping_saves_decision_source_valid CHECK (
        decision_source IN ('user', 'agent_auto', 'agent_accepted')
    ),
    ADD CONSTRAINT episode_mapping_saves_decision_source_consistent CHECK (
        (decision_source = 'user' AND created_by IS NOT NULL AND agent_resolution_id IS NULL)
        OR (decision_source = 'agent_auto' AND created_by IS NULL AND agent_resolution_id IS NOT NULL)
        OR (decision_source = 'agent_accepted' AND created_by IS NOT NULL AND agent_resolution_id IS NOT NULL)
    );

ALTER TABLE episode_mapping_saves
    ALTER COLUMN decision_source DROP DEFAULT;
