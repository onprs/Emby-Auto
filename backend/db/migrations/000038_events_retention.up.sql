-- 支持按保留期批量清理过期事件：以 occurred_at 过滤 + event_sequence 排序分批删除。
CREATE INDEX events_occurred_at_sequence_idx
    ON events (occurred_at, event_sequence);
