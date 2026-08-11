CREATE TABLE admin_users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username text NOT NULL,
    password_hash text NOT NULL,
    disabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT admin_users_username_not_blank CHECK (btrim(username) <> ''),
    CONSTRAINT admin_users_password_hash_not_blank CHECK (btrim(password_hash) <> '')
);

CREATE UNIQUE INDEX admin_users_username_unique
    ON admin_users (lower(username));

CREATE TABLE sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_user_id uuid NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sessions_token_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT sessions_expiry_after_creation CHECK (expires_at > created_at)
);

CREATE INDEX sessions_active_user_idx
    ON sessions (admin_user_id, expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE app_settings (
    name text PRIMARY KEY,
    value jsonb NOT NULL,
    version integer NOT NULL DEFAULT 1,
    updated_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT app_settings_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT app_settings_version_positive CHECK (version > 0)
);

CREATE TABLE app_secrets (
    name text PRIMARY KEY,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    masked_hint text NOT NULL DEFAULT '',
    version integer NOT NULL DEFAULT 1,
    updated_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT app_secrets_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT app_secrets_ciphertext_not_empty CHECK (octet_length(ciphertext) > 0),
    CONSTRAINT app_secrets_nonce_not_empty CHECK (octet_length(nonce) > 0),
    CONSTRAINT app_secrets_version_positive CHECK (version > 0)
);

CREATE TABLE media_series (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tmdb_series_id bigint UNIQUE,
    title text NOT NULL,
    original_title text,
    media_type text NOT NULL DEFAULT 'tv',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    legacy_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT media_series_tmdb_id_positive CHECK (tmdb_series_id IS NULL OR tmdb_series_id > 0),
    CONSTRAINT media_series_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT media_series_type_valid CHECK (media_type IN ('tv')),
    CONSTRAINT media_series_metadata_object CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX media_series_legacy_id_unique
    ON media_series (legacy_id)
    WHERE legacy_id IS NOT NULL;

CREATE TABLE tmdb_seasons (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    series_id uuid NOT NULL REFERENCES media_series (id) ON DELETE CASCADE,
    tmdb_season_id bigint,
    season_number integer NOT NULL,
    name text,
    episode_count integer NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now(),
    upstream_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tmdb_seasons_tmdb_id_positive CHECK (tmdb_season_id IS NULL OR tmdb_season_id > 0),
    CONSTRAINT tmdb_seasons_number_nonnegative CHECK (season_number >= 0),
    CONSTRAINT tmdb_seasons_episode_count_nonnegative CHECK (episode_count >= 0),
    CONSTRAINT tmdb_seasons_payload_object CHECK (jsonb_typeof(upstream_payload) = 'object'),
    UNIQUE (series_id, season_number)
);

CREATE UNIQUE INDEX tmdb_seasons_tmdb_id_unique
    ON tmdb_seasons (tmdb_season_id)
    WHERE tmdb_season_id IS NOT NULL;

CREATE TABLE media_episodes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    season_id uuid NOT NULL REFERENCES tmdb_seasons (id) ON DELETE CASCADE,
    tmdb_episode_id bigint,
    episode_number integer NOT NULL,
    title text NOT NULL,
    air_date date,
    upstream_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT media_episodes_tmdb_id_positive CHECK (tmdb_episode_id IS NULL OR tmdb_episode_id > 0),
    CONSTRAINT media_episodes_number_positive CHECK (episode_number > 0),
    CONSTRAINT media_episodes_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT media_episodes_payload_object CHECK (jsonb_typeof(upstream_payload) = 'object'),
    UNIQUE (season_id, episode_number)
);

CREATE UNIQUE INDEX media_episodes_tmdb_id_unique
    ON media_episodes (tmdb_episode_id)
    WHERE tmdb_episode_id IS NOT NULL;

CREATE TABLE episode_mapping_profiles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    series_id uuid NOT NULL REFERENCES media_series (id) ON DELETE CASCADE,
    name text NOT NULL,
    version integer NOT NULL,
    source_season_lengths integer[],
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
    CONSTRAINT episode_mapping_profiles_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT episode_mapping_profiles_version_positive CHECK (version > 0),
    CONSTRAINT episode_mapping_profiles_lengths_valid CHECK (
        source_season_lengths IS NULL
        OR (
            array_position(source_season_lengths, NULL) IS NULL
            AND 0 < ALL (source_season_lengths)
        )
    ),
    UNIQUE (series_id, name, version)
);

