\set ON_ERROR_STOP on

BEGIN;

INSERT INTO media_series (id, title)
VALUES ('10000000-0000-0000-0000-000000000001', 'Schema Test Series');

INSERT INTO rss_subscriptions (
    id,
    series_id,
    name,
    feed_url,
    source_season,
    enabled
) VALUES (
    '20000000-0000-0000-0000-000000000001',
    '10000000-0000-0000-0000-000000000001',
    'Schema Test Feed',
    'https://example.test/feed.xml',
    2,
    true
);

INSERT INTO rss_entries (
    id,
    subscription_id,
    identity_key,
    download_uri,
    title,
    downloadable,
    source_season,
    source_episode
) VALUES (
    '20000000-0000-0000-0000-000000000002',
    '20000000-0000-0000-0000-000000000001',
    'guid:schema-rss-entry',
    'https://example.test/show-01.torrent',
    'Show S02E01',
    true,
    2,
    1
);

DO $check$
BEGIN
    UPDATE rss_subscriptions
    SET enabled = false,
        next_poll_at = NULL,
        completed_at = now()
    WHERE id = '20000000-0000-0000-0000-000000000001';

    BEGIN
        UPDATE rss_subscriptions
        SET enabled = true,
            next_poll_at = now()
        WHERE id = '20000000-0000-0000-0000-000000000001';
        RAISE EXCEPTION 'completed RSS subscription was enabled';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END
$check$;

DO $check$
BEGIN
    BEGIN
        INSERT INTO rss_entries (
            subscription_id,
            identity_key,
            title,
            downloadable
        ) VALUES (
            '20000000-0000-0000-0000-000000000001',
            'guid:invalid-downloadable-entry',
            'Show S02E02',
            true
        );
        RAISE EXCEPTION 'downloadable RSS entry without URI and coordinates was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END
$check$;

INSERT INTO media_series (id, title)
VALUES ('10000000-0000-0000-0000-000000000014', 'Archived RSS Constraint Series');

INSERT INTO rss_subscriptions (id, series_id, name, feed_url, source_season, enabled)
VALUES (
    '20000000-0000-0000-0000-000000000014',
    '10000000-0000-0000-0000-000000000014',
    'First Active Feed',
    'https://example.test/recreatable.xml',
    1,
    true
);

DO $check$
BEGIN
    BEGIN
        INSERT INTO rss_subscriptions (series_id, name, feed_url, source_season, enabled)
        VALUES (
            '10000000-0000-0000-0000-000000000014',
            'Duplicate Active Feed',
            'https://example.test/recreatable.xml',
            1,
            true
        );
        RAISE EXCEPTION 'duplicate active RSS subscription was accepted';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
END
$check$;

UPDATE rss_subscriptions
SET enabled = false,
    next_poll_at = NULL,
    deleted_at = now()
WHERE id = '20000000-0000-0000-0000-000000000014';

INSERT INTO rss_subscriptions (id, series_id, name, feed_url, source_season, enabled)
VALUES (
    '20000000-0000-0000-0000-000000000015',
    '10000000-0000-0000-0000-000000000014',
    'Replacement Active Feed',
    'https://example.test/recreatable.xml',
    1,
    true
);

DO $check$
DECLARE
    total_count integer;
    active_count integer;
BEGIN
    SELECT count(*), count(*) FILTER (WHERE deleted_at IS NULL)
    INTO total_count, active_count
    FROM rss_subscriptions
    WHERE series_id = '10000000-0000-0000-0000-000000000014'
      AND feed_url = 'https://example.test/recreatable.xml';

    IF total_count <> 2 OR active_count <> 1 THEN
        RAISE EXCEPTION 'RSS archive/recreate counts are total %, active %', total_count, active_count;
    END IF;
END
$check$;

INSERT INTO search_runs (id, query, status, started_at, completed_at)
VALUES (
    '30000000-0000-0000-0000-000000000001',
    'Schema Test Search',
    'completed',
    now(),
    now()
);

