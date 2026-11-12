-- 历史列表与 dashboard 的排序键缺少支撑索引；随运行时间增长会退化为
-- 顺序扫描 + 排序。部分索引只覆盖实际读取的终态入库记录。
CREATE INDEX operations_created_at_idx
    ON operations (created_at DESC, id DESC);

CREATE INDEX operations_updated_at_idx
    ON operations (updated_at DESC, id DESC);

CREATE INDEX episode_tasks_updated_at_idx
    ON episode_tasks (updated_at DESC, id DESC);

CREATE INDEX imports_completed_at_idx
    ON imports (completed_at DESC, id DESC)
    WHERE status = 'succeeded';

CREATE INDEX operations_finished_at_idx
    ON operations (finished_at, id)
    WHERE status IN ('succeeded', 'failed', 'cancelled');

CREATE INDEX agent_resolutions_completed_at_idx
    ON agent_resolutions (completed_at, id)
    WHERE status IN ('failed', 'cancelled', 'expired');

-- 历史保留候选的单一事实来源：仅终态、且未被 RESTRICT 外键引用的行。
-- operations 仍被扫描历史或 Agent 决议引用时不得删除；events 的
-- operation_id 按既有 ON DELETE SET NULL 语义保留事件本身。
CREATE VIEW operation_retention_candidates AS
SELECT operation.id, operation.finished_at, operation.updated_at
FROM operations AS operation
WHERE operation.status IN ('succeeded', 'failed', 'cancelled')
  AND NOT EXISTS (
      SELECT 1 FROM agent_resolutions AS resolution
      WHERE resolution.operation_id = operation.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM emby_scan_runs AS scan
      WHERE scan.operation_id = operation.id
  );

-- 仅终态失败/取消/过期的 Agent 决议可清理；已被映射、保存、裁决、
-- 文件选择或字幕匹配引用的决议始终保留。
CREATE VIEW agent_resolution_retention_candidates AS
SELECT resolution.id, resolution.completed_at, resolution.updated_at
FROM agent_resolutions AS resolution
WHERE resolution.status IN ('failed', 'cancelled', 'expired')
  AND NOT EXISTS (
      SELECT 1 FROM episode_mapping_profiles AS profile
      WHERE profile.agent_resolution_id = resolution.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM episode_mapping_saves AS saved
      WHERE saved.agent_resolution_id = resolution.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM rss_entries AS entry
      WHERE entry.agent_resolution_id = resolution.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM downloads AS download
      WHERE download.agent_resolution_id = resolution.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM rss_entry_adjudications AS adjudication
      WHERE adjudication.resolution_id = resolution.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM subtitle_video_match_scopes AS scope
      WHERE scope.agent_resolution_id = resolution.id
  );