CREATE UNIQUE INDEX episode_mapping_profiles_one_active_version
    ON episode_mapping_profiles (series_id, name)
    WHERE active;

CREATE TABLE episode_mappings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id uuid NOT NULL REFERENCES episode_mapping_profiles (id) ON DELETE CASCADE,
    source_season integer NOT NULL,
    source_episode integer NOT NULL,
    absolute_episode integer,
    target_episode_id uuid REFERENCES media_episodes (id) ON DELETE RESTRICT,
    mapping_status text NOT NULL,
    match_source text NOT NULL,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT episode_mappings_source_season_positive CHECK (source_season > 0),
    CONSTRAINT episode_mappings_source_episode_positive CHECK (source_episode > 0),
    CONSTRAINT episode_mappings_absolute_positive CHECK (absolute_episode IS NULL OR absolute_episode > 0),
    CONSTRAINT episode_mappings_status_valid CHECK (mapping_status IN ('mapped', 'pending')),
    CONSTRAINT episode_mappings_match_source_valid CHECK (match_source IN ('explicit', 'absolute', 'pending')),
    CONSTRAINT episode_mappings_target_matches_status CHECK (
        (mapping_status = 'mapped' AND target_episode_id IS NOT NULL AND error_code IS NULL)
        OR (mapping_status = 'pending' AND target_episode_id IS NULL AND error_code IS NOT NULL)
    ),
    UNIQUE (profile_id, source_season, source_episode)
);

CREATE TABLE search_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    query text NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    requested_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
    error_code text,
    error_message text,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT search_runs_query_not_blank CHECK (btrim(query) <> ''),
    CONSTRAINT search_runs_status_valid CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled'))
);

CREATE TABLE release_candidates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    search_run_id uuid NOT NULL REFERENCES search_runs (id) ON DELETE CASCADE,
    provider text NOT NULL,
    identity_key text NOT NULL,
    title text NOT NULL,
    download_uri text,
    published_at timestamptz,
    size_bytes bigint,
    seeders integer,
    upstream_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT release_candidates_provider_not_blank CHECK (btrim(provider) <> ''),
    CONSTRAINT release_candidates_identity_not_blank CHECK (btrim(identity_key) <> ''),
    CONSTRAINT release_candidates_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT release_candidates_size_nonnegative CHECK (size_bytes IS NULL OR size_bytes >= 0),
    CONSTRAINT release_candidates_seeders_nonnegative CHECK (seeders IS NULL OR seeders >= 0),
    CONSTRAINT release_candidates_payload_object CHECK (jsonb_typeof(upstream_payload) = 'object'),
    UNIQUE (search_run_id, provider, identity_key)
);

CREATE TABLE rss_subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    series_id uuid NOT NULL REFERENCES media_series (id) ON DELETE CASCADE,
    mapping_profile_id uuid REFERENCES episode_mapping_profiles (id) ON DELETE RESTRICT,
    name text NOT NULL,
    feed_url text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    poll_interval_seconds integer NOT NULL DEFAULT 900,
    last_polled_at timestamptz,
    next_poll_at timestamptz,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rss_subscriptions_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT rss_subscriptions_url_not_blank CHECK (btrim(feed_url) <> ''),
    CONSTRAINT rss_subscriptions_poll_interval_valid CHECK (poll_interval_seconds BETWEEN 60 AND 86400),
    CONSTRAINT rss_subscriptions_version_positive CHECK (version > 0),
    UNIQUE (series_id, feed_url)
);

