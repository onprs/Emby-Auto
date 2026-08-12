//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func TestRSSSubscriptionMappingProfilePropagatesOnlyBeforeMaterializationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	workflow := NewRSSWorkflow(db.New(pool), transactor, nil)

	actorID, seriesID, seasonID := uuid.New(), uuid.New(), uuid.New()
	oldProfileID, newProfileID := uuid.New(), uuid.New()
	episodeIDs := []uuid.UUID{uuid.New(), uuid.New()}
	subscriptionID := uuid.New()
	pendingEntryID, protectedEntryID := uuid.New(), uuid.New()
	pendingAcquisitionID, protectedAcquisitionID := uuid.New(), uuid.New()
	downloadID, fileID, transcodeProfileID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-profile-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'RSS Profile Integration')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 2)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_episodes (id, season_id, episode_number, title)
VALUES ($1, $3, 1, 'Episode 1'), ($2, $3, 2, 'Episode 2')`, episodeIDs[0], episodeIDs[1], seasonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active, created_by, decision_source)
VALUES ($1, $3, 'old', 1, ARRAY[2], true, $4, 'user'),
       ($2, $3, 'new', 1, ARRAY[2], true, $4, 'user')`, oldProfileID, newProfileID, seriesID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, absolute_episode, target_episode_id, mapping_status, match_source)
VALUES ($1, $5, 1, 1, 1, $7, 'mapped', 'absolute'),
       ($2, $5, 1, 2, 2, $8, 'mapped', 'absolute'),
       ($3, $6, 1, 1, 1, $7, 'mapped', 'absolute'),
       ($4, $6, 1, 2, 2, $8, 'mapped', 'absolute')`, uuid.New(), uuid.New(), uuid.New(), uuid.New(), oldProfileID, newProfileID, episodeIDs[0], episodeIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, mapping_profile_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, $3, 'RSS Profile Integration', 'https://example.test/profile.xml', false, 900, 1)`, subscriptionID, seriesID, oldProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status)
VALUES ($1, $3, 'guid:pending', 'Pending E01', 'https://example.test/1.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued'),
       ($2, $3, 'guid:protected', 'Protected E02', 'https://example.test/2.torrent', true, ARRAY[]::text[], 1, 2, 'enqueued')`, pendingEntryID, protectedEntryID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, mapping_profile_id, source_kind, rss_entry_id)
VALUES ($1, $3, $4, 'rss', $5), ($2, $3, $4, 'rss', $6)`, pendingAcquisitionID, protectedAcquisitionID, seriesID, oldProfileID, pendingEntryID, protectedEntryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status) VALUES ($1, $2, 'materialized')`, downloadID, protectedAcquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Protected.S01E02.mkv', 1000, 'video', true, 1, 2)`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
  id, name, version, active, is_default, video_codec, encoder, container, file_extension,
  quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, false, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 20, 'copy', 'medium', 'yuv420p', 0, 1)`, transcodeProfileID, "rss-profile-"+transcodeProfileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id)
VALUES ($1, $2, $3, $4)`, taskID, protectedAcquisitionID, fileID, transcodeProfileID); err != nil {
		t.Fatal(err)
	}

	updated, err := workflow.UpdateSubscription(ctx, domain.UpdateRSSSubscription{
		ID: subscriptionID, ExpectedVersion: 1, Name: "RSS Profile Integration",
		FeedURL: "https://example.test/profile.xml", Enabled: false, AutoReview: true, PollInterval: 15 * time.Minute,
		SourceSeason: 1, MappingProfileID: newProfileID, ActorUserID: actorID,
	})
	if err != nil {
		t.Fatalf("UpdateSubscription() error = %v", err)
	}
	if updated.MappingProfileID != newProfileID || !updated.AutoReview {
		t.Fatalf("updated subscription profile/auto-review = %s/%t, want %s/true", updated.MappingProfileID, updated.AutoReview, newProfileID)
	}
	var pendingProfileID, protectedProfileID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT mapping_profile_id FROM acquisitions WHERE id = $1`, pendingAcquisitionID).Scan(&pendingProfileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT mapping_profile_id FROM acquisitions WHERE id = $1`, protectedAcquisitionID).Scan(&protectedProfileID); err != nil {
		t.Fatal(err)
	}
	if pendingProfileID != newProfileID || protectedProfileID != oldProfileID {
		t.Fatalf("acquisition profiles = pending %s protected %s, want %s/%s", pendingProfileID, protectedProfileID, newProfileID, oldProfileID)
	}
}

