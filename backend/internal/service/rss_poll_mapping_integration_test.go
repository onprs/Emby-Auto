//go:build integration

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func TestEnsureDeterministicPollMappingCreatesProfileWhenAutomaticFileMappingIsDisabledIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	seriesID, seasonID, subscriptionID := uuid.New(), uuid.New(), uuid.New()
	episodeIDs := []uuid.UUID{uuid.New(), uuid.New()}
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'First Subscription')`, seriesID, time.Now().UnixNano()); err != nil {
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
INSERT INTO rss_subscriptions (
  id, series_id, name, feed_url, enabled, auto_episode_mapping, poll_interval_seconds, source_season
) VALUES ($1, $2, 'First Subscription', 'https://example.test/feed.xml', true, false, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	staleScopeID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_preacquisition_mapping_scopes (id, subscription_id, subscription_version, source_fingerprint)
VALUES ($1, $2, 1, $3)`, staleScopeID, subscriptionID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	feed := domain.RSSFeed{Entries: []domain.RSSFeedEntry{
		{Title: "First Subscription S01E02", DownloadURI: "https://example.test/e02.torrent"},
		{Title: "First Subscription S01E01", DownloadURI: "https://example.test/e01.torrent"},
	}}

	ready, err := workflow.EnsureDeterministicPollMapping(ctx, uuid.Nil, subscriptionID, feed)
	if err != nil || !ready {
		t.Fatalf("EnsureDeterministicPollMapping() = %t, %v", ready, err)
	}
	var profileID uuid.UUID
	var subscriptionVersion int
	if err := pool.QueryRow(ctx, `SELECT mapping_profile_id, version FROM rss_subscriptions WHERE id = $1`, subscriptionID).
		Scan(&profileID, &subscriptionVersion); err != nil {
		t.Fatal(err)
	}
	if profileID == uuid.Nil || subscriptionVersion != 2 {
		t.Fatalf("subscription profile/version = %s/%d, want non-zero/2", profileID, subscriptionVersion)
	}
	var decisionSource string
	var anchorSeason, anchorEpisode, offset int
	if err := pool.QueryRow(ctx, `
SELECT decision_source, anchor_source_season, anchor_source_episode, target_episode_offset
FROM episode_mapping_profiles WHERE id = $1`, profileID).Scan(&decisionSource, &anchorSeason, &anchorEpisode, &offset); err != nil {
		t.Fatal(err)
	}
	if decisionSource != string(domain.DecisionSourceDeterministic) || anchorSeason != 1 || anchorEpisode != 1 || offset != 0 {
		t.Fatalf("profile provenance/anchor/offset = %s/S%02dE%02d/%d", decisionSource, anchorSeason, anchorEpisode, offset)
	}
	var mappings, acquisitions, continuousPolls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mappings WHERE profile_id = $1 AND mapping_status = 'mapped'`, profileID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions`).Scan(&acquisitions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM operations
WHERE resource_id = $1 AND kind = 'rss.poll' AND idempotency_key = $2`, subscriptionID,
		"rss.poll:"+subscriptionID.String()+":v2:continuous").Scan(&continuousPolls); err != nil {
		t.Fatal(err)
	}
	var staleScopeStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM rss_preacquisition_mapping_scopes WHERE id=$1`, staleScopeID).Scan(&staleScopeStatus); err != nil {
		t.Fatal(err)
	}
	if mappings != 2 || acquisitions != 0 || continuousPolls != 1 || staleScopeStatus != "expired" {
		t.Fatalf("mappings/acquisitions/continuous polls/stale scope = %d/%d/%d/%s, want 2/0/1/expired", mappings, acquisitions, continuousPolls, staleScopeStatus)
	}

	replayed, err := workflow.EnsureDeterministicPollMapping(ctx, uuid.Nil, subscriptionID, feed)
	if err != nil || !replayed {
		t.Fatalf("replayed EnsureDeterministicPollMapping() = %t, %v", replayed, err)
	}
	var profiles, mappingScopes, replayedVersion int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mapping_profiles WHERE series_id = $1`, seriesID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_preacquisition_mapping_scopes WHERE subscription_id = $1 AND status='pending'`, subscriptionID).Scan(&mappingScopes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT version FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&replayedVersion); err != nil {
		t.Fatal(err)
	}
	if profiles != 1 || mappingScopes != 0 || replayedVersion != 2 {
		t.Fatalf("replayed profiles/scopes/version = %d/%d/%d, want 1/0/2", profiles, mappingScopes, replayedVersion)
	}
}

