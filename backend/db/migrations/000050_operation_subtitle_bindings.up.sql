CREATE TABLE operation_subtitle_bindings (
    operation_id uuid PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
    binding_version integer NOT NULL,
    phase text NOT NULL,
    attempt integer NOT NULL,
    attempt_id uuid NOT NULL,
    operation_attempt_count integer NOT NULL,
    binding jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT operation_subtitle_bindings_version_positive CHECK (binding_version > 0),
    CONSTRAINT operation_subtitle_bindings_phase_valid CHECK (phase IN ('started', 'begun', 'scope')),
    CONSTRAINT operation_subtitle_bindings_attempt_positive CHECK (attempt > 0),
    CONSTRAINT operation_subtitle_bindings_operation_attempt_count_positive CHECK (operation_attempt_count > 0),
    CONSTRAINT operation_subtitle_bindings_binding_object CHECK (jsonb_typeof(binding) = 'object')
);