CREATE TABLE rss_entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id uuid NOT NULL REFERENCES rss_subscriptions (id) ON DELETE CASCADE,
    release_candidate_id uuid REFERENCES release_candidates (id) ON DELETE SET NULL,
    identity_key text NOT NULL,
    guid text,
    btih text,
    canonical_url text,
    title text NOT NULL,
    published_at timestamptz,
    status text NOT NULL DEFAULT 'discovered',
    enqueue_attempts integer NOT NULL DEFAULT 0,
    last_error_code text,
    last_error_message text,
    upstream_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    discovered_at timestamptz NOT NULL DEFAULT now(),
    enqueued_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rss_entries_identity_not_blank CHECK (btrim(identity_key) <> ''),
    CONSTRAINT rss_entries_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT rss_entries_btih_valid CHECK (btih IS NULL OR btih ~ '^[0-9a-fA-F]{40}$'),
    CONSTRAINT rss_entries_status_valid CHECK (status IN ('discovered', 'enqueueing', 'enqueued', 'enqueue_failed')),
    CONSTRAINT rss_entries_attempts_nonnegative CHECK (enqueue_attempts >= 0),
    CONSTRAINT rss_entries_payload_object CHECK (jsonb_typeof(upstream_payload) = 'object'),
    UNIQUE (subscription_id, identity_key)
);

CREATE UNIQUE INDEX rss_entries_subscription_btih_unique
    ON rss_entries (subscription_id, lower(btih))
    WHERE btih IS NOT NULL;

CREATE TABLE acquisitions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    series_id uuid NOT NULL REFERENCES media_series (id) ON DELETE RESTRICT,
    mapping_profile_id uuid REFERENCES episode_mapping_profiles (id) ON DELETE RESTRICT,
    source_kind text NOT NULL,
    release_candidate_id uuid REFERENCES release_candidates (id) ON DELETE SET NULL,
    rss_entry_id uuid REFERENCES rss_entries (id) ON DELETE SET NULL,
    source_uri text,
    source_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    legacy_id text,
    created_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT acquisitions_source_kind_valid CHECK (source_kind IN ('search', 'rss', 'manual')),
    CONSTRAINT acquisitions_source_payload_object CHECK (jsonb_typeof(source_payload) = 'object'),
    CONSTRAINT acquisitions_source_reference_valid CHECK (
        (
            source_kind = 'search'
            AND release_candidate_id IS NOT NULL
            AND rss_entry_id IS NULL
            AND source_uri IS NULL
        )
        OR (
            source_kind = 'rss'
            AND rss_entry_id IS NOT NULL
            AND source_uri IS NULL
        )
        OR (
            source_kind = 'manual'
            AND release_candidate_id IS NULL
            AND rss_entry_id IS NULL
            AND source_uri IS NOT NULL
            AND btrim(source_uri) <> ''
        )
    )
);

CREATE UNIQUE INDEX acquisitions_rss_entry_unique
    ON acquisitions (rss_entry_id)
    WHERE rss_entry_id IS NOT NULL;

CREATE UNIQUE INDEX acquisitions_legacy_id_unique
    ON acquisitions (legacy_id)
    WHERE legacy_id IS NOT NULL;

CREATE TABLE downloads (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    acquisition_id uuid NOT NULL REFERENCES acquisitions (id) ON DELETE CASCADE,
    attempt integer NOT NULL DEFAULT 1,
    client_name text NOT NULL DEFAULT 'qbittorrent',
    torrent_hash text,
    status text NOT NULL DEFAULT 'enqueue_pending',
    progress numeric(6, 5) NOT NULL DEFAULT 0,
    save_path text,
    error_code text,
    error_message text,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT downloads_attempt_positive CHECK (attempt > 0),
    CONSTRAINT downloads_client_not_blank CHECK (btrim(client_name) <> ''),
    CONSTRAINT downloads_torrent_hash_valid CHECK (torrent_hash IS NULL OR torrent_hash ~ '^[0-9a-fA-F]{40}$'),
    CONSTRAINT downloads_status_valid CHECK (status IN (
        'enqueue_pending', 'downloading', 'completed', 'selecting_files', 'materialized', 'failed', 'cancelled'
    )),
    CONSTRAINT downloads_progress_valid CHECK (progress BETWEEN 0 AND 1),
    UNIQUE (acquisition_id, attempt)
);

