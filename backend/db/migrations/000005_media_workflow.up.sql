CREATE UNIQUE INDEX media_artifacts_one_kind_per_task
    ON media_artifacts (task_id, kind);

CREATE INDEX download_files_selected_media_idx
    ON download_files (download_id, media_kind, source_season, source_episode, file_index)
    WHERE selected;
