CREATE TABLE acquisition_list_projections (
    acquisition_id uuid PRIMARY KEY REFERENCES acquisitions (id) ON DELETE CASCADE,
    list_visible boolean NOT NULL DEFAULT false,
    phases text[] NOT NULL DEFAULT '{}',
    overall_progress double precision NOT NULL DEFAULT 0,
    content_title text NOT NULL DEFAULT '',
    media_type text NOT NULL DEFAULT 'episode',
    release_year integer NOT NULL DEFAULT 0,
    source_season bigint NOT NULL DEFAULT 9223372036854775807,
    source_episode bigint NOT NULL DEFAULT 9223372036854775807,
    source_kind text NOT NULL DEFAULT '',
    resource_updated_at timestamptz NOT NULL DEFAULT now(),
    source_revision bigint NOT NULL DEFAULT 1,
    model_version integer NOT NULL DEFAULT 0,
    dirty boolean NOT NULL DEFAULT true,
    dirtied_transaction_id bigint NOT NULL DEFAULT txid_current(),
    dirtied_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (overall_progress >= 0 AND overall_progress <= 1),
    CHECK (source_revision > 0 AND model_version >= 0)
);

CREATE INDEX acquisition_list_updated_idx
    ON acquisition_list_projections (resource_updated_at, acquisition_id)
    WHERE NOT dirty AND list_visible;
CREATE INDEX acquisition_list_progress_idx
    ON acquisition_list_projections (overall_progress, acquisition_id)
    WHERE NOT dirty AND list_visible;
CREATE INDEX acquisition_list_content_idx
    ON acquisition_list_projections (
        content_title COLLATE "C", media_type COLLATE "C", release_year,
        source_season, source_episode, acquisition_id
    ) WHERE NOT dirty AND list_visible;
CREATE INDEX acquisition_list_source_idx
    ON acquisition_list_projections (source_kind COLLATE "C", acquisition_id)
    WHERE NOT dirty AND list_visible;
CREATE INDEX acquisition_list_phases_idx
    ON acquisition_list_projections USING gin (phases) WHERE NOT dirty AND list_visible;
CREATE INDEX acquisition_list_attention_idx
    ON acquisition_list_projections (resource_updated_at, acquisition_id) WHERE NOT dirty AND list_visible AND phases @> ARRAY['attention'];
CREATE INDEX acquisition_list_dirty_idx
    ON acquisition_list_projections (acquisition_id) WHERE dirty;
CREATE INDEX acquisition_list_transaction_idx
    ON acquisition_list_projections (dirtied_transaction_id, acquisition_id) WHERE dirty;
CREATE INDEX acquisition_list_version_idx
    ON acquisition_list_projections (model_version, acquisition_id);

CREATE TABLE rss_entry_list_projections (
    entry_id uuid PRIMARY KEY REFERENCES rss_entries (id) ON DELETE CASCADE,
    subscription_id uuid NOT NULL REFERENCES rss_subscriptions (id) ON DELETE CASCADE,
    title_key text NOT NULL DEFAULT '',
    entry_group text NOT NULL DEFAULT 'confirmed',
    reject_reasons text[] NOT NULL DEFAULT '{}',
    overall_progress double precision NOT NULL DEFAULT 0,
    source_season bigint NOT NULL DEFAULT 9223372036854775807,
    source_episode bigint NOT NULL DEFAULT 9223372036854775807,
    discovered_at timestamptz NOT NULL DEFAULT now(),
    source_revision bigint NOT NULL DEFAULT 1,
    model_version integer NOT NULL DEFAULT 0,
    dirty boolean NOT NULL DEFAULT true,
    dirtied_transaction_id bigint NOT NULL DEFAULT txid_current(),
    dirtied_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (overall_progress >= 0 AND overall_progress <= 1),
    CHECK (entry_group IN ('pending', 'confirmed', 'skipped')),
    CHECK (source_revision > 0 AND model_version >= 0)
);

CREATE INDEX rss_entry_list_discovered_idx
    ON rss_entry_list_projections (subscription_id, discovered_at, entry_id) WHERE NOT dirty;