INSERT INTO release_candidates (
    id,
    search_run_id,
    provider,
    identity_key,
    title,
    download_uri
) VALUES (
    '30000000-0000-0000-0000-000000000002',
    '30000000-0000-0000-0000-000000000001',
    'schema',
    'btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    'Schema Test Release',
    'magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
);

INSERT INTO acquisitions (
    id,
    series_id,
    source_kind,
    release_candidate_id
) VALUES (
    '30000000-0000-0000-0000-000000000003',
    '10000000-0000-0000-0000-000000000001',
    'search',
    '30000000-0000-0000-0000-000000000002'
);

DO $check$
BEGIN
    BEGIN
        INSERT INTO acquisitions (
            series_id,
            source_kind,
            release_candidate_id
        ) VALUES (
            '10000000-0000-0000-0000-000000000001',
            'search',
            '30000000-0000-0000-0000-000000000002'
        );
        RAISE EXCEPTION 'duplicate search candidate acquisition was accepted';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
END
$check$;

INSERT INTO acquisitions (id, series_id, source_kind, source_uri)
VALUES (
    '10000000-0000-0000-0000-000000000002',
    '10000000-0000-0000-0000-000000000001',
    'manual',
    'magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
);

INSERT INTO downloads (id, acquisition_id, torrent_hash, status)
VALUES (
    '10000000-0000-0000-0000-000000000003',
    '10000000-0000-0000-0000-000000000002',
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    'materialized'
);

