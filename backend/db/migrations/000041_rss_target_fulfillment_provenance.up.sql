CREATE TABLE rss_target_fulfillments (
    rss_entry_id uuid NOT NULL REFERENCES rss_entries (id) ON DELETE CASCADE,
    target_episode_id uuid NOT NULL REFERENCES media_episodes (id) ON DELETE RESTRICT,
    source text NOT NULL,
    task_id uuid REFERENCES episode_tasks (id) ON DELETE SET NULL,
    verified_at timestamptz NOT NULL DEFAULT now(),
    invalidated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rss_target_fulfillments_source_valid CHECK (
        source IN ('managed_import', 'emby_catalog')
    ),
    PRIMARY KEY (rss_entry_id, target_episode_id, source)
);

CREATE INDEX rss_target_fulfillments_target_idx
    ON rss_target_fulfillments (target_episode_id, source);

-- 优先按成功任务的实际映射锁定历史入库目标，避免后续 profile 变更重解释。
INSERT INTO rss_target_fulfillments (
    rss_entry_id,
    target_episode_id,
    source,
    task_id,
    verified_at
)
SELECT DISTINCT ON (entry.id, mapping.target_episode_id)
    entry.id,
    mapping.target_episode_id,
    'managed_import',
    task.id,
    COALESCE(import_record.completed_at, import_record.updated_at, entry.imported_at, now())
FROM rss_entries AS entry
JOIN acquisitions AS acquisition ON acquisition.rss_entry_id = entry.id
JOIN episode_tasks AS task ON task.acquisition_id = acquisition.id
JOIN episode_mappings AS mapping ON mapping.id = task.mapping_id
JOIN imports AS import_record ON import_record.task_id = task.id AND import_record.status = 'succeeded'
WHERE entry.imported_at IS NOT NULL
  AND entry.fulfillment_source = 'managed_import'
  AND mapping.mapping_status = 'mapped'
ORDER BY entry.id, mapping.target_episode_id, import_record.completed_at DESC NULLS LAST, task.id;

-- 仅回填可由成功 task mapping 证明的历史目标。已清理任务或缺少实际映射的
-- v40 汇总字段无法证明目标，保留原字段用于兼容显示，但不制造 target provenance。
