ALTER TABLE agent_resolutions
    DROP CONSTRAINT agent_resolutions_capability_valid,
    ADD CONSTRAINT agent_resolutions_capability_valid CHECK (
        capability IN (
            'rss_coordinate',
            'rss_release_adjudication',
            'rss_preacquisition_mapping',
            'download_file_resolution',
            'catalog_candidate',
            'episode_mapping',
            'subtitle_video_match'
        )
    );

CREATE TABLE subtitle_video_match_scopes (
    id uuid PRIMARY KEY,
    task_id uuid NOT NULL REFERENCES episode_tasks (id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending',
    selected_candidate_id text,
    agent_resolution_id uuid REFERENCES agent_resolutions (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT subtitle_video_match_scopes_status_valid CHECK (status IN ('pending', 'applied', 'expired')),
    CONSTRAINT subtitle_video_match_scopes_applied_consistent CHECK (
        (status = 'applied' AND selected_candidate_id IS NOT NULL AND agent_resolution_id IS NOT NULL)
        OR (status <> 'applied' AND selected_candidate_id IS NULL AND agent_resolution_id IS NULL)
    ),
    CONSTRAINT subtitle_video_match_scopes_task_unique UNIQUE (task_id)
);

CREATE INDEX subtitle_video_match_scopes_pending_idx
    ON subtitle_video_match_scopes (task_id)
    WHERE status = 'pending';

CREATE TABLE subtitle_video_match_candidates (
    scope_id uuid NOT NULL REFERENCES subtitle_video_match_scopes (id) ON DELETE CASCADE,
    candidate_id text NOT NULL,
    source text NOT NULL,
    stream_index integer,
    format text,
    language text,
    title text,
    path text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope_id, candidate_id),
    CONSTRAINT subtitle_video_match_candidates_source_valid CHECK (source IN ('embedded', 'external')),
    CONSTRAINT subtitle_video_match_candidates_id_not_blank CHECK (btrim(candidate_id) <> ''),
    CONSTRAINT subtitle_video_match_candidates_stream_index_consistent CHECK (
        (source = 'embedded' AND stream_index IS NOT NULL AND stream_index >= 0)
        OR (source = 'external' AND stream_index IS NULL)
    )
);