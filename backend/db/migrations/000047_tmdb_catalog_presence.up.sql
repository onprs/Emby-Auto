-- TMDb 目录以 present 表示上游当前是否仍返回该季/集。上游移除的季/集不再
-- 物理删除：episode_mappings、锚点和 rss_target_fulfillments 的 RESTRICT
-- 外键仍指向这些行，保留历史行可避免整次目录同步被外键回滚。
ALTER TABLE tmdb_seasons
    ADD COLUMN present boolean NOT NULL DEFAULT true;

ALTER TABLE media_episodes
    ADD COLUMN present boolean NOT NULL DEFAULT true;