CREATE INDEX rss_entry_list_title_idx
    ON rss_entry_list_projections (subscription_id, title_key COLLATE "C", entry_id) WHERE NOT dirty;
CREATE INDEX rss_entry_list_episode_idx
    ON rss_entry_list_projections (subscription_id, source_season, source_episode, entry_id) WHERE NOT dirty;
CREATE INDEX rss_entry_list_progress_idx
    ON rss_entry_list_projections (subscription_id, overall_progress, entry_id) WHERE NOT dirty;
CREATE INDEX rss_entry_list_reasons_idx
    ON rss_entry_list_projections USING gin (reject_reasons) WHERE NOT dirty;
CREATE INDEX rss_entry_list_dirty_idx
    ON rss_entry_list_projections (entry_id) WHERE dirty;
CREATE INDEX rss_entry_list_transaction_idx
    ON rss_entry_list_projections (dirtied_transaction_id, entry_id) WHERE dirty;
CREATE INDEX rss_entry_list_version_idx
    ON rss_entry_list_projections (model_version, entry_id);

-- 业务写入先锁相关订阅汇总行，再更新父子列表投影；锁定不改变汇总状态。
CREATE FUNCTION lock_list_projection_subscriptions(
    target_acquisition_ids uuid[], target_entry_ids uuid[], target_subscription_ids uuid[]
)
RETURNS void LANGUAGE plpgsql AS $function$
BEGIN
    PERFORM progress.subscription_id
    FROM rss_subscription_progress AS progress
    WHERE progress.subscription_id IN (
        SELECT entry.subscription_id
        FROM acquisitions AS acquisition
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE acquisition.id = ANY(target_acquisition_ids)
        UNION
        SELECT entry.subscription_id FROM rss_entries AS entry WHERE entry.id = ANY(target_entry_ids)
        UNION
        SELECT candidate.id FROM unnest(target_subscription_ids) AS candidate(id)
    )
    ORDER BY progress.subscription_id
    FOR UPDATE OF progress;
END
$function$;

-- 同一事务对同一资源的重复写入合并为一次失效，提交前由 Go 批量重算。
CREATE FUNCTION mark_acquisition_list_projections_dirty(target_ids uuid[])
RETURNS void LANGUAGE plpgsql AS $function$
BEGIN
    INSERT INTO acquisition_list_projections (acquisition_id)
    SELECT acquisition.id
    FROM acquisitions AS acquisition
    WHERE acquisition.id = ANY(target_ids)
    ORDER BY acquisition.id
    ON CONFLICT (acquisition_id) DO UPDATE
    SET source_revision = acquisition_list_projections.source_revision + 1,
        dirty = true,
        dirtied_transaction_id = txid_current(),
        dirtied_at = clock_timestamp()
    WHERE NOT acquisition_list_projections.dirty
       OR acquisition_list_projections.dirtied_transaction_id <> txid_current();
END
$function$;

CREATE FUNCTION mark_rss_entry_list_projections_dirty(target_ids uuid[])
RETURNS void LANGUAGE plpgsql AS $function$
BEGIN
    -- 所有路径遵循父获取任务、子条目投影的锁顺序。
    PERFORM projection.acquisition_id
    FROM acquisition_list_projections AS projection
    JOIN acquisitions AS acquisition ON acquisition.id = projection.acquisition_id
    WHERE acquisition.rss_entry_id = ANY(target_ids)
    ORDER BY projection.acquisition_id
    FOR UPDATE OF projection;

    INSERT INTO rss_entry_list_projections (entry_id, subscription_id)
    SELECT entry.id, entry.subscription_id
    FROM rss_entries AS entry
    WHERE entry.id = ANY(target_ids)
    ORDER BY entry.id
    ON CONFLICT (entry_id) DO UPDATE
    SET source_revision = rss_entry_list_projections.source_revision + 1,
        dirty = true,
        dirtied_transaction_id = txid_current(),
        dirtied_at = clock_timestamp()
    WHERE NOT rss_entry_list_projections.dirty
       OR rss_entry_list_projections.dirtied_transaction_id <> txid_current();