INSERT INTO acquisitions (id, series_id, source_kind, source_uri)
VALUES
    (
        '10000000-0000-0000-0000-000000000016',
        '10000000-0000-0000-0000-000000000001',
        'manual',
        'magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    ),
    (
        '10000000-0000-0000-0000-000000000017',
        '10000000-0000-0000-0000-000000000001',
        'manual',
        'magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    );

INSERT INTO downloads (id, acquisition_id, torrent_hash, status)
VALUES
    (
        '10000000-0000-0000-0000-000000000018',
        '10000000-0000-0000-0000-000000000016',
        'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
        'cancelled'
    ),
    (
        '10000000-0000-0000-0000-000000000019',
        '10000000-0000-0000-0000-000000000017',
        'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB',
        'downloading'
    );

DO $check$
BEGIN
    BEGIN
        INSERT INTO downloads (acquisition_id, attempt, torrent_hash, status)
        VALUES (
            '10000000-0000-0000-0000-000000000016',
            2,
            'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
            'completed'
        );
        RAISE EXCEPTION 'a second active download reused an active torrent hash';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
END
$check$;

INSERT INTO download_files (
    id,
    download_id,
    file_index,
    relative_path,
    size_bytes,
    media_kind,
    selected,
    source_season,
    source_episode
) VALUES (
    '10000000-0000-0000-0000-000000000004',
    '10000000-0000-0000-0000-000000000003',
    0,
    'Series/Episode01.mkv',
    1048576,
    'video',
    true,
    1,
    1
);

INSERT INTO transcode_profiles (
    id,
    name,
    version,
    is_default,
    video_codec,
    encoder,
    container,
    file_extension,
    quality_mode,
    quality_value,
    audio_policy,
    preset,
    pixel_format,
    max_concurrency
) VALUES (
    '10000000-0000-0000-0000-000000000005',
    'schema-test',
    1,
    true,
    'hevc',
    'libx265',
    'matroska',
    'mkv',
    'crf',
    20,
    'copy',
    'medium',
    'yuv420p10le',
    1
);

INSERT INTO media_series (id, tmdb_movie_id, title, media_type, release_year)
VALUES (
    '11000000-0000-0000-0000-000000000001',
    54321,
    'Schema Test Movie',
    'movie',
    2024
);

DO $check$
BEGIN
    BEGIN
        INSERT INTO media_series (tmdb_movie_id, title, media_type)
        VALUES (54322, 'Movie Without Year', 'movie');
        RAISE EXCEPTION 'movie metadata without a release year was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END
$check$;

INSERT INTO acquisitions (id, series_id, source_kind, source_uri)
VALUES (
    '11000000-0000-0000-0000-000000000002',
    '11000000-0000-0000-0000-000000000001',
    'manual',
    'magnet:?xt=urn:btih:cccccccccccccccccccccccccccccccccccccccc'
);

INSERT INTO downloads (id, acquisition_id, torrent_hash, status)
VALUES (
    '11000000-0000-0000-0000-000000000003',
    '11000000-0000-0000-0000-000000000002',
    'cccccccccccccccccccccccccccccccccccccccc',
    'materialized'
);

INSERT INTO download_files (
    id,
    download_id,
    file_index,
    relative_path,
    size_bytes,
    media_kind,
    selected,
    source_season,
    source_episode
) VALUES (
    '11000000-0000-0000-0000-000000000004',
    '11000000-0000-0000-0000-000000000003',
    0,
    'Movie/source.mkv',
    2097152,
    'video',
    true,
    1,
    1
);

INSERT INTO episode_tasks (
    id,
    acquisition_id,
    source_video_file_id,
    mapping_id,
    transcode_profile_id,
    media_type
) VALUES (
    '11000000-0000-0000-0000-000000000005',
    '11000000-0000-0000-0000-000000000002',
    '11000000-0000-0000-0000-000000000004',
    NULL,
    '10000000-0000-0000-0000-000000000005',
    'movie'
);

DO $check$
BEGIN
    BEGIN
        INSERT INTO episode_tasks (
            acquisition_id,
            source_video_file_id,
            transcode_profile_id,
            media_type
        ) VALUES (
            '10000000-0000-0000-0000-000000000002',
            '10000000-0000-0000-0000-000000000004',
            '10000000-0000-0000-0000-000000000005',
            'documentary'
        );
        RAISE EXCEPTION 'unsupported task media type was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END
$check$;

INSERT INTO episode_tasks (
    id,
    acquisition_id,
    source_video_file_id,
    transcode_profile_id,
    state,
    video_state,
    subtitle_state
) VALUES (
    '10000000-0000-0000-0000-000000000006',
    '10000000-0000-0000-0000-000000000002',
    '10000000-0000-0000-0000-000000000004',
    '10000000-0000-0000-0000-000000000005',
    'awaiting_review',
    'video_ready',
    'ass_ready'
);

INSERT INTO media_artifacts (
    id,
    task_id,
    source_file_id,
    transcode_profile_id,
    kind,
    basename,
    file_path,
    format,
    size_bytes,
    checksum_sha256
) VALUES (
    '10000000-0000-0000-0000-000000000007',
    '10000000-0000-0000-0000-000000000006',
    '10000000-0000-0000-0000-000000000004',
    '10000000-0000-0000-0000-000000000005',
    'video',
    'Schema Test Series - S01E01 - Episode One',
    '/staging/Schema Test Series - S01E01 - Episode One.mkv',
    'matroska',
    1048576,
    decode(repeat('11', 32), 'hex')
), (
    '10000000-0000-0000-0000-000000000008',
    '10000000-0000-0000-0000-000000000006',
    NULL,
    NULL,
    'subtitle',
    'Schema Test Series - S01E01 - Episode One',
    '/staging/Schema Test Series - S01E01 - Episode One.ass',
    'ass',
    4096,
    decode(repeat('22', 32), 'hex')
);

DO $check$
BEGIN
    BEGIN
        INSERT INTO media_artifacts (
            task_id,
            source_file_id,
            transcode_profile_id,
            kind,
            basename,
            file_path,
            format,
            size_bytes,
            checksum_sha256
        ) VALUES (
            '10000000-0000-0000-0000-000000000006',
            '10000000-0000-0000-0000-000000000004',
            '10000000-0000-0000-0000-000000000005',
            'video',
            'Schema Test Series - S01E01 - Episode One',
            '/staging/duplicate-video.mkv',
            'matroska',
            2048,
            decode(repeat('33', 32), 'hex')
        );
        RAISE EXCEPTION 'second video artifact for one task was accepted';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
END
$check$;

INSERT INTO artifact_sets (
    id,
    task_id,
    transcode_profile_id,
    basename,
    video_artifact_id,
    subtitle_artifact_id
) VALUES (
    '10000000-0000-0000-0000-000000000009',
    '10000000-0000-0000-0000-000000000006',
    '10000000-0000-0000-0000-000000000005',
    'Schema Test Series - S01E01 - Episode One',
    '10000000-0000-0000-0000-000000000007',
    '10000000-0000-0000-0000-000000000008'
);

SET CONSTRAINTS artifact_sets_validate_pair IMMEDIATE;

INSERT INTO reviews (
    id,
    task_id,
    decision,
    notes,
    idempotency_key,
    expected_task_version
) VALUES (
    '10000000-0000-0000-0000-000000000014',
    '10000000-0000-0000-0000-000000000006',
    'approved',
    'schema review',
    'schema-review-idempotency-key',
    1
);

INSERT INTO imports (
    id,
    task_id,
    attempt,
    status
) VALUES (
    '10000000-0000-0000-0000-000000000015',
    '10000000-0000-0000-0000-000000000006',
    1,
    'queued'
);

INSERT INTO cleanup_runs (
    id,
    task_id,
    download_id,
    attempt,
    status
) VALUES (
    '10000000-0000-0000-0000-000000000016',
    '10000000-0000-0000-0000-000000000006',
    '10000000-0000-0000-0000-000000000003',
    1,
    'queued'
);

DO $check$
BEGIN
    BEGIN
        UPDATE artifact_sets
        SET basename = 'mismatched basename'
        WHERE id = '10000000-0000-0000-0000-000000000009';
        RAISE EXCEPTION 'artifact set mismatch was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        UPDATE media_artifacts
        SET format = 'srt'
        WHERE id = '10000000-0000-0000-0000-000000000008';
        RAISE EXCEPTION 'paired artifact mutation was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO transcode_profiles (
            name,
            version,
            video_codec,
            encoder,
            container,
            file_extension,
            quality_mode,
            quality_value,
            audio_policy,
            audio_codec,
            preset,
            pixel_format
        ) VALUES (
            'invalid-audio-profile',
            1,
            'h264',
            'libx264',
            'matroska',
            'mkv',
            'crf',
            20,
            'transcode',
            NULL,
            'medium',
            'yuv420p'
        );
        RAISE EXCEPTION 'transcode profile without audio codec was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO reviews (
            task_id,
            decision,
            idempotency_key,
            expected_task_version
        ) VALUES (
            '10000000-0000-0000-0000-000000000006',
            'approved',
            'schema-review-idempotency-key-duplicate',
            1
        );
        RAISE EXCEPTION 'second review for one task was accepted';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO imports (task_id, attempt, status)
        VALUES ('10000000-0000-0000-0000-000000000006', 2, 'queued');
        RAISE EXCEPTION 'second active import for one task was accepted';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO cleanup_runs (task_id, download_id, attempt, status)
        VALUES (
            '10000000-0000-0000-0000-000000000006',
            '10000000-0000-0000-0000-000000000003',
            2,
            'running'
        );
        RAISE EXCEPTION 'second active cleanup for one task was accepted';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO imports (task_id, attempt, status)
        VALUES ('10000000-0000-0000-0000-000000000006', 3, 'succeeded');
        RAISE EXCEPTION 'successful import without destination paths was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        UPDATE episode_tasks
        SET state = 'arbitrary_frontend_state'
        WHERE id = '10000000-0000-0000-0000-000000000006';
        RAISE EXCEPTION 'unknown task state was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO acquisitions (series_id, source_kind)
        VALUES ('10000000-0000-0000-0000-000000000001', 'manual');
        RAISE EXCEPTION 'manual acquisition without source URI was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO operations (
            kind,
            idempotency_key,
            status,
            max_attempts,
            timeout_seconds
        ) VALUES (
            'schema.invalid-failure',
            'schema-test-invalid-failure',
            'failed',
            1,
            60
        );
        RAISE EXCEPTION 'failed operation without error audit was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END
