//go:build integration

package legacymigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestApplyIsIdempotentAndInjectedFailureRollsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	var auditTable string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('legacy_migration_runs')::text").Scan(&auditTable); err != nil || auditTable == "" {
		t.Fatalf("migration 000007 must be applied before integration test: %v", err)
	}

	testID := uuid.New().String()
	profileID := uuid.New()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		var remnants int
		if err := pool.QueryRow(cleanupCtx, `
SELECT
    (SELECT count(*) FROM legacy_migration_runs WHERE source_kind IN ($1, $2))
  + (SELECT count(*) FROM transcode_profiles WHERE id = $3)
  + (SELECT count(*) FROM media_series WHERE legacy_id LIKE $4)
`, "test_runtime_"+testID, "test_failure_"+testID, profileID, "%"+testID+"%").Scan(&remnants); err != nil {
			t.Errorf("verify integration cleanup: %v", err)
		} else if remnants != 0 {
			t.Errorf("integration cleanup left %d database rows", remnants)
		}
	})
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (
    id, name, version, active, is_default, video_codec, encoder, container, file_extension,
    quality_mode, quality_value, audio_policy, preset, pixel_format, thread_count, max_concurrency
) VALUES ($1, $2, 1, true, false, 'av1', 'libsvtav1', 'matroska', 'mkv', 'crf', 30, 'copy', '6', 'yuv420p10le', 0, 1)
`, profileID, "legacy-migration-test-"+testID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM transcode_profiles WHERE id = $1", profileID)
	})

	successSourceKind := "test_runtime_" + testID
	successRecord := integrationFixtureRecord(t, "success-"+testID, "Success "+testID)
	successRecord.Payload["tmdb"] = map[string]any{
		"hinted_season": 1,
		"episodes": []any{
			map[string]any{"episode_number": 1, "name": "Catalog One", "air_date": "2026-01-01"},
			map[string]any{"episode_number": 2, "name": "Catalog Two", "air_date": "2026-01-08"},
		},
	}
	rssRecord := makeRecord(t, "rss_draft", "rss-"+testID, map[string]any{
		"draft_id": "rss-" + testID, "canonical_series": "RSS " + testID,
		"source_season": 3, "title": "Fixture RSS", "feed_url": "https://example.test/" + testID + "/feed.xml",
	})
	invalidRSSRecord := makeRecord(t, "rss_draft", "invalid-rss-"+testID, map[string]any{
		"draft_id": "invalid-rss-" + testID, "canonical_series": "Invalid RSS " + testID,
		"source_season": 1, "feed_url": "https://user:secret@example.test/feed.xml",
	})
	successPlan, err := BuildPlan(ctx, Snapshot{
		SourceKind: successSourceKind, Records: []Record{successRecord}, RSSDrafts: []Record{rssRecord, invalidRSSRecord},
	}, verifiedFixturePlanOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	failureSourceKind := "test_failure_" + testID
	failureRecord := integrationFixtureRecord(t, "failure-"+testID, "Failure "+testID)
	failurePlan, err := BuildPlan(ctx, Snapshot{SourceKind: failureSourceKind, Records: []Record{failureRecord}}, verifiedFixturePlanOptions(t))
	if err != nil {
		t.Fatal(err)
	}

	existingSeriesID := uuid.New()
	existingSeasonID := uuid.New()
	existingEpisodeOneID := uuid.New()
	existingEpisodeTwoID := uuid.New()
	existingMappingProfileID := uuid.New()
	existingMappingID := uuid.New()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM media_series WHERE id = $1", existingSeriesID)
	})
	seedStatements := []struct {
		query string
		args  []any
	}{
		{query: "INSERT INTO media_series (id, title, legacy_id) VALUES ($1, $2, $3)", args: []any{existingSeriesID, successPlan.Tasks[0].SeriesTitle, "legacy-series:" + successPlan.Tasks[0].SeriesKey}},
		{query: "INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 2)", args: []any{existingSeasonID, existingSeriesID}},
		{query: "INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $3, 1, 'Existing One'), ($2, $3, 2, 'Existing Two')", args: []any{existingEpisodeOneID, existingEpisodeTwoID, existingSeasonID}},
		{query: "INSERT INTO episode_mapping_profiles (id, series_id, name, version, active) VALUES ($1, $2, 'legacy-migration', 1, false)", args: []any{existingMappingProfileID, existingSeriesID}},
		{query: "INSERT INTO episode_mappings (id, profile_id, source_season, source_episode, target_episode_id, mapping_status, match_source) VALUES ($1, $2, 1, 2, $3, 'mapped', 'explicit')", args: []any{existingMappingID, existingMappingProfileID, existingEpisodeTwoID}},
	}
	for _, statement := range seedStatements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	runIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM events WHERE resource_id = ANY($1)", []uuid.UUID{successPlan.Tasks[0].TaskID, successPlan.Subscriptions[0].SubscriptionID, failurePlan.Tasks[0].TaskID})
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM legacy_migration_items WHERE migration_run_id = ANY($1)", runIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM legacy_migration_runs WHERE id = ANY($1)", runIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM acquisitions WHERE id = ANY($1)", []uuid.UUID{successPlan.Tasks[0].AcquisitionID, failurePlan.Tasks[0].AcquisitionID})
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM rss_subscriptions WHERE id = $1", successPlan.Subscriptions[0].SubscriptionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM media_series WHERE id = ANY($1)", []uuid.UUID{existingSeriesID, successPlan.Tasks[0].SeriesID, successPlan.Subscriptions[0].SeriesID, failurePlan.Tasks[0].SeriesID})
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM transcode_profiles WHERE id = $1", profileID)
	})

	first, err := Apply(ctx, pool, successPlan, ApplyOptions{RunID: runIDs[0], ProfileID: profileID})
	if err != nil {
		t.Fatalf("first Apply() error = %v", err)
	}
	if first.Counts.Imported != 2 || first.Counts.Unchanged != 0 || first.Counts.Invalid != 1 || first.Counts.ArtifactPairs != 1 || first.Counts.Events != 2 {
		t.Fatalf("first counts = %#v", first.Counts)
	}
	assertTaskCount(t, ctx, pool, successPlan.Tasks[0].TaskID, 1)
	var actualSeriesID, actualMappingID uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT acquisition.series_id, task.mapping_id FROM episode_tasks task JOIN acquisitions acquisition ON acquisition.id = task.acquisition_id WHERE task.id = $1", successPlan.Tasks[0].TaskID).Scan(&actualSeriesID, &actualMappingID); err != nil {
		t.Fatal(err)
	}
	if actualSeriesID != existingSeriesID || actualMappingID != existingMappingID {
		t.Fatalf("natural-key reuse = series %s mapping %s", actualSeriesID, actualMappingID)
	}
	var catalogEpisodeCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media_episodes episode JOIN tmdb_seasons season ON season.id = episode.season_id WHERE season.series_id = $1", existingSeriesID).Scan(&catalogEpisodeCount); err != nil {
		t.Fatal(err)
	}
	if catalogEpisodeCount != 2 {
		t.Fatalf("catalog episode count = %d, want 2", catalogEpisodeCount)
	}
	var migratedSourceSeason int
	if err := pool.QueryRow(ctx, "SELECT source_season FROM rss_subscriptions WHERE id = $1", successPlan.Subscriptions[0].SubscriptionID).Scan(&migratedSourceSeason); err != nil {
		t.Fatal(err)
	}
	if migratedSourceSeason != 3 {
		t.Fatalf("migrated RSS source season = %d, want 3", migratedSourceSeason)
	}

	second, err := Apply(ctx, pool, successPlan, ApplyOptions{RunID: runIDs[1], ProfileID: profileID})
	if err != nil {
		t.Fatalf("second Apply() error = %v", err)
	}
	if second.Counts.Imported != 0 || second.Counts.Unchanged != 3 || second.Counts.Invalid != 1 {
		t.Fatalf("second counts = %#v", second.Counts)
	}
	assertTaskCount(t, ctx, pool, successPlan.Tasks[0].TaskID, 1)

	changed := successRecord
	changed.Payload = cloneObject(successRecord.Payload)
	changed.Payload["unknown_field"] = "changed-after-dry-run"
	changedPlan, err := BuildPlan(ctx, Snapshot{SourceKind: successSourceKind, Records: []Record{changed}}, verifiedFixturePlanOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(ctx, pool, changedPlan, ApplyOptions{RunID: runIDs[2], ProfileID: profileID})
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Code != "legacy_record_changed" {
		t.Fatalf("changed Apply() error = %#v, want legacy_record_changed", err)
	}

	failed, err := Apply(ctx, pool, failurePlan, ApplyOptions{RunID: runIDs[3], ProfileID: profileID, FailAfter: 1})
	if !errors.As(err, &applyErr) || applyErr.Code != "injected_failure" {
		t.Fatalf("failure Apply() error = %#v, want injected_failure", err)
	}
	if failed.Counts.Imported != 0 || failed.Counts.ArtifactPairs != 0 || failed.Counts.Events != 0 {
		t.Fatalf("rolled back counts = %#v", failed.Counts)
	}
	assertTaskCount(t, ctx, pool, failurePlan.Tasks[0].TaskID, 0)
	var runStatus string
	var itemCount int
	if err := pool.QueryRow(ctx, "SELECT status FROM legacy_migration_runs WHERE id = $1", runIDs[3]).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM legacy_migration_items WHERE migration_run_id = $1", runIDs[3]).Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if runStatus != "failed" || itemCount != 0 {
		t.Fatalf("failure audit = status %q items %d", runStatus, itemCount)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM acquisitions WHERE id = $1", successPlan.Tasks[0].AcquisitionID); err != nil {
		t.Fatal(err)
	}
	_, err = Apply(ctx, pool, successPlan, ApplyOptions{RunID: runIDs[4], ProfileID: profileID})
	if !errors.As(err, &applyErr) || applyErr.Code != "migrated_resource_missing" {
		t.Fatalf("missing resource Apply() error = %#v, want migrated_resource_missing", err)
	}
}

func integrationFixtureRecord(t *testing.T, legacyID, seriesTitle string) Record {
	t.Helper()
	root := t.TempDir()
	base := "Episode - S01E01 - One"
	video := filepath.Join(root, base+".mkv")
	subtitle := filepath.Join(root, base+".ass")
	if err := os.WriteFile(video, []byte("integration-video-"+legacyID), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitle, []byte("[Script Info]\nTitle: "+legacyID+"\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,Fixture\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	record := fixtureTaskRecord(legacyID, video, subtitle)
	record.Payload["canonical_series"] = seriesTitle
	return record
}

func assertTaskCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID, expected int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM episode_tasks WHERE id = $1", taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("task %s count = %d, want %d", taskID, count, expected)
	}
}
