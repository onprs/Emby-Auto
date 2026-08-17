CREATE TABLE rss_subscription_progress (
    subscription_id uuid PRIMARY KEY REFERENCES rss_subscriptions (id) ON DELETE CASCADE,
    overall_progress double precision NOT NULL DEFAULT 0,
    task_count integer NOT NULL DEFAULT 0,
    completed_task_count integer NOT NULL DEFAULT 0,
    attention_task_count integer NOT NULL DEFAULT 0,
    source_revision bigint NOT NULL DEFAULT 1,
    calculated_revision bigint NOT NULL DEFAULT 0,
    model_version integer NOT NULL DEFAULT 0,
    dirty boolean NOT NULL DEFAULT true,
    dirtied_transaction_id bigint NOT NULL DEFAULT txid_current(),
    dirtied_at timestamptz NOT NULL DEFAULT now(),
    calculated_at timestamptz,
    CONSTRAINT rss_subscription_progress_value_valid CHECK (
        overall_progress >= 0 AND overall_progress <= 1
    ),
    CONSTRAINT rss_subscription_progress_counts_nonnegative CHECK (
        task_count >= 0
        AND completed_task_count >= 0
        AND attention_task_count >= 0
        AND completed_task_count <= task_count
    ),
    CONSTRAINT rss_subscription_progress_revisions_valid CHECK (
        source_revision > 0
        AND calculated_revision >= 0
        AND calculated_revision <= source_revision
        AND model_version >= 0
    ),
    CONSTRAINT rss_subscription_progress_clean_state_valid CHECK (
        dirty
        OR (
            calculated_revision = source_revision
            AND model_version > 0
            AND calculated_at IS NOT NULL
        )
    )
);

CREATE INDEX rss_subscription_progress_sort_idx
    ON rss_subscription_progress (model_version, overall_progress, subscription_id)
    INCLUDE (task_count, completed_task_count, attention_task_count)
    WHERE NOT dirty;

CREATE INDEX rss_subscription_progress_model_version_idx
    ON rss_subscription_progress (model_version, subscription_id);

CREATE INDEX rss_subscription_progress_dirty_idx
    ON rss_subscription_progress (dirtied_at, subscription_id)
    WHERE dirty;

-- 业务写入只负责在同一事务内标记投影过期；唯一的 Go 重算入口继续
-- 复用公开 acquisition lifecycle 规则，并在提交前或 reconciliation 中写回。
CREATE FUNCTION mark_rss_subscription_progress_dirty(target_subscription_id uuid)
RETURNS void
LANGUAGE plpgsql
AS $function$
BEGIN
    IF target_subscription_id IS NULL THEN
        RETURN;
    END IF;

    INSERT INTO rss_subscription_progress (
        subscription_id,
        source_revision,
        calculated_revision,
        model_version,
        dirty,
        dirtied_transaction_id,
        dirtied_at
    )
    SELECT
        subscription.id,
        1,
        0,
        0,
        true,
        txid_current(),
        clock_timestamp()
    FROM rss_subscriptions AS subscription
    WHERE subscription.id = target_subscription_id
    ON CONFLICT (subscription_id) DO UPDATE
    SET source_revision = rss_subscription_progress.source_revision + 1,
        dirty = true,
        dirtied_transaction_id = txid_current(),
        dirtied_at = clock_timestamp();
END
$function$;

-- OLD/NEW 关联先解析成去重且有序的订阅集合，避免外键改绑时
-- 以相反顺序获取多个投影行锁，并避免普通 UPDATE 重复递增 revision。
CREATE FUNCTION mark_rss_subscription_progress_dirty_many(target_subscription_ids uuid[])
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    target_subscription_id uuid;
BEGIN
    FOR target_subscription_id IN
        SELECT DISTINCT candidate.subscription_id
        FROM unnest(target_subscription_ids) AS candidate(subscription_id)
        WHERE candidate.subscription_id IS NOT NULL
        ORDER BY candidate.subscription_id
    LOOP
        PERFORM mark_rss_subscription_progress_dirty(target_subscription_id);
    END LOOP;
END
$function$;

CREATE FUNCTION mark_rss_subscription_progress_for_entries(target_entry_ids uuid[])
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    target_subscription_id uuid;
BEGIN
    FOR target_subscription_id IN
        SELECT DISTINCT entry.subscription_id
        FROM rss_entries AS entry
        WHERE entry.id = ANY(target_entry_ids)
        ORDER BY entry.subscription_id
    LOOP
        PERFORM mark_rss_subscription_progress_dirty(target_subscription_id);
    END LOOP;