$check$;

INSERT INTO legacy_migration_runs (
    id,
    source_kind,
    source_fingerprint,
    status,
    counts
) VALUES (
    '70000000-0000-0000-0000-000000000001',
    'schema_test',
    decode(repeat('71', 32), 'hex'),
    'running',
    '{"discovered": 1}'::jsonb
);

INSERT INTO legacy_migration_items (
    id,
    source_kind,
    legacy_id,
    fingerprint,
    migration_run_id,
    status,
    resource_type,
    resource_id,
    legacy_payload
) VALUES (
    '70000000-0000-0000-0000-000000000002',
    'schema_test/task',
    'legacy-schema-task',
    decode(repeat('72', 32), 'hex'),
    '70000000-0000-0000-0000-000000000001',
    'imported',
    'episode_task',
    '10000000-0000-0000-0000-000000000006',
    '{"unknownField": "preserved"}'::jsonb
);

DO $check$
BEGIN
    BEGIN
        INSERT INTO legacy_migration_runs (
            id,
            source_kind,
            source_fingerprint
        ) VALUES (
            '70000000-0000-0000-0000-000000000003',
            'schema_test',
            decode('73', 'hex')
        );
        RAISE EXCEPTION 'migration run accepted a short fingerprint';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO legacy_migration_items (
            id,
            source_kind,
            legacy_id,
            fingerprint,
            migration_run_id,
            status,
            legacy_payload
        ) VALUES (
            '70000000-0000-0000-0000-000000000004',
            'schema_test/task',
            'missing-resource',
            decode(repeat('74', 32), 'hex'),
            '70000000-0000-0000-0000-000000000001',
            'imported',
            '{}'::jsonb
        );
        RAISE EXCEPTION 'imported migration item without a resource was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        UPDATE legacy_migration_runs
        SET status = 'failed',
            completed_at = now()
        WHERE id = '70000000-0000-0000-0000-000000000001';
        RAISE EXCEPTION 'failed migration run without error audit was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END
