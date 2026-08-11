//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/agentharness"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestRSSReleaseAdjudicationStagesDuplicateCoordinatesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	rssWorkflow := NewRSSWorkflow(db.New(pool), transactor, NewOperationScheduler(transactor, &integrationJobInserter{}))
	seriesID, subscriptionID, pollOperationID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Duplicate Coordinate Series')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Duplicate Coordinate Feed', 'https://example.test/duplicate-coordinate.xml', true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'running', 3, 60)`, pollOperationID, subscriptionID, "rss-poll-"+pollOperationID.String()); err != nil {
		t.Fatal(err)
	}
	feed := domain.RSSFeed{Title: "Duplicate Coordinate", Entries: []domain.RSSFeedEntry{
		{GUID: "episode-1-primary", Title: "Duplicate Coordinate Series - S01E01 [Primary]", DownloadURI: "magnet:?xt=urn:btih:1123456789abcdef0123456789abcdef01234567"},
		{GUID: "episode-1-alternate", Title: "Duplicate Coordinate Series - S01E01 [Alternate]", DownloadURI: "magnet:?xt=urn:btih:2123456789abcdef0123456789abcdef01234567"},
		{GUID: "episode-2", Title: "Duplicate Coordinate Series - S01E02", DownloadURI: "magnet:?xt=urn:btih:3123456789abcdef0123456789abcdef01234567"},
	}}
	persisted, err := rssWorkflow.PersistPoll(ctx, pollOperationID, subscriptionID, feed, domain.RSSPollPersistOptions{AdjudicateReleases: true})
	if err != nil {
		t.Fatalf("PersistPoll() error = %v", err)
	}
	if len(persisted.Candidates) != 1 || len(persisted.AgentAdjudicationBatchIDs) != 1 {
		t.Fatalf("persisted candidates/batches = %d/%d, want 1/1", len(persisted.Candidates), len(persisted.AgentAdjudicationBatchIDs))
	}
	var staged, episodeOneAcquisitions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rss_entry_adjudications WHERE batch_id = $1 AND state = 'pending'`, persisted.AgentAdjudicationBatchIDs[0]).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions AS acquisition JOIN rss_entries AS entry ON entry.id = acquisition.rss_entry_id WHERE entry.subscription_id = $1 AND entry.source_episode = 1`, subscriptionID).Scan(&episodeOneAcquisitions); err != nil {
		t.Fatal(err)
	}
	if staged != 2 || episodeOneAcquisitions != 0 {
		t.Fatalf("staged duplicate entries/acquisitions = %d/%d, want 2/0", staged, episodeOneAcquisitions)
	}

	legacyResolutionID, legacyOperationID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'succeeded', 3, 60);
INSERT INTO agent_resolutions (
    id, operation_id, version, capability, resource_type, resource_id, trigger, status,
    input_fingerprint, configuration_version, protocol, provider_origin, model, prompt_version, toolset_version,
    proposal, validation
) VALUES (
    $2, $1, 2, 'rss_release_adjudication', 'rss_adjudication_batch', $4, 'automatic', 'review_required',
    $5, 1, 'openai_chat_completions', 'https://provider.example', 'fixture-model', 'rss-release-adjudication-v2', 'agent-tools-v1',
    '{"decision":"resolved"}'::jsonb, '{"verdict":"invalid","reasonCodes":["rss_adjudication_coordinate_duplicate"]}'::jsonb
)`, legacyOperationID, legacyResolutionID, "legacy-adjudication-"+legacyResolutionID.String(), persisted.AgentAdjudicationBatchIDs[0], make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	replayed, err := rssWorkflow.PersistPoll(ctx, pollOperationID, subscriptionID, feed, domain.RSSPollPersistOptions{AdjudicateReleases: true})
	if err != nil {
		t.Fatalf("replayed PersistPoll() error = %v", err)
	}
	if len(replayed.AgentAdjudicationBatchIDs) != 1 || replayed.AgentAdjudicationBatchIDs[0] != persisted.AgentAdjudicationBatchIDs[0] {
		t.Fatalf("legacy review replay batches = %v, want original pending batch", replayed.AgentAdjudicationBatchIDs)
	}
}

