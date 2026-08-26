package legacymigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var eventTopicPartRegexp = regexp.MustCompile(`[^a-z0-9_.-]+`)

type ApplyOptions struct {
	RunID     uuid.UUID
	ProfileID uuid.UUID
	FailAfter int
}

type ApplyResult struct {
	Counts Counts
}

type ApplyError struct {
	Code    string
	Message string
	Cause   error
}

func (err *ApplyError) Error() string {
	if err.Cause != nil {
		return err.Message + ": " + err.Cause.Error()
	}
	return err.Message
}

func (err *ApplyError) Unwrap() error { return err.Cause }

func Apply(ctx context.Context, pool *pgxpool.Pool, plan Plan, options ApplyOptions) (result ApplyResult, returnErr error) {
	result.Counts.Discovered = plan.Counts.Discovered
	result.Counts.PlannedTasks = plan.Counts.PlannedTasks
	result.Counts.PlannedRSS = plan.Counts.PlannedRSS
	result.Counts.Skipped = plan.Counts.Skipped
	result.Counts.Invalid = plan.Counts.Invalid
	if pool == nil {
		return result, &ApplyError{Code: "target_database_required", Message: "target database is required"}
	}
	if options.RunID == uuid.Nil || options.ProfileID == uuid.Nil {
		return result, &ApplyError{Code: "migration_option_invalid", Message: "run ID and transcode profile ID are required"}
	}
	countsPayload, _ := json.Marshal(plan.Counts)
	if _, err := pool.Exec(ctx, `
INSERT INTO legacy_migration_runs (id, source_kind, source_fingerprint, status, counts)
VALUES ($1, $2, $3, 'running', $4)
`, options.RunID, plan.SourceKind, plan.Fingerprint, countsPayload); err != nil {
		return result, &ApplyError{Code: "migration_run_create_failed", Message: "create migration run", Cause: err}
	}
	defer func() {
		if returnErr == nil {
			return
		}
		result.Counts.Imported = 0
		result.Counts.ArtifactPairs = 0
		result.Counts.Events = 0
		code, message := migrationErrorDetails(returnErr)
		failureCounts, _ := json.Marshal(result.Counts)
		_, _ = pool.Exec(context.Background(), `
UPDATE legacy_migration_runs
SET status = 'failed', counts = $2, error_code = $3, error_message = $4, completed_at = now()
WHERE id = $1 AND status = 'running'
`, options.RunID, failureCounts, code, message)
	}()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return result, &ApplyError{Code: "target_transaction_failed", Message: "begin target transaction", Cause: err}
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('emby-auto:legacy-migration'))"); err != nil {
		return result, &ApplyError{Code: "migration_lock_failed", Message: "acquire migration lock", Cause: err}
	}
	var videoCodec, container, profileExtension, audioPolicy, audioCodec string
	if err := tx.QueryRow(ctx, `
SELECT video_codec, container, file_extension, audio_policy, COALESCE(audio_codec, '')
FROM transcode_profiles
WHERE id = $1 AND active
`, options.ProfileID).Scan(&videoCodec, &container, &profileExtension, &audioPolicy, &audioCodec); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return result, &ApplyError{Code: "transcode_profile_unavailable", Message: "selected transcode profile does not exist or is inactive"}
		}
		return result, &ApplyError{Code: "transcode_profile_lookup_failed", Message: "load transcode profile", Cause: err}
	}
	targetProfile, err := NewArtifactProfile(videoCodec, container, profileExtension, audioPolicy, audioCodec)
	if err != nil {
		return result, &ApplyError{Code: "transcode_profile_invalid", Message: "selected transcode profile cannot validate legacy artifacts", Cause: err}
	}
	if profileExtension != plan.ProfileExtension || !artifactProfilesEqual(targetProfile, plan.ArtifactProfile) {
		return result, &ApplyError{Code: "transcode_profile_mismatch", Message: "selected transcode profile differs from the verified migration plan"}
	}

	itemsByID := make(map[uuid.UUID]PlannedItem, len(plan.Items))
	for _, item := range plan.Items {
		itemsByID[item.ID] = item
	}
	processedItems := make(map[uuid.UUID]struct{}, len(plan.Items))
	processedCount := 0
	for _, task := range plan.Tasks {
		item := itemsByID[task.ItemID]
		unchanged, err := existingMigrationItem(ctx, tx, item)
		if err != nil {
			return result, err
		}
		if unchanged {
			result.Counts.Unchanged++
			processedItems[item.ID] = struct{}{}
			continue
		}
		if err := applyTask(ctx, tx, task, options.ProfileID, plan.SourceKind); err != nil {
			return result, &ApplyError{Code: "task_import_failed", Message: "import legacy task " + item.LegacyID, Cause: err}
		}
		if err := insertMigrationItem(ctx, tx, options.RunID, item); err != nil {
			return result, err
		}
		result.Counts.Imported++
		if task.ArtifactSetID != uuid.Nil {
			result.Counts.ArtifactPairs++
		}
		result.Counts.Events += len(task.History)
		processedItems[item.ID] = struct{}{}
		processedCount++
		if options.FailAfter > 0 && processedCount >= options.FailAfter {
			return result, &ApplyError{Code: "injected_failure", Message: fmt.Sprintf("injected failure after %d new items", processedCount)}
		}
	}
	for _, subscription := range plan.Subscriptions {
		item := itemsByID[subscription.ItemID]
		unchanged, err := existingMigrationItem(ctx, tx, item)
		if err != nil {
			return result, err
		}
		if unchanged {
			result.Counts.Unchanged++
			processedItems[item.ID] = struct{}{}
			continue
		}
		resourceID, err := applySubscription(ctx, tx, subscription, plan.SourceKind)
		if err != nil {
			return result, &ApplyError{Code: "rss_subscription_import_failed", Message: "import legacy RSS subscription " + item.LegacyID, Cause: err}
		}
		item.ResourceID = resourceID
		if err := insertMigrationItem(ctx, tx, options.RunID, item); err != nil {
			return result, err
		}
		result.Counts.Imported++
		result.Counts.Events++
		processedItems[item.ID] = struct{}{}
		processedCount++
		if options.FailAfter > 0 && processedCount >= options.FailAfter {
			return result, &ApplyError{Code: "injected_failure", Message: fmt.Sprintf("injected failure after %d new items", processedCount)}
		}
	}
	for _, item := range plan.Items {
		if _, exists := processedItems[item.ID]; exists {
			continue
		}
		unchanged, err := existingMigrationItem(ctx, tx, item)
		if err != nil {
			return result, err
		}
		if unchanged {
			result.Counts.Unchanged++
			continue
		}
		if err := insertMigrationItem(ctx, tx, options.RunID, item); err != nil {
			return result, err
		}
		processedCount++
		if options.FailAfter > 0 && processedCount >= options.FailAfter {
			return result, &ApplyError{Code: "injected_failure", Message: fmt.Sprintf("injected failure after %d new items", processedCount)}
		}
	}
	finalCounts, _ := json.Marshal(result.Counts)
	if _, err := tx.Exec(ctx, `
UPDATE legacy_migration_runs
SET status = 'completed', counts = $2, completed_at = now()
WHERE id = $1 AND status = 'running'
`, options.RunID, finalCounts); err != nil {
		return result, &ApplyError{Code: "migration_run_complete_failed", Message: "complete migration run", Cause: err}
	}
	if err := tx.Commit(ctx); err != nil {
		return result, &ApplyError{Code: "migration_commit_failed", Message: "commit migration transaction", Cause: err}
	}
	return result, nil
}