$check$;

INSERT INTO operations (
    id,
    kind,
    idempotency_key,
    max_attempts,
    timeout_seconds
) VALUES (
    '10000000-0000-0000-0000-000000000010',
    'schema.test',
    'schema-test-idempotency-key',
    3,
    60
);

INSERT INTO operations (
    id,
    kind,
    idempotency_key,
    max_attempts,
    timeout_seconds
) VALUES (
    '10000000-0000-0000-0000-000000000011',
    'schema.test',
    'schema-test-idempotency-key',
    3,
    60
)
ON CONFLICT (idempotency_key) DO NOTHING;

INSERT INTO events (id, topic, operation_id)
VALUES
    (
        '10000000-0000-0000-0000-000000000012',
        'schema.test.started',
        '10000000-0000-0000-0000-000000000010'
    ),
    (
        '10000000-0000-0000-0000-000000000013',
        'schema.test.completed',
        '10000000-0000-0000-0000-000000000010'
    );

DO $check$
DECLARE
    operation_count integer;
    first_sequence bigint;
    second_sequence bigint;
BEGIN
    SELECT count(*) INTO operation_count
    FROM operations
    WHERE idempotency_key = 'schema-test-idempotency-key';

    IF operation_count <> 1 THEN
        RAISE EXCEPTION 'idempotency key produced % operations', operation_count;
    END IF;

    SELECT event_sequence INTO first_sequence
    FROM events
    WHERE id = '10000000-0000-0000-0000-000000000012';

    SELECT event_sequence INTO second_sequence
    FROM events
    WHERE id = '10000000-0000-0000-0000-000000000013';

    IF second_sequence <= first_sequence THEN
        RAISE EXCEPTION 'event sequence did not increase';
    END IF;
END
$check$;

SELECT 'core schema constraints passed' AS result;

ROLLBACK;