func TestRSSReleaseAdjudicationStagesAndAppliesOneSelectedReleaseIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	rssWorkflow := NewRSSWorkflow(db.New(pool), transactor, scheduler)
	seriesID, subscriptionID, pollOperationID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Adjudication Series')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Adjudication Feed', 'https://example.test/adjudication.xml', true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'rss.poll', 'rss_subscription', $2, $3, 'running', 3, 60)`, pollOperationID, subscriptionID, "rss-poll-"+pollOperationID.String()); err != nil {
		t.Fatal(err)
	}
	feed := domain.RSSFeed{Title: "Adjudication", Entries: []domain.RSSFeedEntry{
		{GUID: "release-standard", Title: "Adjudication Series - S01E03", DownloadURI: "magnet:?xt=urn:btih:2123456789abcdef0123456789abcdef01234567"},
		{GUID: "release-alpha", Title: "Arbitrary release alpha", DownloadURI: "magnet:?xt=urn:btih:3123456789abcdef0123456789abcdef01234567"},
		{GUID: "release-corrected", Title: "Completely different corrected release", DownloadURI: "magnet:?xt=urn:btih:4123456789abcdef0123456789abcdef01234567"},
	}}
	persisted, err := rssWorkflow.PersistPoll(ctx, pollOperationID, subscriptionID, feed, domain.RSSPollPersistOptions{AdjudicateReleases: true})
	if err != nil {
		t.Fatalf("PersistPoll() error = %v", err)
	}
	if len(persisted.Candidates) != 1 || len(persisted.AgentAdjudicationBatchIDs) != 1 {
		t.Fatalf("persisted = %#v, want one deterministic candidate and one staged fallback batch", persisted)
	}
	batchID := persisted.AgentAdjudicationBatchIDs[0]
	replayed, err := rssWorkflow.PersistPoll(ctx, pollOperationID, subscriptionID, feed, domain.RSSPollPersistOptions{AdjudicateReleases: true})
	if err != nil {
		t.Fatalf("replayed PersistPoll() error = %v", err)
	}
	if len(replayed.Candidates) != 1 || len(replayed.AgentAdjudicationBatchIDs) != 1 || replayed.AgentAdjudicationBatchIDs[0] != batchID {
		t.Fatalf("replayed persisted = %#v, want deterministic candidate and unresolved original batch", replayed)
	}
	rows, err := pool.Query(ctx, `
SELECT entry.id, entry.title
FROM rss_entries AS entry
JOIN rss_entry_adjudications AS adjudication ON adjudication.entry_id = entry.id
WHERE adjudication.batch_id = $1
ORDER BY entry.title`, batchID)
	if err != nil {
		t.Fatal(err)
	}
	entryIDs := make(map[string]uuid.UUID, 2)
	for rows.Next() {
		var id uuid.UUID
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			t.Fatal(err)
		}
		entryIDs[title] = id
	}
	rows.Close()
	if len(entryIDs) != 2 {
		t.Fatalf("staged entries = %d, want 2", len(entryIDs))
	}
	ignoredID := entryIDs["Arbitrary release alpha"]
	selectedID := entryIDs["Completely different corrected release"]
	resolutionID, agentOperationID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'running', 3, 60)`, agentOperationID, resolutionID, "agent-resolve-"+resolutionID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_resolutions (
    id, operation_id, capability, resource_type, resource_id, trigger, status,
    input_fingerprint, configuration_version, protocol, provider_origin, model, prompt_version, toolset_version
) VALUES (
    $1, $2, 'rss_release_adjudication', 'rss_adjudication_batch', $3, 'automatic', 'proposed',
    $4, 1, 'openai_chat_completions', 'https://provider.example', 'fixture-model', 'rss-release-adjudication-v1', 'agent-tools-v1'
)`, resolutionID, agentOperationID, batchID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	season, episode := 1, 1
	proposal := domain.AgentRSSReleaseAdjudicationProposal{
		BatchID: batchID, ScopedEntryIDs: []uuid.UUID{ignoredID, selectedID}, Decision: "resolved",
		Entries: []domain.AgentRSSReleaseDisposition{
			{EntryID: ignoredID, Disposition: "ignore", RelatedEntryID: &selectedID, EvidenceCodes: []string{"alternate_release"}},
			{EntryID: selectedID, Disposition: "select", SourceSeason: &season, SourceEpisode: &episode, EvidenceCodes: []string{"preferred_release"}},
		},
	}
	agentService := NewAgentResolutionService(db.New(pool), transactor, scheduler, nil, nil, nil)
	resolution := domain.AgentResolution{ID: resolutionID, OperationID: agentOperationID, Version: 1, Capability: domain.AgentCapabilityRSSReleaseAdjudication, ResourceType: "rss_adjudication_batch", ResourceID: batchID, Status: domain.AgentResolutionProposed}
	validation := domain.AgentProposalValidation{Verdict: domain.AgentValidationAutoApplicable, ReasonCodes: []string{}}
	if err := agentService.applyRSSReleaseAdjudication(ctx, resolution, proposal, validation, uuid.Nil); err != nil {
		t.Fatalf("applyRSSReleaseAdjudication() error = %v", err)
	}
	var selectedState, selectedStatus, ignoredState, batchStatus, resolutionStatus string
	if err := pool.QueryRow(ctx, `