func TestCompletedRSSSubscriptionRejectsEnableAndPollIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	workflow := NewRSSWorkflow(db.New(pool), database.NewTransactor(pool), nil)
	seriesID, subscriptionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Completed RSS')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, completed_at)
VALUES ($1, $2, 'Completed RSS', 'https://example.test/completed.xml', false, 900, 1, now())
`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}

	_, err := workflow.UpdateSubscription(ctx, domain.UpdateRSSSubscription{
		ID: subscriptionID, ExpectedVersion: 1, Name: "Completed RSS", FeedURL: "https://example.test/completed.xml",
		Enabled: true, SourceSeason: 1, PollInterval: 15 * time.Minute,
	})
	var updateError *Error
	if !errors.As(err, &updateError) || updateError.Code != "rss_subscription_completed" || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("UpdateSubscription(enable completed) error = %#v", err)
	}

	_, err = workflow.ScheduleManualPoll(ctx, subscriptionID, "completed-poll", uuid.New())
	var pollError *Error
	if !errors.As(err, &pollError) || pollError.Code != "rss_subscription_completed" || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("ScheduleManualPoll(completed) error = %#v", err)
	}
	var enabled bool
	var version int
	if err := pool.QueryRow(ctx, `SELECT enabled, version FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&enabled, &version); err != nil {
		t.Fatal(err)
	}
	if enabled || version != 1 {
		t.Fatalf("completed subscription changed = enabled %t version %d", enabled, version)
	}
}

func TestCreateRSSSubscriptionAutomaticallySelectsUniqueCompatibleProfileIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	actorID, seriesID, profileID := uuid.New(), uuid.New(), uuid.New()
	tmdbSeriesID := time.Now().UnixNano()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-auto-profile-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Auto Profile Show')`, seriesID, tmdbSeriesID); err != nil {
		t.Fatal(err)
	}
	seedCompleteRSSMappingProfile(t, ctx, pool, actorID, seriesID, profileID, "Auto S01")

	created, err := workflow.CreateSubscription(ctx, domain.CreateRSSSubscription{
		TMDbSeriesID: tmdbSeriesID, SeriesTitle: "Auto Profile Show", Name: "Auto Profile RSS",
		FeedURL: "https://example.test/auto-profile.xml", Enabled: false, AutoEpisodeMapping: true, SourceSeason: 1,
		PollInterval: 15 * time.Minute, ActorUserID: actorID,
	})
	if err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	if created.MappingProfileID != profileID || !created.AutoEpisodeMapping {
		t.Fatalf("created mapping profile/policy = %s/%t, want %s/true", created.MappingProfileID, created.AutoEpisodeMapping, profileID)
	}
	var persistedProfileID uuid.UUID
	var persistedAutoMapping, autoSelected, eventAutoMapping bool
	if err := pool.QueryRow(ctx, `SELECT mapping_profile_id, auto_episode_mapping FROM rss_subscriptions WHERE id = $1`, created.ID).Scan(&persistedProfileID, &persistedAutoMapping); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT (data->>'mappingProfileAutoSelected')::boolean, (data->>'autoEpisodeMapping')::boolean