func TestReconcilePreAcquisitionMappingPollsIsNarrowAndIdempotentIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	seriesID, seasonID, episodeID, subscriptionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Recovery Subscription')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 1)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, 1, 'Episode 1')`, episodeID, seasonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
  id, series_id, name, feed_url, enabled, auto_episode_mapping, poll_interval_seconds, source_season
) VALUES ($1, $2, 'Recovery Subscription', 'https://example.test/recovery.xml', true, true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (
  id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count,
  timeout_seconds, error_code, error_message, finished_at
) VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'failed', 5, 5, 30,
          'rss_realtime_mapping_unavailable', 'mapped targets unavailable', now())`,
		uuid.New(), subscriptionID, "rss.poll:"+subscriptionID.String()+":v1:continuous"); err != nil {
		t.Fatal(err)
	}

	count, err := workflow.ReconcilePreAcquisitionMappingPolls(ctx)
	if err != nil || count != 1 {
		t.Fatalf("ReconcilePreAcquisitionMappingPolls() = %d, %v, want 1/nil", count, err)
	}
	count, err = workflow.ReconcilePreAcquisitionMappingPolls(ctx)
	if err != nil || count != 0 {
		t.Fatalf("replayed ReconcilePreAcquisitionMappingPolls() = %d, %v, want 0/nil", count, err)
	}
	var recoveryOperations int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM operations
WHERE idempotency_key = $1 AND status = 'queued'`,
		"rss.poll:recovery:preacquisition-mapping-v1:"+subscriptionID.String()).Scan(&recoveryOperations); err != nil {
		t.Fatal(err)
	}
	if recoveryOperations != 1 {
		t.Fatalf("recovery operation count = %d, want 1", recoveryOperations)
	}
}

func TestReconcileDuePollsRecoversEveryTerminalGenerationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	seriesID, subscriptionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Due Poll Recovery')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
  id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season, next_poll_at
) VALUES ($1, $2, 'Due Poll Recovery', 'https://example.test/due.xml', true, 900, 1, now() - interval '1 hour')`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (
  id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count,
  timeout_seconds, error_code, error_message, finished_at
) VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'failed', 5, 5, 30,
          'rss_storage_unavailable', 'storage unavailable', now())`,
		uuid.New(), subscriptionID, "rss.poll:"+subscriptionID.String()+":v1:continuous"); err != nil {
		t.Fatal(err)
	}

	type reconciliationResult struct {
		count int
		err   error
	}
	start := make(chan struct{})
	results := make(chan reconciliationResult, 2)
	for range 2 {
		go func() {
			<-start
			count, reconcileErr := workflow.ReconcileDuePolls(ctx)
			results <- reconciliationResult{count: count, err: reconcileErr}
		}()
	}
	close(start)
	count := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent ReconcileDuePolls() error = %v", result.err)
		}
		count += result.count
	}
	if count != 1 {
		t.Fatalf("concurrent ReconcileDuePolls() created %d operations, want 1", count)
	}
	var firstKey string
	var nextPollInFuture bool
	if err := pool.QueryRow(ctx, `
SELECT idempotency_key
FROM operations
WHERE resource_id = $1 AND status = 'queued'
ORDER BY created_at DESC LIMIT 1`, subscriptionID).Scan(&firstKey); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT next_poll_at > now() FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&nextPollInFuture); err != nil {
		t.Fatal(err)
	}
	wantFirstKey := "rss.poll:recovery:due-v1:" + subscriptionID.String() + ":v1:g1"
	if firstKey != wantFirstKey || !nextPollInFuture {
		t.Fatalf("first recovery = key %q future %t, want %q/true", firstKey, nextPollInFuture, wantFirstKey)
	}
	count, err = workflow.ReconcileDuePolls(ctx)
	if err != nil || count != 0 {
		t.Fatalf("active ReconcileDuePolls() = %d, %v, want 0/nil", count, err)
	}

	if _, err := pool.Exec(ctx, `
UPDATE operations
SET status = 'failed', error_code = 'rss_storage_unavailable', error_message = 'still unavailable', finished_at = now()
WHERE idempotency_key = $1`, firstKey); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE rss_subscriptions SET next_poll_at = now() - interval '1 minute' WHERE id = $1`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	count, err = workflow.ReconcileDuePolls(ctx)
	if err != nil || count != 1 {
		t.Fatalf("next-generation ReconcileDuePolls() = %d, %v, want 1/nil", count, err)
	}
	var secondKey string
	if err := pool.QueryRow(ctx, `