END
$function$;

CREATE FUNCTION mark_rss_subscription_progress_for_acquisitions(target_acquisition_ids uuid[])
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    target_subscription_id uuid;
BEGIN
    FOR target_subscription_id IN
        SELECT DISTINCT entry.subscription_id
        FROM acquisitions AS acquisition
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE acquisition.id = ANY(target_acquisition_ids)
        ORDER BY entry.subscription_id
    LOOP
        PERFORM mark_rss_subscription_progress_dirty(target_subscription_id);
    END LOOP;
END
$function$;

CREATE FUNCTION mark_rss_subscription_progress_for_downloads(target_download_ids uuid[])
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    target_subscription_id uuid;
BEGIN
    FOR target_subscription_id IN
        SELECT DISTINCT entry.subscription_id
        FROM downloads AS download
        JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE download.id = ANY(target_download_ids)
        ORDER BY entry.subscription_id
    LOOP
        PERFORM mark_rss_subscription_progress_dirty(target_subscription_id);
    END LOOP;
END
$function$;

CREATE FUNCTION mark_rss_subscription_progress_for_tasks(target_task_ids uuid[])
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    target_subscription_id uuid;
BEGIN
    FOR target_subscription_id IN
        SELECT DISTINCT entry.subscription_id
        FROM episode_tasks AS task
        JOIN acquisitions AS acquisition ON acquisition.id = task.acquisition_id
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE task.id = ANY(target_task_ids)
        ORDER BY entry.subscription_id
    LOOP
        PERFORM mark_rss_subscription_progress_dirty(target_subscription_id);
    END LOOP;
END
$function$;

CREATE FUNCTION mark_rss_subscription_progress_for_mapping_profiles(target_profile_ids uuid[])
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    target_subscription_id uuid;
BEGIN
    FOR target_subscription_id IN
        SELECT DISTINCT entry.subscription_id
        FROM acquisitions AS acquisition
        JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
        WHERE acquisition.mapping_profile_id = ANY(target_profile_ids)
        ORDER BY entry.subscription_id
    LOOP
        PERFORM mark_rss_subscription_progress_dirty(target_subscription_id);
    END LOOP;
END
$function$;

CREATE FUNCTION mark_rss_subscription_progress_from_change()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
    target_subscription_id uuid;
