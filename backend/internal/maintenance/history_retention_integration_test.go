//go:build integration

package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestHistoryRetentionDeletesOnlyEligibleRowsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	actorID, seriesID := uuid.New(), uuid.New()
	old := time.Now().Add(-100 * 24 * time.Hour)
	recent := time.Now().Add(-2 * time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "retention-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Retention Series')`, seriesID); err != nil {
		t.Fatal(err)
	}
	insertOperation := func(name, status string, at time.Time) uuid.UUID {
		t.Helper()
		id := uuid.New()
		var finished any
		if status != "queued" && status != "running" {
			finished = at
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, idempotency_key, status, max_attempts, timeout_seconds, created_at, started_at, finished_at, updated_at)
VALUES ($1, 'retention.fixture', $2, $3, 1, 60, $4, $4, $5, $4)
`, id, "retention-"+id.String(), status, at, finished); err != nil {
			t.Fatal(err)
		}
		return id
	}
	operationEligible := insertOperation("eligible", "succeeded", old)
	operationWithResolution := insertOperation("withResolution", "succeeded", old)
	operationScan := insertOperation("scan", "succeeded", old)
	operationAppliedResolution := insertOperation("appliedResolution", "succeeded", old)
	operationActive := insertOperation("active", "running", old)
	operationRecent := insertOperation("recent", "succeeded", recent)
	operationEvent := insertOperation("event", "succeeded", old)
	operationProfile, operationSave := insertOperation("profile", "succeeded", old), insertOperation("save", "succeeded", old)
	operationEntry, operationDownload := insertOperation("entry", "succeeded", old), insertOperation("download", "succeeded", old)
	operationAdjudication := insertOperation("adjudication", "succeeded", old)
	operationSubtitle := insertOperation("subtitle", "succeeded", old)

	insertResolution := func(name string, operationID uuid.UUID, status string, at time.Time) uuid.UUID {
		t.Helper()
		id := uuid.New()
		var appliedAt any
		if status == "applied" {
			appliedAt = at
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO agent_resolutions (
    id, operation_id, capability, resource_type, resource_id, trigger, status,
    input_fingerprint, configuration_version, protocol, provider_origin, model,
    prompt_version, toolset_version, proposal, validation, applied_at, completed_at, created_at, updated_at
) VALUES ($1, $2, 'episode_mapping', 'acquisition', $3, 'automatic', $4,
    decode(repeat('0a', 32), 'hex'), 0, 'openai_chat_completions', 'https://agent.example',
    'fixture-model', 'v1', 'v1', '{}'::jsonb, '{}'::jsonb, $5, $6, $6, $6)
`, id, operationID, uuid.New(), status, appliedAt, at); err != nil {
			t.Fatal(err)
		}
		return id
	}
	resolutionEligible := insertResolution("eligible", operationWithResolution, "failed", old)
	resolutionProfile := insertResolution("profile", operationProfile, "failed", old)
	resolutionSave := insertResolution("save", operationSave, "cancelled", old)
	resolutionEntry := insertResolution("entry", operationEntry, "expired", old)
	resolutionDownload := insertResolution("download", operationDownload, "failed", old)
	resolutionAdjudication := insertResolution("adjudication", operationAdjudication, "failed", old)
	resolutionSubtitle := insertResolution("subtitle", operationSubtitle, "failed", old)
	resolutionApplied := insertResolution("applied", operationAppliedResolution, "applied", old)

	profileID, acquisitionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mapping_profiles (id, series_id, name, version, decision_source, agent_resolution_id)
VALUES ($1, $2, 'retention-profile', 1, 'agent_auto', $3)
`, profileID, seriesID, resolutionProfile); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO acquisitions (id, series_id, source_kind, source_uri, created_by)
VALUES ($1, $2, 'manual', $3, $4)
`, acquisitionID, seriesID, "magnet:?xt=urn:btih:"+uuid.NewString(), actorID); err != nil {
		t.Fatal(err)
	}
	downloadID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO downloads (id, acquisition_id, status, file_resolution_source, agent_resolution_id)
VALUES ($1, $2, 'completed', 'agent_auto', $3)
`, downloadID, acquisitionID, resolutionDownload); err != nil {
		t.Fatal(err)
	}
	transcodeProfileID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO transcode_profiles (id, name, version, video_codec, encoder, container, file_extension, quality_mode, quality_value, audio_policy, preset, pixel_format)
VALUES ($1, $2, 1, 'h264', 'libx264', 'matroska', 'mkv', 'crf', 23, 'copy', 'medium', 'yuv420p')
`, transcodeProfileID, "retention-"+transcodeProfileID.String()); err != nil {
		t.Fatal(err)
	}
	fileID, taskID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO download_files (id, download_id, file_index, relative_path, size_bytes, media_kind, selected)
