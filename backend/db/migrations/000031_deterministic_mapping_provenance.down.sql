DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM episode_mapping_saves WHERE decision_source = 'deterministic'
    ) OR EXISTS (
        SELECT 1 FROM episode_mapping_profiles WHERE decision_source = 'deterministic'
    ) THEN
        RAISE EXCEPTION 'cannot downgrade deterministic Mapping provenance while deterministic records exist';
    END IF;
END
$$;

ALTER TABLE episode_mapping_saves
    DROP CONSTRAINT episode_mapping_saves_decision_source_consistent,
    DROP CONSTRAINT episode_mapping_saves_decision_source_valid,
    ADD CONSTRAINT episode_mapping_saves_decision_source_valid CHECK (
        decision_source IN ('user', 'agent_auto', 'agent_accepted')
    ),
    ADD CONSTRAINT episode_mapping_saves_decision_source_consistent CHECK (
        (decision_source = 'user' AND created_by IS NOT NULL AND agent_resolution_id IS NULL)
        OR (decision_source = 'agent_auto' AND created_by IS NULL AND agent_resolution_id IS NOT NULL)
        OR (decision_source = 'agent_accepted' AND created_by IS NOT NULL AND agent_resolution_id IS NOT NULL)
    );

ALTER TABLE episode_mapping_profiles
    DROP CONSTRAINT episode_mapping_profiles_decision_source_consistent,
    DROP CONSTRAINT episode_mapping_profiles_decision_source_valid,
    ADD CONSTRAINT episode_mapping_profiles_decision_source_valid CHECK (
        decision_source IN ('user', 'agent_auto', 'agent_accepted', 'legacy')
    ),
    ADD CONSTRAINT episode_mapping_profiles_decision_source_consistent CHECK (
        (decision_source = 'user' AND created_by IS NOT NULL AND agent_resolution_id IS NULL)
        OR (decision_source = 'agent_auto' AND created_by IS NULL AND agent_resolution_id IS NOT NULL)
        OR (decision_source = 'agent_accepted' AND created_by IS NOT NULL AND agent_resolution_id IS NOT NULL)
        OR (decision_source = 'legacy' AND agent_resolution_id IS NULL)
    );