FROM events
WHERE resource_type = 'rss_subscription' AND resource_id = $1 AND topic = 'rss.subscription.created'`, created.ID).Scan(&autoSelected, &eventAutoMapping); err != nil {
		t.Fatal(err)
	}
	if persistedProfileID != profileID || !persistedAutoMapping || !autoSelected || !eventAutoMapping {
		t.Fatalf("persisted profile/policy/event = %s/%t/%t/%t, want %s/true/true/true", persistedProfileID, persistedAutoMapping, autoSelected, eventAutoMapping, profileID)
	}
}

func TestCreateRSSSubscriptionDoesNotGuessBetweenCompatibleProfilesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	actorID, seriesID := uuid.New(), uuid.New()
	tmdbSeriesID := time.Now().UnixNano()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-ambiguous-profile-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Ambiguous Profile Show')`, seriesID, tmdbSeriesID); err != nil {
		t.Fatal(err)
	}
	catalog := seedRSSMappingCatalog(t, ctx, pool, seriesID)
	seedRSSMappingProfile(t, ctx, pool, actorID, seriesID, uuid.New(), "Layout A", catalog)
	seedRSSMappingProfile(t, ctx, pool, actorID, seriesID, uuid.New(), "Layout B", catalog)

	created, err := workflow.CreateSubscription(ctx, domain.CreateRSSSubscription{
		TMDbSeriesID: tmdbSeriesID, SeriesTitle: "Ambiguous Profile Show", Name: "Ambiguous Profile RSS",
		FeedURL: "https://example.test/ambiguous-profile.xml", Enabled: false, SourceSeason: 1,
		PollInterval: 15 * time.Minute, ActorUserID: actorID,
	})
	if err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	if created.MappingProfileID != uuid.Nil {
		t.Fatalf("created mapping profile = %s, want no automatic selection", created.MappingProfileID)
	}
}

func TestCreateRSSSubscriptionRejectsIncompatibleExplicitProfileIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	workflow := NewRSSWorkflow(db.New(pool), database.NewTransactor(pool), nil)

	actorID, seriesID, otherSeriesID := uuid.New(), uuid.New(), uuid.New()
	otherProfileID, inactiveProfileID, incompleteProfileID := uuid.New(), uuid.New(), uuid.New()
	tmdbSeriesID := time.Now().UnixNano()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-invalid-profile-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_series (id, tmdb_series_id, title)
VALUES ($1, $3, 'Requested Show'), ($2, $4, 'Other Show')`, seriesID, otherSeriesID, tmdbSeriesID, tmdbSeriesID+1); err != nil {
		t.Fatal(err)
	}
	requestedCatalog := seedRSSMappingCatalog(t, ctx, pool, seriesID)
	seedCompleteRSSMappingProfile(t, ctx, pool, actorID, otherSeriesID, otherProfileID, "Other S01")
	seedRSSMappingProfile(t, ctx, pool, actorID, seriesID, inactiveProfileID, "Inactive S01", requestedCatalog)
	if _, err := pool.Exec(ctx, `UPDATE episode_mapping_profiles SET active = false WHERE id = $1`, inactiveProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active, created_by, decision_source)
VALUES ($1, $2, 'Incomplete S01', 1, ARRAY[2], true, $3, 'user')`, incompleteProfileID, seriesID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, absolute_episode, target_episode_id, mapping_status, match_source)
VALUES ($1, $2, 1, 1, 1, $3, 'mapped', 'absolute')`, uuid.New(), incompleteProfileID, requestedCatalog[0]); err != nil {
		t.Fatal(err)
	}

	for name, profileID := range map[string]uuid.UUID{
		"wrong series": otherProfileID,
		"inactive":     inactiveProfileID,
		"incomplete":   incompleteProfileID,
	} {
		t.Run(name, func(t *testing.T) {
			feedURL := "https://example.test/invalid-profile-" + profileID.String() + ".xml"
			_, err := workflow.CreateSubscription(ctx, domain.CreateRSSSubscription{
				TMDbSeriesID: tmdbSeriesID, SeriesTitle: "Requested Show", MappingProfileID: profileID,
				Name: "Invalid Profile RSS", FeedURL: feedURL, Enabled: false,
				SourceSeason: 1, PollInterval: 15 * time.Minute, ActorUserID: actorID,
			})
			var serviceError *Error
			if !errors.As(err, &serviceError) || serviceError.Code != "invalid_rss_subscription" || serviceError.Details["field"] != "mappingProfileId" {
				t.Fatalf("CreateSubscription() error = %#v, want mappingProfileId validation error", err)
			}
			var subscriptionCount int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_subscriptions WHERE feed_url = $1`, feedURL).Scan(&subscriptionCount); err != nil {
				t.Fatal(err)
			}
			if subscriptionCount != 0 {
				t.Fatalf("invalid profile subscription count = %d, want 0", subscriptionCount)
			}
		})
	}
}

