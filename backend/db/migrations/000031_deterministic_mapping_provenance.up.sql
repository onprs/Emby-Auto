ALTER TABLE episode_mapping_profiles
    DROP CONSTRAINT episode_mapping_profiles_decision_source_consistent,
    DROP CONSTRAINT episode_mapping_profiles_decision_source_valid,
    ADD CONSTRAINT episode_mapping_profiles_decision_source_valid CHECK (
        decision_source IN ('deterministic', 'user', 'agent_auto', 'agent_accepted', 'legacy')
    ),
    ADD CONSTRAINT episode_mapping_profiles_decision_source_consistent CHECK (
        (decision_source = 'deterministic' AND created_by IS NULL AND agent_resolution_id IS NULL)
        OR (decision_source = 'user' AND created_by IS NOT NULL AND agent_resolution_id IS NULL)
        OR (decision_source = 'agent_auto' AND created_by IS NULL AND agent_resolution_id IS NOT NULL)
        OR (decision_source = 'agent_accepted' AND created_by IS NOT NULL AND agent_resolution_id IS NOT NULL)
        OR (decision_source = 'legacy' AND agent_resolution_id IS NULL)
    );

ALTER TABLE episode_mapping_saves
    DROP CONSTRAINT episode_mapping_saves_decision_source_consistent,
    DROP CONSTRAINT episode_mapping_saves_decision_source_valid,
    ADD CONSTRAINT episode_mapping_saves_decision_source_valid CHECK (
        decision_source IN ('deterministic', 'user', 'agent_auto', 'agent_accepted')
    ),
    ADD CONSTRAINT episode_mapping_saves_decision_source_consistent CHECK (
        (decision_source = 'deterministic' AND created_by IS NULL AND agent_resolution_id IS NULL)
        OR (decision_source = 'user' AND created_by IS NOT NULL AND agent_resolution_id IS NULL)
        OR (decision_source = 'agent_auto' AND created_by IS NULL AND agent_resolution_id IS NOT NULL)
        OR (decision_source = 'agent_accepted' AND created_by IS NOT NULL AND agent_resolution_id IS NOT NULL)
    );