SELECT adjudication.state, entry.status
FROM rss_entries AS entry
JOIN rss_entry_adjudications AS adjudication ON adjudication.entry_id = entry.id
WHERE entry.id = $1`, selectedID).Scan(&selectedState, &selectedStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM rss_entry_adjudications WHERE entry_id = $1`, ignoredID).Scan(&ignoredState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM rss_adjudication_batches WHERE id = $1`, batchID).Scan(&batchStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_resolutions WHERE id = $1`, resolutionID).Scan(&resolutionStatus); err != nil {
		t.Fatal(err)
	}
	var acquisitions, downloads, enqueueOperations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisitions WHERE rss_entry_id = $1`, selectedID).Scan(&acquisitions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM downloads AS download JOIN acquisitions AS acquisition ON acquisition.id = download.acquisition_id WHERE acquisition.rss_entry_id = $1`, selectedID).Scan(&downloads); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = 'download.enqueue'`).Scan(&enqueueOperations); err != nil {
		t.Fatal(err)
	}
	if selectedState != "selected" || selectedStatus != "enqueueing" || ignoredState != "ignored" || batchStatus != "applied" || resolutionStatus != "applied" || acquisitions != 1 || downloads != 1 || enqueueOperations != 1 {
		t.Fatalf("states selected=%s/%s ignored=%s batch=%s resolution=%s counts=%d/%d/%d", selectedState, selectedStatus, ignoredState, batchStatus, resolutionStatus, acquisitions, downloads, enqueueOperations)
	}
}

func TestLegacyEmptyAutomaticAgentFailureCanBeRequeuedOnceIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	queries := db.New(pool)
	configuration := deterministicAgentConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Agent: domain.AgentSettings{
		Enabled: true, RequestTimeoutSeconds: 60,
	}}}}
	service := NewAgentResolutionService(queries, transactor, scheduler, configuration, nil, nil)

	resolutionID, operationID, batchID := uuid.New(), uuid.New(), uuid.New()
	seriesID, subscriptionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'Legacy Retry Series')`, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($1, $2, 'Legacy Retry Feed', 'https://example.test/legacy-retry.xml', true, 900, 1)`, subscriptionID, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO rss_adjudication_batches (id, subscription_id, status, entry_count)
VALUES ($1, $2, 'pending', 1)`, batchID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count,
    timeout_seconds, error_code, error_message, finished_at
) VALUES ($1, 'agent.resolve', 'agent_resolution', $2, $3, 'failed', 3, 1, 60,
          'agent_submission_invalid', 'agent_submission_invalid', now());
INSERT INTO agent_resolutions (
    id, operation_id, version, capability, resource_type, resource_id, trigger, status,
    input_fingerprint, configuration_version, protocol, provider_origin, model, prompt_version,
    toolset_version, proposal, error_code, error_message, completed_at
) VALUES (
    $2, $1, 3, 'rss_release_adjudication', 'rss_adjudication_batch', $4, 'automatic', 'failed',
    $5, 1, 'openai_chat_completions', 'https://provider.example', 'fixture-model',
    'rss-release-adjudication-v3', 'agent-tools-v1', '{}'::jsonb,
    'agent_submission_invalid', 'agent_submission_invalid', now()
)`, operationID, resolutionID, "legacy-failed-"+resolutionID.String(), batchID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}

	result, err := service.RetryAutomatic(ctx, resolutionID, 3)
	if err != nil {
		t.Fatalf("RetryAutomatic() error = %v", err)
	}
	if result.Resolution.Status != domain.AgentResolutionQueued || result.Resolution.ErrorCode != "" || len(result.Resolution.Proposal) > 2 || result.Operation.Status != "queued" || result.Operation.MaxAttempts != 3 {
		t.Fatalf("retry result = %#v / %#v", result.Resolution, result.Operation)
	}
	if _, err := service.RetryAutomatic(ctx, resolutionID, 3); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("duplicate RetryAutomatic() error = %#v, want state conflict", err)
	}
	if err := service.persistAgentResolutionSteps(ctx, resolutionID, 1, []agentharness.Step{{
		Sequence: 1, ToolName: "submit_rss_release_adjudication", Status: "rejected",
		ErrorCode: "rss_adjudication_coordinate_duplicate", ArgumentsDigest: make([]byte, 32), ResultDigest: make([]byte, 32),
	}}); err != nil {
		t.Fatalf("persist first partial step: %v", err)
	}
	if err := service.persistAgentResolutionSteps(ctx, resolutionID, 2, []agentharness.Step{{
		Sequence: 1, ToolName: "submit_rss_release_adjudication", Status: "rejected",
		ErrorCode: "rss_adjudication_scope_incomplete", ArgumentsDigest: make([]byte, 32), ResultDigest: make([]byte, 32),
	}}); err != nil {
		t.Fatalf("persist second partial step: %v", err)
	}
	var firstSequence, secondSequence int
	var firstCode, secondCode string
	if err := pool.QueryRow(ctx, `
SELECT min(sequence), max(sequence), min(error_code), max(error_code)
FROM agent_resolution_steps
WHERE resolution_id = $1`, resolutionID).Scan(&firstSequence, &secondSequence, &firstCode, &secondCode); err != nil {
		t.Fatal(err)
	}
	if firstSequence != 1 || secondSequence != 65 || firstCode != "rss_adjudication_coordinate_duplicate" || secondCode != "rss_adjudication_scope_incomplete" {
		t.Fatalf("partial step audit = %d/%d %s/%s", firstSequence, secondSequence, firstCode, secondCode)
	}
}