CREATE UNIQUE INDEX downloads_torrent_hash_unique
    ON downloads (lower(torrent_hash))
    WHERE torrent_hash IS NOT NULL;

CREATE TABLE download_files (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    download_id uuid NOT NULL REFERENCES downloads (id) ON DELETE CASCADE,
    file_index integer NOT NULL,
    relative_path text NOT NULL,
    size_bytes bigint NOT NULL,
    media_kind text NOT NULL DEFAULT 'unknown',
    selected boolean NOT NULL DEFAULT false,
    source_season integer,
    source_episode integer,
    language text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT download_files_index_nonnegative CHECK (file_index >= 0),
    CONSTRAINT download_files_path_relative CHECK (
        btrim(relative_path) <> ''
        AND relative_path !~ '^(?:[A-Za-z]:[\\/]|/|\\\\)'
        AND relative_path !~ '(^|[\\/])\.\.([\\/]|$)'
    ),
    CONSTRAINT download_files_size_nonnegative CHECK (size_bytes >= 0),
    CONSTRAINT download_files_media_kind_valid CHECK (media_kind IN ('unknown', 'video', 'subtitle', 'extra', 'other')),
    CONSTRAINT download_files_source_season_positive CHECK (source_season IS NULL OR source_season > 0),
    CONSTRAINT download_files_source_episode_positive CHECK (source_episode IS NULL OR source_episode > 0),
    UNIQUE (download_id, file_index)
);

CREATE TABLE transcode_profiles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    version integer NOT NULL,
    active boolean NOT NULL DEFAULT true,
    is_default boolean NOT NULL DEFAULT false,
    video_codec text NOT NULL,
    encoder text NOT NULL,
    container text NOT NULL,
    file_extension text NOT NULL,
    quality_mode text NOT NULL,
    quality_value numeric(8, 3) NOT NULL,
    audio_policy text NOT NULL,
    audio_codec text,
    preset text NOT NULL,
    pixel_format text NOT NULL,
    thread_count integer NOT NULL DEFAULT 0,
    max_concurrency integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
    CONSTRAINT transcode_profiles_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT transcode_profiles_version_positive CHECK (version > 0),
    CONSTRAINT transcode_profiles_codec_not_blank CHECK (btrim(video_codec) <> ''),
    CONSTRAINT transcode_profiles_encoder_not_blank CHECK (btrim(encoder) <> ''),
    CONSTRAINT transcode_profiles_container_not_blank CHECK (btrim(container) <> ''),
    CONSTRAINT transcode_profiles_extension_valid CHECK (file_extension ~ '^[a-z0-9]+$'),
    CONSTRAINT transcode_profiles_quality_mode_valid CHECK (quality_mode IN ('crf', 'cq', 'bitrate')),
    CONSTRAINT transcode_profiles_quality_nonnegative CHECK (quality_value >= 0),
    CONSTRAINT transcode_profiles_audio_policy_valid CHECK (audio_policy IN ('copy', 'transcode')),
    CONSTRAINT transcode_profiles_audio_codec_required CHECK (
        audio_policy = 'copy'
        OR (audio_codec IS NOT NULL AND btrim(audio_codec) <> '')
    ),
    CONSTRAINT transcode_profiles_preset_not_blank CHECK (btrim(preset) <> ''),
    CONSTRAINT transcode_profiles_pixel_format_not_blank CHECK (btrim(pixel_format) <> ''),
    CONSTRAINT transcode_profiles_threads_nonnegative CHECK (thread_count >= 0),
    CONSTRAINT transcode_profiles_concurrency_positive CHECK (max_concurrency > 0),
    UNIQUE (name, version)
);