SELECT idempotency_key
FROM operations
WHERE resource_id = $1 AND status = 'queued'
ORDER BY created_at DESC LIMIT 1`, subscriptionID).Scan(&secondKey); err != nil {
		t.Fatal(err)
	}
	wantSecondKey := "rss.poll:recovery:due-v1:" + subscriptionID.String() + ":v1:g2"
	if secondKey != wantSecondKey {
		t.Fatalf("second recovery key = %q, want %q", secondKey, wantSecondKey)
	}
}

func TestTMDbCatalogCompletionRetriesDeterministicRSSMappingBeforeAgentIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)
	workflow := NewRSSWorkflow(db.New(pool), transactor, scheduler)

	seriesID, subscriptionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Catalog Race')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
  id, series_id, name, feed_url, enabled, auto_episode_mapping, poll_interval_seconds, source_season
) VALUES ($1, $2, 'Catalog Race', 'https://example.test/race.xml', true, true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	feed := domain.RSSFeed{Entries: []domain.RSSFeedEntry{
		{GUID: "race-1", Title: "Catalog Race S01E03", DownloadURI: "https://example.test/race-1.torrent"},
	}}
	preparation, err := workflow.PreparePollMapping(ctx, uuid.Nil, subscriptionID, feed)
	if err != nil || preparation.ScopeID == uuid.Nil || preparation.Ready {
		t.Fatalf("PreparePollMapping() = %#v, %v", preparation, err)
	}

	seasonID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 3)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_episodes (id, season_id, episode_number, title)
VALUES ($1, $4, 1, 'Episode 1'), ($2, $4, 2, 'Episode 2'), ($3, $4, 3, 'Episode 3')`, uuid.New(), uuid.New(), uuid.New(), seasonID); err != nil {
		t.Fatal(err)
	}
	agentSettings := domain.DefaultAgentSettings()
	agentSettings.Enabled = true
	agentSettings.EpisodeMappingEnabled = true
	agentSettings.BaseURL = "https://provider.example/v1"
	agentSettings.Model = "fixture-model"
	agentService := NewAgentResolutionService(
		db.New(pool), transactor, scheduler,
		deterministicAgentConfigurationStub{configuration: domain.Configuration{
			Version: 1, Settings: domain.RuntimeSettings{Agent: agentSettings},
		}},
		nil, nil,
	).WithRSSPreacquisitionMappingAgent(workflow)

	count, err := agentService.ReconcileAutomaticRSSPreacquisitionMappingsForSeries(ctx, seriesID)
	if err != nil || count != 1 {
		t.Fatalf("ReconcileAutomaticRSSPreacquisitionMappingsForSeries() = %d, %v", count, err)
	}
	count, err = agentService.ReconcileAutomaticRSSPreacquisitionMappingsForSeries(ctx, seriesID)
	if err != nil || count != 0 {
		t.Fatalf("replayed reconciliation = %d, %v", count, err)
	}
	var mappings, resolutions, agentOperations, acquisitions, version int
	var profileAssigned bool
	var decisionSource, scopeStatus string
	if err := pool.QueryRow(ctx, `SELECT mapping_profile_id IS NOT NULL, version FROM rss_subscriptions WHERE id=$1`, subscriptionID).Scan(&profileAssigned, &version); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT decision_source FROM episode_mapping_profiles WHERE series_id=$1 AND active`, seriesID).Scan(&decisionSource); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM rss_preacquisition_mapping_scopes WHERE id=$1`, preparation.ScopeID).Scan(&scopeStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mappings m JOIN episode_mapping_profiles p ON p.id=m.profile_id WHERE p.series_id=$1`, seriesID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM agent_resolutions
WHERE capability='rss_preacquisition_mapping' AND resource_type='rss_preacquisition_mapping_scope' AND resource_id=$1`, preparation.ScopeID).Scan(&resolutions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind='agent.resolve' AND resource_type='agent_resolution'`).Scan(&agentOperations); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions`).Scan(&acquisitions); err != nil {
		t.Fatal(err)
	}
	if !profileAssigned || version != 2 || decisionSource != string(domain.DecisionSourceDeterministic) || scopeStatus != "applied" ||
		mappings != 3 || resolutions != 0 || agentOperations != 0 || acquisitions != 0 {
		t.Fatalf("profile/version/source/scope/mappings/resolutions/operations/acquisitions = %t/%d/%s/%s/%d/%d/%d/%d",
			profileAssigned, version, decisionSource, scopeStatus, mappings, resolutions, agentOperations, acquisitions)
	}
}