BEGIN
    CASE TG_TABLE_NAME
    WHEN 'rss_subscriptions' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.source_season IS NOT DISTINCT FROM OLD.source_season
           AND NEW.completed_at IS NOT DISTINCT FROM OLD.completed_at
           AND NEW.deleted_at IS NOT DISTINCT FROM OLD.deleted_at THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_dirty_many(ARRAY[NEW.id]);
        ELSIF TG_OP = 'UPDATE' THEN
            PERFORM mark_rss_subscription_progress_dirty_many(ARRAY[OLD.id, NEW.id]);
        END IF;
    WHEN 'rss_entries' THEN
        IF TG_OP = 'INSERT' AND NEW.imported_at IS NULL THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'UPDATE'
           AND NEW.subscription_id IS NOT DISTINCT FROM OLD.subscription_id
           AND NEW.source_season IS NOT DISTINCT FROM OLD.source_season
           AND NEW.source_episode IS NOT DISTINCT FROM OLD.source_episode
           AND NEW.imported_at IS NOT DISTINCT FROM OLD.imported_at THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_dirty_many(ARRAY[NEW.subscription_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_dirty_many(ARRAY[OLD.subscription_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_dirty_many(ARRAY[OLD.subscription_id, NEW.subscription_id]);
        END IF;
    WHEN 'acquisitions' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.rss_entry_id IS NOT DISTINCT FROM OLD.rss_entry_id
           AND NEW.series_id IS NOT DISTINCT FROM OLD.series_id
           AND NEW.mapping_profile_id IS NOT DISTINCT FROM OLD.mapping_profile_id
           AND NEW.deletion_requested_at IS NOT DISTINCT FROM OLD.deletion_requested_at THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_for_entries(ARRAY[NEW.rss_entry_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_for_entries(ARRAY[OLD.rss_entry_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_for_entries(ARRAY[OLD.rss_entry_id, NEW.rss_entry_id]);
        END IF;
    WHEN 'downloads' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.acquisition_id IS NOT DISTINCT FROM OLD.acquisition_id
           AND NEW.attempt IS NOT DISTINCT FROM OLD.attempt
           AND NEW.status IS NOT DISTINCT FROM OLD.status
           AND NEW.progress IS NOT DISTINCT FROM OLD.progress
           AND NEW.failure_stage IS NOT DISTINCT FROM OLD.failure_stage
           AND NEW.error_code IS NOT DISTINCT FROM OLD.error_code
           AND NEW.deleted_at IS NOT DISTINCT FROM OLD.deleted_at THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_for_acquisitions(ARRAY[NEW.acquisition_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_for_acquisitions(ARRAY[OLD.acquisition_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_for_acquisitions(ARRAY[OLD.acquisition_id, NEW.acquisition_id]);
        END IF;
    WHEN 'download_files' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.download_id IS NOT DISTINCT FROM OLD.download_id
           AND NEW.selected IS NOT DISTINCT FROM OLD.selected
           AND NEW.media_kind IS NOT DISTINCT FROM OLD.media_kind
           AND NEW.source_season IS NOT DISTINCT FROM OLD.source_season
           AND NEW.source_episode IS NOT DISTINCT FROM OLD.source_episode THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_for_downloads(ARRAY[NEW.download_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_for_downloads(ARRAY[OLD.download_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_for_downloads(ARRAY[OLD.download_id, NEW.download_id]);
        END IF;
    WHEN 'episode_tasks' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.acquisition_id IS NOT DISTINCT FROM OLD.acquisition_id
           AND NEW.source_video_file_id IS NOT DISTINCT FROM OLD.source_video_file_id
           AND NEW.mapping_id IS NOT DISTINCT FROM OLD.mapping_id
           AND NEW.state IS NOT DISTINCT FROM OLD.state
           AND NEW.video_state IS NOT DISTINCT FROM OLD.video_state
           AND NEW.subtitle_state IS NOT DISTINCT FROM OLD.subtitle_state
           AND NEW.failure_stage IS NOT DISTINCT FROM OLD.failure_stage THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_for_acquisitions(ARRAY[NEW.acquisition_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_for_acquisitions(ARRAY[OLD.acquisition_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_for_acquisitions(ARRAY[OLD.acquisition_id, NEW.acquisition_id]);
        END IF;
    WHEN 'artifact_sets' THEN
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[NEW.task_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[OLD.task_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[OLD.task_id, NEW.task_id]);
        END IF;
    WHEN 'reviews' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.task_id IS NOT DISTINCT FROM OLD.task_id
           AND NEW.decision IS NOT DISTINCT FROM OLD.decision THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[NEW.task_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[OLD.task_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[OLD.task_id, NEW.task_id]);
        END IF;
    WHEN 'imports' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.task_id IS NOT DISTINCT FROM OLD.task_id
           AND NEW.attempt IS NOT DISTINCT FROM OLD.attempt
           AND NEW.status IS NOT DISTINCT FROM OLD.status THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[NEW.task_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[OLD.task_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[OLD.task_id, NEW.task_id]);
        END IF;
    WHEN 'cleanup_runs' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.task_id IS NOT DISTINCT FROM OLD.task_id
           AND NEW.attempt IS NOT DISTINCT FROM OLD.attempt
           AND NEW.status IS NOT DISTINCT FROM OLD.status THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[NEW.task_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[OLD.task_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[OLD.task_id, NEW.task_id]);
        END IF;
    WHEN 'operations' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.id IS NOT DISTINCT FROM OLD.id
           AND NEW.kind IS NOT DISTINCT FROM OLD.kind
           AND NEW.resource_type IS NOT DISTINCT FROM OLD.resource_type
           AND NEW.resource_id IS NOT DISTINCT FROM OLD.resource_id
           AND NEW.status IS NOT DISTINCT FROM OLD.status
           AND NEW.created_at IS NOT DISTINCT FROM OLD.created_at THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            IF NEW.kind = 'emby.refresh' AND NEW.resource_type = 'episode_task' THEN
                PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[NEW.resource_id]);
            END IF;
        ELSIF TG_OP = 'DELETE' THEN
            IF OLD.kind = 'emby.refresh' AND OLD.resource_type = 'episode_task' THEN
                PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[OLD.resource_id]);
            END IF;
        ELSE
            IF (OLD.kind = 'emby.refresh' AND OLD.resource_type = 'episode_task')
               OR (NEW.kind = 'emby.refresh' AND NEW.resource_type = 'episode_task') THEN
                PERFORM mark_rss_subscription_progress_for_tasks(ARRAY[
                    CASE
                        WHEN OLD.kind = 'emby.refresh' AND OLD.resource_type = 'episode_task' THEN OLD.resource_id
                    END,
                    CASE
                        WHEN NEW.kind = 'emby.refresh' AND NEW.resource_type = 'episode_task' THEN NEW.resource_id
                    END
                ]);
            END IF;
        END IF;
    WHEN 'episode_mappings' THEN
        IF TG_OP = 'UPDATE'
           AND NEW.profile_id IS NOT DISTINCT FROM OLD.profile_id
           AND NEW.source_season IS NOT DISTINCT FROM OLD.source_season
           AND NEW.source_episode IS NOT DISTINCT FROM OLD.source_episode
           AND NEW.mapping_status IS NOT DISTINCT FROM OLD.mapping_status
           AND NEW.target_episode_id IS NOT DISTINCT FROM OLD.target_episode_id THEN
            RETURN NEW;
        END IF;
        IF TG_OP = 'INSERT' THEN
            PERFORM mark_rss_subscription_progress_for_mapping_profiles(ARRAY[NEW.profile_id]);
        ELSIF TG_OP = 'DELETE' THEN
            PERFORM mark_rss_subscription_progress_for_mapping_profiles(ARRAY[OLD.profile_id]);
        ELSE
            PERFORM mark_rss_subscription_progress_for_mapping_profiles(ARRAY[OLD.profile_id, NEW.profile_id]);
        END IF;
    WHEN 'media_series' THEN
        IF TG_OP = 'UPDATE' AND NEW.media_type IS DISTINCT FROM OLD.media_type THEN
            FOR target_subscription_id IN
                SELECT affected.subscription_id
                FROM (
                    SELECT subscription.id AS subscription_id
                    FROM rss_subscriptions AS subscription
                    WHERE subscription.series_id IN (OLD.id, NEW.id)
                    UNION
                    SELECT entry.subscription_id
                    FROM acquisitions AS acquisition
                    JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id
                    WHERE acquisition.series_id IN (OLD.id, NEW.id)
                ) AS affected
                ORDER BY affected.subscription_id
            LOOP
                PERFORM mark_rss_subscription_progress_dirty(target_subscription_id);
            END LOOP;
        END IF;
    END CASE;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$function$;

CREATE TRIGGER rss_subscription_progress_subscription_changes
    AFTER INSERT OR UPDATE OR DELETE ON rss_subscriptions
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_entry_changes
    AFTER INSERT OR UPDATE OR DELETE ON rss_entries
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_acquisition_changes
    AFTER INSERT OR UPDATE OR DELETE ON acquisitions
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_download_changes
    AFTER INSERT OR UPDATE OR DELETE ON downloads
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_download_file_changes
    AFTER INSERT OR UPDATE OR DELETE ON download_files
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_task_changes
    AFTER INSERT OR UPDATE OR DELETE ON episode_tasks
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_artifact_set_changes
    AFTER INSERT OR UPDATE OR DELETE ON artifact_sets
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_review_changes
    AFTER INSERT OR UPDATE OR DELETE ON reviews
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_import_changes
    AFTER INSERT OR UPDATE OR DELETE ON imports
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_cleanup_changes
    AFTER INSERT OR UPDATE OR DELETE ON cleanup_runs
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_operation_changes
    AFTER INSERT OR UPDATE OR DELETE ON operations
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_mapping_changes
    AFTER INSERT OR UPDATE OR DELETE ON episode_mappings
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();
CREATE TRIGGER rss_subscription_progress_media_series_changes
    AFTER UPDATE ON media_series
    FOR EACH ROW EXECUTE FUNCTION mark_rss_subscription_progress_from_change();

-- 历史数据只登记为待重算，避免在 migration 中复制 Go lifecycle；新版本
-- API/Worker 在对外服务前通过同一个版本化 reconciliation 完成 backfill。
INSERT INTO rss_subscription_progress (
    subscription_id,
    source_revision,
    calculated_revision,
    model_version,
    dirty,
    dirtied_transaction_id,
    dirtied_at
)
SELECT
    subscription.id,
    1,
    0,
    0,
    true,
    txid_current(),
    now()
FROM rss_subscriptions AS subscription
ON CONFLICT (subscription_id) DO NOTHING;