CREATE UNIQUE INDEX transcode_profiles_one_active_version
    ON transcode_profiles (name)
    WHERE active;

CREATE UNIQUE INDEX transcode_profiles_one_default
    ON transcode_profiles (is_default)
    WHERE is_default AND active;

CREATE TABLE episode_tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    acquisition_id uuid NOT NULL REFERENCES acquisitions (id) ON DELETE CASCADE,
    source_video_file_id uuid NOT NULL REFERENCES download_files (id) ON DELETE RESTRICT,
    mapping_id uuid REFERENCES episode_mappings (id) ON DELETE RESTRICT,
    transcode_profile_id uuid NOT NULL REFERENCES transcode_profiles (id) ON DELETE RESTRICT,
    state text NOT NULL DEFAULT 'media_queued',
    video_state text NOT NULL DEFAULT 'transcode_queued',
    subtitle_state text NOT NULL DEFAULT 'subtitle_queued',
    version integer NOT NULL DEFAULT 1,
    error_code text,
    error_message text,
    legacy_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT episode_tasks_state_valid CHECK (state IN (
        'media_queued', 'processing', 'finalizing', 'awaiting_review', 'approved', 'rejected',
        'import_queued', 'importing', 'imported', 'failed', 'cancelled'
    )),
    CONSTRAINT episode_tasks_video_state_valid CHECK (video_state IN ('transcode_queued', 'transcoding', 'video_ready', 'failed', 'cancelled')),
    CONSTRAINT episode_tasks_subtitle_state_valid CHECK (subtitle_state IN ('subtitle_queued', 'extracting_or_converting', 'ass_ready', 'failed', 'cancelled')),
    CONSTRAINT episode_tasks_version_positive CHECK (version > 0),
    UNIQUE (acquisition_id, source_video_file_id)
);

CREATE UNIQUE INDEX episode_tasks_legacy_id_unique
    ON episode_tasks (legacy_id)
    WHERE legacy_id IS NOT NULL;

CREATE INDEX episode_tasks_state_idx
    ON episode_tasks (state, updated_at, id);

CREATE TABLE media_artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id uuid NOT NULL REFERENCES episode_tasks (id) ON DELETE CASCADE,
    source_file_id uuid REFERENCES download_files (id) ON DELETE SET NULL,
    transcode_profile_id uuid REFERENCES transcode_profiles (id) ON DELETE RESTRICT,
    kind text NOT NULL,
    basename text NOT NULL,
    file_path text NOT NULL,
    format text NOT NULL,
    size_bytes bigint NOT NULL,
    checksum_sha256 bytea NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT media_artifacts_kind_valid CHECK (kind IN ('video', 'subtitle')),
    CONSTRAINT media_artifacts_basename_not_blank CHECK (btrim(basename) <> ''),
    CONSTRAINT media_artifacts_path_not_blank CHECK (btrim(file_path) <> ''),
    CONSTRAINT media_artifacts_format_not_blank CHECK (btrim(format) <> ''),
    CONSTRAINT media_artifacts_size_positive CHECK (size_bytes > 0),
    CONSTRAINT media_artifacts_checksum_length CHECK (octet_length(checksum_sha256) = 32),
    CONSTRAINT media_artifacts_metadata_object CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (task_id, kind, checksum_sha256),
    UNIQUE (file_path)
);

CREATE TABLE artifact_sets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id uuid NOT NULL UNIQUE REFERENCES episode_tasks (id) ON DELETE CASCADE,
    transcode_profile_id uuid NOT NULL REFERENCES transcode_profiles (id) ON DELETE RESTRICT,
    basename text NOT NULL,
    video_artifact_id uuid NOT NULL UNIQUE REFERENCES media_artifacts (id) ON DELETE RESTRICT,
    subtitle_artifact_id uuid NOT NULL UNIQUE REFERENCES media_artifacts (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT artifact_sets_basename_not_blank CHECK (btrim(basename) <> ''),
    CONSTRAINT artifact_sets_artifacts_distinct CHECK (video_artifact_id <> subtitle_artifact_id)
);