func existingMigrationItem(ctx context.Context, tx pgx.Tx, item PlannedItem) (bool, error) {
	var existing []byte
	var status, resourceType, resourceID string
	err := tx.QueryRow(ctx, `
SELECT fingerprint, status, COALESCE(resource_type, ''), COALESCE(resource_id::text, '')
FROM legacy_migration_items
WHERE source_kind = $1 AND legacy_id = $2
FOR UPDATE
`, item.SourceKind, item.LegacyID).Scan(&existing, &status, &resourceType, &resourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, &ApplyError{Code: "migration_item_lookup_failed", Message: "look up existing migration item", Cause: err}
	}
	if !bytes.Equal(existing, item.Fingerprint) {
		return false, &ApplyError{Code: "legacy_record_changed", Message: "legacy record changed after a prior successful migration: " + item.LegacyID}
	}
	if status != "imported" {
		return true, nil
	}
	var exists bool
	switch resourceType {
	case "episode_task":
		err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM episode_tasks WHERE id::text = $1)", resourceID).Scan(&exists)
	case "rss_subscription":
		err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM rss_subscriptions WHERE id::text = $1)", resourceID).Scan(&exists)
	default:
		return false, &ApplyError{Code: "migrated_resource_invalid", Message: "migration audit references an unsupported resource type"}
	}
	if err != nil {
		return false, &ApplyError{Code: "migrated_resource_lookup_failed", Message: "verify previously migrated resource", Cause: err}
	}
	if !exists {
		return false, &ApplyError{Code: "migrated_resource_missing", Message: "a previously migrated resource is missing: " + item.LegacyID}
	}
	return true, nil
}