func TestUpdateRSSSubscriptionAutoProfileRecoversMappingBlockedDownloadIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	actorID, seriesID, profileID := uuid.New(), uuid.New(), uuid.New()
	subscriptionID, entryID, acquisitionID := uuid.New(), uuid.New(), uuid.New()
	downloadID, fileID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-profile-recovery-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Recovery Profile Show')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	seedCompleteRSSMappingProfile(t, ctx, pool, actorID, seriesID, profileID, "Recovery S01")
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Recovery Profile RSS', 'https://example.test/profile-recovery.xml', false, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status)
VALUES ($1, $2, 'guid:profile-recovery', 'Recovery S01E01', 'https://example.test/recovery.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued')`, entryID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id) VALUES ($1, $2, 'rss', $3)`, acquisitionID, seriesID, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, torrent_hash, status, progress, failure_stage, error_code, error_message)
VALUES ($1, $2, '1111111111111111111111111111111111111111', 'failed', 1, 'materialize', 'mapping_profile_required', 'mapping is required')`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected, source_season, source_episode)
VALUES ($1, $2, 0, 'Recovery.S01E01.mkv', 1000, 'video', true, 1, 1)`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}

	updated, err := workflow.UpdateSubscription(ctx, domain.UpdateRSSSubscription{
		ID: subscriptionID, ExpectedVersion: 1, Name: "Recovery Profile RSS",
		FeedURL: "https://example.test/profile-recovery.xml", Enabled: false, AutoEpisodeMapping: true, SourceSeason: 1,
		PollInterval: 15 * time.Minute, ActorUserID: actorID,
	})
	if err != nil {
		t.Fatalf("UpdateSubscription() error = %v", err)
	}
	if updated.MappingProfileID != profileID || !updated.AutoEpisodeMapping {
		t.Fatalf("updated mapping profile/policy = %s/%t, want %s/true", updated.MappingProfileID, updated.AutoEpisodeMapping, profileID)
	}
	var acquisitionProfileID uuid.UUID
	var downloadStatus string
	var failureStage, errorCode *string
	var downloadVersion int
	if err := pool.QueryRow(ctx, `SELECT mapping_profile_id FROM acquisitions WHERE id = $1`, acquisitionID).Scan(&acquisitionProfileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status, failure_stage, error_code, version FROM downloads WHERE id = $1`, downloadID).Scan(&downloadStatus, &failureStage, &errorCode, &downloadVersion); err != nil {
		t.Fatal(err)
	}
	var operationCount, riverJobCount, mappingBackfillCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*), count(operation.river_job_id)
FROM operations AS operation
LEFT JOIN river_job AS job ON job.id = operation.river_job_id
WHERE operation.kind = 'download.materialize'
  AND operation.resource_type = 'download'
  AND operation.resource_id = $1
  AND operation.status = 'queued'
  AND job.kind = 'download.materialize'`, downloadID).Scan(&operationCount, &riverJobCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'tmdb.sync' AND resource_type = 'media_series' AND resource_id = $1 AND status = 'queued'`, seriesID).Scan(&mappingBackfillCount); err != nil {
		t.Fatal(err)
	}
	if acquisitionProfileID != profileID || downloadStatus != "completed" || failureStage != nil || errorCode != nil || downloadVersion != 2 {
		t.Fatalf("recovered state = profile %s status %s stage %v code %v version %d", acquisitionProfileID, downloadStatus, failureStage, errorCode, downloadVersion)
	}
	if operationCount != 1 || riverJobCount != 1 || mappingBackfillCount != 1 {
		t.Fatalf("materialize operation/jobs and Mapping backfill = %d/%d/%d, want 1/1/1", operationCount, riverJobCount, mappingBackfillCount)
	}
}

func seedCompleteRSSMappingProfile(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	actorID uuid.UUID,
	seriesID uuid.UUID,
	profileID uuid.UUID,
	name string,
) {
	t.Helper()
	catalog := seedRSSMappingCatalog(t, ctx, pool, seriesID)
	seedRSSMappingProfile(t, ctx, pool, actorID, seriesID, profileID, name, catalog)
}

func seedRSSMappingCatalog(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seriesID uuid.UUID) []uuid.UUID {
	t.Helper()
	seasonID := uuid.New()
	episodeIDs := []uuid.UUID{uuid.New(), uuid.New()}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 2)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_episodes (id, season_id, episode_number, title)
VALUES ($1, $3, 1, 'Episode 1'), ($2, $3, 2, 'Episode 2')`, episodeIDs[0], episodeIDs[1], seasonID); err != nil {
		t.Fatal(err)
	}
	return episodeIDs
}