END
$function$;

CREATE FUNCTION mark_acquisition_list_projection_from_change()
RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE
    previous jsonb := CASE WHEN TG_OP <> 'INSERT' THEN to_jsonb(OLD) ELSE '{}'::jsonb END;
    current_row jsonb := CASE WHEN TG_OP <> 'DELETE' THEN to_jsonb(NEW) ELSE '{}'::jsonb END;
    target_ids uuid[] := '{}';
    related_ids uuid[];
BEGIN
    CASE TG_TABLE_NAME
    WHEN 'acquisitions' THEN
        target_ids := ARRAY[(previous->>'id')::uuid, (current_row->>'id')::uuid];
    WHEN 'downloads', 'episode_tasks' THEN
        target_ids := ARRAY[(previous->>'acquisition_id')::uuid, (current_row->>'acquisition_id')::uuid];
    WHEN 'download_files' THEN
        SELECT array_agg(download.acquisition_id) INTO target_ids
        FROM downloads AS download
        WHERE download.id = ANY(ARRAY[(previous->>'download_id')::uuid, (current_row->>'download_id')::uuid]);
    WHEN 'episode_mapping_profiles', 'episode_mappings' THEN
        related_ids := CASE WHEN TG_TABLE_NAME = 'episode_mapping_profiles'
            THEN ARRAY[(previous->>'id')::uuid, (current_row->>'id')::uuid]
            ELSE ARRAY[(previous->>'profile_id')::uuid, (current_row->>'profile_id')::uuid] END;
        SELECT array_agg(acquisition.id) INTO target_ids
        FROM acquisitions AS acquisition WHERE acquisition.mapping_profile_id = ANY(related_ids);
    WHEN 'media_series' THEN
        SELECT array_agg(acquisition.id) INTO target_ids
        FROM acquisitions AS acquisition
        WHERE acquisition.series_id = ANY(ARRAY[(previous->>'id')::uuid, (current_row->>'id')::uuid]);
    WHEN 'artifact_sets', 'reviews', 'imports', 'cleanup_runs' THEN
        SELECT array_agg(task.acquisition_id) INTO target_ids
        FROM episode_tasks AS task
        WHERE task.id = ANY(ARRAY[(previous->>'task_id')::uuid, (current_row->>'task_id')::uuid]);
    WHEN 'operations' THEN
        related_ids := ARRAY[
            CASE WHEN previous->>'kind' = 'emby.refresh' AND previous->>'resource_type' = 'episode_task'
                THEN (previous->>'resource_id')::uuid END,
            CASE WHEN current_row->>'kind' = 'emby.refresh' AND current_row->>'resource_type' = 'episode_task'
                THEN (current_row->>'resource_id')::uuid END
        ];
        SELECT array_agg(task.acquisition_id) INTO target_ids
        FROM episode_tasks AS task WHERE task.id = ANY(related_ids);
    ELSE
        RAISE EXCEPTION 'unsupported acquisition list source table: %', TG_TABLE_NAME;
    END CASE;
    PERFORM lock_list_projection_subscriptions(
        target_ids,
        CASE WHEN TG_TABLE_NAME = 'acquisitions'
            THEN ARRAY[(previous->>'rss_entry_id')::uuid, (current_row->>'rss_entry_id')::uuid]
            ELSE '{}'::uuid[] END,
        '{}'::uuid[]
    );
    IF TG_WHEN = 'BEFORE' THEN
        RETURN OLD;
    END IF;
    PERFORM mark_acquisition_list_projections_dirty(target_ids);
    IF TG_TABLE_NAME = 'acquisitions' THEN
        PERFORM mark_rss_entry_list_projections_dirty(
            ARRAY[(previous->>'rss_entry_id')::uuid, (current_row->>'rss_entry_id')::uuid]
        );
    END IF;
    RETURN NULL;
END
$function$;