func TestTMDbCatalogCompletionFallsBackToAgentForNonstandardRSSMappingIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)
	workflow := NewRSSWorkflow(db.New(pool), transactor, scheduler)

	seriesID, subscriptionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Catalog Offset')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
  id, series_id, name, feed_url, enabled, auto_episode_mapping, poll_interval_seconds, source_season
) VALUES ($1, $2, 'Catalog Offset', 'https://example.test/offset.xml', true, true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	preparation, err := workflow.PreparePollMapping(ctx, uuid.Nil, subscriptionID, domain.RSSFeed{Entries: []domain.RSSFeedEntry{
		{GUID: "offset-1", Title: "Catalog Offset S02E01", DownloadURI: "https://example.test/offset-1.torrent"},
	}})
	if err != nil || preparation.ScopeID == uuid.Nil || preparation.Ready {
		t.Fatalf("PreparePollMapping() = %#v, %v", preparation, err)
	}
	seasonID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count) VALUES ($1, $2, 1, 1)`, seasonID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_episodes (id, season_id, episode_number, title) VALUES ($1, $2, 1, 'Episode 1')`, uuid.New(), seasonID); err != nil {
		t.Fatal(err)
	}
	agentSettings := domain.DefaultAgentSettings()
	agentSettings.Enabled = true
	agentSettings.EpisodeMappingEnabled = true
	agentSettings.BaseURL = "https://provider.example/v1"
	agentSettings.Model = "fixture-model"
	agentService := NewAgentResolutionService(
		db.New(pool), transactor, scheduler,
		deterministicAgentConfigurationStub{configuration: domain.Configuration{
			Version: 1, Settings: domain.RuntimeSettings{Agent: agentSettings},
		}},
		nil, nil,
	).WithRSSPreacquisitionMappingAgent(workflow)

	count, err := agentService.ReconcileAutomaticRSSPreacquisitionMappingsForSeries(ctx, seriesID)
	if err != nil || count != 1 {
		t.Fatalf("ReconcileAutomaticRSSPreacquisitionMappingsForSeries() = %d, %v", count, err)
	}
	var resolutions, agentOperations, profiles, acquisitions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_resolutions WHERE capability='rss_preacquisition_mapping' AND resource_id=$1`, preparation.ScopeID).Scan(&resolutions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind='agent.resolve'`).Scan(&agentOperations); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mapping_profiles WHERE series_id=$1`, seriesID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions`).Scan(&acquisitions); err != nil {
		t.Fatal(err)
	}
	if resolutions != 1 || agentOperations != 1 || profiles != 0 || acquisitions != 0 {
		t.Fatalf("resolutions/operations/profiles/acquisitions = %d/%d/%d/%d", resolutions, agentOperations, profiles, acquisitions)
	}
}

func TestAgentPreacquisitionMappingCreatesProfileWhenAutomaticFileMappingIsDisabledIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, riverClient))

	seriesID, subscriptionID := uuid.New(), uuid.New()
	season1ID, season2ID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Offset Subscription')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO tmdb_seasons (id, series_id, season_number, episode_count)