func seedRSSMappingProfile(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	actorID uuid.UUID,
	seriesID uuid.UUID,
	profileID uuid.UUID,
	name string,
	episodeIDs []uuid.UUID,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (id, series_id, name, version, source_season_lengths, active, created_by, decision_source)
VALUES ($1, $2, $3, 1, ARRAY[2], true, $4, 'user')`, profileID, seriesID, name, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, absolute_episode, target_episode_id, mapping_status, match_source)
VALUES ($1, $3, 1, 1, 1, $4, 'mapped', 'absolute'),
       ($2, $3, 1, 2, 2, $5, 'mapped', 'absolute')`, uuid.New(), uuid.New(), profileID, episodeIDs[0], episodeIDs[1]); err != nil {
		t.Fatal(err)
	}
}

func TestRSSSubscriptionCanBeRecreatedAfterArchiveIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))
	actorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "rss-recreate-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	input := domain.CreateRSSSubscription{
		TMDbSeriesID: 300112,
		SeriesTitle:  "BanG Dream! YUME∞MITA",
		Name:         "First Feed",
		FeedURL:      "https://example.test/yume-mita.xml",
		Enabled:      false,
		SourceSeason: 1,
		PollInterval: 15 * time.Minute,
		ActorUserID:  actorID,
	}

	first, err := workflow.CreateSubscription(ctx, input)
	if err != nil {
		t.Fatalf("create first subscription: %v", err)
	}
	if err := workflow.ArchiveSubscription(ctx, first.ID, first.Version, actorID); err != nil {
		t.Fatalf("archive first subscription: %v", err)
	}

	input.Name = "Replacement Feed"
	second, err := workflow.CreateSubscription(ctx, input)
	if err != nil {
		t.Fatalf("create replacement subscription: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("replacement subscription reused archived ID %s", first.ID)
	}

	input.Name = "Duplicate Active Feed"
	_, err = workflow.CreateSubscription(ctx, input)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "state_conflict" {
		t.Fatalf("create duplicate active subscription error = %#v, want state_conflict", err)
	}

	var totalCount, activeCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE deleted_at IS NULL)
		FROM rss_subscriptions
		WHERE series_id = $1 AND feed_url = $2`, second.SeriesID, input.FeedURL).Scan(&totalCount, &activeCount); err != nil {
		t.Fatal(err)
	}
	if totalCount != 2 || activeCount != 1 {
		t.Fatalf("subscription counts = total %d active %d, want 2/1", totalCount, activeCount)
	}
}

func TestRSSSubscriptionProgressAggregatesAcquisitionsAndSortsGloballyIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	workflow := NewRSSWorkflow(db.New(pool), database.NewTransactor(pool), nil)

	seriesID := uuid.New()
	progressSubscriptionID, emptySubscriptionID := uuid.New(), uuid.New()
	firstEntryID, secondEntryID := uuid.New(), uuid.New()
	firstAcquisitionID, secondAcquisitionID, downloadID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'RSS Progress Integration')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $3, 'Progress Feed', 'https://example.test/progress.xml', true, 900, 1),
       ($2, $3, 'Empty Feed', 'https://example.test/empty.xml', true, 900, 1)`, progressSubscriptionID, emptySubscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons, source_season, source_episode, status)