CREATE FUNCTION mark_rss_entry_list_projection_from_change()
RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE
    previous jsonb := CASE WHEN TG_OP <> 'INSERT' THEN to_jsonb(OLD) ELSE '{}'::jsonb END;
    current_row jsonb := CASE WHEN TG_OP <> 'DELETE' THEN to_jsonb(NEW) ELSE '{}'::jsonb END;
    target_ids uuid[];
    subscription_ids uuid[] := '{}';
BEGIN
    CASE TG_TABLE_NAME
    WHEN 'rss_entries' THEN
        target_ids := ARRAY[(previous->>'id')::uuid, (current_row->>'id')::uuid];
        subscription_ids := ARRAY[(previous->>'subscription_id')::uuid, (current_row->>'subscription_id')::uuid];
    WHEN 'rss_entry_adjudications' THEN
        target_ids := ARRAY[(previous->>'entry_id')::uuid, (current_row->>'entry_id')::uuid];
    WHEN 'rss_acquisition_provenance' THEN
        target_ids := ARRAY[(previous->>'rss_entry_id')::uuid, (current_row->>'rss_entry_id')::uuid];
    WHEN 'acquisition_list_projections' THEN
        target_ids := ARRAY[
            (SELECT acquisition.rss_entry_id FROM acquisitions AS acquisition
             WHERE acquisition.id = (current_row->>'acquisition_id')::uuid)
        ];
    ELSE
        RAISE EXCEPTION 'unsupported RSS entry list source table: %', TG_TABLE_NAME;
    END CASE;
    -- 补算已持有父投影锁，派生写回沿父到子的顺序推进。
    IF TG_TABLE_NAME <> 'acquisition_list_projections' THEN
        PERFORM lock_list_projection_subscriptions('{}'::uuid[], target_ids, subscription_ids);
    END IF;
    IF TG_WHEN = 'BEFORE' THEN
        RETURN OLD;
    END IF;
    PERFORM mark_rss_entry_list_projections_dirty(target_ids);
    RETURN NULL;
END
$function$;

DO $block$
DECLARE source_table text;
BEGIN
    FOREACH source_table IN ARRAY ARRAY[
        'acquisitions', 'downloads', 'download_files', 'episode_tasks',
        'episode_mapping_profiles', 'episode_mappings', 'media_series',
        'artifact_sets', 'reviews', 'imports', 'cleanup_runs', 'operations'
    ] LOOP
        EXECUTE format(
            'CREATE TRIGGER list_projection_change AFTER INSERT OR UPDATE OR DELETE ON %I '
            'FOR EACH ROW EXECUTE FUNCTION mark_acquisition_list_projection_from_change()', source_table
        );
    END LOOP;
    FOREACH source_table IN ARRAY ARRAY['rss_entries', 'rss_entry_adjudications', 'rss_acquisition_provenance'] LOOP
        EXECUTE format(
            'CREATE TRIGGER list_projection_change AFTER INSERT OR UPDATE OR DELETE ON %I '
            'FOR EACH ROW EXECUTE FUNCTION mark_rss_entry_list_projection_from_change()', source_table
        );
    END LOOP;
END
$block$;

-- 在外键级联删除投影之前取得订阅锁。
CREATE TRIGGER list_projection_delete_lock BEFORE DELETE ON acquisitions
    FOR EACH ROW EXECUTE FUNCTION mark_acquisition_list_projection_from_change();
CREATE TRIGGER list_projection_delete_lock BEFORE DELETE ON rss_entries
    FOR EACH ROW EXECUTE FUNCTION mark_rss_entry_list_projection_from_change();

CREATE TRIGGER rss_entry_list_projection_change
    AFTER INSERT OR UPDATE ON acquisition_list_projections
    FOR EACH ROW EXECUTE FUNCTION mark_rss_entry_list_projection_from_change();

-- 历史数据只标记待重算，由启动恢复流程按固定大小批次生成索引字段。
INSERT INTO acquisition_list_projections (acquisition_id)
SELECT id FROM acquisitions ORDER BY id;
INSERT INTO rss_entry_list_projections (entry_id, subscription_id)
SELECT id, subscription_id FROM rss_entries ORDER BY id
ON CONFLICT (entry_id) DO NOTHING;