CREATE FUNCTION validate_artifact_set() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    video media_artifacts%ROWTYPE;
    subtitle media_artifacts%ROWTYPE;
BEGIN
    SELECT * INTO video FROM media_artifacts WHERE id = NEW.video_artifact_id;
    SELECT * INTO subtitle FROM media_artifacts WHERE id = NEW.subtitle_artifact_id;

    IF video.id IS NULL OR subtitle.id IS NULL THEN
        RAISE EXCEPTION 'artifact set references missing artifacts' USING ERRCODE = '23514';
    END IF;
    IF video.task_id <> NEW.task_id OR subtitle.task_id <> NEW.task_id THEN
        RAISE EXCEPTION 'artifact set artifacts must belong to its task' USING ERRCODE = '23514';
    END IF;
    IF video.kind <> 'video' OR subtitle.kind <> 'subtitle' THEN
        RAISE EXCEPTION 'artifact set must contain one video and one subtitle' USING ERRCODE = '23514';
    END IF;
    IF video.transcode_profile_id IS DISTINCT FROM NEW.transcode_profile_id THEN
        RAISE EXCEPTION 'artifact set video must match its transcode profile' USING ERRCODE = '23514';
    END IF;
    IF video.basename <> NEW.basename OR subtitle.basename <> NEW.basename THEN
        RAISE EXCEPTION 'artifact set artifacts must share its basename' USING ERRCODE = '23514';
    END IF;
    IF lower(subtitle.format) <> 'ass' THEN
        RAISE EXCEPTION 'artifact set subtitle must use ASS format' USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;

CREATE CONSTRAINT TRIGGER artifact_sets_validate_pair
    AFTER INSERT OR UPDATE ON artifact_sets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION validate_artifact_set();

CREATE FUNCTION guard_paired_artifact_update() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM artifact_sets
        WHERE video_artifact_id = OLD.id OR subtitle_artifact_id = OLD.id
    ) AND (
        NEW.task_id IS DISTINCT FROM OLD.task_id
        OR NEW.transcode_profile_id IS DISTINCT FROM OLD.transcode_profile_id
        OR NEW.kind IS DISTINCT FROM OLD.kind
        OR NEW.basename IS DISTINCT FROM OLD.basename
        OR NEW.format IS DISTINCT FROM OLD.format
    ) THEN
        RAISE EXCEPTION 'paired artifact identity fields are immutable' USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER media_artifacts_guard_paired_update
    BEFORE UPDATE ON media_artifacts
    FOR EACH ROW EXECUTE FUNCTION guard_paired_artifact_update();

CREATE TABLE reviews (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id uuid NOT NULL UNIQUE REFERENCES episode_tasks (id) ON DELETE CASCADE,
    decision text NOT NULL,
    notes text NOT NULL DEFAULT '',
    reviewed_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
    reviewed_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT reviews_decision_valid CHECK (decision IN ('approved', 'rejected'))
);

CREATE TABLE imports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id uuid NOT NULL REFERENCES episode_tasks (id) ON DELETE CASCADE,
    attempt integer NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'queued',
    destination_video_path text,
    destination_subtitle_path text,
    error_code text,
    error_message text,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT imports_attempt_positive CHECK (attempt > 0),
    CONSTRAINT imports_status_valid CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT imports_success_paths_required CHECK (
        status <> 'succeeded'
        OR (
            destination_video_path IS NOT NULL
            AND btrim(destination_video_path) <> ''
            AND destination_subtitle_path IS NOT NULL
            AND btrim(destination_subtitle_path) <> ''
        )
    ),
    UNIQUE (task_id, attempt)
);

CREATE UNIQUE INDEX imports_one_success_per_task
    ON imports (task_id)
    WHERE status = 'succeeded';