VALUES ($1, $3, 'guid:progress-1', 'Progress E01', 'https://example.test/1.torrent', true, ARRAY[]::text[], 1, 1, 'enqueued'),
       ($2, $3, 'guid:progress-2', 'Progress E02', 'https://example.test/2.torrent', true, ARRAY[]::text[], 1, 2, 'enqueued')`, firstEntryID, secondEntryID, progressSubscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, rss_entry_id)
VALUES ($1, $3, 'rss', $4), ($2, $3, 'rss', $5)`, firstAcquisitionID, secondAcquisitionID, seriesID, firstEntryID, secondEntryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, acquisition_id, status, progress) VALUES ($1, $2, 'downloading', 0.5)`, downloadID, secondAcquisitionID); err != nil {
		t.Fatal(err)
	}

	progressSubscription, err := workflow.GetSubscription(ctx, progressSubscriptionID)
	if err != nil {
		t.Fatalf("GetSubscription() error = %v", err)
	}
	if progressSubscription.TaskCount != 2 || progressSubscription.CompletedTaskCount != 0 || progressSubscription.AttentionTaskCount != 0 {
		t.Fatalf("subscription counts = %#v, want 2/0/0", progressSubscription)
	}
	if progressSubscription.OverallProgress != 0.09 {
		t.Fatalf("subscription progress = %f, want mean of 0.02 and 0.16", progressSubscription.OverallProgress)
	}

	entryPage, err := NewReadService(db.New(pool)).ListRSSEntries(ctx, progressSubscriptionID, nil, 10, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListRSSEntries() error = %v", err)
	}
	entryProgress := make(map[uuid.UUID]float64, len(entryPage.Items))
	for _, entry := range entryPage.Items {
		if entry.AcquisitionID == nil || entry.AcquisitionProgress == nil {
			t.Fatalf("RSS entry acquisition progress = %#v", entry)
		}
		entryProgress[entry.ID] = entry.AcquisitionProgress.OverallProgress
	}
	if entryProgress[firstEntryID] != 0.02 || entryProgress[secondEntryID] != 0.16 {
		t.Fatalf("RSS entry progress = %#v, want first 0.02 and second 0.16", entryProgress)
	}

	sortBy, sortOrder := "progress", "desc"
	page, err := workflow.ListSubscriptions(ctx, nil, 10, &sortBy, &sortOrder)
	if err != nil {
		t.Fatalf("ListSubscriptions() error = %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != progressSubscriptionID || page.Items[1].ID != emptySubscriptionID {
		t.Fatalf("progress order = %#v, want progress subscription before empty subscription", page.Items)
	}
	if page.Items[1].OverallProgress != 0 || page.Items[1].TaskCount != 0 {
		t.Fatalf("empty subscription progress = %#v, want zero", page.Items[1])
	}
}