VALUES ($1, $2, 0, 'Episode.mkv', 1024, 'video', true)
`, fileID, downloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_tasks (id, acquisition_id, source_video_file_id, transcode_profile_id)
VALUES ($1, $2, $3, $4)
`, taskID, acquisitionID, fileID, transcodeProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO subtitle_video_match_scopes (id, task_id, status, selected_candidate_id, agent_resolution_id)
VALUES ($1, $2, 'applied', 'candidate', $3)
`, uuid.New(), taskID, resolutionSubtitle); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO episode_mapping_saves (id, acquisition_id, profile_id, idempotency_key, request_fingerprint, result_payload, decision_source, agent_resolution_id)
VALUES ($1, $2, $3, $4, decode(repeat('0b', 32), 'hex'), '{}'::jsonb, 'agent_auto', $5)
`, uuid.New(), acquisitionID, profileID, "retention-save-"+uuid.NewString(), resolutionSave); err != nil {
		t.Fatal(err)
	}
	subscriptionID, entryID, batchID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url)
VALUES ($1, $2, 'Retention Feed', $3)
`, subscriptionID, seriesID, "https://example.test/"+subscriptionID.String()+".xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, source_season, source_episode, coordinate_source, agent_resolution_id)
VALUES ($1, $2, $3, 'Retention Entry', 'https://example.test/episode.torrent', true, 1, 1, 'agent_auto', $4)
`, entryID, subscriptionID, "guid:"+entryID.String(), resolutionEntry); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rss_adjudication_batches (id, subscription_id, status, entry_count) VALUES ($1, $2, 'applied', 1)`, batchID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entry_adjudications (entry_id, subscription_id, batch_id, state, source, resolution_id)
VALUES ($1, $2, $3, 'selected', 'agent_auto', $4)
`, entryID, subscriptionID, batchID, resolutionAdjudication); err != nil {
		t.Fatal(err)
	}
	scanID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO emby_scan_runs (id, operation_id, status, created_by, completed_at)
VALUES ($1, $2, 'succeeded', $3, $4)
`, scanID, operationScan, actorID, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operation_attempts (id, operation_id, attempt, status, started_at, finished_at)
VALUES ($1, $2, 1, 'succeeded', $3, $3)
`, uuid.New(), operationEligible, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO events (topic, operation_id, occurred_at) VALUES ('operation.succeeded', $1, $2)`, operationEvent, old); err != nil {
		t.Fatal(err)
	}

	retention := NewHistoryRetention(db.New(pool), database.NewTransactor(pool))
	request := HistoryRetentionRequest{Before: time.Now().Add(-90 * 24 * time.Hour), BatchSize: 100, MaxBatches: 5}
	dryRun, err := retention.Run(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.OperationCandidates != 2 || dryRun.ResolutionCandidates != 1 || dryRun.OperationsDeleted != 0 || dryRun.ResolutionsDeleted != 0 {
		t.Fatalf("dry-run result = %#v", dryRun)
	}
	var retainedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id = ANY($1::uuid[])`, []uuid.UUID{
		operationEligible, operationWithResolution, operationScan, operationAppliedResolution,
		operationActive, operationRecent, operationEvent,
	}).Scan(&retainedCount); err != nil || retainedCount != 7 {
		t.Fatalf("dry-run deleted rows: count=%d err=%v", retainedCount, err)
	}

	applied, err := retention.Run(ctx, HistoryRetentionRequest{
		Before: time.Now().Add(-90 * 24 * time.Hour), BatchSize: 100, MaxBatches: 5, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied.ResolutionsDeleted != 1 || applied.OperationsDeleted != 3 {
		t.Fatalf("apply result = %#v", applied)
	}
	expectDeleted := []uuid.UUID{operationEligible, operationWithResolution, operationEvent, resolutionEligible}
	for _, id := range expectDeleted {
		var count int
		if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM operations WHERE id = $1) + (SELECT count(*) FROM agent_resolutions WHERE id = $1)
`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("eligible row %s still exists", id)
		}
	}
	expectRetained := []uuid.UUID{operationScan, operationAppliedResolution, operationActive, operationRecent}
	expectRetained = append(expectRetained, operationProfile, operationSave, operationEntry, operationDownload, operationAdjudication, operationSubtitle)
	expectRetained = append(expectRetained, resolutionProfile, resolutionSave, resolutionEntry, resolutionDownload, resolutionAdjudication, resolutionSubtitle, resolutionApplied)
	for _, id := range expectRetained {
		var count int
		if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM operations WHERE id = $1) + (SELECT count(*) FROM agent_resolutions WHERE id = $1)
`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("referenced row %s was deleted", id)
		}
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_attempts WHERE operation_id = $1`, operationEligible).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("operation attempts = %d, err = %v", attempts, err)
	}
	var eventOperation *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT operation_id FROM events WHERE topic = 'operation.succeeded' AND occurred_at < $1 ORDER BY occurred_at DESC LIMIT 1`, recent).Scan(&eventOperation); err != nil {
		t.Fatal(err)
	}
	if eventOperation != nil {
		t.Fatalf("event operation_id = %s, want NULL", eventOperation)
	}
}