CREATE TABLE cleanup_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id uuid NOT NULL REFERENCES episode_tasks (id) ON DELETE CASCADE,
    download_id uuid REFERENCES downloads (id) ON DELETE SET NULL,
    attempt integer NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'queued',
    torrent_removed boolean NOT NULL DEFAULT false,
    staged_files_removed boolean NOT NULL DEFAULT false,
    error_code text,
    error_message text,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT cleanup_runs_attempt_positive CHECK (attempt > 0),
    CONSTRAINT cleanup_runs_status_valid CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    UNIQUE (task_id, attempt)
);

CREATE TABLE operations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    resource_type text,
    resource_id uuid,
    idempotency_key text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'queued',
    river_job_id bigint,
    max_attempts integer NOT NULL DEFAULT 1,
    attempt_count integer NOT NULL DEFAULT 0,
    timeout_seconds integer NOT NULL,
    cancel_requested_at timestamptz,
    heartbeat_at timestamptz,
    error_code text,
    error_message text,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT operations_kind_not_blank CHECK (btrim(kind) <> ''),
    CONSTRAINT operations_resource_pair CHECK ((resource_type IS NULL) = (resource_id IS NULL)),
    CONSTRAINT operations_idempotency_key_not_blank CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT operations_status_valid CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT operations_max_attempts_positive CHECK (max_attempts > 0),
    CONSTRAINT operations_attempt_count_valid CHECK (attempt_count BETWEEN 0 AND max_attempts),
    CONSTRAINT operations_timeout_positive CHECK (timeout_seconds > 0),
    CONSTRAINT operations_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT operations_terminal_finished_at CHECK (
        (status IN ('queued', 'running') AND finished_at IS NULL)
        OR (status IN ('succeeded', 'failed', 'cancelled') AND finished_at IS NOT NULL)
    ),
    CONSTRAINT operations_failure_error_required CHECK (
        status <> 'failed'
        OR (
            error_code IS NOT NULL
            AND btrim(error_code) <> ''
            AND error_message IS NOT NULL
            AND btrim(error_message) <> ''
        )
    )
);

CREATE INDEX operations_pending_idx
    ON operations (status, created_at, id)
    WHERE status IN ('queued', 'running');

CREATE INDEX operations_resource_idx
    ON operations (resource_type, resource_id, created_at DESC)
    WHERE resource_id IS NOT NULL;

CREATE TABLE operation_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    operation_id uuid NOT NULL REFERENCES operations (id) ON DELETE CASCADE,
    attempt integer NOT NULL,
    status text NOT NULL,
    worker_id text,
    error_code text,
    error_message text,
    started_at timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz,
    finished_at timestamptz,
    CONSTRAINT operation_attempts_attempt_positive CHECK (attempt > 0),
    CONSTRAINT operation_attempts_status_valid CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT operation_attempts_terminal_finished_at CHECK (
        (status = 'running' AND finished_at IS NULL)
        OR (status IN ('succeeded', 'failed', 'cancelled') AND finished_at IS NOT NULL)
    ),
    CONSTRAINT operation_attempts_failure_error_required CHECK (
        status <> 'failed'
        OR (
            error_code IS NOT NULL
            AND btrim(error_code) <> ''
            AND error_message IS NOT NULL
            AND btrim(error_message) <> ''
        )
    ),
    UNIQUE (operation_id, attempt)
);

CREATE TABLE events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_sequence bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    topic text NOT NULL,
    resource_type text,
    resource_id uuid,
    operation_id uuid REFERENCES operations (id) ON DELETE SET NULL,
    actor_user_id uuid REFERENCES admin_users (id) ON DELETE SET NULL,
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT events_topic_not_blank CHECK (btrim(topic) <> ''),
    CONSTRAINT events_resource_pair CHECK ((resource_type IS NULL) = (resource_id IS NULL)),
    CONSTRAINT events_data_object CHECK (jsonb_typeof(data) = 'object')
);

CREATE INDEX events_resource_sequence_idx
    ON events (resource_type, resource_id, event_sequence)
    WHERE resource_id IS NOT NULL;

CREATE INDEX events_topic_sequence_idx
    ON events (topic, event_sequence);