func applyTask(ctx context.Context, tx pgx.Tx, task PlannedTask, profileID uuid.UUID, sourceKind string) error {
	seriesID, err := upsertSeries(ctx, tx, task.SeriesID, task.SeriesKey, task.TMDbSeriesID, task.SeriesTitle, task.Payload, task.CreatedAt, task.UpdatedAt)
	if err != nil {
		return err
	}
	if err := insertTMDbCatalog(ctx, tx, seriesID, task.SeriesKey, task.Payload, task.CreatedAt, task.UpdatedAt); err != nil {
		return err
	}
	mappingProfileID := uuid.Nil
	mappingID := uuid.Nil
	if task.MappingID != uuid.Nil {
		mappingProfileID, mappingID, err = insertMappingGraph(ctx, tx, task, seriesID)
		if err != nil {
			return err
		}
	}
	legacyPayload := map[string]any{"legacySource": sourceKind, "legacyPayload": task.Payload}
	payloadJSON, err := json.Marshal(legacyPayload)
	if err != nil {
		return err
	}
	var acquisitionMapping any
	if mappingProfileID != uuid.Nil {
		acquisitionMapping = mappingProfileID
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO acquisitions (
    id, series_id, mapping_profile_id, source_kind, source_uri, source_payload, legacy_id, created_at, updated_at
) VALUES ($1, $2, $3, 'manual', $4, $5, $6, $7, $8)
ON CONFLICT (id) DO NOTHING
`, task.AcquisitionID, seriesID, acquisitionMapping, "legacy://"+task.AcquisitionID.String(), payloadJSON, "legacy-acquisition:"+task.AcquisitionKey, task.CreatedAt, task.UpdatedAt); err != nil {
		return err
	}
	var torrentHash any
	if task.TorrentHash != "" {
		torrentHash = task.TorrentHash
	}
	var savePath any
	if task.SavePath != "" {
		savePath = task.SavePath
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO downloads (
    id, acquisition_id, attempt, client_name, torrent_hash, status, progress, save_path,
    started_at, completed_at, created_at, updated_at
) VALUES ($1, $2, 1, 'qbittorrent', $3, 'materialized', 1, $4, $5, $6, $5, $6)
ON CONFLICT (id) DO NOTHING
`, task.DownloadID, task.AcquisitionID, torrentHash, savePath, task.CreatedAt, task.UpdatedAt); err != nil {
		return err
	}
	var sourceSeason, sourceEpisode any
	if task.SourceSeason > 0 {
		sourceSeason = task.SourceSeason
	}
	if task.SourceEpisode > 0 {
		sourceEpisode = task.SourceEpisode
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind, selected,
    source_season, source_episode, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, 'video', true, $6, $7, $8, $9)
ON CONFLICT (id) DO NOTHING
`, task.SourceFileID, task.DownloadID, task.SourceFileIndex, task.SourceRelativePath, task.SourceFileSize, sourceSeason, sourceEpisode, task.CreatedAt, task.UpdatedAt); err != nil {
		return err
	}
	var mapping any
	if mappingID != uuid.Nil {
		mapping = mappingID
	}
	var errorCode, errorMessage, failureStage any
	if task.ErrorCode != "" {
		errorCode, errorMessage = task.ErrorCode, task.ErrorMessage
	}
	if task.TaskState == "failed" {
		switch {
		case task.VideoState == "failed":
			failureStage = "video"
		case task.SubtitleState == "failed":
			failureStage = "subtitle"
		default:
			failureStage = "finalize"
		}
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO episode_tasks (
    id, acquisition_id, source_video_file_id, mapping_id, transcode_profile_id,
    state, video_state, subtitle_state, version, failure_stage, error_code, error_message, legacy_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $10, $11, $12, $13, $14)
ON CONFLICT (id) DO NOTHING
`, task.TaskID, task.AcquisitionID, task.SourceFileID, mapping, profileID, task.TaskState, task.VideoState, task.SubtitleState, failureStage, errorCode, errorMessage, task.LegacyID, task.CreatedAt, task.UpdatedAt); err != nil {
		return err
	}
	if task.ArtifactSetID != uuid.Nil {
		metadata, _ := json.Marshal(map[string]any{"legacyMigration": true})
		if _, err := tx.Exec(ctx, `
INSERT INTO media_artifacts (
    id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path,
    format, size_bytes, checksum_sha256, metadata, created_at
) VALUES ($1, $2, $3, $4, 'video', $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO NOTHING
`, task.VideoArtifactID, task.TaskID, task.SourceFileID, profileID, task.BaseName, task.VideoPath, task.VideoFormat, task.VideoSize, task.VideoChecksum, metadata, task.UpdatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO media_artifacts (
    id, task_id, source_file_id, transcode_profile_id, kind, basename, file_path,
    format, size_bytes, checksum_sha256, metadata, created_at
) VALUES ($1, $2, $3, NULL, 'subtitle', $4, $5, 'ass', $6, $7, $8, $9)
ON CONFLICT (id) DO NOTHING
`, task.SubtitleArtifactID, task.TaskID, task.SourceFileID, task.BaseName, task.SubtitlePath, task.SubtitleSize, task.SubtitleChecksum, metadata, task.UpdatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO artifact_sets (
    id, task_id, transcode_profile_id, basename, video_artifact_id, subtitle_artifact_id, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO NOTHING
`, task.ArtifactSetID, task.TaskID, profileID, task.BaseName, task.VideoArtifactID, task.SubtitleArtifactID, task.UpdatedAt); err != nil {
			return err
		}
	}
	if task.ReviewID != uuid.Nil {
		if _, err := tx.Exec(ctx, `
INSERT INTO reviews (
    id, task_id, decision, notes, reviewed_at, idempotency_key, expected_task_version
) VALUES ($1, $2, $3, $4, $5, $6, 1)
ON CONFLICT (id) DO NOTHING
`, task.ReviewID, task.TaskID, task.ReviewDecision, task.ReviewNotes, task.ReviewedAt, "legacy-review:"+task.LegacyID); err != nil {
			return err
		}
	}
	if task.ImportID != uuid.Nil {
		if _, err := tx.Exec(ctx, `
INSERT INTO imports (
    id, task_id, attempt, status, destination_video_path, destination_subtitle_path,
    started_at, completed_at, created_at, updated_at
) VALUES ($1, $2, 1, 'succeeded', $3, $4, $5, $5, $5, $5)
ON CONFLICT (id) DO NOTHING
`, task.ImportID, task.TaskID, task.LibraryVideoPath, task.LibrarySubtitlePath, task.ImportedAt); err != nil {
			return err
		}
	}
	for index, event := range task.History {
		data, err := json.Marshal(map[string]any{"legacyPayload": event, "legacyIndex": index})
		if err != nil {
			return err
		}
		occurredAt := nonzeroTime(
			parseLegacyTime(textFrom(event, "occurred_at", "created_at", "timestamp", "time", "at")),
			task.UpdatedAt,
		)
		topic := legacyEventTopic(event)
		if _, err := tx.Exec(ctx, `
INSERT INTO events (id, topic, resource_type, resource_id, data, occurred_at)
VALUES ($1, $2, 'episode_task', $3, $4, $5)
ON CONFLICT (id) DO NOTHING
`, deterministicID("event", sourceKind, task.LegacyID, fmt.Sprintf("%d", index)), topic, task.TaskID, data, occurredAt); err != nil {
			return err
		}
	}
	return nil
}