VALUES ($1, $3, 1, 2), ($2, $3, 2, 2)`, season1ID, season2ID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO media_episodes (id, season_id, episode_number, title)
VALUES
  ($1, $5, 1, 'One'), ($2, $5, 2, 'Two'),
  ($3, $6, 1, 'Thirteen'), ($4, $6, 2, 'Fourteen')`,
		uuid.New(), uuid.New(), uuid.New(), uuid.New(), season1ID, season2ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
  id, series_id, name, feed_url, enabled, auto_episode_mapping, poll_interval_seconds, source_season
) VALUES ($1, $2, 'Offset Subscription', 'https://example.test/offset.xml', true, false, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	pollOperationID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'running', 5, 30)`,
		pollOperationID, subscriptionID, "offset-poll-"+pollOperationID.String()); err != nil {
		t.Fatal(err)
	}
	feed := domain.RSSFeed{Entries: []domain.RSSFeedEntry{
		{GUID: "offset-13", Title: "Offset Subscription S01E03", DownloadURI: "https://example.test/e03.torrent"},
		{GUID: "offset-14", Title: "Offset Subscription S01E04", DownloadURI: "https://example.test/e04.torrent"},
	}}
	preparation, err := workflow.PreparePollMapping(ctx, pollOperationID, subscriptionID, feed)
	if err != nil {
		t.Fatalf("PreparePollMapping() error = %v", err)
	}
	if preparation.Ready || preparation.Applied || preparation.ScopeID == uuid.Nil || len(preparation.AgentCoordinateCandidates) != 0 {
		t.Fatalf("PreparePollMapping() = %#v", preparation)
	}
	var entries, acquisitions, profiles int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_entries WHERE subscription_id = $1`, subscriptionID).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions`).Scan(&acquisitions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mapping_profiles WHERE series_id = $1`, seriesID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if entries != 2 || acquisitions != 0 || profiles != 0 {
		t.Fatalf("pre-Agent entries/acquisitions/profiles = %d/%d/%d, want 2/0/0", entries, acquisitions, profiles)
	}

	anchorSource := domain.EpisodeCoordinate{Season: 1, Episode: 3}
	anchorTarget := domain.EpisodeCoordinate{Season: 2, Episode: 1}
	preview, err := workflow.PreviewRSSPreacquisitionMapping(ctx, preparation.ScopeID, anchorSource, anchorTarget)
	if err != nil || len(preview) != 4 {
		t.Fatalf("PreviewRSSPreacquisitionMapping() rows/error = %d/%v, want 4/nil", len(preview), err)
	}

	agentSettings := domain.DefaultAgentSettings()
	agentSettings.Enabled = true
	agentSettings.EpisodeMappingEnabled = true
	agentSettings.AllowAutomaticEpisodeMapping = true
	agentSettings.BaseURL = "https://provider.example/v1"
	agentSettings.Model = "fixture-model"
	settingsJSON, err := json.Marshal(domain.RuntimeSettings{Agent: agentSettings})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app_settings (name, value, version) VALUES ('runtime', $1, 1)`, settingsJSON); err != nil {
		t.Fatal(err)
	}
	resolutionID, agentOperationID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'running', 3, 60)`,
		agentOperationID, resolutionID, "offset-agent-"+resolutionID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_resolutions (
  id, operation_id, capability, resource_type, resource_id, resource_version, trigger, status,
  input_fingerprint, configuration_version, protocol, provider_origin, model, prompt_version, toolset_version
) VALUES (
  $1, $2, 'rss_preacquisition_mapping', 'rss_preacquisition_mapping_scope', $3, 1, 'automatic', 'proposed',
  $4, 1, 'openai_chat_completions', 'https://provider.example', 'fixture-model',
  'rss-preacquisition-mapping-v1', 'agent-tools-v1'
)`, resolutionID, agentOperationID, preparation.ScopeID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	resolution := domain.AgentResolution{
		ID: resolutionID, OperationID: agentOperationID, Version: 1,
		Capability:   domain.AgentCapabilityRSSPreacquisitionMapping,
		ResourceType: "rss_preacquisition_mapping_scope", ResourceID: preparation.ScopeID,
		Status: domain.AgentResolutionProposed,
	}
	proposal := domain.AgentRSSPreacquisitionMappingProposal{
		ScopeID: preparation.ScopeID, SourceSeason: anchorSource.Season, SourceEpisode: anchorSource.Episode,
		TargetSeason: anchorTarget.Season, TargetEpisode: anchorTarget.Episode,
		EvidenceCodes: []string{"episode_title_alignment"}, Decision: "resolved",
	}
	validation := domain.AgentProposalValidation{Verdict: domain.AgentValidationAutoApplicable, ReasonCodes: []string{}}
	if err := workflow.ApplyAgentRSSPreacquisitionMapping(ctx, resolution, proposal, validation); err != nil {
		t.Fatalf("ApplyAgentRSSPreacquisitionMapping() error = %v", err)
	}

	var profileID uuid.UUID
	var subscriptionVersion int
	if err := pool.QueryRow(ctx, `SELECT mapping_profile_id, version FROM rss_subscriptions WHERE id = $1`, subscriptionID).Scan(&profileID, &subscriptionVersion); err != nil {
		t.Fatal(err)
	}
	var decisionSource, resolutionStatus, scopeStatus string
	var profileResolutionID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT decision_source, agent_resolution_id FROM episode_mapping_profiles WHERE id = $1`, profileID).Scan(&decisionSource, &profileResolutionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_resolutions WHERE id = $1`, resolutionID).Scan(&resolutionStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM rss_preacquisition_mapping_scopes WHERE id = $1`, preparation.ScopeID).Scan(&scopeStatus); err != nil {
		t.Fatal(err)
	}
	var mappings, continuousPolls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episode_mappings WHERE profile_id = $1 AND mapping_status = 'mapped'`, profileID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1`, "rss.poll:"+subscriptionID.String()+":v2:continuous").Scan(&continuousPolls); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions`).Scan(&acquisitions); err != nil {
		t.Fatal(err)
	}
	if subscriptionVersion != 2 || decisionSource != string(domain.DecisionSourceAgentAuto) || profileResolutionID != resolutionID ||
		resolutionStatus != string(domain.AgentResolutionApplied) || scopeStatus != "applied" || mappings != 4 || continuousPolls != 1 || acquisitions != 0 {
		t.Fatalf("applied version/source/resolution/scope/mappings/polls/acquisitions = %d/%s/%s/%s/%d/%d/%d",
			subscriptionVersion, decisionSource, resolutionStatus, scopeStatus, mappings, continuousPolls, acquisitions)
	}
}

func TestAgentCoordinateWithoutProfileSchedulesMappingContinuationWithoutAcquisitionIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewOperationScheduler(transactor, riverClient)

	seriesID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, tmdb_series_id, title) VALUES ($1, $2, 'Coordinate Subscription')`, seriesID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (
  id, series_id, name, feed_url, enabled, auto_episode_mapping, poll_interval_seconds, source_season
) VALUES ($1, $2, 'Coordinate Subscription', 'https://example.test/coordinate.xml', true, true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_entries (
  id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons
) VALUES ($1, $2, $3, 'Coordinate release', 'https://example.test/coordinate.torrent', false, ARRAY['episode_not_detected']::text[])`,
		entryID, subscriptionID, "coordinate-"+entryID.String()); err != nil {
		t.Fatal(err)
	}
	resolutionID, operationID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'running', 3, 60)`,
		operationID, resolutionID, "coordinate-agent-"+resolutionID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_resolutions (
  id, operation_id, capability, resource_type, resource_id, trigger, status,
  input_fingerprint, configuration_version, protocol, provider_origin, model, prompt_version, toolset_version
) VALUES (
  $1, $2, 'rss_coordinate', 'rss_entry', $3, 'automatic', 'proposed',
  $4, 1, 'openai_chat_completions', 'https://provider.example', 'fixture-model', 'rss-coordinate-v1', 'agent-tools-v1'
)`, resolutionID, operationID, entryID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	agentService := NewAgentResolutionService(db.New(pool), transactor, scheduler, nil, nil, nil)
	resolution := domain.AgentResolution{
		ID: resolutionID, OperationID: operationID, Version: 1,
		Capability: domain.AgentCapabilityRSSCoordinate, ResourceType: "rss_entry", ResourceID: entryID,
		Status: domain.AgentResolutionProposed,
	}
	proposal := domain.AgentRSSCoordinateProposal{
		EntryID: entryID, SourceSeason: 1, SourceEpisode: 13, EvidenceCodes: []string{"neighbor_sequence"}, Decision: "resolved",
	}
	validation := domain.AgentProposalValidation{Verdict: domain.AgentValidationAutoApplicable, ReasonCodes: []string{}}
	if err := agentService.applyRSSCoordinate(ctx, resolution, proposal, validation, uuid.Nil); err != nil {
		t.Fatalf("applyRSSCoordinate() error = %v", err)
	}
	var season, episode int
	var coordinateSource, resolutionStatus string
	if err := pool.QueryRow(ctx, `SELECT source_season, source_episode, coordinate_source FROM rss_entries WHERE id = $1`, entryID).Scan(&season, &episode, &coordinateSource); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_resolutions WHERE id = $1`, resolutionID).Scan(&resolutionStatus); err != nil {
		t.Fatal(err)
	}
	var acquisitions, downloads, continuations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE rss_entry_id = $1`, entryID).Scan(&acquisitions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM downloads`).Scan(&downloads); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE idempotency_key = $1`, "rss.poll:mapping-coordinate:"+resolutionID.String()).Scan(&continuations); err != nil {
		t.Fatal(err)
	}
	if season != 1 || episode != 13 || coordinateSource != string(domain.DecisionSourceAgentAuto) ||
		resolutionStatus != string(domain.AgentResolutionApplied) || acquisitions != 0 || downloads != 0 || continuations != 1 {
		t.Fatalf("coordinate/source/resolution/acquisitions/downloads/continuations = S%02dE%02d/%s/%s/%d/%d/%d",
			season, episode, coordinateSource, resolutionStatus, acquisitions, downloads, continuations)
	}
}