func insertMappingGraph(ctx context.Context, tx pgx.Tx, task PlannedTask, seriesID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	profileID, err := upsertLegacyMappingProfile(ctx, tx, task.MappingProfileID, seriesID, task.CreatedAt)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	metadata := legacyTMDbMetadata(task.Payload)
	seasonID, err := upsertLegacySeason(ctx, tx, task.SeasonID, seriesID, task.TargetSeason, task.TargetEpisode, metadata, task.CreatedAt, task.UpdatedAt)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	episodeID, err := upsertLegacyEpisode(ctx, tx, task.EpisodeID, seasonID, task.TargetEpisode, task.EpisodeTitle, nil, metadata, task.CreatedAt, task.UpdatedAt)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	var mappingID uuid.UUID
	var targetEpisodeID *uuid.UUID
	if err := tx.QueryRow(ctx, `
INSERT INTO episode_mappings (
    id, profile_id, source_season, source_episode, target_episode_id,
    mapping_status, match_source, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, 'mapped', 'explicit', $6, $7)
ON CONFLICT (profile_id, source_season, source_episode, source_episode_fraction_hundredths) DO UPDATE SET
    updated_at = episode_mappings.updated_at
RETURNING id, target_episode_id
`, task.MappingID, profileID, task.SourceSeason, task.SourceEpisode, episodeID, task.CreatedAt, task.UpdatedAt).Scan(&mappingID, &targetEpisodeID); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if targetEpisodeID == nil || *targetEpisodeID != episodeID {
		return uuid.Nil, uuid.Nil, fmt.Errorf("legacy mapping conflicts with an existing source coordinate")
	}
	return profileID, mappingID, nil
}

func upsertLegacyMappingProfile(ctx context.Context, tx pgx.Tx, plannedID, seriesID uuid.UUID, createdAt time.Time) (uuid.UUID, error) {
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active, created_at, decision_source)
VALUES ($1, $2, 'legacy-migration', 1, NULL, false, $3, 'legacy')
ON CONFLICT (series_id, name, version) DO UPDATE SET
    active = episode_mapping_profiles.active
RETURNING id
`, plannedID, seriesID, createdAt).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func upsertLegacySeason(
	ctx context.Context,
	tx pgx.Tx,
	plannedID, seriesID uuid.UUID,
	seasonNumber, episodeCount int,
	metadata []byte,
	createdAt, updatedAt time.Time,
) (uuid.UUID, error) {
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
INSERT INTO tmdb_seasons (
    id, series_id, season_number, name, episode_count, fetched_at, upstream_payload, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (series_id, season_number) DO UPDATE SET
    episode_count = GREATEST(tmdb_seasons.episode_count, EXCLUDED.episode_count),
    updated_at = GREATEST(tmdb_seasons.updated_at, EXCLUDED.updated_at)
RETURNING id
`, plannedID, seriesID, seasonNumber, fmt.Sprintf("Season %d", seasonNumber), episodeCount, updatedAt, metadata, createdAt, updatedAt).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func upsertLegacyEpisode(
	ctx context.Context,
	tx pgx.Tx,
	plannedID, seasonID uuid.UUID,
	episodeNumber int,
	title string,
	airDate any,
	metadata []byte,
	createdAt, updatedAt time.Time,
) (uuid.UUID, error) {
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
INSERT INTO media_episodes (
    id, season_id, episode_number, title, air_date, upstream_payload, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (season_id, episode_number) DO UPDATE SET
    updated_at = media_episodes.updated_at
RETURNING id
`, plannedID, seasonID, episodeNumber, title, airDate, metadata, createdAt, updatedAt).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func applySubscription(ctx context.Context, tx pgx.Tx, subscription PlannedSubscription, sourceKind string) (uuid.UUID, error) {
	seriesID, err := upsertSeries(ctx, tx, subscription.SeriesID, subscription.SeriesKey, subscription.TMDbSeriesID, subscription.SeriesTitle, subscription.Payload, subscription.CreatedAt, subscription.UpdatedAt)
	if err != nil {
		return uuid.Nil, err
	}
	if err := insertTMDbCatalog(ctx, tx, seriesID, subscription.SeriesKey, subscription.Payload, subscription.CreatedAt, subscription.UpdatedAt); err != nil {
		return uuid.Nil, err
	}
	mappingProfileID, err := upsertLegacyMappingProfile(ctx, tx, subscription.MappingProfileID, seriesID, subscription.CreatedAt)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	var storedSourceSeason int
	err = tx.QueryRow(ctx, `
INSERT INTO rss_subscriptions (
    id, series_id, mapping_profile_id, name, feed_url, source_season, enabled, poll_interval_seconds,
    next_poll_at, version, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, 900, $8, 1, $9, $10)
ON CONFLICT (series_id, feed_url) WHERE deleted_at IS NULL DO UPDATE SET
    updated_at = rss_subscriptions.updated_at
RETURNING id, source_season
`, subscription.SubscriptionID, seriesID, mappingProfileID, subscription.Name, subscription.FeedURL, subscription.SourceSeason, subscription.Enabled, subscription.UpdatedAt, subscription.CreatedAt, subscription.UpdatedAt).Scan(&id, &storedSourceSeason)
	if err != nil {
		return uuid.Nil, err
	}
	if storedSourceSeason != subscription.SourceSeason {
		return uuid.Nil, fmt.Errorf("legacy RSS source season conflicts with an existing subscription")
	}
	data, _ := json.Marshal(map[string]any{"legacyPayload": subscription.Payload, "legacySource": sourceKind})
	if _, err := tx.Exec(ctx, `
INSERT INTO events (id, topic, resource_type, resource_id, data, occurred_at)
VALUES ($1, 'legacy.rss_subscription.migrated', 'rss_subscription', $2, $3, $4)
ON CONFLICT (id) DO NOTHING
`, deterministicID("event", sourceKind, "rss", subscription.LegacyID), id, data, subscription.UpdatedAt); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func upsertSeries(ctx context.Context, tx pgx.Tx, plannedID uuid.UUID, seriesKey string, tmdbID int64, title string, legacyPayload map[string]any, createdAt, updatedAt time.Time) (uuid.UUID, error) {
	legacyID := "legacy-series:" + seriesKey
	var id uuid.UUID
	var err error
	if tmdbID > 0 {
		err = tx.QueryRow(ctx, "SELECT id FROM media_series WHERE tmdb_series_id = $1", tmdbID).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, "SELECT id FROM media_series WHERE legacy_id = $1", legacyID).Scan(&id)
	}
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	metadata := legacyTMDbMetadata(legacyPayload)
	var tmdb any
	if tmdbID > 0 {
		tmdb = tmdbID
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO media_series (id, tmdb_series_id, title, media_type, metadata, legacy_id, created_at, updated_at)
VALUES ($1, $2, $3, 'tv', $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    updated_at = GREATEST(media_series.updated_at, EXCLUDED.updated_at)
RETURNING id
`, plannedID, tmdb, title, metadata, legacyID, createdAt, updatedAt).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func insertTMDbCatalog(ctx context.Context, tx pgx.Tx, seriesID uuid.UUID, seriesKey string, payload map[string]any, createdAt, updatedAt time.Time) error {
	tmdb := objectValue(payload["tmdb"])
	raw := objectValue(tmdb["raw"])
	episodes := objectSlice(tmdb["episodes"])
	if len(episodes) == 0 {
		episodes = objectSlice(raw["episodes"])
	}
	seasonNumber, seasonPresent := firstInteger(tmdb["hinted_season"], raw["season"], payload["tmdb_season"])
	if !seasonPresent || seasonNumber < 0 || len(episodes) == 0 {
		return nil
	}
	maxEpisode := 0
	validEpisodes := make([]map[string]any, 0, len(episodes))
	for _, episode := range episodes {
		number := positiveInt(episode["episode_number"])
		title := textFrom(episode, "name", "title")
		if number <= 0 || title == "" {
			continue
		}
		if number > maxEpisode {
			maxEpisode = number
		}
		validEpisodes = append(validEpisodes, episode)
	}
	if len(validEpisodes) == 0 {
		return nil
	}
	seasonID, err := upsertLegacySeason(
		ctx, tx, deterministicID("season", seriesKey, fmt.Sprintf("%d", seasonNumber)), seriesID,
		seasonNumber, maxEpisode, legacyTMDbMetadata(payload), createdAt, updatedAt,
	)
	if err != nil {
		return err
	}
	for _, episode := range validEpisodes {
		number := positiveInt(episode["episode_number"])
		var airDate any
		if value := textFrom(episode, "air_date"); value != "" {
			if parsed, err := time.Parse("2006-01-02", value); err == nil {
				airDate = parsed
			}
		}
		episodePayload, _ := json.Marshal(map[string]any{"legacyMigration": true, "legacyPayload": episode})
		if _, err := upsertLegacyEpisode(
			ctx, tx,
			deterministicID("episode", seriesKey, fmt.Sprintf("%d", seasonNumber), fmt.Sprintf("%d", number)),
			seasonID, number, textFrom(episode, "name", "title"), airDate, episodePayload, createdAt, updatedAt,
		); err != nil {
			return err
		}
	}
	return nil
}

func legacyTMDbMetadata(payload map[string]any) []byte {
	metadata := map[string]any{"legacyMigration": true}
	if tmdb := objectValue(payload["tmdb"]); len(tmdb) > 0 {
		metadata["legacyPayload"] = tmdb
	}
	encoded, _ := json.Marshal(metadata)
	return encoded
}

func insertMigrationItem(ctx context.Context, tx pgx.Tx, runID uuid.UUID, item PlannedItem) error {
	payload, err := json.Marshal(item.Payload)
	if err != nil {
		return &ApplyError{Code: "legacy_payload_encode_failed", Message: "encode legacy payload", Cause: err}
	}
	var resourceType, resourceID, errorCode, errorMessage any
	if item.ResourceType != "" && item.ResourceID != uuid.Nil {
		resourceType, resourceID = item.ResourceType, item.ResourceID
	}
	if item.ErrorCode != "" {
		errorCode, errorMessage = item.ErrorCode, item.ErrorMessage
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO legacy_migration_items (
    id, source_kind, legacy_id, fingerprint, migration_run_id, status,
    resource_type, resource_id, error_code, error_message, legacy_payload
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
`, item.ID, item.SourceKind, item.LegacyID, item.Fingerprint, runID, item.Status, resourceType, resourceID, errorCode, errorMessage, payload); err != nil {
		return &ApplyError{Code: "migration_item_create_failed", Message: "record migrated legacy item " + item.LegacyID, Cause: err}
	}
	return nil
}

func legacyEventTopic(event map[string]any) string {
	part := strings.ToLower(firstText(
		textFrom(event, "event", "type", "action", "status", "stage"),
		"history",
	))
	part = strings.Trim(eventTopicPartRegexp.ReplaceAllString(part, "_"), "_.-")
	if part == "" {
		part = "history"
	}
	return "legacy.task." + part
}

func migrationErrorDetails(err error) (string, string) {
	var migrationErr *ApplyError
	if errors.As(err, &migrationErr) {
		return migrationErr.Code, migrationErr.Message
	}
	return "migration_failed", "legacy migration failed"
}
